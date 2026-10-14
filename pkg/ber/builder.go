package ber

import "fmt"

// Builder encodes BER with definite lengths into a reusable buffer. The first
// error is kept and returned by Bytes; later calls are ignored. The zero
// value is ready to use.
type Builder struct {
	buf []byte
	err error
}

// Reset clears the Builder and keeps its buffer.
func (b *Builder) Reset() {
	b.buf = b.buf[:0]
	b.err = nil
}

// Bytes returns the encoded data. It is valid until the next call to Reset.
func (b *Builder) Bytes() ([]byte, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.buf, nil
}

// Constructed writes a constructed element with tag, whose contents are
// written by f. The length is filled in after f returns.
func (b *Builder) Constructed(tag Tag, f func(*Builder)) {
	pos, ok := b.begin(tag, true)
	if !ok {
		return
	}
	f(b)
	b.end(pos)
}

// begin writes the tag and reserves one byte for the length at the returned
// position.
func (b *Builder) begin(tag Tag, constructed bool) (int, bool) {
	if b.err != nil {
		return 0, false
	}
	if !tag.valid() {
		b.err = fmt.Errorf("%w: tag %v", ErrInvalid, tag)
		return 0, false
	}
	b.buf = appendTag(b.buf, tag, constructed)
	b.buf = append(b.buf, 0)
	return len(b.buf) - 1, true
}

// end fills in the length of the contents following pos. If the long form
// is needed, the contents are moved behind it.
func (b *Builder) end(pos int) {
	if b.err != nil {
		return
	}
	n := len(b.buf) - pos - 1
	if n < 0x80 {
		b.buf[pos] = byte(n)
		return
	}
	size := lengthSize(n)
	for range size {
		b.buf = append(b.buf, 0)
	}
	copy(b.buf[pos+1+size:], b.buf[pos+1:pos+1+n])
	// Writes the length in place: b.buf is long enough, so no reallocation.
	appendLength(b.buf[:pos], n)
}

func (b *Builder) Bool(v bool) {
	if pos, ok := b.begin(TagBoolean, false); ok {
		if v {
			b.buf = append(b.buf, 0xFF)
		} else {
			b.buf = append(b.buf, 0x00)
		}
		b.end(pos)
	}
}

// Int writes an INTEGER in its minimal form.
func (b *Builder) Int(v int64) {
	if pos, ok := b.begin(TagInteger, false); ok {
		b.buf = appendIntBytes(b.buf, v, intSize(v))
		b.end(pos)
	}
}

func (b *Builder) Real(v float64) {
	if pos, ok := b.begin(TagReal, false); ok {
		b.buf = appendReal(b.buf, v)
		b.end(pos)
	}
}

func (b *Builder) UTF8(v string) {
	if pos, ok := b.begin(TagUTF8String, false); ok {
		b.buf = append(b.buf, v...)
		b.end(pos)
	}
}

func (b *Builder) Octets(v []byte) {
	if pos, ok := b.begin(TagOctetString, false); ok {
		b.buf = append(b.buf, v...)
		b.end(pos)
	}
}

func (b *Builder) Null() {
	if pos, ok := b.begin(TagNull, false); ok {
		b.end(pos)
	}
}

func (b *Builder) RelativeOID(v []uint32) {
	if pos, ok := b.begin(TagRelativeOID, false); ok {
		for _, c := range v {
			b.buf = appendBase128(b.buf, c)
		}
		b.end(pos)
	}
}

// intSize returns the minimal number of bytes of v in two's complement.
func intSize(v int64) int {
	n := 1
	for n < 8 && (v < -1<<(8*n-1) || v >= 1<<(8*n-1)) {
		n++
	}
	return n
}

func appendIntBytes(b []byte, v int64, size int) []byte {
	for i := size - 1; i >= 0; i-- {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}
