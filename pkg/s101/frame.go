// Package s101 implements S101, the framing protocol that transports Ember+
// messages (spec: "Message Framing"). Decoder and Encoder read and write frames of both variants,
// with escaping and CRC or with a length prefix. Assembler and Split join
// and divide messages that span several frames.
package s101

import (
	"bufio"
	"encoding"
	"errors"
	"io"
)

// Framing control bytes (spec: "The S101 protocol").
const (
	BOF = 0xFE
	EOF = 0xFF
	CE  = 0xFD
	XOR = 0x20
	INV = 0xF8
)

const (
	MaxPayloadSize = 1024 // spec: "Message length"
	MaxFrameSize   = 1290 // including framing overhead; default decoder limit
)

type Variant uint8

const (
	VariantEscaping    Variant = 1 // BOF/EOF, escaping, CRC (default)
	VariantNonEscaping Variant = 2 // 0xF8 + length field, no CRC
)

type Command byte

const (
	CommandEmBER             Command = 0x00
	CommandKeepAliveRequest  Command = 0x01
	CommandKeepAliveResponse Command = 0x02
)

type Flags byte

const (
	FlagFirst  Flags = 0x80
	FlagLast   Flags = 0x40
	FlagEmpty  Flags = 0x20
	FlagSingle       = FlagFirst | FlagLast
)

const (
	SlotDefault  byte = 0x00
	MessageEmBER byte = 0x0E
	VersionS101  byte = 0x01
	DTDGlow      byte = 0x01
)

// Recoverable errors: the next ReadFrame resumes at the next frame.
// All other errors come from the underlying io.Reader or io.Writer.
var (
	ErrFrameTooLarge = errors.New("s101: frame too large")
	ErrInvalidFrame  = errors.New("s101: invalid frame")
)

var (
	_ encoding.BinaryAppender    = (*Frame)(nil)
	_ encoding.BinaryMarshaler   = (*Frame)(nil)
	_ encoding.BinaryUnmarshaler = (*Frame)(nil)
)

// Frame is an S101 packet without framing (no BOF/EOF/CRC, not escaped).
// Flags, DTD and AppBytes are only used with CommandEmBER. For any other
// command, Payload holds the bytes following the header (usually none).
type Frame struct {
	Slot     byte
	Message  byte
	Command  Command
	Version  byte
	Flags    Flags
	DTD      byte
	AppBytes []byte
	Payload  []byte
}

func NewKeepAliveRequest() Frame {
	return Frame{Slot: SlotDefault, Message: MessageEmBER, Command: CommandKeepAliveRequest, Version: VersionS101}
}

func NewKeepAliveResponse() Frame {
	return Frame{Slot: SlotDefault, Message: MessageEmBER, Command: CommandKeepAliveResponse, Version: VersionS101}
}

// AppendBinary appends the header and payload of f to b, without framing.
func (f *Frame) AppendBinary(b []byte) ([]byte, error) {
	b = append(b, f.Slot, f.Message, byte(f.Command), f.Version)
	if f.Command != CommandEmBER {
		return append(b, f.Payload...), nil
	}
	if len(f.AppBytes) > 0xFF {
		return b, ErrInvalidFrame
	}
	b = append(b, byte(f.Flags), f.DTD, byte(len(f.AppBytes)))
	b = append(b, f.AppBytes...)
	return append(b, f.Payload...), nil
}

func (f *Frame) MarshalBinary() ([]byte, error) {
	return f.AppendBinary(nil)
}

// UnmarshalBinary decodes header and payload from data. It copies data.
func (f *Frame) UnmarshalBinary(data []byte) error {
	return f.decode(append([]byte(nil), data...))
}

// decode parses data into f. AppBytes and Payload alias data.
func (f *Frame) decode(data []byte) error {
	if len(data) < 4 {
		return ErrInvalidFrame
	}
	*f = Frame{
		Slot:    data[0],
		Message: data[1],
		Command: Command(data[2]),
		Version: data[3],
	}
	if f.Command != CommandEmBER {
		f.Payload = data[4:]
		return nil
	}
	if len(data) < 7 {
		return ErrInvalidFrame
	}
	f.Flags = Flags(data[4])
	f.DTD = data[5]
	n := int(data[6])
	rest := data[7:]
	if len(rest) < n {
		return ErrInvalidFrame
	}
	f.AppBytes = rest[:n:n]
	f.Payload = rest[n:]
	return nil
}

type byteReader interface {
	io.Reader
	io.ByteReader
}

// Decoder reads S101 frames from a stream. It is not safe for concurrent use.
type Decoder struct {
	r       byteReader
	buf     []byte
	max     int
	variant Variant
	cs      checksum
}

// NewDecoder returns a Decoder reading from r. If r does not implement
// io.ByteReader, it is wrapped in a bufio.Reader.
func NewDecoder(r io.Reader) *Decoder {
	br, ok := r.(byteReader)
	if !ok {
		br = bufio.NewReader(r)
	}
	return &Decoder{r: br, max: MaxFrameSize}
}

// SetMaxFrameSize sets the maximum size of an unescaped frame including the
// CRC. Larger frames are discarded with ErrFrameTooLarge.
func (d *Decoder) SetMaxFrameSize(n int) {
	d.max = n
}

// Variant returns the variant of the most recently read frame.
func (d *Decoder) Variant() Variant {
	return d.variant
}

// ReadFrame reads the next frame into f. Bytes outside of frames are skipped.
// f.AppBytes and f.Payload point into an internal buffer and are only valid
// until the next call to ReadFrame.
func (d *Decoder) ReadFrame(f *Frame) error {
	for {
		b, err := d.r.ReadByte()
		if err != nil {
			return err
		}
		switch b {
		case BOF:
			d.variant = VariantEscaping
			if err := d.readEscaped(); err != nil {
				return err
			}
			return f.decode(d.buf)
		case INV:
			d.variant = VariantNonEscaping
			if err := d.readNonEscaped(); err != nil {
				return err
			}
			return f.decode(d.buf)
		}
	}
}

// readEscaped reads a variant 1 frame after its BOF into d.buf and verifies
// and strips the CRC.
func (d *Decoder) readEscaped() error {
	d.buf = d.buf[:0]
	for {
		b, err := d.readByte()
		if err != nil {
			return err
		}
		switch {
		case b == EOF:
			d.cs.Reset()
			d.cs.Write(d.buf)
			if len(d.buf) < 2 {
				return ErrInvalidFrame
			}
			if d.cs.crc16 != CRC16_VALID {
				return ErrInvalidChecksum
			}
			d.buf = d.buf[:len(d.buf)-2]
			return nil
		case b == BOF:
			d.buf = d.buf[:0]
			continue
		case b == CE:
			if b, err = d.readByte(); err != nil {
				return err
			}
			if b == BOF {
				d.buf = d.buf[:0]
				continue
			}
			if b >= INV {
				return ErrInvalidFrame
			}
			b ^= XOR
		case b >= INV:
			return ErrInvalidFrame
		}
		if len(d.buf) >= d.max {
			return ErrFrameTooLarge
		}
		d.buf = append(d.buf, b)
	}
}

// readNonEscaped reads a variant 2 frame after its 0xF8 start byte into d.buf.
func (d *Decoder) readNonEscaped() error {
	count, err := d.readByte()
	if err != nil {
		return err
	}
	if count&^0x07 != 0 {
		return ErrInvalidFrame
	}
	var n uint64
	for range count {
		b, err := d.readByte()
		if err != nil {
			return err
		}
		n = n<<8 | uint64(b)
	}
	if n > uint64(d.max) {
		if _, err := io.CopyN(io.Discard, d.r, int64(n)); err != nil {
			return unexpected(err)
		}
		return ErrFrameTooLarge
	}
	if cap(d.buf) < int(n) {
		d.buf = make([]byte, n)
	}
	d.buf = d.buf[:n]
	if _, err := io.ReadFull(d.r, d.buf); err != nil {
		return unexpected(err)
	}
	return nil
}

func (d *Decoder) readByte() (byte, error) {
	b, err := d.r.ReadByte()
	return b, unexpected(err)
}

// unexpected turns io.EOF inside a frame into io.ErrUnexpectedEOF.
func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// Encoder writes S101 frames. Each frame is written with a single Write call.
// It is not safe for concurrent use.
type Encoder struct {
	w       io.Writer
	variant Variant
	raw     []byte
	buf     []byte
	cs      checksum
}

// NewEncoder returns an Encoder writing VariantEscaping frames to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w, variant: VariantEscaping}
}

func (e *Encoder) SetVariant(v Variant) {
	e.variant = v
}

func (e *Encoder) WriteFrame(f *Frame) error {
	raw, err := f.AppendBinary(e.raw[:0])
	e.raw = raw
	if err != nil {
		return err
	}
	if e.variant == VariantNonEscaping {
		e.buf = appendNonEscaped(e.buf[:0], raw)
	} else {
		e.cs.Reset()
		e.cs.Write(raw)
		e.raw = e.cs.Sum(raw)
		e.buf = appendEscaped(e.buf[:0], e.raw)
	}
	_, err = e.w.Write(e.buf)
	return err
}

func appendEscaped(b, raw []byte) []byte {
	b = append(b, BOF)
	for _, c := range raw {
		if c >= INV {
			b = append(b, CE, c^XOR)
		} else {
			b = append(b, c)
		}
	}
	return append(b, EOF)
}

// appendNonEscaped uses the smallest possible number of length bytes.
func appendNonEscaped(b, raw []byte) []byte {
	n := uint64(len(raw))
	count := 0
	for v := n; v > 0; v >>= 8 {
		count++
	}
	b = append(b, INV, byte(count))
	for i := count - 1; i >= 0; i-- {
		b = append(b, byte(n>>(8*i)))
	}
	return append(b, raw...)
}
