package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/MarcosTypeAP/go-p2p/internal/assert"
	"github.com/MarcosTypeAP/go-p2p/internal/buildflags"
	"github.com/MarcosTypeAP/go-p2p/internal/crossplatform"
	"github.com/MarcosTypeAP/go-p2p/internal/logging"
	"golang.org/x/sync/errgroup"
)

const DefaultPort = 42069

var _privateIP net.IP

func getPrivateIP() net.IP {
	if _privateIP != nil {
		return _privateIP
	}

	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		panic(fmt.Errorf("could not get the machine's private ip: %v", err))
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	_privateIP = localAddr.IP

	return _privateIP
}

var logger *slog.Logger

func init() {
	loggerOpts := slog.HandlerOptions{
		AddSource: false,
		Level:     slog.LevelInfo,
	}
	if buildflags.DevMode {
		loggerOpts.AddSource = true
		loggerOpts.Level = slog.LevelDebug
	}
	logger = slog.New(logging.NewLoggingHandler(os.Stdout, &loggerOpts))
}

func contextWithCloseChannel(ctx context.Context, closeCh chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-closeCh:
			cancel()
		}
	}()

	return ctx, cancel
}

var aLongTimeAgo = time.Unix(0, 0)

// contextConn is a net.Conn wrapper that holds a context.Context to cancel operations
// when the context is canceled.
// This is to avoid creating goroutines for each operation.
type contextConn struct {
	net.Conn
	ctx context.Context
}

func newContextConn(conn net.Conn, ctx context.Context) contextConn {
	go func() {
		<-ctx.Done()
		conn.SetDeadline(aLongTimeAgo)
	}()

	return contextConn{conn, ctx}
}

// Read implements io.Reader and returns the context error if canceled.
func (c contextConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) && c.ctx.Err() != nil {
			err = c.ctx.Err()
		}
	}
	return n, err
}

// Write implements io.Writer and returns the context error if canceled.
func (c contextConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) && c.ctx.Err() != nil {
			err = c.ctx.Err()
		}
	}
	return n, err
}

// A PacketPool is a thread-safe pool of Packet.
type PacketPool struct {
	pool []struct {
		packet Packet
		exp    int64
	}
	mu sync.Mutex
}

func (p *PacketPool) packetsAvailable() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	n := 0

	for _, slot := range p.pool {
		if slot.packet == nil {
			continue
		}
		n++
	}

	return n
}

// Get returns a packet from the pool with at least the given size.
// If it does not find one, it allocates it.
//
// Panics if size > MaxPacketSize.
func (p *PacketPool) Get(size int) Packet {
	assert.LessEqual(size, MaxPacketSize)

	p.mu.Lock()
	defer p.mu.Unlock()

	for i, slot := range p.pool {
		if slot.packet == nil {
			continue
		}

		if cap(slot.packet) < size {
			continue
		}

		p.pool[i] = struct {
			packet Packet
			exp    int64
		}{nil, 0}

		return slot.packet[:size]
	}

	return make(Packet, size)
}

func getSource(skip int) (string, int) {
	_, file, line, ok := runtime.Caller(1 + skip)
	assert.True(ok)

	return file, line
}

// Put returns packets to the pool.
func (p *PacketPool) Put(packets ...Packet) {
	p.mu.Lock()
	defer p.mu.Unlock()

	const maxPoolSize = 128
	const packetLifetime = 500 // ms

	now := time.Now().UnixMilli()

	slot := 0

NextPacket:
	for i := range packets {
		item := struct {
			packet Packet
			exp    int64
		}{
			packet: packets[i],
			exp:    now + packetLifetime,
		}

		for ; slot < len(p.pool); slot++ {
			if p.pool[slot].packet != nil && p.pool[slot].exp > now {
				continue
			}

			p.pool[slot] = item
			slot++
			continue NextPacket
		}

		p.pool = append(p.pool, item)
	}
}

// Clone returns a packet from the pool with the content of the given packet.
func (p *PacketPool) Clone(packet Packet) Packet {
	newPacket := p.Get(len(packet))
	copy(newPacket, packet)
	return newPacket
}

func assertChanBufferSize[T any](ch chan<- T, size int) {
	assert.NotEqual(ch, nil)
	assert.Equal(cap(ch), size)
}

type PendingAck struct {
	// Controls who owns the packet to avoid data-races.
	packet     chan Packet  // buffer size of 1
	signalAck  chan<- error // buffer size of 1
	nextResend int64        // Unix Milliseconds
	tries      int32
	packetKind PacketKind
}

func newPendingAck(
	packetKind PacketKind,
	packetCh chan Packet,
	signalAck chan<- error,
	receiveAckTimeout time.Duration,
) PendingAck {
	assert.Equal(cap(packetCh), 1)
	assert.Equal(cap(signalAck), 1)

	return PendingAck{
		packet:     packetCh,
		signalAck:  signalAck,
		nextResend: time.Now().Add(receiveAckTimeout).UnixMilli(),
		packetKind: packetKind,
	}
}

var ErrPeerClosed = errors.New("connection closed by peer")
var ErrSendQueueFull = errors.New("send queue full")
var ErrPendingAckQueueFull = errors.New("pending acknowledgment queue full")

// Conn is a peer to peer network connection.
// Multiple goroutines may invoke methods on a Conn simultaneously.
type Conn struct {
	conn contextConn

	opts connOptions

	// Holds a function that signals to stop the connection.
	disconnectCh chan context.CancelFunc
	// Holds the error why the connection ended.
	closingErrCh chan error // buffer size of 1

	packetPool PacketPool

	// It is used to keep track of which packet is to be receive next and helps to maintain the packet order.
	nextRecvID uint32
	// Packets received to early are stored here to be released when all previous packets have arrived.
	recvPacketsBuf []Packet

	// It is used to keep track of the next packet's ID.
	nextSendID uint32

	signal struct {
		// When closed, all handlers (goroutines) should stop and return.
		closed chan struct{}
		// Closed when the connection has finished closing and cleaning.
		done chan struct{}
	}
	connected atomic.Bool

	sendQueue chan struct {
		packet    Packet
		signalAck chan<- error // buffer size of 1
	}

	// It holds the info of packets that needs to receive an acknowledgment.
	pendingAcks     map[PacketID]PendingAck
	pendingAckMutex sync.Mutex
	// Queue of pending packets to be resent.
	pendingAckQueue chan PacketID

	// Callbacks per packet kind.
	recvHandlers map[PacketKind]func(Packet)
	// Callback for unregistered packet kinds.
	recvUnexpectedHandler func(Packet)
	recvRequests          []struct {
		kind     PacketKind
		buf      []byte
		response chan<- struct { // buffer size of 1
			Packet Packet
			Err    error
		}
	}
	recvHandlersMutex sync.Mutex
}

type queueBehavior byte

const (
	queueBlock queueBehavior = iota
	queueDiscardPacket
	queueReturnError
	queuePanic
)

var sendQueueFullBehaviors = map[string]queueBehavior{
	"block":   queueBlock,
	"discard": queueDiscardPacket,
	"error":   queueReturnError,
	"panic":   queuePanic,
}

var pendingAckQueueFullBehaviors = map[string]queueBehavior{
	"error": queueReturnError,
	"panic": queuePanic,
}

type connOptions struct {
	LocalAddr                   string
	SendQueueSize               int
	SendQueueFullBehavior       queueBehavior
	ResendTries                 int32
	PendingAckQueueSize         int
	PendingAckQueueFullBehavior queueBehavior
	ReceiveAckTimeout           time.Duration
	ReceiveBufferSize           int
	ConnectionRefusedCooldown   time.Duration
	ReuseAddress                bool

	// Testing

	packetLoss byte // percentage
}

var DefaultOptions = connOptions{
	LocalAddr:                   fmt.Sprintf("%s:%d", getPrivateIP(), DefaultPort),
	SendQueueSize:               64,
	SendQueueFullBehavior:       queueBlock,
	ResendTries:                 5,
	PendingAckQueueSize:         64 * 5,
	PendingAckQueueFullBehavior: queuePanic,
	ReceiveAckTimeout:           100 * time.Millisecond,
	ReceiveBufferSize:           64,
	ConnectionRefusedCooldown:   1 * time.Second,
	ReuseAddress:                false,
}

type OptFunc func(*connOptions)

// Change local address.
// If IP is missing, the machine's private IP is used
// If port is missing, DefaultPort is used.
func WithLocalAddr(localAddr string) OptFunc {
	return func(o *connOptions) {
		localAddr = strings.TrimSpace(localAddr)

		if localAddr == "" || localAddr == ":" {
			return
		}

		parts := strings.SplitN(localAddr, ":", 2)
		if len(parts) == 1 {
			o.LocalAddr = fmt.Sprintf("%s:%d", parts[0], DefaultPort)
			return
		}

		switch {
		case parts[0] == "":
			o.LocalAddr = fmt.Sprintf("%s:%s", getPrivateIP(), parts[1])
		case parts[1] == "":
			o.LocalAddr = fmt.Sprintf("%s:%d", parts[0], DefaultPort)
		default:
			o.LocalAddr = localAddr
		}
	}
}

// The number of packets that can be queued for sending before it becomes full.
func WithSendQueueSize(size int) OptFunc {
	return func(o *connOptions) {
		o.SendQueueSize = size
	}
}

// What should happen when the send queue is full.
//
// Possible values are:
//
//	"block": block until it is not full.
//	"discard": discard the packet.
//	"error": return an error.
//	"panic": well... panic.
func WithSendQueueFullBehavior(behavior string) OptFunc {
	b, exists := sendQueueFullBehaviors[behavior]
	if !exists {
		panic("invalid behavior value")
	}
	return func(o *connOptions) {
		o.SendQueueFullBehavior = b
	}
}

// Number of attempts to receive acknowledgment before disconnecting.
// Use -1 to disable retries.
func WithResendTries(tries int32) OptFunc {
	return func(o *connOptions) {
		o.ResendTries = tries
	}
}

// The number of packets that can be queued waiting for an acknowledgment before it becomes full.
func WithPendingAckQueueSize(size int) OptFunc {
	return func(o *connOptions) {
		o.PendingAckQueueSize = size
	}
}

// What should happen when the pending ack queue is full.
//
// Possible values are:
//
//	"error": return an error.
//	"panic": well... panic.
func WithPendingAckQueueFullBehavior(behavior string) OptFunc {
	b, exists := pendingAckQueueFullBehaviors[behavior]
	if !exists {
		panic("invalid behavior value")
	}
	return func(o *connOptions) {
		o.PendingAckQueueFullBehavior = b
	}
}

// Time to wait for an acknowledgment before attempting to resend the packet.
func WithReceiveAckTimeout(timeout time.Duration) OptFunc {
	return func(o *connOptions) {
		o.ReceiveAckTimeout = timeout
	}
}

// Buffer size to store packets received to early (to maintain order).
func WithReceiveBufferSize(size int) OptFunc {
	return func(o *connOptions) {
		o.ReceiveBufferSize = size
	}
}

// Time between failed connection attempts.
func WithConnectionRefusedCooldown(cooldown time.Duration) OptFunc {
	return func(o *connOptions) {
		o.ConnectionRefusedCooldown = cooldown
	}
}

// Sets syscall.SO_REUSEADDR on the connection socket.
func WithReuseAddress() OptFunc {
	return func(o *connOptions) {
		o.ReuseAddress = true
	}
}

func withPacketLoss(percentage byte) OptFunc {
	return func(o *connOptions) {
		o.packetLoss = percentage
	}
}

func newConn(opts connOptions) *Conn {
	disconnectCh := make(chan context.CancelFunc, 1)
	disconnectCh <- nil

	return &Conn{
		opts: opts,

		disconnectCh: disconnectCh,
		closingErrCh: make(chan error, 1),

		signal: struct {
			closed chan struct{}
			done   chan struct{}
		}{
			closed: make(chan struct{}),
			done:   make(chan struct{}),
		},

		sendQueue: make(chan struct {
			packet    Packet
			signalAck chan<- error
		}, opts.SendQueueSize),

		pendingAcks:     make(map[PacketID]PendingAck, opts.SendQueueSize),
		pendingAckQueue: make(chan PacketID, opts.PendingAckQueueSize),

		recvHandlers:   make(map[PacketKind]func(Packet), 16),
		recvPacketsBuf: make([]Packet, 0, opts.ReceiveBufferSize),
	}
}

type connDialer struct {
	dialer net.Dialer
}

func (d *connDialer) setLocalAddr(addr string) error {
	localAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return fmt.Errorf("resolve local address: %w", err)
	}
	d.dialer.LocalAddr = localAddr
	return nil
}

func (d *connDialer) setControl(ctrl func(network, address string, c syscall.RawConn) error) {
	d.dialer.Control = ctrl
}

func (d *connDialer) dialContext(ctx context.Context, address string) (net.Conn, error) {
	return d.dialer.DialContext(ctx, "udp4", address)
}

// Used for mocking
type genericDialer interface {
	setLocalAddr(addr string) error
	setControl(func(network, address string, c syscall.RawConn) error)
	dialContext(ctx context.Context, address string) (net.Conn, error)
}

// Dial connects to the given address.
//
// It waits for the connection to be established.
// The connection is established after sending packets and receiving an acknowledgment from the other peer.
// If the address does not have a port, DefaultPort will be used.
//
// You can see the default options in DefaultOptions.
func Dial(ctx context.Context, raddr string, options ...OptFunc) (*Conn, error) {
	return dial(ctx, &connDialer{}, raddr, options...)
}

func dial(ctx context.Context, dialer genericDialer, raddr string, options ...OptFunc) (*Conn, error) {
	opts := DefaultOptions
	for _, f := range options {
		f(&opts)
	}

	c := newConn(opts)

	if opts.ReuseAddress {
		dialer.setControl(func(network, address string, c syscall.RawConn) error {
			err := crossplatform.SetReuseAddr(c, true)
			if err != nil {
				return fmt.Errorf("set reuse address: %w", err)
			}
			return nil
		})
	}

	err := dialer.setLocalAddr(opts.LocalAddr)
	if err != nil {
		return nil, err
	}

	{
		parts := strings.SplitN(raddr, ":", 2)
		if len(parts) == 1 || parts[1] == "" {
			raddr = fmt.Sprintf("%s:%d", parts[0], DefaultPort)
		}
	}

	conn, err := dialer.dialContext(ctx, raddr)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	ctx, cancel := contextWithCloseChannel(ctx, c.signal.closed)
	<-c.disconnectCh
	c.disconnectCh <- cancel

	c.conn = newContextConn(conn, ctx)

	errg, ctx := errgroup.WithContext(ctx)

	go func() {
		<-ctx.Done()
		cancel()
		close(c.signal.closed)

		closeErr := errg.Wait()
		c.closingErrCh <- closeErr

		c.cleanStuff(closeErr)

		if !errors.Is(closeErr, ErrPeerClosed) {
			c.sendReset()
		}

		_ = c.conn.Close()
		close(c.signal.done)
	}()

	errg.Go(func() error { return c.handleKeepAlive(ctx) })
	errg.Go(func() error { return c.handleReceive() })
	errg.Go(func() error { return c.handleSend(ctx) })
	errg.Go(func() error { return c.handlePendingAck(ctx) })

	ping := NewPacketBuilder(make([]byte, HeaderSize), PacketPing, FlagNeedAck).Build()
	err = <-c.Send(ping)
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}

	c.connected.Store(true)
	return c, nil
}

// LocalAddr returns the local network address, if known.
func (c *Conn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// RemoteAddr returns the remote network address, if known.
func (c *Conn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *Conn) cleanStuff(closeErr error) {
	close(c.pendingAckQueue)
	c.pendingAckMutex.Lock()
	{
		for _, pending := range c.pendingAcks {
			if pending.signalAck != nil {
				assertChanBufferSize(pending.signalAck, 1)
				pending.signalAck <- closeErr
			}
			c.packetPool.Put(<-pending.packet)
		}
		clear(c.pendingAcks)
	}
	c.pendingAckMutex.Unlock()

	c.recvHandlersMutex.Lock()
	{
		for _, req := range c.recvRequests {
			assertChanBufferSize(req.response, 1)
			req.response <- struct {
				Packet Packet
				Err    error
			}{
				nil,
				closeErr,
			}
		}
	}
	c.recvHandlersMutex.Unlock()
}

func (c *Conn) sendReset() {
	reset := NewPacketBuilder(make([]byte, HeaderSize), PacketReset, FlagNeedAck).Build()
	reset.setID(c.nextRecvID)

	tries := c.opts.ResendTries
	if tries == -1 {
		tries = 10
	}
	c.conn.SetWriteDeadline(time.Time{})
	for range tries {
		err := c.sendPacket(reset)
		if err != nil {
			logger.Error("Sending PacketReset", "peer", c.conn.RemoteAddr(), "err", err)
			return
		}

		c.conn.SetReadDeadline(time.Now().Add(c.opts.ReceiveAckTimeout))

		packet, err := c.readPacket(make([]byte, HeaderSize))
		if err != nil {
			if errors.Is(err, context.Canceled) {
				continue
			}
			logger.Error("Waiting for PacketReset's ACK", "peer", c.conn.RemoteAddr(), "err", err)
			return
		}
		if err := packet.Validate(); err != nil {
			continue
		}
		if packet.Kind() != PacketAck {
			continue
		}
		if packet.ID() != c.nextRecvID {
			continue
		}
		return
	}
	logger.Error("Could not receive ACK for PacketReset", "peer", c.conn.RemoteAddr())
}

// Close signals the connection to end.
// Before closing the connection it tries to signal the other peer that its closing the connection.
// There is no guarantee that the other peer will receive the closing signal, especially if it is due to an error.
//
// Panics if called when the connection is not established yet.
// It is safe to call it from multiple goroutines.
func (c *Conn) Close() {
	disconnect := <-c.disconnectCh
	if disconnect == nil {
		panic("connection not established yet")
	}
	disconnect()
	c.disconnectCh <- disconnect
}

// Done returns a channel that is closed when the connection has finished closing and cleaning.
//
// It is safe to call it from multiple goroutines.
func (c *Conn) Done() <-chan struct{} {
	return c.signal.done
}

// Err returns the reason why the connection ended.
//
// If the other peer sends a closing signal, the error will be ErrPeerClosed.
//
// It is safe to call it from multiple goroutines.
func (c *Conn) Err() error {
	return <-c.closingErrCh
}

// handleKeepAlive sends a keep alive packet periodically.
func (c *Conn) handleKeepAlive(ctx context.Context) error {
	timer := time.NewTimer(0)

	keepAlive := NewPacketBuilder(make([]byte, HeaderSize), PacketKeepAlive, FlagNone).Build()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}

		c.Send(keepAlive)

		timer.Reset(30 * time.Second)
	}
}

// handleReceive reads the connection and processes the received packets.
func (c *Conn) handleReceive() error {
	ackPacket := NewPacketBuilder(make([]byte, HeaderSize), PacketAck, FlagNone).Build()

	readBuf := c.packetPool.Get(MaxPacketSize)

	for {
		readBuf = readBuf[:cap(readBuf)]

		packet, err := c.readPacket(readBuf)
		if err != nil {
			return fmt.Errorf("reading packet: %w", err)
		}

		if packet.HasFlags(FlagNeedAck) {
			ackPacket.setID(packet.ID())

			if err := c.sendPacket(ackPacket); err != nil {
				return fmt.Errorf("sending ack (id=%d): %w", packet.ID(), err)
			}
		}

		switch packet.Kind() {
		case PacketKeepAlive:
			continue

		case PacketReset:
			// if !c.connected.Load() { // skip packets from previous socket
			// 	continue
			// }
			return ErrPeerClosed

		case PacketAck:
			c.pendingAckMutex.Lock()
			pendingAck, exists := c.pendingAcks[packet.ID()]
			if !exists {
				c.pendingAckMutex.Unlock()
				continue
			}
			delete(c.pendingAcks, packet.ID())
			c.pendingAckMutex.Unlock()

			if pendingAck.signalAck != nil {
				pendingAck.signalAck <- nil
			}

			c.packetPool.Put(<-pendingAck.packet)
			continue
		}

		if packet.HasFlags(FlagNeedAck) {
			if packet.ID() < c.nextRecvID {
				continue
			}

			if packet.Kind() == PacketPing {
				c.nextRecvID++
				continue
			}

			if packet.ID() != c.nextRecvID {
				if len(c.recvPacketsBuf) == cap(c.recvPacketsBuf) {
					return fmt.Errorf("receive buffer full")
				}
				c.recvPacketsBuf = append(c.recvPacketsBuf, packet)
				readBuf = c.packetPool.Get(MaxPacketSize)
				continue
			}

			c.nextRecvID++
		}

		c.recvHandlersMutex.Lock()
		{
			c.handleReceivedPacket(packet)

			if len(c.recvPacketsBuf) > 0 {
				slices.SortFunc(c.recvPacketsBuf, func(p1, p2 Packet) int {
					return int(p1.ID()) - int(p2.ID())
				})

				var idx int
				var pkt Packet
				for idx, pkt = range c.recvPacketsBuf {
					if pkt.ID() < c.nextRecvID {
						continue
					}
					if pkt.ID() != c.nextRecvID {
						break
					}
					c.handleReceivedPacket(pkt)
					c.nextRecvID++
				}
				c.packetPool.Put(c.recvPacketsBuf[:idx]...)

				left := len(c.recvPacketsBuf) - idx
				for i := range left {
					c.recvPacketsBuf[i] = c.recvPacketsBuf[idx+i]
				}
				c.recvPacketsBuf = c.recvPacketsBuf[:left]
			}
		}
		c.recvHandlersMutex.Unlock()
	}
}

// handleReceivedPacket processes the received packet, routes it to the corresponding packet handler.
func (c *Conn) handleReceivedPacket(packet Packet) {
	for i, request := range c.recvRequests {
		if request.kind != packet.Kind() {
			continue
		}

		n := copy(request.buf, packet)
		assert.Equal(n, len(packet))

		assertChanBufferSize(request.response, 1)
		request.response <- struct {
			Packet Packet
			Err    error
		}{
			request.buf[:n],
			nil,
		}

		c.recvRequests = slices.Delete(c.recvRequests, i, i+1)
		return
	}

	if handler := c.recvHandlers[packet.Kind()]; handler != nil {
		handler(packet)
	} else if c.recvUnexpectedHandler != nil {
		c.recvUnexpectedHandler(packet)
	} else {
		logger.Warn("Unexpected packet", "peer", c.conn.RemoteAddr(), "packet", packet.String())
	}
}

// Called only by Conn.handleReceive
func (c *Conn) readPacket(buf []byte) (Packet, error) {
	var random *rand.Rand
	if c.opts.packetLoss > 0 {
		random = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	fails := 0

	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			if crossplatform.IsConnectionRefusedError(err) {
				if c.connected.Load() {
					fails++
					if fails >= 3 {
						return nil, ErrPeerClosed
					}
				}
				time.Sleep(c.opts.ConnectionRefusedCooldown)
				continue
			}
			return nil, err
		}

		if c.opts.packetLoss > 0 && random.Intn(100) < int(c.opts.packetLoss) {
			continue
		}

		packet := Packet(buf[:n])

		if err := packet.Validate(); err != nil {
			logger.Warn("invalid packet", "peer", c.conn.RemoteAddr(), "err", err, "packet", packet.String())
			continue
		}

		return packet, nil
	}
}

func (c *Conn) sendPacket(packet Packet) error {
	fails := 0

	for {
		n, err := c.conn.Write(packet)
		if err != nil {
			if crossplatform.IsConnectionRefusedError(err) {
				if c.connected.Load() {
					fails++
					if fails >= 3 {
						return ErrPeerClosed
					}
				}
				time.Sleep(c.opts.ConnectionRefusedCooldown)
				continue
			}
			// Random linux conntrack event
			if crossplatform.IsLinuxPermissionError(err) {
				fails++
				if fails >= 3 {
					return err
				}
				time.Sleep(c.opts.ConnectionRefusedCooldown)
				continue
			}
			return err
		}
		assert.Equal(n, len(packet))

		return nil
	}
}

// handlePendingAck is in charge of resending packets if they are not acknowledged.
// The connection is ended if a packet exceeds a certain number of resending attempts.
func (c *Conn) handlePendingAck(ctx context.Context) error {
	timer := time.NewTimer(0)
	timer.Stop()

	for {
		var currPacketID PacketID

		select {
		case <-ctx.Done():
			return ctx.Err()
		case currPacketID = <-c.pendingAckQueue:
		}

		c.pendingAckMutex.Lock()
		pendingAck, exists := c.pendingAcks[currPacketID]
		c.pendingAckMutex.Unlock()
		if !exists {
			continue
		}

		timer.Reset(time.UnixMilli(pendingAck.nextResend).Sub(time.Now()))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}

		var packet Packet

		c.pendingAckMutex.Lock()
		{
			pendingAck, exists = c.pendingAcks[currPacketID]
			if !exists {
				c.pendingAckMutex.Unlock()
				continue
			}

			packet = <-pendingAck.packet

			if c.connected.Load() {
				pendingAck.tries++

				if c.opts.ResendTries != -1 && pendingAck.tries == c.opts.ResendTries {
					delete(c.pendingAcks, currPacketID)
					c.pendingAckMutex.Unlock()

					err := fmt.Errorf("waiting for ack: peer does not respond")
					if pendingAck.signalAck != nil {
						pendingAck.signalAck <- err
					}

					c.packetPool.Put(packet)
					return err
				}

				pendingAck.nextResend = time.Now().Add(c.opts.ReceiveAckTimeout).UnixMilli()

				c.pendingAcks[currPacketID] = pendingAck
			}
		}
		c.pendingAckMutex.Unlock()

		err := c.sendPacket(packet)
		if err != nil {
			return fmt.Errorf("re-sending packet: %w", err)
		}

		if !c.connected.Load() {
			time.Sleep(c.opts.ConnectionRefusedCooldown)
		}

		if err := c.pushPacketIDToPendingAckQueue(currPacketID); err != nil {
			return err
		}
		pendingAck.packet <- packet
	}
}

func (c *Conn) pushPacketIDToPendingAckQueue(packetID uint32) error {
	select {
	case c.pendingAckQueue <- packetID:
	default:
		switch c.opts.PendingAckQueueFullBehavior {
		case queueReturnError:
			return ErrPendingAckQueueFull
		case queuePanic:
			panic(ErrPendingAckQueueFull)
		default:
			panic("not reached")
		}
	}
	return nil
}

func (c *Conn) getNextSendID() uint32 {
	id := c.nextSendID
	c.nextSendID++
	return id
}

// handleSend sends packets received through the send queue and register them into the pending ack queue if necessary.
func (c *Conn) handleSend(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case queueItem := <-c.sendQueue:
			var packetCh chan Packet

			needAck := queueItem.packet.HasFlags(FlagNeedAck)
			if needAck {
				packetID := c.getNextSendID()
				queueItem.packet.setID(packetID)

				packetCh = make(chan Packet, 1)

				c.pendingAckMutex.Lock()
				{
					c.pendingAcks[packetID] = newPendingAck(
						queueItem.packet.Kind(),
						packetCh,
						queueItem.signalAck,
						c.opts.ReceiveAckTimeout,
					)
				}
				c.pendingAckMutex.Unlock()
			}

			err := c.sendPacket(queueItem.packet)
			if err != nil {
				if needAck {
					packetCh <- queueItem.packet
				}
				return fmt.Errorf("sending packet: %w", err)
			}

			if needAck {
				packetID := queueItem.packet.ID() // avoid data race
				packetCh <- queueItem.packet
				if err := c.pushPacketIDToPendingAckQueue(packetID); err != nil {
					return err
				}
			} else {
				c.packetPool.Put(queueItem.packet)
			}
		}
	}
}

// Send sends a packet to the other peer.
//
// This method is asyncronous and simpy places the packet in the send queue.
// If the packet requires any acknowledgment, it will be signaled by the returned channel,
// otherwise it will be nil.
//
// It is safe to call it from multiple goroutines.
func (c *Conn) Send(packet Packet) <-chan error {
	return c.send(context.Background(), packet)
}

// SendContext is the same as Conn.Send but it receives a context.
//
// Useful when the "full send queue" behavior is to block.
func (c *Conn) SendContext(ctx context.Context, packet Packet) <-chan error {
	return c.send(ctx, packet)
}

func (c *Conn) send(ctx context.Context, packet Packet) <-chan error {
	var signalAck chan error

	if packet.HasFlags(FlagNeedAck) {
		signalAck = make(chan error, 1)
	}

	item := struct {
		packet    Packet
		signalAck chan<- error
	}{
		packet:    c.packetPool.Clone(packet),
		signalAck: signalAck,
	}

	select {
	case c.sendQueue <- item:
		return signalAck
	default:
	}

	switch c.opts.SendQueueFullBehavior {
	case queueBlock:
		select {
		case <-ctx.Done():
			if signalAck != nil {
				signalAck <- ctx.Err()
			}
		case c.sendQueue <- item:
		}

	case queueReturnError:
		if signalAck != nil {
			signalAck <- ErrSendQueueFull
		}

	case queueDiscardPacket:
		if signalAck != nil {
			signalAck <- nil
		}

	case queuePanic:
		panic(ErrSendQueueFull)
	}

	return signalAck
}

// OnReceive registers a packet handler.
//
// The given handler will be called with the received packet matching the packet kind.
// The passed packet should only live within the scope of the handler function; if you want to use it outside, you should allocate a copy.
//
// Packets are processed serially, and the receiving process is paused in the meantime, so the handlers should return as soon as possible.
// It is safe to call it from multiple goroutines.
func (c *Conn) OnReceive(kind PacketKind, handler func(packet Packet)) {
	c.recvHandlersMutex.Lock()
	c.recvHandlers[kind] = handler
	c.recvHandlersMutex.Unlock()
}

// OnReceiveUnexpected registers a packet handler for received packets that do not have a registered handler.
//
// The given handler will be called with the received packet that does not match any registered packet kind.
// The passed packet should only live within the scope of the handler function; if you want to use it outside, you should allocate a copy.
//
// Packets are processed serially, and the receiving process is paused in the meantime, so the handlers should return as soon as possible.
// It is safe to call it from multiple goroutines.
func (c *Conn) OnReceiveUnexpected(handler func(packet Packet)) {
	c.recvHandlersMutex.Lock()
	c.recvUnexpectedHandler = handler
	c.recvHandlersMutex.Unlock()
}

// Receive waits to receive a packet that matches the given packet kind.
//
// The received packet is written to buf and delivered over the returned channel.
//
// It is safe to call it from multiple goroutines.
func (c *Conn) Receive(kind PacketKind, buf []byte) <-chan struct {
	Packet Packet
	Err    error
} {
	res := make(chan struct {
		Packet Packet
		Err    error
	}, 1)

	request := struct {
		kind     PacketKind
		buf      []byte
		response chan<- struct {
			Packet Packet
			Err    error
		}
	}{
		kind:     kind,
		buf:      buf,
		response: res,
	}

	c.recvHandlersMutex.Lock()
	c.recvRequests = append(c.recvRequests, request)
	c.recvHandlersMutex.Unlock()

	return res
}
