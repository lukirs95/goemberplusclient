package ber

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
)

func build(t *testing.T, f func(*Builder)) []byte {
	t.Helper()
	var b Builder
	f(&b)
	data, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), data...)
}

func next(t *testing.T, data []byte) TLV {
	t.Helper()
	r := NewReader(data)
	tlv, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if r.More() {
		t.Fatalf("trailing data after % X", data)
	}
	return tlv
}

func TestIntSpecTable(t *testing.T) {
	// Integer table of the specification ("Integer [X.690 8.3]").
	tests := []struct {
		v    int64
		want []byte
	}{
		{1, []byte{0x01, 0x01}},
		{-1, []byte{0x01, 0xFF}},
		{255, []byte{0x02, 0x00, 0xFF}},
		{127, []byte{0x01, 0x7F}},
		{128, []byte{0x02, 0x00, 0x80}},
		{-128, []byte{0x01, 0x80}},
		{1<<16 - 1, []byte{0x03, 0x00, 0xFF, 0xFF}},
		{1 << 15, []byte{0x03, 0x00, 0x80, 0x00}},
		{-1 << 15, []byte{0x02, 0x80, 0x00}},
	}
	for _, tt := range tests {
		got := build(t, func(b *Builder) { b.Int(tt.v) })
		want := append([]byte{0x02}, tt.want...)
		if !bytes.Equal(got, want) {
			t.Errorf("%d: got % X, want % X", tt.v, got, want)
		}
		if v, err := next(t, got).Int(); err != nil || v != tt.v {
			t.Errorf("%d: decoded %d, %v", tt.v, v, err)
		}
	}
}

func TestIntRange(t *testing.T) {
	for _, v := range []int64{0, math.MaxInt64, math.MinInt64, math.MaxInt32, math.MinInt32, 1 << 40} {
		got, err := next(t, build(t, func(b *Builder) { b.Int(v) })).Int()
		if err != nil || got != v {
			t.Errorf("%d: got %d, %v", v, got, err)
		}
	}
}

func TestIntDecodeLenient(t *testing.T) {
	v, err := next(t, []byte{0x02, 0x02, 0x00, 0x01}).Int()
	if err != nil || v != 1 {
		t.Fatalf("non-minimal: %d, %v", v, err)
	}
	for _, data := range [][]byte{{0x02, 0x00}, append([]byte{0x02, 0x09}, make([]byte, 9)...)} {
		if _, err := next(t, data).Int(); !errors.Is(err, ErrInvalid) {
			t.Errorf("% X: got %v", data, err)
		}
	}
}

func TestInt32(t *testing.T) {
	if _, err := next(t, build(t, func(b *Builder) { b.Int(math.MaxInt32 + 1) })).Int32(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	if v, err := next(t, build(t, func(b *Builder) { b.Int(math.MinInt32) })).Int32(); err != nil || v != math.MinInt32 {
		t.Fatalf("got %d, %v", v, err)
	}
}

func TestSpecEncodingExample(t *testing.T) {
	// Specification, "The BER namespace": integer 1333 with explicit tag
	// APPLICATION 1. The specification
	// shows the tag byte without the constructed bit; Builder sets it.
	want := []byte{0x61, 0x04, 0x02, 0x02, 0x05, 0x35}
	got := build(t, func(b *Builder) {
		b.Constructed(Application(1), func(b *Builder) { b.Int(1333) })
	})
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
	for _, data := range [][]byte{want, {0x41, 0x04, 0x02, 0x02, 0x05, 0x35}} {
		outer := next(t, data)
		if outer.Tag != Application(1) {
			t.Fatalf("tag %v", outer.Tag)
		}
		inner, err := outer.Explicit()
		if err != nil {
			t.Fatal(err)
		}
		if v, err := inner.Int(); err != nil || v != 1333 {
			t.Fatalf("% X: got %d, %v", data, v, err)
		}
	}
}

func TestPrimitives(t *testing.T) {
	data := build(t, func(b *Builder) {
		b.Bool(true)
		b.Bool(false)
		b.UTF8("Grüße")
		b.Octets([]byte{0x00, 0xFF})
		b.Null()
		b.RelativeOID([]uint32{1, 2, 300})
		b.RelativeOID(nil)
	})
	want := []byte{
		0x01, 0x01, 0xFF,
		0x01, 0x01, 0x00,
		0x0C, 0x07, 'G', 'r', 0xC3, 0xBC, 0xC3, 0x9F, 'e',
		0x04, 0x02, 0x00, 0xFF,
		0x05, 0x00,
		0x0D, 0x04, 0x01, 0x02, 0x82, 0x2C,
		0x0D, 0x00,
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("got % X\nwant % X", data, want)
	}

	r := NewReader(data)
	must := func() TLV {
		t.Helper()
		tlv, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		return tlv
	}
	if v, err := must().Bool(); err != nil || !v {
		t.Errorf("bool true: %v, %v", v, err)
	}
	if v, err := must().Bool(); err != nil || v {
		t.Errorf("bool false: %v, %v", v, err)
	}
	if v, err := must().UTF8(); err != nil || v != "Grüße" {
		t.Errorf("utf8: %q, %v", v, err)
	}
	if v, err := must().Octets(); err != nil || !bytes.Equal(v, []byte{0x00, 0xFF}) {
		t.Errorf("octets: % X, %v", v, err)
	}
	if err := must().Null(); err != nil {
		t.Errorf("null: %v", err)
	}
	if v, err := must().RelativeOID(nil); err != nil || len(v) != 3 || v[0] != 1 || v[1] != 2 || v[2] != 300 {
		t.Errorf("oid: %v, %v", v, err)
	}
	if v, err := must().RelativeOID(nil); err != nil || len(v) != 0 {
		t.Errorf("empty oid: %v, %v", v, err)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("got %v, want io.EOF", err)
	}
}

func TestPrimitiveErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		f    func(TLV) error
	}{
		{"bool length", []byte{0x01, 0x02, 0x00, 0x00}, func(t TLV) error { _, err := t.Bool(); return err }},
		{"null length", []byte{0x05, 0x01, 0x00}, func(t TLV) error { return t.Null() }},
		{"oid incomplete", []byte{0x0D, 0x01, 0x81}, func(t TLV) error { _, err := t.RelativeOID(nil); return err }},
		{"oid overflow", []byte{0x0D, 0x05, 0x90, 0x80, 0x80, 0x80, 0x00}, func(t TLV) error { _, err := t.RelativeOID(nil); return err }},
		{"constructed integer", []byte{0x22, 0x03, 0x02, 0x01, 0x01}, func(t TLV) error { _, err := t.Int(); return err }},
	}
	for _, tt := range tests {
		if err := tt.f(next(t, tt.data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", tt.name, err)
		}
	}
}

func TestRelativeOIDMax(t *testing.T) {
	data := build(t, func(b *Builder) { b.RelativeOID([]uint32{math.MaxUint32}) })
	if want := []byte{0x0D, 0x05, 0x8F, 0xFF, 0xFF, 0xFF, 0x7F}; !bytes.Equal(data, want) {
		t.Fatalf("got % X", data)
	}
	v, err := next(t, data).RelativeOID(nil)
	if err != nil || v[0] != math.MaxUint32 {
		t.Fatalf("got %v, %v", v, err)
	}
}

func TestTagError(t *testing.T) {
	_, err := next(t, []byte{0x0C, 0x00}).Int()
	te, ok := errors.AsType[*TagError](err)
	if !ok || te.Want != TagInteger || te.Got != TagUTF8String {
		t.Fatalf("got %v", err)
	}
	if err.Error() != "ber: expected INTEGER, got UTF8String" {
		t.Fatalf("message %q", err.Error())
	}
}

func TestRealEncoding(t *testing.T) {
	tests := []struct {
		v    float64
		want []byte
	}{
		{0, nil},
		{1, []byte{0x80, 0x00, 0x01}},
		{0.5, []byte{0x80, 0xFF, 0x01}},
		{-2.5, []byte{0xC0, 0xFF, 0x05}},
		{1333, []byte{0x80, 0x00, 0x05, 0x35}},
		{math.Inf(1), []byte{0x40}},
		{math.Inf(-1), []byte{0x41}},
		{math.Copysign(0, -1), []byte{0x43}},
		{math.MaxFloat64, []byte{0x81, 0x03, 0xCB, 0x1F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{math.SmallestNonzeroFloat64, []byte{0x81, 0xFB, 0xCE, 0x01}},
	}
	for _, tt := range tests {
		got := build(t, func(b *Builder) { b.Real(tt.v) })
		want := append([]byte{0x09, byte(len(tt.want))}, tt.want...)
		if !bytes.Equal(got, want) {
			t.Errorf("%g: got % X, want % X", tt.v, got, want)
		}
	}
}

func TestRealRoundTrip(t *testing.T) {
	values := []float64{
		0, math.Copysign(0, -1), 1, -1, 0.1, -0.1, 1e-300, 1e300, math.Pi, -64.0, 15.0,
		math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, 2.2250738585072014e-308,
		math.Inf(1), math.Inf(-1), float64(1 << 53), float64(1<<53 + 2),
	}
	for i := range uint64(1000) {
		values = append(values, math.Float64frombits(i*0x9E3779B97F4A7C15))
	}
	for _, v := range values {
		got, err := next(t, build(t, func(b *Builder) { b.Real(v) })).Real()
		if err != nil {
			t.Fatalf("%g: %v", v, err)
		}
		if math.Float64bits(got) != math.Float64bits(v) && !(math.IsNaN(v) && math.IsNaN(got)) {
			t.Fatalf("%g (%#x): got %g (%#x)", v, math.Float64bits(v), got, math.Float64bits(got))
		}
	}
	got, err := next(t, build(t, func(b *Builder) { b.Real(math.NaN()) })).Real()
	if err != nil || !math.IsNaN(got) {
		t.Fatalf("NaN: got %g, %v", got, err)
	}
}

func TestRealDecodeBases(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want float64
	}{
		{"base 8", []byte{0x90, 0x02, 0x01}, 64},
		{"base 16 scale 1", []byte{0xA4, 0x01, 0x03}, 96},
		{"negative", []byte{0xC0, 0x00, 0x05}, -5},
		{"long exponent format", []byte{0x83, 0x01, 0x02, 0x03}, 12},
		{"two-byte exponent", []byte{0x81, 0xFF, 0xFE, 0x01}, 0.25},
		{"huge exponent", []byte{0x82, 0x7F, 0xFF, 0xFF, 0x01}, math.Inf(1)},
		{"tiny exponent", []byte{0x82, 0x80, 0x00, 0x00, 0x01}, 0},
	}
	for _, tt := range tests {
		v, err := parseReal(tt.data)
		if err != nil || v != tt.want {
			t.Errorf("%s: got %g, %v; want %g", tt.name, v, err, tt.want)
		}
	}
}

func TestRealDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		err  error
	}{
		{"decimal", []byte{0x01, '1'}, ErrUnsupported},
		{"special with extra byte", []byte{0x40, 0x00}, ErrInvalid},
		{"unknown special", []byte{0x44}, ErrInvalid},
		{"reserved base", []byte{0xB0, 0x00, 0x01}, ErrInvalid},
		{"missing exponent", []byte{0x81, 0x01}, ErrInvalid},
		{"missing exponent length", []byte{0x83}, ErrInvalid},
		{"zero exponent length", []byte{0x83, 0x00, 0x01}, ErrInvalid},
		{"long mantissa", append([]byte{0x80, 0x00}, make([]byte, 9)...), ErrUnsupported},
	}
	for _, tt := range tests {
		if _, err := parseReal(tt.data); !errors.Is(err, tt.err) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.err)
		}
	}
}

func TestTags(t *testing.T) {
	tests := []struct {
		tag  Tag
		want []byte
	}{
		{Context(0), []byte{0xA0}},
		{Context(30), []byte{0xBE}},
		{Context(31), []byte{0xBF, 0x1F}},
		{Application(127), []byte{0x7F, 0x7F}},
		{Application(200), []byte{0x7F, 0x81, 0x48}},
		{Tag{ClassPrivate, math.MaxUint32}, []byte{0xFF, 0x8F, 0xFF, 0xFF, 0xFF, 0x7F}},
		{TagSet, []byte{0x31}},
	}
	for _, tt := range tests {
		data := build(t, func(b *Builder) { b.Constructed(tt.tag, func(*Builder) {}) })
		want := append(append([]byte(nil), tt.want...), 0x00)
		if !bytes.Equal(data, want) {
			t.Errorf("%v: got % X, want % X", tt.tag, data, want)
		}
		tlv := next(t, data)
		if tlv.Tag != tt.tag || !tlv.Constructed {
			t.Errorf("%v: decoded %v constructed=%v", tt.tag, tlv.Tag, tlv.Constructed)
		}
	}
}

func TestTagString(t *testing.T) {
	for tag, want := range map[Tag]string{
		TagRelativeOID:    "RELATIVE-OID",
		Universal(99):     "UNIVERSAL 99",
		Application(11):   "APPLICATION 11",
		Context(2):        "CONTEXT 2",
		{ClassPrivate, 1}: "PRIVATE 1",
	} {
		if got := tag.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestBuilderLengthForms(t *testing.T) {
	for _, tt := range []struct {
		n      int
		header []byte
	}{
		{0, []byte{0x04, 0x00}},
		{127, []byte{0x04, 0x7F}},
		{128, []byte{0x04, 0x81, 0x80}},
		{300, []byte{0x04, 0x82, 0x01, 0x2C}},
		{70000, []byte{0x04, 0x83, 0x01, 0x11, 0x70}},
	} {
		content := bytes.Repeat([]byte{0xAB}, tt.n)
		data := build(t, func(b *Builder) { b.Octets(content) })
		if !bytes.Equal(data[:len(tt.header)], tt.header) || !bytes.Equal(data[len(tt.header):], content) {
			t.Errorf("%d: header % X", tt.n, data[:min(len(data), 6)])
		}

		// Nested constructed elements must be moved correctly.
		data = build(t, func(b *Builder) {
			b.Constructed(Context(1), func(b *Builder) {
				b.Constructed(TagSequence, func(b *Builder) { b.Octets(content) })
				b.Int(7)
			})
		})
		outer, err := next(t, data).Children()
		if err != nil {
			t.Fatal(err)
		}
		seq, _ := outer.Next()
		last, _ := outer.Next()
		inner, _ := seq.Explicit()
		v, _ := inner.Octets()
		if i, err := last.Int(); err != nil || i != 7 || !bytes.Equal(v, content) {
			t.Errorf("%d: nested round trip failed: %d, %v", tt.n, i, err)
		}
	}
}

func TestBuilderInvalidTag(t *testing.T) {
	var b Builder
	b.Int(1)
	b.Constructed(Universal(0), func(b *Builder) { t.Fatal("f called") })
	b.Int(2)
	if _, err := b.Bytes(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	b.Reset()
	b.Int(3)
	if data, err := b.Bytes(); err != nil || !bytes.Equal(data, []byte{0x02, 0x01, 0x03}) {
		t.Fatalf("after reset: % X, %v", data, err)
	}
}

func TestReaderLengthErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		err  error
	}{
		{"missing length", []byte{0x02}, ErrTruncated},
		{"short value", []byte{0x02, 0x02, 0x01}, ErrTruncated},
		{"short long form", []byte{0x04, 0x82, 0x01}, ErrTruncated},
		{"reserved length", []byte{0x04, 0xFF}, ErrInvalid},
		{"length too long", append([]byte{0x04, 0x89}, make([]byte, 9)...), ErrInvalid},
		{"huge length", []byte{0x04, 0x88, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, ErrTruncated},
		{"indefinite primitive", []byte{0x04, 0x80, 0x00, 0x00}, ErrInvalid},
		{"end-of-contents", []byte{0x00, 0x00}, ErrInvalid},
		{"truncated tag", []byte{0x1F, 0x81}, ErrTruncated},
		{"tag overflow", []byte{0x1F, 0x90, 0x80, 0x80, 0x80, 0x00, 0x00}, ErrInvalid},
	}
	for _, tt := range tests {
		r := NewReader(tt.data)
		if _, err := r.Next(); !errors.Is(err, tt.err) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.err)
		}
		if len(r.rest) != len(tt.data) {
			t.Errorf("%s: reader advanced on error", tt.name)
		}
	}
	long := next(t, []byte{0x04, 0x81, 0x01, 0xAA})
	if !bytes.Equal(long.Value, []byte{0xAA}) {
		t.Errorf("non-minimal long form: % X", long.Value)
	}
}

func TestIndefiniteLength(t *testing.T) {
	// Root { RootElementCollection { [0] { INTEGER 5 } } }, all indefinite
	// except the innermost element, followed by a sibling.
	data := []byte{
		0x60, 0x80,
		0x6B, 0x80,
		0xA0, 0x03, 0x02, 0x01, 0x05,
		0x00, 0x00,
		0x00, 0x00,
		0x05, 0x00,
	}
	r := NewReader(data)
	root, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if root.Tag != Application(0) || !bytes.Equal(root.Value, data[2:11]) {
		t.Fatalf("root %v % X", root.Tag, root.Value)
	}
	if err := func() error {
		s, err := r.Next()
		if err != nil {
			return err
		}
		return s.Null()
	}(); err != nil {
		t.Fatalf("sibling: %v", err)
	}

	coll, err := root.Explicit()
	if err != nil {
		t.Fatal(err)
	}
	elems, err := coll.Children()
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := elems.Next()
	if err != nil {
		t.Fatal(err)
	}
	inner, err := ctx.Explicit()
	if err != nil {
		t.Fatal(err)
	}
	if v, err := inner.Int(); err != nil || v != 5 || elems.More() {
		t.Fatalf("got %d, %v", v, err)
	}
}

func TestIndefiniteErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		err  error
	}{
		{"missing eoc", []byte{0x30, 0x80, 0x05, 0x00}, ErrTruncated},
		{"missing nested eoc", []byte{0x30, 0x80, 0x30, 0x80, 0x00, 0x00}, ErrTruncated},
		{"malformed eoc", []byte{0x30, 0x80, 0x00, 0x01, 0x00}, ErrInvalid},
		{"constructed eoc", []byte{0x30, 0x80, 0x20, 0x00}, ErrInvalid},
		{"truncated child", []byte{0x30, 0x80, 0x04, 0x05, 0x00, 0x00}, ErrTruncated},
	}
	for _, tt := range tests {
		r := NewReader(tt.data)
		if _, err := r.Next(); !errors.Is(err, tt.err) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.err)
		}
	}
}

func TestIndefiniteDeepNesting(t *testing.T) {
	// Finding the end must not recurse, regardless of the nesting depth.
	const depth = 100000
	data := append(bytes.Repeat([]byte{0x30, 0x80}, depth), bytes.Repeat([]byte{0x00, 0x00}, depth)...)
	r := NewReader(data)
	tlv, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(tlv.Value) != len(data)-4 {
		t.Fatalf("value length %d", len(tlv.Value))
	}
}

func TestExplicitErrors(t *testing.T) {
	for _, data := range [][]byte{
		{0xA0, 0x00},
		{0xA0, 0x04, 0x05, 0x00, 0x05, 0x00},
	} {
		if _, err := next(t, data).Explicit(); !errors.Is(err, ErrInvalid) {
			t.Errorf("% X: got %v", data, err)
		}
	}
	if _, err := next(t, []byte{0x02, 0x01, 0x00}).Children(); !errors.Is(err, ErrInvalid) {
		t.Errorf("children of primitive: got %v", err)
	}
}

func getDirectory(b *Builder, path []uint32) {
	b.Constructed(Application(0), func(b *Builder) {
		b.Constructed(Application(11), func(b *Builder) {
			b.Constructed(Context(0), func(b *Builder) {
				b.Constructed(Application(10), func(b *Builder) {
					b.Constructed(Context(0), func(b *Builder) { b.RelativeOID(path) })
					b.Constructed(Context(2), func(b *Builder) {
						b.Constructed(Application(4), func(b *Builder) {
							b.Constructed(Context(0), func(b *Builder) {
								b.Constructed(Application(2), func(b *Builder) {
									b.Constructed(Context(0), func(b *Builder) { b.Int(32) })
								})
							})
						})
					})
				})
			})
		})
	})
}

func TestAllocations(t *testing.T) {
	var b Builder
	path := []uint32{1, 3}
	getDirectory(&b, path)
	data, _ := b.Bytes()
	data = append([]byte(nil), data...)

	if n := testing.AllocsPerRun(100, func() {
		b.Reset()
		getDirectory(&b, path)
	}); n != 0 {
		t.Errorf("builder: %v allocs", n)
	}

	var walk func(r Reader)
	walk = func(r Reader) {
		for r.More() {
			tlv, err := r.Next()
			if err != nil {
				t.Fatal(err)
			}
			if tlv.Constructed {
				c, _ := tlv.Children()
				walk(c)
			}
		}
	}
	if n := testing.AllocsPerRun(100, func() { walk(NewReader(data)) }); n != 0 {
		t.Errorf("reader: %v allocs", n)
	}
}

func FuzzReader(f *testing.F) {
	var b Builder
	getDirectory(&b, []uint32{1, 2, 300})
	seed, _ := b.Bytes()
	f.Add(seed)
	f.Add([]byte{0x60, 0x80, 0x6B, 0x80, 0xA0, 0x03, 0x02, 0x01, 0x05, 0x00, 0x00, 0x00, 0x00})
	f.Add([]byte{0x09, 0x03, 0x80, 0xFF, 0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		var walk func(r Reader, depth int)
		walk = func(r Reader, depth int) {
			for r.More() {
				tlv, err := r.Next()
				if err != nil {
					return
				}
				tlv.Bool()
				tlv.Int()
				tlv.Real()
				tlv.RelativeOID(nil)
				tlv.Explicit()
				if c, err := tlv.Children(); err == nil && depth < 64 {
					walk(c, depth+1)
				}
			}
		}
		walk(NewReader(data), 0)
	})
}
