package p2p

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/MarcosTypeAP/go-p2p/internal/assert"
)

type PacketFlag byte

const (
	FlagNone PacketFlag = 1 << iota >> 1
	// Indicates that the receiver needs to send an acknowledgment.
	FlagNeedAck
)

type PacketKind uint16

const (
	PacketInvalid PacketKind = iota

	// Represents and acknowledgment.
	PacketAck
	// Tells the receiving peer to close the connection.
	PacketReset
	PacketPing
	// Used to keep a "hole" open in the peer's NAT.
	PacketKeepAlive

	// Used to register new packet kinds with iota.
	// It is not a valid packet kind in itself.
	//
	//Usage:
	//
	//    const (
	//        PacketA = p2p.PacketCustom + iota
	//        PacketB
	//        PacketC
	//        ...
	//    )
	PacketCustom
)

var packetKindNamesArr = []string{
	PacketInvalid:   "INVALID",
	PacketAck:       "ACK",
	PacketReset:     "RST",
	PacketPing:      "PING",
	PacketKeepAlive: "KEEPALIVE",
}
var packetKindNamesMap = map[PacketKind]string{}

// RegisterPacketKindName sets the name for a packet kind to be retrieved with PacketKind.String().
//
// This function should only be used for packets without the //p2p:packet directive.
// Panics if the packet kind was already registered.
func RegisterPacketKindName(kind PacketKind, name string) {
	if int(kind) < len(packetKindNamesArr) {
		if packetKindNamesArr[kind] != "" {
			panic(fmt.Sprintf("name already registered for packet kind %d", kind))
		}
		packetKindNamesArr[kind] = name
		return
	}
	if _, ok := packetKindNamesMap[kind]; ok {
		panic(fmt.Sprintf("name already registered for packet kind %d", kind))
	}
	packetKindNamesMap[kind] = name
}

func (k PacketKind) String() string {
	if int(k) < len(packetKindNamesArr) && packetKindNamesArr[k] != "" {
		return packetKindNamesArr[k]
	}
	if name, ok := packetKindNamesMap[k]; ok {
		return name
	}
	return "kind(" + fmt.Sprint(uint16(k)) + ")"
}

type PacketID = uint32

const (
	Version = 0

	MaxPacketSize = 4096
)

// Size
const (
	sVersion = 1
	sKind    = 2
	sFlags   = 1
	sID      = 4
)

// Offset
const (
	oVersion = 0
	oKind    = oVersion + sVersion
	oFlags   = oKind + sKind
	oID      = oFlags + sFlags
)

const HeaderSize = sVersion + sKind + sFlags + sID

// | version | kind | flags | id | payload |
type Packet []byte

func (p Packet) Validate() error {
	if len(p) < HeaderSize {
		return fmt.Errorf("too short: min %d, got %d", HeaderSize, len(p))
	}
	if len(p) > MaxPacketSize {
		return fmt.Errorf("too long: max %d, got %d", MaxPacketSize, len(p))
	}
	if p.Version() != Version {
		return fmt.Errorf("version mismatch: expected %d, got %d", Version, p.Version())
	}
	if p.Kind() == PacketInvalid {
		return fmt.Errorf("invalid packet kind")
	}
	return nil
}

func init() {
	assert.Equal(sVersion, 1)
}
func (p Packet) Version() int {
	return int(p[oVersion])
}
func (p Packet) setVersion() {
	p[oVersion] = Version
}

func init() {
	assert.Equal(sKind, 2)
}
func (p Packet) Kind() PacketKind {
	return PacketKind(binary.BigEndian.Uint16(p[oKind : oKind+sKind]))
}
func (p Packet) setKind(kind PacketKind) {
	binary.BigEndian.PutUint16(p[oKind:oKind+sKind], uint16(kind))
}

func init() {
	assert.Equal(sFlags, 1)
}
func (p Packet) Flags() PacketFlag {
	return PacketFlag(p[oFlags])
}
func (p Packet) HasFlags(flags PacketFlag) bool {
	return PacketFlag(p[oFlags])&flags == flags
}
func (p Packet) setFlags(flags PacketFlag) {
	p[3] = byte(flags)
}

func init() {
	assert.Equal(sID, 4)
}
func (p Packet) ID() uint32 {
	return binary.BigEndian.Uint32(p[oID : oID+sID])
}
func (p Packet) setID(id uint32) {
	binary.BigEndian.PutUint32(p[oID:oID+sID], id)
}

func (p Packet) Payload() []byte {
	return p[HeaderSize:]
}

func (p Packet) String() string {
	if p.Validate() != nil {
		if len(p) >= HeaderSize {
			return fmt.Sprintf("(invalid packet)%v(size=%d)", []byte(p)[:HeaderSize], len(p))
		}
		return fmt.Sprintf("(invalid packet)%v(size=%d)", []byte(p), len(p))
	}
	return fmt.Sprintf("[id=%d kind=%s flags=%08b size=%d]", p.ID(), p.Kind(), p.Flags(), len(p))
}

// A PacketBuilder helps to build a packet payload by keeping track of the write offset.
type PacketBuilder struct {
	buf []byte
	// Write offset
	wo int
}

// NewPacketBuilder creates a new PacketBuilder and initializes the packet header.
func NewPacketBuilder(buf []byte, kind PacketKind, flags PacketFlag) PacketBuilder {
	Packet(buf).setVersion()
	Packet(buf).setKind(kind)
	Packet(buf).setFlags(flags)
	Packet(buf).setID(0)

	return PacketBuilder{
		buf: buf,
		wo:  HeaderSize,
	}
}

// Build returns the built packet.
//
// Panics if the written payload is larger than MaxPacketSize.
func (p PacketBuilder) Build() Packet {
	assert.LessEqual(p.wo, MaxPacketSize, fmt.Errorf("exceeded max packet size: max %d, got %d", MaxPacketSize, p.wo))
	return p.buf[:p.wo]
}

// Reset deletes the payload.
// The header is left untouched.
func (p *PacketBuilder) Reset() {
	p.wo = HeaderSize
}

// WriteBool writes a bool value to the payload.
func (p *PacketBuilder) WriteBool(v bool) {
	if v {
		p.buf[p.wo] = 1
	} else {
		p.buf[p.wo] = 0
	}
	p.wo += 1
}

// WriteUint writes a uint value to the payload.
func (p *PacketBuilder) WriteUint(v uint) {
	binary.BigEndian.PutUint64(p.buf[p.wo:], uint64(v))
	p.wo += 8
}

// WriteUint8 writes a uint8 value to the payload.
func (p *PacketBuilder) WriteUint8(v uint8) {
	p.buf[p.wo] = v
	p.wo += 1
}

// WriteUint16 writes a uint16 value to the payload.
func (p *PacketBuilder) WriteUint16(v uint16) {
	binary.BigEndian.PutUint16(p.buf[p.wo:], v)
	p.wo += 2
}

// WriteUint32 writes a uint32 value to the payload.
func (p *PacketBuilder) WriteUint32(v uint32) {
	binary.BigEndian.PutUint32(p.buf[p.wo:], v)
	p.wo += 4
}

// WriteUint64 writes a uint64 value to the payload.
func (p *PacketBuilder) WriteUint64(v uint64) {
	binary.BigEndian.PutUint64(p.buf[p.wo:], v)
	p.wo += 8
}

// WriteInt writes a int value to the payload.
func (p *PacketBuilder) WriteInt(v int) {
	p.WriteUint64(uint64(v))
}

// WriteInt8 writes a int8 value to the payload.
func (p *PacketBuilder) WriteInt8(v int8) {
	p.WriteUint8(uint8(v))
}

// WriteInt16 writes a int16 value to the payload.
func (p *PacketBuilder) WriteInt16(v int16) {
	p.WriteUint16(uint16(v))
}

// WriteInt32 writes a int32 value to the payload.
func (p *PacketBuilder) WriteInt32(v int32) {
	p.WriteUint32(uint32(v))
}

// WriteInt64 writes a int64 value to the payload.
func (p *PacketBuilder) WriteInt64(v int64) {
	p.WriteUint64(uint64(v))
}

// WriteFloat writes a float value to the payload.
func (p *PacketBuilder) WriteFloat(v float64) {
	p.WriteUint64(math.Float64bits(v))
}

// WriteFloat32 writes a float32 value to the payload.
func (p *PacketBuilder) WriteFloat32(v float32) {
	p.WriteUint32(math.Float32bits(v))
}

// WriteFloat64 writes a float64 value to the payload.
func (p *PacketBuilder) WriteFloat64(v float64) {
	p.WriteUint64(math.Float64bits(v))
}

// WriteRune writes a rune value to the payload.
func (p *PacketBuilder) WriteRune(v rune) {
	p.WriteUint32(uint32(v))
}

// WriteString writes a string value to the payload.
func (p *PacketBuilder) WriteString(s string) {
	p.WriteUint16(uint16(len(s)))

	n := copy(p.buf[p.wo:p.wo+len(s)], s)
	assert.Equal(n, len(s))

	p.wo += n
}

// WriteBytes writes a []byte value to the payload.
func (p *PacketBuilder) WriteBytes(b []byte) {
	p.WriteUint16(uint16(len(b)))

	n := copy(p.buf[p.wo:p.wo+len(b)], b)
	assert.Equal(n, len(b))

	p.wo += n
}

// WriteAngle writes an angle value to the payload.
//
// An angle is 2 bytes long and has 3 decimal places of precision.
// Allowed angle range:
//
//	[-PI*2, PI*2]
func (p *PacketBuilder) WriteAngle(v float32) {
	if !(-math.Pi*2 <= v && v <= math.Pi*2) {
		panic(fmt.Errorf("invalid angle range: expected [-PI*2, PI*2], got %f", v))
	}

	if v < 0 {
		p.WriteUint16(1<<15 | uint16(v*1000*-1))
		return
	}
	p.WriteUint16(uint16(v * 1000))
}

// A PacketParser helps parse a packet by keeping track of the read offset and delaying any errors until the end.
type PacketParser struct {
	packet Packet
	// Read offset
	ro  int
	err error
}

// NewPacketParser creates a new PacketParser and performs some validations.
func NewPacketParser(packet Packet) PacketParser {
	return PacketParser{
		packet: packet,
		ro:     HeaderSize,
		err:    packet.Validate(),
	}
}

// SetErr sets an error in the parser.
//
// Only the first call will set an error.
func (p *PacketParser) SetErr(err error) {
	if p.err == nil {
		p.err = fmt.Errorf("%w: offset=%d", err, p.ro)
	}
}

func (p *PacketParser) hasBytes(size int) bool {
	if len(p.packet)-p.ro < size {
		p.SetErr(fmt.Errorf("missing data"))
		return false
	}
	return true
}

// Err returns the first error set.
func (p *PacketParser) Err() error {
	return p.err
}

// ParseBool parses a bool value from the payload.
func (p *PacketParser) ParseBool() bool {
	return p.ParseUint8() != 0
}

// ParseUint parses a uint value from the payload.
func (p *PacketParser) ParseUint() uint {
	return uint(p.ParseUint64())
}

// ParseUint8 parses a uint8 value from the payload.
func (p *PacketParser) ParseUint8() uint8 {
	if !p.hasBytes(1) {
		return 0
	}
	v := p.packet[p.ro]
	p.ro += 1
	return v
}

// ParseUint16 parses a uint16 value from the payload.
func (p *PacketParser) ParseUint16() uint16 {
	if !p.hasBytes(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(p.packet[p.ro:])
	p.ro += 2
	return v
}

// ParseUint32 parses a uint32 value from the payload.
func (p *PacketParser) ParseUint32() uint32 {
	if !p.hasBytes(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(p.packet[p.ro:])
	p.ro += 4
	return v
}

// ParseUint64 parses a uint64 value from the payload.
func (p *PacketParser) ParseUint64() uint64 {
	if !p.hasBytes(8) {
		return 0
	}
	v := binary.BigEndian.Uint64(p.packet[p.ro:])
	p.ro += 8
	return v
}

// ParseInt parses a int value from the payload.
func (p *PacketParser) ParseInt() int {
	return int(p.ParseUint64())
}

// ParseInt8 parses a int8 value from the payload.
func (p *PacketParser) ParseInt8() int8 {
	return int8(p.ParseUint8())
}

// ParseInt16 parses a int16 value from the payload.
func (p *PacketParser) ParseInt16() int16 {
	return int16(p.ParseUint16())
}

// ParseInt32 parses a int32 value from the payload.
func (p *PacketParser) ParseInt32() int32 {
	return int32(p.ParseUint32())
}

// ParseInt64 parses a int64 value from the payload.
func (p *PacketParser) ParseInt64() int64 {
	return int64(p.ParseUint64())
}

// ParseFloat parses a float value from the payload.
func (p *PacketParser) ParseFloat() float64 {
	return math.Float64frombits(p.ParseUint64())
}

// ParseFloat32 parses a float32 value from the payload.
func (p *PacketParser) ParseFloat32() float32 {
	return math.Float32frombits(p.ParseUint32())
}

// ParseFloat64 parses a float64 value from the payload.
func (p *PacketParser) ParseFloat64() float64 {
	return math.Float64frombits(p.ParseUint64())
}

// ParseRune parses a rune value from the payload.
func (p *PacketParser) ParseRune() rune {
	return rune(p.ParseUint32())
}

// ParseString parses a string value from the payload.
func (p *PacketParser) ParseString() string {
	length := int(p.ParseUint16())
	if length == 0 || !p.hasBytes(length) {
		return ""
	}
	s := string(p.packet[p.ro : p.ro+length])
	p.ro += length
	return s
}

// ParseBytes parses a []byte value from the payload.
//
// This does not make a copy.
// Sounds goofy ngl
func (p *PacketParser) ParseBytes() []byte {
	length := int(p.ParseUint16())
	if length == 0 || !p.hasBytes(length) {
		return nil
	}
	b := p.packet[p.ro : p.ro+length]
	p.ro += length
	return b
}

// ParseAngle parses an angle value from the payload.
//
// An angle is 2 bytes long and has 3 decimal places of precision.
// Allowed angle range:
//
//	[-PI*2, PI*2]
func (p *PacketParser) ParseAngle() float32 {
	v := p.ParseUint16()
	// (1 - (0|1)*2) * uint15(v) / 1000
	return float32(1-((int(v)&(1<<15))>>15)*2) * float32(v&(1<<15-1)) / 1000
}

// GetBytes returns the next n bytes from payload.
func (p *PacketParser) GetBytes(n int) []byte {
	if !p.hasBytes(n) {
		return nil
	}
	b := p.packet[p.ro : p.ro+n]
	p.ro += n
	return b
}
