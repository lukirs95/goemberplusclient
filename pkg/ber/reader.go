package ber

import (
	"fmt"
	"io"
	"math"
)

// Reader reads consecutive elements from a byte slice. The zero value is an
// empty Reader. Reader does not copy its input; all returned slices point into
// it.
type Reader struct {
	rest []byte
}

func NewReader(data []byte) Reader {
	return Reader{rest: data}
}

// More reports whether another element follows.
func (r *Reader) More() bool {
	return len(r.rest) > 0
}

// TLV is a single BER element.
type TLV struct {
	Tag         Tag
	Constructed bool
	// Value holds the contents octets. For the indefinite length form, the
	// end-of-contents marker is not included.
	Value []byte
}

// Next reads the next element. It returns io.EOF when no element is left.
// On error, the Reader is left unchanged.
func (r *Reader) Next() (TLV, error) {
	if len(r.rest) == 0 {
		return TLV{}, io.EOF
	}
	tag, constructed, length, off, err := parseHeader(r.rest)
	if err != nil {
		return TLV{}, err
	}
	if tag == Universal(0) {
		return TLV{}, fmt.Errorf("%w: unexpected end-of-contents", ErrInvalid)
	}
	data := r.rest[off:]
	end := length
	if length == lengthIndefinite {
		if end, err = findEOC(data); err != nil {
			return TLV{}, err
		}
		r.rest = data[end+2:]
	} else {
		if length > len(data) {
			return TLV{}, ErrTruncated
		}
		r.rest = data[length:]
	}
	return TLV{Tag: tag, Constructed: constructed, Value: data[:end:end]}, nil
}

// parseHeader parses tag and length and returns the header size.
func parseHeader(data []byte) (tag Tag, constructed bool, length, size int, err error) {
	tag, constructed, n, err := parseTag(data)
	if err != nil {
		return
	}
	length, m, err := parseLength(data[n:])
	if err != nil {
		return
	}
	if length == lengthIndefinite && !constructed {
		err = fmt.Errorf("%w: indefinite length on primitive element", ErrInvalid)
		return
	}
	size = n + m
	return
}

// findEOC returns the length of indefinite length contents, i.e. the offset
// of their end-of-contents marker. It counts nesting levels instead of
// recursing, so the stack usage is constant for any input.
func findEOC(data []byte) (int, error) {
	depth := 1
	off := 0
	for {
		tag, _, length, n, err := parseHeader(data[off:])
		if err != nil {
			return 0, err
		}
		if tag == Universal(0) {
			if data[off] != 0 || n != 2 || length != 0 {
				return 0, fmt.Errorf("%w: malformed end-of-contents", ErrInvalid)
			}
			depth--
			if depth == 0 {
				return off, nil
			}
			off += n
			continue
		}
		off += n
		if length == lengthIndefinite {
			depth++
			continue
		}
		if length > len(data)-off {
			return 0, ErrTruncated
		}
		off += length
	}
}

// Children returns a Reader over the contents of a constructed element.
func (t TLV) Children() (Reader, error) {
	if !t.Constructed {
		return Reader{}, fmt.Errorf("%w: %v is not constructed", ErrInvalid, t.Tag)
	}
	return NewReader(t.Value), nil
}

// Explicit unwraps an explicitly tagged value: the contents of t must consist
// of exactly one element, which is returned.
//
// The constructed bit of t is not checked. Ember+ encoders set it, but the
// encoding example in the specification (spec: "The BER namespace") does
// not.
func (t TLV) Explicit() (TLV, error) {
	r := NewReader(t.Value)
	inner, err := r.Next()
	if err == io.EOF {
		return TLV{}, fmt.Errorf("%w: empty explicit tag %v", ErrInvalid, t.Tag)
	}
	if err != nil {
		return TLV{}, err
	}
	if r.More() {
		return TLV{}, fmt.Errorf("%w: explicit tag %v contains more than one element", ErrInvalid, t.Tag)
	}
	return inner, nil
}

func (t TLV) primitive(want Tag) error {
	if t.Tag != want {
		return &TagError{Want: want, Got: t.Tag}
	}
	if t.Constructed {
		return fmt.Errorf("%w: constructed %v", ErrInvalid, want)
	}
	return nil
}

func (t TLV) Bool() (bool, error) {
	if err := t.primitive(TagBoolean); err != nil {
		return false, err
	}
	if len(t.Value) != 1 {
		return false, fmt.Errorf("%w: BOOLEAN of length %d", ErrInvalid, len(t.Value))
	}
	return t.Value[0] != 0, nil
}

// Int decodes an INTEGER of at most 64 bits. Non-minimal encodings are
// accepted.
func (t TLV) Int() (int64, error) {
	if err := t.primitive(TagInteger); err != nil {
		return 0, err
	}
	v, ok := parseInt(t.Value)
	if !ok {
		return 0, fmt.Errorf("%w: INTEGER of length %d", ErrInvalid, len(t.Value))
	}
	return v, nil
}

func parseInt(b []byte) (int64, bool) {
	if len(b) == 0 || len(b) > 8 {
		return 0, false
	}
	v := int64(int8(b[0]))
	for _, c := range b[1:] {
		v = v<<8 | int64(c)
	}
	return v, true
}

// Int32 decodes an INTEGER in the range of Integer32.
func (t TLV) Int32() (int32, error) {
	v, err := t.Int()
	if err != nil {
		return 0, err
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("%w: INTEGER %d out of 32-bit range", ErrInvalid, v)
	}
	return int32(v), nil
}

func (t TLV) Real() (float64, error) {
	if err := t.primitive(TagReal); err != nil {
		return 0, err
	}
	return parseReal(t.Value)
}

// UTF8 returns the string value. The contents are not validated as UTF-8.
func (t TLV) UTF8() (string, error) {
	if err := t.primitive(TagUTF8String); err != nil {
		return "", err
	}
	return string(t.Value), nil
}

// Octets returns the contents of an OCTET STRING. The result points into the
// input.
func (t TLV) Octets() ([]byte, error) {
	if err := t.primitive(TagOctetString); err != nil {
		return nil, err
	}
	return t.Value, nil
}

func (t TLV) Null() error {
	if err := t.primitive(TagNull); err != nil {
		return err
	}
	if len(t.Value) != 0 {
		return fmt.Errorf("%w: NULL of length %d", ErrInvalid, len(t.Value))
	}
	return nil
}

// RelativeOID appends the components of a RELATIVE-OID to dst.
func (t TLV) RelativeOID(dst []uint32) ([]uint32, error) {
	if err := t.primitive(TagRelativeOID); err != nil {
		return dst, err
	}
	for data := t.Value; len(data) > 0; {
		v, n, err := parseBase128(data)
		if err == ErrTruncated {
			err = fmt.Errorf("%w: incomplete RELATIVE-OID component", ErrInvalid)
		}
		if err != nil {
			return dst, err
		}
		dst = append(dst, v)
		data = data[n:]
	}
	return dst, nil
}
