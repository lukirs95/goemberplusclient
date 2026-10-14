package glow

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Message is the content of a Glow root: Elements or Streams.
type Message interface {
	isMessage()
}

// Elements is a flat list of elements with absolute paths. Parents come
// before their children.
type Elements []Element

// Streams holds the values of subscribed stream parameters.
type Streams []StreamEntry

func (Elements) isMessage() {}
func (Streams) isMessage()  {}

type StreamEntry struct {
	Identifier int32
	Value      Value
}

// StreamDescription locates a parameter value inside an octet stream entry
// shared by several parameters (spec: "StreamDescriptor").
type StreamDescription struct {
	Format StreamFormat
	Offset int32
}

// Extract reads the described value from the octets of a stream entry.
// Integers are returned as Int, floats as Real. Unsigned 64-bit values above
// math.MaxInt64 wrap around.
func (d StreamDescription) Extract(octets []byte) (Value, error) {
	kind := d.Format >> 3
	size := 1 << (d.Format >> 1 & 0x03)
	var order binary.ByteOrder = binary.BigEndian
	if d.Format&1 != 0 {
		order = binary.LittleEndian
	}
	if kind > 2 || (kind == 2 && size < 4) || (size == 1 && d.Format&1 != 0) {
		return nil, fmt.Errorf("glow: unknown stream format %d", d.Format)
	}
	if d.Offset < 0 || int(d.Offset)+size > len(octets) {
		return nil, fmt.Errorf("glow: stream value at offset %d exceeds %d octets", d.Offset, len(octets))
	}
	b := octets[d.Offset:]

	var u uint64
	switch size {
	case 1:
		u = uint64(b[0])
	case 2:
		u = uint64(order.Uint16(b))
	case 4:
		u = uint64(order.Uint32(b))
	case 8:
		u = order.Uint64(b)
	}
	switch kind {
	case 0:
		return Int(u), nil
	case 1:
		shift := 64 - 8*size
		return Int(int64(u<<shift) >> shift), nil
	}
	if size == 4 {
		return Real(math.Float32frombits(uint32(u))), nil
	}
	return Real(math.Float64frombits(u)), nil
}
