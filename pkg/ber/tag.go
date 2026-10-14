// Package ber implements the subset of the Basic Encoding Rules (X.690) used
// by Ember+ (EmBER).
package ber

import (
	"errors"
	"fmt"
)

var (
	ErrTruncated   = errors.New("ber: truncated data")
	ErrInvalid     = errors.New("ber: invalid encoding")
	ErrUnsupported = errors.New("ber: unsupported encoding")
)

// TagError is returned when an element has a different tag than expected.
type TagError struct {
	Want, Got Tag
}

func (e *TagError) Error() string {
	return fmt.Sprintf("ber: expected %v, got %v", e.Want, e.Got)
}

type Class uint8

const (
	ClassUniversal   Class = 0x00
	ClassApplication Class = 0x40
	ClassContext     Class = 0x80
	ClassPrivate     Class = 0xC0
)

const constructedBit = 0x20

// Tag identifies a BER element, without the constructed bit.
type Tag struct {
	Class  Class
	Number uint32
}

func Universal(n uint32) Tag   { return Tag{ClassUniversal, n} }
func Application(n uint32) Tag { return Tag{ClassApplication, n} }
func Context(n uint32) Tag     { return Tag{ClassContext, n} }

// Universal types used by EmBER.
var (
	TagBoolean     = Universal(1)
	TagInteger     = Universal(2)
	TagOctetString = Universal(4)
	TagNull        = Universal(5)
	TagReal        = Universal(9)
	TagUTF8String  = Universal(12)
	TagRelativeOID = Universal(13)
	TagSequence    = Universal(16)
	TagSet         = Universal(17)
)

var universalNames = map[uint32]string{
	0:  "EOC",
	1:  "BOOLEAN",
	2:  "INTEGER",
	4:  "OCTET STRING",
	5:  "NULL",
	9:  "REAL",
	12: "UTF8String",
	13: "RELATIVE-OID",
	16: "SEQUENCE",
	17: "SET",
}

func (t Tag) String() string {
	switch t.Class {
	case ClassUniversal:
		if name, ok := universalNames[t.Number]; ok {
			return name
		}
		return fmt.Sprintf("UNIVERSAL %d", t.Number)
	case ClassApplication:
		return fmt.Sprintf("APPLICATION %d", t.Number)
	case ClassContext:
		return fmt.Sprintf("CONTEXT %d", t.Number)
	case ClassPrivate:
		return fmt.Sprintf("PRIVATE %d", t.Number)
	}
	return fmt.Sprintf("INVALID CLASS %#02x %d", byte(t.Class), t.Number)
}

func (t Tag) valid() bool {
	return t.Class&^0xC0 == 0 && t != Universal(0)
}

func appendTag(b []byte, t Tag, constructed bool) []byte {
	first := byte(t.Class)
	if constructed {
		first |= constructedBit
	}
	if t.Number < 0x1F {
		return append(b, first|byte(t.Number))
	}
	b = append(b, first|0x1F)
	return appendBase128(b, t.Number)
}

// parseTag returns the tag, the constructed flag and the number of bytes read.
func parseTag(data []byte) (Tag, bool, int, error) {
	if len(data) == 0 {
		return Tag{}, false, 0, ErrTruncated
	}
	t := Tag{Class: Class(data[0] & 0xC0), Number: uint32(data[0] & 0x1F)}
	constructed := data[0]&constructedBit != 0
	if t.Number != 0x1F {
		return t, constructed, 1, nil
	}
	n, size, err := parseBase128(data[1:])
	if err != nil {
		return Tag{}, false, 0, err
	}
	t.Number = n
	return t, constructed, 1 + size, nil
}

// lengthIndefinite marks the indefinite length form.
const lengthIndefinite = -1

// parseLength returns the length (or lengthIndefinite) and the number of
// bytes read. Definite lengths are not checked against the available data.
func parseLength(data []byte) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, ErrTruncated
	}
	first := data[0]
	switch {
	case first < 0x80:
		return int(first), 1, nil
	case first == 0x80:
		return lengthIndefinite, 1, nil
	case first == 0xFF:
		return 0, 0, ErrInvalid
	}
	n := int(first & 0x7F)
	if n > 8 {
		return 0, 0, ErrInvalid
	}
	if len(data) < 1+n {
		return 0, 0, ErrTruncated
	}
	var length uint64
	for _, b := range data[1 : 1+n] {
		length = length<<8 | uint64(b)
	}
	if length > uint64(maxInt) {
		return 0, 0, ErrTruncated
	}
	return int(length), 1 + n, nil
}

const maxInt = int(^uint(0) >> 1)

func appendLength(b []byte, n int) []byte {
	if n < 0x80 {
		return append(b, byte(n))
	}
	size := lengthSize(n)
	b = append(b, 0x80|byte(size))
	for i := size - 1; i >= 0; i-- {
		b = append(b, byte(n>>(8*i)))
	}
	return b
}

// lengthSize returns the number of bytes of the long form length n.
func lengthSize(n int) int {
	size := 0
	for ; n > 0; n >>= 8 {
		size++
	}
	return size
}

func appendBase128(b []byte, v uint32) []byte {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(v & 0x7F)
	for v >>= 7; v > 0; v >>= 7 {
		i--
		tmp[i] = 0x80 | byte(v&0x7F)
	}
	return append(b, tmp[i:]...)
}

// parseBase128 parses one base-128 number of at most 32 bits.
func parseBase128(data []byte) (uint32, int, error) {
	var v uint64
	for i, b := range data {
		v = v<<7 | uint64(b&0x7F)
		if v > 0xFFFFFFFF {
			return 0, 0, ErrInvalid
		}
		if b&0x80 == 0 {
			return uint32(v), i + 1, nil
		}
	}
	return 0, 0, ErrTruncated
}
