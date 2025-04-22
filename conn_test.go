package p2p

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MarcosTypeAP/go-p2p/internal/assert"
	"github.com/MarcosTypeAP/go-p2p/internal/crossplatform"
	"github.com/MarcosTypeAP/go-p2p/internal/expect"
)

const ReceiveQueueSize = 32

const (
	PacketTest1 PacketKind = PacketCustom + iota
	PacketTest2
	PacketTest3
)

func TestLocalAddressOption(t *testing.T) {
	expectAddr := func(addr, want string) {
		opts := DefaultOptions
		WithLocalAddr(addr)(&opts)
		expect.Equal(t, opts.LocalAddr, want)
	}

	parts := strings.SplitN(DefaultOptions.LocalAddr, ":", 2)
	assert.Equal(len(parts), 2)
	defaultAddr := parts[0]
	defaultPort := parts[1]

	expectAddr("", DefaultOptions.LocalAddr)
	expectAddr(":", DefaultOptions.LocalAddr)
	expectAddr("69", "69:"+defaultPort)
	expectAddr("69:", "69:"+defaultPort)
	expectAddr(":420", defaultAddr+":420")
	expectAddr("69:420", "69:420")
}

func TestPacketPool(t *testing.T) {
	t.Parallel()

	pool := PacketPool{}

	expect.Equal(t, pool.packetsAvailable(), 0)

	p1 := pool.Get(69)
	expect.Equal(t, len(p1), 69)

	p2 := pool.Get(420)
	expect.Equal(t, len(p2), 420)

	p3 := pool.Get(33)
	expect.Equal(t, len(p3), 33)

	expect.Equal(t, pool.packetsAvailable(), 0)

	pool.Put(p1)
	expect.Equal(t, pool.packetsAvailable(), 1)

	p4 := pool.Get(6)
	expect.Equal(t, len(p4), 6)
	expect.Equal(t, pool.packetsAvailable(), 0)

	pool.Put(p2, p3, p4)
	expect.Equal(t, pool.packetsAvailable(), 3)

	p5 := pool.Get(400)
	expect.Equal(t, len(p5), 400)
	expect.Equal(t, pool.packetsAvailable(), 2)

	p6 := pool.Get(70)
	expect.Equal(t, len(p6), 70)
	expect.Equal(t, pool.packetsAvailable(), 2)

	p7 := pool.Clone(p6)
	expect.Equal(t, len(p7), len(p6))
	expect.Equal(t, pool.packetsAvailable(), 2)

	pool.Put(p5, p6, p7)
	expect.Equal(t, pool.packetsAvailable(), 5)

	pool.Put(nil)
	expect.Equal(t, pool.packetsAvailable(), 5)
}

var addrMutex = sync.Mutex{}
var lastAddrSuffix int

func getAddrPair() (string, string) {
	addrMutex.Lock()
	defer addrMutex.Unlock()

	if lastAddrSuffix >= 5000 {
		lastAddrSuffix = 0
	}

	addr1 := fmt.Sprintf("127.0.0.1:%d", 60_000+lastAddrSuffix+1)
	addr2 := fmt.Sprintf("127.0.0.1:%d", 60_000+lastAddrSuffix+2)

	lastAddrSuffix += 2

	return addr1, addr2
}

func newConnectionPair(tb testing.TB, ctx context.Context, addr1, addr2 string, opts1, opts2 []OptFunc) (*Conn, *Conn) {
	tb.Helper()

	ctx, cancel := context.WithCancel(ctx)
	tb.Cleanup(cancel)

	var conn1, conn2 *Conn

	connErr1 := make(chan error, 1)
	connErr2 := make(chan error, 1)

	sharedOpts := []OptFunc{
		WithConnectionRefusedCooldown(10 * time.Millisecond),
		WithReceiveAckTimeout(10 * time.Millisecond),
		WithSendQueueFullBehavior("block"),
		WithSendQueueSize(ReceiveQueueSize),
		WithResendTries(10),
	}

	go func() {
		opts := slices.Clone(sharedOpts)
		opts = append(opts, WithLocalAddr(addr1))
		opts = append(opts, opts1...)

		var err error
		conn1, err = Dial(ctx, addr2, opts...)

		if err == nil {
			tb.Cleanup(func() {
				conn1.Close()
				<-conn1.Done()
				if err := conn1.Err(); !errors.Is(err, context.Canceled) && !errors.Is(err, ErrPeerClosed) {
					slog.Error("conn1", "err", err)
				}
			})
			tb.Logf("new connection (conn1) laddr=%s raddr=%s", addr1, addr2)
		}
		connErr1 <- err
	}()
	go func() {
		opts := slices.Clone(sharedOpts)
		opts = append(opts, WithLocalAddr(addr2))
		opts = append(opts, opts2...)

		var err error
		conn2, err = Dial(ctx, addr1, opts...)

		if err == nil {
			tb.Cleanup(func() {
				conn2.Close()
				<-conn2.Done()
				if err := conn2.Err(); !errors.Is(err, context.Canceled) && !errors.Is(err, ErrPeerClosed) {
					slog.Error("conn2", "err", err)
				}
			})
			tb.Logf("new connection (conn2) laddr=%s raddr=%s", addr2, addr1)
		}

		connErr2 <- err
	}()

	for range 2 {
		select {
		case err := <-connErr1:
			if crossplatform.IsBindError(err) {
				return newConnectionPair(tb, ctx, addr1, addr2, opts1, opts2)
			}
			expect.NoError(tb, err, "conn1")
			if err != nil {
				cancel()
				return nil, nil
			}
		case err := <-connErr2:
			if crossplatform.IsBindError(err) {
				return newConnectionPair(tb, ctx, addr1, addr2, opts1, opts2)
			}
			expect.NoError(tb, err, "conn2")
			if err != nil {
				cancel()
				return nil, nil
			}
		}
	}

	return conn1, conn2
}

func makeTestPacket(buf []byte, kind PacketKind, flags PacketFlag, v byte) Packet {
	builder := NewPacketBuilder(buf, kind, flags)
	builder.WriteUint8(v)
	return builder.Build()
}

type PacketTester struct {
	packets []Packet
	values  []bool
}

func newPacketTester(ammount byte, kind PacketKind, flags PacketFlag) PacketTester {
	packets := make([]Packet, ammount)
	const size = HeaderSize + 1
	bigBuf := make([]byte, size*int(ammount))
	for i := range int(ammount) {
		buf := bigBuf[size*i : size*(i+1)]
		packets[i] = makeTestPacket(buf, kind, flags, byte(i))
	}
	return PacketTester{
		packets: packets,
		values:  make([]bool, ammount),
	}
}

func (p *PacketTester) Packets() iter.Seq[Packet] {
	return func(yield func(packet Packet) bool) {
		for _, packet := range p.packets {
			if !yield(packet) {
				return
			}
		}
	}
}

func checkIDs(ids []byte) error {
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			return fmt.Errorf("wrong packet order: expected %d, got %d", ids[i-1]+1, ids[i])
		}
	}
	return nil
}

func (p *PacketTester) CheckPacketValue(tb testing.TB, packet Packet) error {
	tb.Helper()
	value := packet[HeaderSize]
	if int(value) >= len(p.values) || p.values[value] {
		return fmt.Errorf("wrong packet value: %d", value)
	}
	p.values[value] = true
	return nil
}

var addr1T1, addr2T1 = getAddrPair()

func TestConnection(t *testing.T) {
	t.Parallel()

	_, _ = newConnectionPair(t, context.Background(), addr1T1, addr2T1, nil, nil)
}

var addr1T2, addr2T2 = getAddrPair()

func TestSendAndHandleNoACKParallel(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T2, addr2T2, nil, nil)

	const N = ReceiveQueueSize

	errs := make(chan error, N)

	packetTester := newPacketTester(N, PacketTest1, FlagNone)

	conn1.OnReceive(PacketTest1, func(packet Packet) {
		errs <- packetTester.CheckPacketValue(t, packet)
	})

	for packet := range packetTester.Packets() {
		go conn2.Send(packet)
	}
	for range N {
		if err := <-errs; err != nil {
			t.Fatalf("error: %v", err)
		}
	}
}

var addr1T3, addr2T3 = getAddrPair()

func TestSendAndHandleACKParallel(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T3, addr2T3, nil, nil)

	const N = ReceiveQueueSize

	errs := make(chan error, N*2)

	packetTester := newPacketTester(N, PacketTest1, FlagNeedAck)

	ids := make([]byte, 0, len(packetTester.packets))

	conn1.OnReceive(PacketTest1, func(packet Packet) {
		ids = append(ids, byte(packet.ID()))

		if err := packetTester.CheckPacketValue(t, packet); err != nil {
			errs <- err
			return
		}

		errs <- nil
	})

	for packet := range packetTester.Packets() {
		go func() {
			errs <- <-conn2.Send(packet)
		}()
	}
	for range N * 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if err := checkIDs(ids); err != nil {
		t.Fatal(err)
	}
}

var addr1T4, addr2T4 = getAddrPair()

func TestSendAndHandleACKWithPacketLossParallel(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T4, addr2T4,
		[]OptFunc{withPacketLoss(40), WithResendTries(-1)},
		[]OptFunc{withPacketLoss(10), WithResendTries(-1)},
	)

	const N = ReceiveQueueSize

	errs := make(chan error, N*2)

	packetTester := newPacketTester(N, PacketTest1, FlagNeedAck)

	ids := make([]byte, 0, len(packetTester.packets))

	conn1.OnReceive(PacketTest1, func(packet Packet) {
		ids = append(ids, byte(packet.ID()))

		if err := packetTester.CheckPacketValue(t, packet); err != nil {
			errs <- err
			return
		}

		errs <- nil
	})

	for packet := range packetTester.Packets() {
		go func() {
			errs <- <-conn2.Send(packet)
		}()
	}
	for range N * 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if err := checkIDs(ids); err != nil {
		t.Fatal(err)
	}
}

var addr1T5, addr2T5 = getAddrPair()

func TestReceiveNoACK(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T5, addr2T5, nil, nil)

	const N = ReceiveQueueSize

	errs := make(chan error, N)

	packetTester := newPacketTester(N, PacketTest1, FlagNone)

	for packet := range packetTester.Packets() {
		go func() {
			recv := conn1.Receive(PacketTest1, make([]byte, 16))

			_ = conn2.Send(packet)

			res := <-recv
			if res.Err != nil {
				errs <- fmt.Errorf("error: %w", res.Err)
				return
			}

			if err := packetTester.CheckPacketValue(t, packet); err != nil {
				errs <- err
				return
			}

			errs <- nil
		}()
	}

	for range N {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

var addr1T6, addr2T6 = getAddrPair()

func TestReceiveACK(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T6, addr2T6, nil, nil)

	const N = ReceiveQueueSize

	packetTester := newPacketTester(N, PacketTest1, FlagNeedAck)

	ids := make([]byte, 0, len(packetTester.packets))

	buf := make([]byte, HeaderSize+1)
	for packet := range packetTester.Packets() {
		recv := conn1.Receive(PacketTest1, buf)

		err := <-conn2.Send(packet)
		expect.NoError(t, err)

		res := <-recv
		expect.NoError(t, res.Err, res)
		expect.NoError(t, packetTester.CheckPacketValue(t, res.Packet))

		ids = append(ids, byte(res.Packet.ID()))
	}
	if err := checkIDs(ids); err != nil {
		t.Fatal(err)
	}
}

var addr1T7, addr2T7 = getAddrPair()

func TestReceiveUnexpectedACK(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T7, addr2T7, nil, nil)

	const N = ReceiveQueueSize

	errs := make(chan error, N*2)

	packetTester := newPacketTester(N, PacketTest1, FlagNeedAck)

	ids := make([]byte, 0, len(packetTester.packets))

	conn1.OnReceiveUnexpected(func(packet Packet) {
		ids = append(ids, byte(packet.ID()))

		if err := packetTester.CheckPacketValue(t, packet); err != nil {
			errs <- nil
			return
		}

		errs <- nil
	})

	for packet := range packetTester.Packets() {
		go func() {
			errs <- <-conn2.Send(packet)
		}()
	}
	for range N * 2 {
		expect.NoError(t, <-errs)
	}
	if err := checkIDs(ids); err != nil {
		t.Fatal(err)
	}
}

var addr1T8, addr2T8 = getAddrPair()

func TestReceiveUnexpectedNoACK(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T8, addr2T8, nil, nil)

	const N = ReceiveQueueSize

	errs := make(chan error, N)

	packetTester := newPacketTester(N, PacketTest1, FlagNone)

	conn1.OnReceiveUnexpected(func(packet Packet) {
		if err := packetTester.CheckPacketValue(t, packet); err != nil {
			errs <- err
			return
		}

		errs <- nil
	})

	for packet := range packetTester.Packets() {
		go conn2.Send(packet)
	}

	for range N {
		expect.NoError(t, <-errs)
	}
}

var addr1T9, addr2T9 = getAddrPair()

func TestReceiveFilterPacketKindACK(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T9, addr2T9, nil, nil)

	const N = ReceiveQueueSize / 3

	errs := make(chan error, N*3*2)

	packetTester1 := newPacketTester(N, PacketTest1, FlagNeedAck)
	packetTester2 := newPacketTester(N, PacketTest2, FlagNeedAck)
	packetTester3 := newPacketTester(N, PacketTest3, FlagNeedAck)

	ids := make([]byte, 0, len(packetTester1.packets)+len(packetTester2.packets)+len(packetTester3.packets))

	conn1.OnReceive(PacketTest1, func(packet Packet) {
		ids = append(ids, byte(packet.ID()))

		if err := packetTester1.CheckPacketValue(t, packet); err != nil {
			errs <- fmt.Errorf("(1): %w", err)
			return
		}

		errs <- nil
	})
	conn1.OnReceive(PacketTest2, func(packet Packet) {
		ids = append(ids, byte(packet.ID()))

		if err := packetTester2.CheckPacketValue(t, packet); err != nil {
			errs <- fmt.Errorf("(2): %w", err)
			return
		}

		errs <- nil
	})
	conn1.OnReceiveUnexpected(func(packet Packet) {
		ids = append(ids, byte(packet.ID()))

		if err := packetTester3.CheckPacketValue(t, packet); err != nil {
			errs <- fmt.Errorf("(3): %w", err)
			return
		}

		errs <- nil
	})

	for packet := range packetTester1.Packets() {
		go func() {
			errs <- <-conn2.Send(packet)
		}()
	}
	for packet := range packetTester2.Packets() {
		go func() {
			errs <- <-conn2.Send(packet)
		}()
	}
	for packet := range packetTester3.Packets() {
		go func() {
			errs <- <-conn2.Send(packet)
		}()
	}

	for range N * 3 * 2 {
		expect.NoError(t, <-errs)
	}
	if err := checkIDs(ids); err != nil {
		t.Fatal(err)
	}
}

var addr1T10, addr2T10 = getAddrPair()

func TestReceiveFilterPacketKindNoACK(t *testing.T) {
	t.Parallel()

	conn1, conn2 := newConnectionPair(t, context.Background(), addr1T10, addr2T10, nil, nil)

	const N = ReceiveQueueSize / 3

	errs := make(chan error, N*3)

	packetTester1 := newPacketTester(N, PacketTest1, FlagNone)
	packetTester2 := newPacketTester(N, PacketTest2, FlagNone)
	packetTester3 := newPacketTester(N, PacketTest3, FlagNone)

	conn1.OnReceive(PacketTest1, func(packet Packet) {
		if err := packetTester1.CheckPacketValue(t, packet); err != nil {
			errs <- fmt.Errorf("(1): %w", err)
			return
		}
		errs <- nil
	})
	conn1.OnReceive(PacketTest2, func(packet Packet) {
		if err := packetTester2.CheckPacketValue(t, packet); err != nil {
			errs <- fmt.Errorf("(2): %w", err)
			return
		}
		errs <- nil
	})
	conn1.OnReceiveUnexpected(func(packet Packet) {
		if err := packetTester3.CheckPacketValue(t, packet); err != nil {
			errs <- fmt.Errorf("(3): %w", err)
			return
		}
		errs <- nil
	})

	for packet := range packetTester1.Packets() {
		go conn2.Send(packet)
	}
	for packet := range packetTester2.Packets() {
		go conn2.Send(packet)
	}
	for packet := range packetTester3.Packets() {
		go conn2.Send(packet)
	}

	for range N * 3 {
		expect.NoError(t, <-errs)
	}
}

var addr1T11, addr2T11 = getAddrPair()

func TestReceiveEdgeCases(t *testing.T) {
	t.Parallel()

	conn1, _ := newConnectionPair(t, context.Background(), addr1T11, addr2T11, nil, nil)

	testcases := [][]byte{
		nil,
		{0},
		func() []byte {
			packet := NewPacketBuilder(make([]byte, HeaderSize), PacketTest1, FlagNone).Build()
			packet[oVersion] = Version + 1
			return packet
		}(),
		NewPacketBuilder(make([]byte, MaxPacketSize), PacketTest1, FlagNone).Build(),
		NewPacketBuilder(make([]byte, MaxPacketSize+1), PacketTest1, FlagNone).Build(),
	}

	for i, b := range testcases {
		n, err := conn1.conn.Conn.Write(b)
		expect.NoError(t, err, i)
		expect.Equal(t, n, len(b), i)
	}

	time.Sleep(10 * time.Millisecond) // give conn2 time to process the packets
}

// Useless
func BenchmarkReceiveNoACK(b *testing.B) {
	const (
		addr1 = "127.0.0.1:61000"
		addr2 = "127.0.0.1:62000"
	)

	var conn1, conn2 *Conn

	connected := make(chan error)
	go func() {
		var err error
		conn1, err = Dial(
			context.Background(),
			addr2,
			WithLocalAddr(addr1),
			WithConnectionRefusedCooldown(time.Millisecond),
			WithSendQueueFullBehavior("block"),
			WithSendQueueSize(ReceiveQueueSize),
		)
		connected <- err
	}()

	var err error
	conn2, err = Dial(
		context.Background(),
		addr1,
		WithLocalAddr(addr2),
		WithConnectionRefusedCooldown(time.Millisecond),
		WithSendQueueFullBehavior("block"),
		WithSendQueueSize(ReceiveQueueSize),
	)
	assert.NoError(err)
	b.Cleanup(conn2.Close)

	err = <-connected
	assert.NoError(err)
	b.Cleanup(conn1.Close)

	packet := NewPacketBuilder(make([]byte, 512), PacketTest1, FlagNone).Build()

	buf := make([]byte, 512)
	for b.Loop() {
		res := conn2.Receive(PacketTest1, buf)
		conn1.Send(packet)
		<-res
	}
}

// Useless
func BenchmarkTCP(b *testing.B) {
	var (
		addr1 = "127.0.0.1:61000"
		addr2 = "127.0.0.1:62000"
	)

	resolve := func(addr string) *net.TCPAddr {
		a, _ := net.ResolveTCPAddr("tcp4", addr)
		return a
	}

	ln, err := net.ListenTCP("tcp4", resolve(addr2))
	assert.NoError(err)
	b.Cleanup(func() { _ = ln.Close() })

	conn1, err := net.DialTCP("tcp4", resolve(addr1), resolve(addr2))
	assert.NoError(err)
	b.Cleanup(func() { _ = conn1.Close() })

	conn2, err := ln.AcceptTCP()
	assert.NoError(err)
	b.Cleanup(func() { _ = conn2.Close() })

	packet := NewPacketBuilder(make([]byte, 512), PacketTest1, FlagNone).Build()

	buf := make([]byte, 512)
	for b.Loop() {
		n, err := conn1.Write(packet)
		assert.NoError(err)
		assert.Equal(n, len(packet))

		n, err = conn2.Read(buf)
		assert.NoError(err)
		assert.Equal(n, len(packet))
	}
}

// Useless
func BenchmarkUDP(b *testing.B) {
	var (
		addr1 = "127.0.0.1:61000"
		addr2 = "127.0.0.1:62000"
	)

	resolve := func(addr string) *net.UDPAddr {
		a, _ := net.ResolveUDPAddr("udp4", addr)
		return a
	}

	conn1, err := net.DialUDP("udp4", resolve(addr1), resolve(addr2))
	assert.NoError(err)
	b.Cleanup(func() { _ = conn1.Close() })

	conn2, err := net.DialUDP("udp4", resolve(addr2), resolve(addr1))
	assert.NoError(err)
	b.Cleanup(func() { _ = conn2.Close() })

	packet := NewPacketBuilder(make([]byte, 512), PacketTest1, FlagNone).Build()

	buf := make([]byte, 512)
	for b.Loop() {
		n, err := conn1.Write(packet)
		assert.NoError(err)
		assert.Equal(n, len(packet))

		n, err = conn2.Read(buf)
		assert.NoError(err)
		assert.Equal(n, len(packet))
	}
}
