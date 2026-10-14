package s101

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// Specification, "Variant 1: Escaping": data 0xFF, 0x00, 0xF9, 0x01 encodes
// to this frame.
var specEscaped = []byte{0xFE, 0xFD, 0xDF, 0x00, 0xFD, 0xD9, 0x01, 0x95, 0x83, 0xFF}

func emberFrame(payload []byte) Frame {
	return Frame{
		Slot:     SlotDefault,
		Message:  MessageEmBER,
		Command:  CommandEmBER,
		Version:  VersionS101,
		Flags:    FlagSingle,
		DTD:      DTDGlow,
		AppBytes: []byte{0x1F, 0x02},
		Payload:  payload,
	}
}

// escaped frames raw data with a valid CRC.
func escaped(raw []byte) []byte {
	cs := newChecksum()
	cs.Write(raw)
	return appendEscaped(nil, cs.Sum(append([]byte(nil), raw...)))
}

func allBytes() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func encode(t *testing.T, v Variant, frames ...Frame) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	enc.SetVariant(v)
	for i := range frames {
		if err := enc.WriteFrame(&frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func assertFrame(t *testing.T, got, want Frame) {
	t.Helper()
	if got.Slot != want.Slot || got.Message != want.Message || got.Command != want.Command ||
		got.Version != want.Version || got.Flags != want.Flags || got.DTD != want.DTD ||
		!bytes.Equal(got.AppBytes, want.AppBytes) || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEncoderSpecExample(t *testing.T) {
	f := Frame{Slot: 0xFF, Message: 0x00, Command: 0xF9, Version: 0x01}
	if got := encode(t, VariantEscaping, f); !bytes.Equal(got, specEscaped) {
		t.Fatalf("got % X, want % X", got, specEscaped)
	}
}

func TestDecoderSpecExample(t *testing.T) {
	var f Frame
	if err := NewDecoder(bytes.NewReader(specEscaped)).ReadFrame(&f); err != nil {
		t.Fatal(err)
	}
	assertFrame(t, f, Frame{Slot: 0xFF, Message: 0x00, Command: 0xF9, Version: 0x01, Payload: []byte{}})
}

func TestDecoderSpecNonEscaping(t *testing.T) {
	// Specification, "Variant 2: Non-Escaping": four length bytes, ten
	// payload bytes.
	data := []byte{0xF8, 0x04, 0x00, 0x00, 0x00, 0x0A,
		0x00, 0x0E, 0x00, 0x01, 0xC0, 0x01, 0x00, 0xFE, 0xF8, 0xFF}
	dec := NewDecoder(bytes.NewReader(data))
	var f Frame
	if err := dec.ReadFrame(&f); err != nil {
		t.Fatal(err)
	}
	want := emberFrame([]byte{0xFE, 0xF8, 0xFF})
	want.AppBytes = []byte{}
	assertFrame(t, f, want)
	if dec.Variant() != VariantNonEscaping {
		t.Fatalf("variant = %d", dec.Variant())
	}
}

func TestEncoderNonEscapingMinimalLength(t *testing.T) {
	f := NewKeepAliveRequest()
	want := []byte{0xF8, 0x01, 0x04, 0x00, 0x0E, 0x01, 0x01}
	if got := encode(t, VariantNonEscaping, f); !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}

	f = emberFrame(make([]byte, 300))
	got := encode(t, VariantNonEscaping, f)
	if !bytes.Equal(got[:4], []byte{0xF8, 0x02, 0x01, 0x35}) { // 7 + 2 + 300 = 0x0135
		t.Fatalf("got header % X", got[:4])
	}
}

func TestRoundTrip(t *testing.T) {
	frames := []Frame{
		emberFrame(allBytes()),
		NewKeepAliveRequest(),
		NewKeepAliveResponse(),
		emberFrame([]byte{}),
	}
	for _, v := range []Variant{VariantEscaping, VariantNonEscaping} {
		dec := NewDecoder(bytes.NewReader(encode(t, v, frames...)))
		for _, want := range frames {
			var f Frame
			if err := dec.ReadFrame(&f); err != nil {
				t.Fatalf("variant %d: %v", v, err)
			}
			if want.Command != CommandEmBER {
				want.Payload = []byte{}
			}
			assertFrame(t, f, want)
			if dec.Variant() != v {
				t.Fatalf("variant = %d, want %d", dec.Variant(), v)
			}
		}
		var f Frame
		if err := dec.ReadFrame(&f); err != io.EOF {
			t.Fatalf("variant %d: got %v, want io.EOF", v, err)
		}
	}
}

func TestEscapedFrameHasNoControlBytes(t *testing.T) {
	data := encode(t, VariantEscaping, emberFrame(allBytes()))
	for i, b := range data[1 : len(data)-1] {
		if b >= INV && b != CE {
			t.Fatalf("unescaped byte %#02x at %d", b, i+1)
		}
	}
}

func TestDecoderMixedVariants(t *testing.T) {
	a, b := emberFrame([]byte{1, 2, 3}), emberFrame([]byte{4, 5, 6})
	data := append(encode(t, VariantEscaping, a), encode(t, VariantNonEscaping, b)...)
	dec := NewDecoder(bytes.NewReader(data))
	for _, want := range []Frame{a, b} {
		var f Frame
		if err := dec.ReadFrame(&f); err != nil {
			t.Fatal(err)
		}
		assertFrame(t, f, want)
	}
}

func TestDecoderWithoutByteReader(t *testing.T) {
	want := emberFrame(allBytes())
	data := encode(t, VariantEscaping, want)
	dec := NewDecoder(iotest.OneByteReader(bytes.NewReader(data)))
	var f Frame
	if err := dec.ReadFrame(&f); err != nil {
		t.Fatal(err)
	}
	assertFrame(t, f, want)
}

func TestDecoderSkipsGarbage(t *testing.T) {
	want := emberFrame([]byte{1, 2, 3})
	data := append([]byte{0x00, 0x42, EOF, CE}, encode(t, VariantEscaping, want)...)
	var f Frame
	if err := NewDecoder(bytes.NewReader(data)).ReadFrame(&f); err != nil {
		t.Fatal(err)
	}
	assertFrame(t, f, want)
}

func TestDecoderBOFRestartsFrame(t *testing.T) {
	want := emberFrame([]byte{1, 2, 3})
	frame := encode(t, VariantEscaping, want)
	for _, prefix := range [][]byte{{BOF, 0x01, 0x02}, {BOF, 0x01, CE}} {
		var f Frame
		data := append(append([]byte(nil), prefix...), frame...)
		if err := NewDecoder(bytes.NewReader(data)).ReadFrame(&f); err != nil {
			t.Fatalf("prefix % X: %v", prefix, err)
		}
		assertFrame(t, f, want)
	}
}

func TestDecoderRecoverableErrors(t *testing.T) {
	good := emberFrame([]byte{1, 2, 3})

	corrupt := encode(t, VariantEscaping, good)
	corrupt[2] ^= 0x01 // message type

	invalidByte := []byte{BOF, 0x00, 0xFA, 0x00, EOF}
	shortFrame := []byte{BOF, EOF}
	shortHeader := escaped([]byte{0x00, 0x0E, 0x00, 0x01, 0xC0}) // EmBER header without DTD/AppBytes
	tooLargeEscaped := encode(t, VariantEscaping, emberFrame(make([]byte, 100)))
	tooLargeNonEscaped := encode(t, VariantNonEscaping, emberFrame(make([]byte, 100)))

	tests := []struct {
		name string
		data []byte
		err  error
	}{
		{"checksum", corrupt, ErrInvalidChecksum},
		{"invalid byte", invalidByte, ErrInvalidFrame},
		{"short frame", shortFrame, ErrInvalidFrame},
		{"short header", shortHeader, ErrInvalidFrame},
		{"too large escaped", tooLargeEscaped, ErrFrameTooLarge},
		{"too large non-escaping", tooLargeNonEscaped, ErrFrameTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := append(append([]byte(nil), tt.data...), encode(t, VariantEscaping, good)...)
			dec := NewDecoder(bytes.NewReader(data))
			dec.SetMaxFrameSize(64)
			var f Frame
			if err := dec.ReadFrame(&f); !errors.Is(err, tt.err) {
				t.Fatalf("got %v, want %v", err, tt.err)
			}
			if err := dec.ReadFrame(&f); err != nil {
				t.Fatalf("after error: %v", err)
			}
			assertFrame(t, f, good)
		})
	}
}

func TestDecoderUnexpectedEOF(t *testing.T) {
	full := encode(t, VariantEscaping, emberFrame([]byte{1, 2, 3}))
	fullNE := encode(t, VariantNonEscaping, emberFrame([]byte{1, 2, 3}))
	for _, data := range [][]byte{full[:5], {BOF, 0x00, CE}, fullNE[:1], fullNE[:2], fullNE[:5]} {
		var f Frame
		if err := NewDecoder(bytes.NewReader(data)).ReadFrame(&f); err != io.ErrUnexpectedEOF {
			t.Fatalf("% X: got %v, want io.ErrUnexpectedEOF", data, err)
		}
	}
}

type countingWriter struct {
	writes int
	bytes.Buffer
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(p)
}

func TestEncoderSingleWrite(t *testing.T) {
	for _, v := range []Variant{VariantEscaping, VariantNonEscaping} {
		var w countingWriter
		enc := NewEncoder(&w)
		enc.SetVariant(v)
		f := emberFrame(allBytes())
		if err := enc.WriteFrame(&f); err != nil {
			t.Fatal(err)
		}
		if w.writes != 1 {
			t.Fatalf("variant %d: %d writes", v, w.writes)
		}
	}
}

func TestEncoderInvalidAppBytes(t *testing.T) {
	f := emberFrame(nil)
	f.AppBytes = make([]byte, 256)
	if err := NewEncoder(io.Discard).WriteFrame(&f); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("got %v", err)
	}
}

func TestBuffersAreReused(t *testing.T) {
	f := emberFrame(allBytes())
	enc := NewEncoder(io.Discard)
	if n := testing.AllocsPerRun(100, func() { enc.WriteFrame(&f) }); n != 0 {
		t.Errorf("encoder: %v allocs per frame", n)
	}

	data := encode(t, VariantEscaping, f)
	r := bytes.NewReader(data)
	dec := NewDecoder(r)
	var got Frame
	if n := testing.AllocsPerRun(100, func() {
		r.Reset(data)
		dec.ReadFrame(&got)
	}); n != 0 {
		t.Errorf("decoder: %v allocs per frame", n)
	}
}

func TestUnmarshalBinaryCopies(t *testing.T) {
	want := emberFrame([]byte{1, 2, 3})
	data, err := want.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if err := f.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	for i := range data {
		data[i] = 0
	}
	assertFrame(t, f, want)
}
