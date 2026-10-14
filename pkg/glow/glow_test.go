package glow

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
)

// ---- Helpers that build provider messages independently of Encode ----------

type bf = func(*ber.Builder)

func root(entries ...bf) []byte {
	var b ber.Builder
	b.Constructed(ber.Application(0), func(b *ber.Builder) {
		b.Constructed(ber.Application(11), func(b *ber.Builder) {
			for _, e := range entries {
				b.Constructed(ber.Context(0), e)
			}
		})
	})
	data, err := b.Bytes()
	if err != nil {
		panic(err)
	}
	return bytes.Clone(data)
}

func ctx(n uint32, f bf) bf {
	return func(b *ber.Builder) { b.Constructed(ber.Context(n), f) }
}

func app(n uint32, fs ...bf) bf {
	return func(b *ber.Builder) {
		b.Constructed(ber.Application(n), func(b *ber.Builder) {
			for _, f := range fs {
				f(b)
			}
		})
	}
}

func set(fs ...bf) bf {
	return func(b *ber.Builder) {
		b.Constructed(ber.TagSet, func(b *ber.Builder) {
			for _, f := range fs {
				f(b)
			}
		})
	}
}

func integer(v int64) bf   { return func(b *ber.Builder) { b.Int(v) } }
func utf8(s string) bf     { return func(b *ber.Builder) { b.UTF8(s) } }
func boolean(v bool) bf    { return func(b *ber.Builder) { b.Bool(v) } }
func oid(p ...uint32) bf   { return func(b *ber.Builder) { b.RelativeOID(p) } }
func real(v float64) bf    { return func(b *ber.Builder) { b.Real(v) } }
func octets(v []byte) bf   { return func(b *ber.Builder) { b.Octets(v) } }
func null() bf             { return func(b *ber.Builder) { b.Null() } }
func children(es ...bf) bf { return ctx(2, app(4, wrap0(es)...)) }

func wrap0(es []bf) []bf {
	out := make([]bf, len(es))
	for i, e := range es {
		out[i] = ctx(0, e)
	}
	return out
}

func node(n int64, fs ...bf) bf      { return app(3, append([]bf{ctx(0, integer(n))}, fs...)...) }
func parameter(n int64, fs ...bf) bf { return app(1, append([]bf{ctx(0, integer(n))}, fs...)...) }

func decodeElements(t *testing.T, data []byte) Elements {
	t.Helper()
	msg, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	els, ok := msg.(Elements)
	if !ok {
		t.Fatalf("got %T", msg)
	}
	return els
}

func encode(t *testing.T, elems ...Element) []byte {
	t.Helper()
	var b ber.Builder
	if err := Encode(&b, elems...); err != nil {
		t.Fatal(err)
	}
	data, _ := b.Bytes()
	return bytes.Clone(data)
}

// ---- Path ---------------------------------------------------------------

func TestPath(t *testing.T) {
	p, err := ParsePath("1.3.2")
	if err != nil || !slices.Equal(p, Path{1, 3, 2}) || p.String() != "1.3.2" {
		t.Fatalf("got %v, %v", p, err)
	}
	if r, err := ParsePath(""); err != nil || len(r) != 0 || r.String() != "" {
		t.Fatalf("root: %v, %v", r, err)
	}
	for _, s := range []string{"1..2", "a", "1.-2", "4294967296", "1."} {
		if _, err := ParsePath(s); err == nil {
			t.Errorf("%q: no error", s)
		}
	}

	parent := p.Parent()
	if !slices.Equal(parent, Path{1, 3}) || Path(nil).Parent() != nil {
		t.Fatalf("parent %v", parent)
	}
	child := parent.Child(9)
	if !slices.Equal(child, Path{1, 3, 9}) || !slices.Equal(p, Path{1, 3, 2}) {
		t.Fatalf("child %v modified %v", child, p)
	}
	// Appending to a parent must not overwrite the original path.
	_ = append(parent, 7)
	if p[2] != 2 {
		t.Fatal("Parent shares capacity with the original path")
	}
	if !p.HasPrefix(Path{1, 3}) || !p.HasPrefix(nil) || p.HasPrefix(Path{1, 4}) || parent.HasPrefix(p) {
		t.Fatal("HasPrefix")
	}
}

// ---- Encode -------------------------------------------------------------

func TestEncodeRootGetDirectory(t *testing.T) {
	// GetDirectory on the root with definite lengths and without a field
	// mask, derived from the DTD. Lawo's own consumer sends the same command
	// with indefinite lengths and dirFieldMask=all (see capture_test.go).
	want := []byte{0x60, 0x0B, 0x6B, 0x09, 0xA0, 0x07, 0x62, 0x05, 0xA0, 0x03, 0x02, 0x01, 0x20}
	if got := encode(t, &Command{Number: CmdGetDirectory}); !bytes.Equal(got, want) {
		t.Fatalf("got % X\nwant % X", got, want)
	}
}

func TestEncodeQualifiedCommand(t *testing.T) {
	tests := []struct {
		target ElementKind
		tag    ber.Tag
	}{
		{KindNode, ber.Application(10)},
		{KindParameter, ber.Application(9)},
		{KindMatrix, ber.Application(17)},
		{KindFunction, ber.Application(20)},
	}
	for _, tt := range tests {
		got := encode(t, &Command{Path: Path{1, 3}, Target: tt.target, Number: CmdGetDirectory, DirFieldMask: FieldsAll})
		want := root(app(tt.tag.Number,
			ctx(0, oid(1, 3)),
			children(app(2, ctx(0, integer(32)), ctx(1, integer(-1)))),
		))
		if !bytes.Equal(got, want) {
			t.Errorf("%v: got % X\nwant % X", tt.target, got, want)
		}
	}
}

func TestEncodeSubscribeOmitsMask(t *testing.T) {
	got := encode(t, &Command{Path: Path{1}, Target: KindParameter, Number: CmdSubscribe, DirFieldMask: FieldsAll})
	want := root(app(9, ctx(0, oid(1)), children(app(2, ctx(0, integer(30))))))
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X\nwant % X", got, want)
	}
}

func TestEncodeSetValue(t *testing.T) {
	got := encode(t, &Parameter{Path: Path{1, 3, 2}, Value: String("255.255.252.0")})
	want := root(app(9, ctx(0, oid(1, 3, 2)), ctx(1, set(ctx(2, utf8("255.255.252.0"))))))
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X\nwant % X", got, want)
	}
}

func TestEncodeErrors(t *testing.T) {
	for _, el := range []Element{
		&Command{Path: Path{1}, Number: CmdGetDirectory},
		&Parameter{Value: Int(1)},
		&Node{},
	} {
		var b ber.Builder
		if err := Encode(&b, el); err == nil {
			t.Errorf("%#v: no error", el)
		}
		if data, _ := b.Bytes(); len(data) != 0 {
			t.Errorf("%#v: wrote data on error", el)
		}
	}
}

// ---- Decode -------------------------------------------------------------

func TestDecodeNestedResponse(t *testing.T) {
	// Specification, "Querying a data provider": Device(1) with Network(3) containing Mask(2), plus an empty node.
	data := root(node(1,
		ctx(1, set(ctx(0, utf8("Device")), ctx(1, utf8("The Device")))),
		children(
			node(3, children(
				parameter(2, ctx(1, set(
					ctx(0, utf8("Mask")),
					ctx(2, utf8("255.255.255.0")),
					ctx(5, integer(3)),
				))),
			)),
			node(4),
		),
	))
	els := decodeElements(t, data)
	if len(els) != 4 {
		t.Fatalf("got %d elements", len(els))
	}

	device := els[0].(*Node)
	if !slices.Equal(device.Path, Path{1}) || *device.Identifier != "Device" || *device.Description != "The Device" || !device.HasChildren {
		t.Errorf("device %+v", device)
	}
	network := els[1].(*Node)
	if !slices.Equal(network.Path, Path{1, 3}) || network.Identifier != nil || !network.HasChildren {
		t.Errorf("network %+v", network)
	}
	mask := els[2].(*Parameter)
	if !slices.Equal(mask.Path, Path{1, 3, 2}) || *mask.Identifier != "Mask" ||
		mask.Value != String("255.255.255.0") || *mask.Access != AccessReadWrite {
		t.Errorf("mask %+v", mask)
	}
	empty := els[3].(*Node)
	if !slices.Equal(empty.Path, Path{1, 4}) || empty.HasChildren || empty.hasContents() {
		t.Errorf("empty node %+v", empty)
	}
}

func TestDecodeQualified(t *testing.T) {
	data := root(
		app(10, ctx(0, oid(1, 2)), ctx(1, set(ctx(3, boolean(false))))),
		app(9, ctx(0, oid(1, 2, 7)), ctx(1, set(ctx(2, integer(-5))))),
		app(10, ctx(0, oid(5)), children(parameter(1, ctx(1, set(ctx(2, boolean(true))))))),
	)
	els := decodeElements(t, data)
	want := []Path{{1, 2}, {1, 2, 7}, {5}, {5, 1}}
	if len(els) != len(want) {
		t.Fatalf("got %d elements", len(els))
	}
	for i, p := range want {
		if !slices.Equal(els[i].ElementPath(), p) {
			t.Errorf("element %d: path %v, want %v", i, els[i].ElementPath(), p)
		}
	}
	if *els[0].(*Node).IsOnline {
		t.Error("isOnline")
	}
	if els[1].(*Parameter).Value != Int(-5) || els[3].(*Parameter).Value != Bool(true) {
		t.Error("values")
	}
}

func TestDecodeIndefiniteLength(t *testing.T) {
	// QualifiedParameter 1.2 with value 5, all containers indefinite.
	data := []byte{
		0x60, 0x80,
		0x6B, 0x80,
		0xA0, 0x80,
		0x69, 0x80,
		0xA0, 0x04, 0x0D, 0x02, 0x01, 0x02,
		0xA1, 0x80,
		0x31, 0x80,
		0xA2, 0x03, 0x02, 0x01, 0x05,
		0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00,
	}
	els := decodeElements(t, data)
	if len(els) != 1 {
		t.Fatalf("got %d elements", len(els))
	}
	p := els[0].(*Parameter)
	if !slices.Equal(p.Path, Path{1, 2}) || p.Value != Int(5) {
		t.Fatalf("got %+v", p)
	}
}

func TestDecodeAllParameterFields(t *testing.T) {
	data := root(parameter(1, ctx(1, set(
		ctx(0, utf8("gain")),
		ctx(1, utf8("Gain")),
		ctx(2, real(-6.5)),
		ctx(3, real(-128)),
		ctx(4, integer(15)),
		ctx(5, integer(3)),
		ctx(6, utf8("%.1f°dB")),
		ctx(7, utf8("Off\n~Debug\nOn")),
		ctx(8, integer(10)),
		ctx(9, boolean(true)),
		ctx(10, utf8("(5 * $)\n($ / 5)")),
		ctx(11, integer(2)),
		ctx(12, null()),
		ctx(13, integer(2)),
		ctx(14, integer(110)),
		ctx(15, app(8, ctx(0, app(7, ctx(0, utf8("Low")), ctx(1, integer(-1)))))),
		ctx(16, app(12, ctx(0, integer(int64(Float32LE))), ctx(1, integer(4)))),
		ctx(17, utf8("de.example.gain")),
		ctx(18, oid(9, 1)),
		ctx(99, utf8("unknown field")),
	))))
	got := decodeElements(t, data)[0].(*Parameter)
	want := &Parameter{
		Path:              Path{1},
		Identifier:        new("gain"),
		Description:       new("Gain"),
		Value:             Real(-6.5),
		Minimum:           Real(-128),
		Maximum:           Int(15),
		Access:            new(AccessReadWrite),
		Format:            new("%.1f°dB"),
		Enumeration:       []string{"Off", "~Debug", "On"},
		Factor:            new(int32(10)),
		IsOnline:          new(true),
		Formula:           new("(5 * $)\n($ / 5)"),
		Step:              new(int32(2)),
		Default:           Null{},
		Type:              new(TypeReal),
		StreamIdentifier:  new(int32(110)),
		EnumMap:           []EnumMapEntry{{"Low", -1}},
		StreamDescriptor:  &StreamDescription{Format: Float32LE, Offset: 4},
		SchemaIdentifiers: new("de.example.gain"),
		TemplateReference: Path{9, 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	elems := []Element{
		&Node{
			Path:              Path{1, 2},
			Identifier:        new("node"),
			Description:       new("Node"),
			IsRoot:            new(true),
			IsOnline:          new(false),
			SchemaIdentifiers: new("a.b\nc.d"),
			TemplateReference: Path{3},
		},
		&Parameter{
			Path:             Path{1, 2, 3},
			Value:            Octets{0x00, 0xFF},
			Minimum:          Null{},
			Enumeration:      []string{},
			EnumMap:          []EnumMapEntry{},
			Access:           new(AccessWrite),
			StreamDescriptor: &StreamDescription{Format: UInt16BE},
		},
		&Parameter{Path: Path{4}, Value: Real(math.Inf(-1))},
		&Node{Path: Path{5}},
	}
	got := decodeElements(t, encode(t, elems...))
	if !reflect.DeepEqual([]Element(got), elems) {
		t.Fatalf("got  %+v\nwant %+v", got, elems)
	}
}

func TestDecodeSkipsUnsupported(t *testing.T) {
	data := root(
		app(2, ctx(0, integer(32))),                                  // command
		app(25, ctx(0, oid(2))),                                      // qualified template
		node(3, children(app(24, ctx(0, integer(1))), parameter(2))), // template + parameter
	)
	els := decodeElements(t, data)
	if len(els) != 2 || !slices.Equal(els[1].ElementPath(), Path{3, 2}) {
		t.Fatalf("got %+v", els)
	}
}

func TestDecodeStreams(t *testing.T) {
	var b ber.Builder
	b.Constructed(ber.Application(0), func(b *ber.Builder) {
		app(6,
			ctx(0, app(5, ctx(0, integer(110)), ctx(1, integer(-20)))),
			ctx(0, app(5, ctx(0, integer(111)), ctx(1, octets([]byte{1, 2, 3, 4})))),
		)(b)
	})
	data, _ := b.Bytes()
	msg, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	want := Streams{{110, Int(-20)}, {111, Octets{1, 2, 3, 4}}}
	if !reflect.DeepEqual(msg, want) {
		t.Fatalf("got %+v", msg)
	}
}

func TestDecodeDoesNotAliasInput(t *testing.T) {
	data := root(parameter(1, ctx(1, set(
		ctx(0, utf8("id")),
		ctx(2, octets([]byte{1, 2, 3})),
		ctx(18, oid(4, 5)),
	))))
	p := decodeElements(t, data)[0].(*Parameter)
	for i := range data {
		data[i] = 0
	}
	if *p.Identifier != "id" || !bytes.Equal(p.Value.(Octets), []byte{1, 2, 3}) || !slices.Equal(p.TemplateReference, Path{4, 5}) {
		t.Fatalf("decoded values changed: %+v", p)
	}
}

func TestDecodeDepthLimit(t *testing.T) {
	nested := func(depth int) []byte {
		el := node(1)
		for range depth - 1 {
			el = node(1, children(el))
		}
		return root(el)
	}
	if els := decodeElements(t, nested(MaxDepth)); len(els) != MaxDepth {
		t.Fatalf("got %d elements at max depth", len(els))
	}
	if _, err := Decode(nested(MaxDepth + 1)); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("got %v", err)
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		err  error
	}{
		{"empty", nil, ErrInvalid},
		{"empty qualified path", root(app(10, ctx(0, oid()))), ErrInvalid},
		{"wrong root", []byte{0x61, 0x00}, ErrNoMessage},
		{"unknown root choice", []byte{0x60, 0x02, 0x78, 0x00}, ErrNoMessage},
		{"missing number", root(app(3, ctx(1, set()))), ErrInvalid},
		{"negative number", root(node(-1)), ErrInvalid},
		{"entry not context 0", []byte{0x60, 0x04, 0x6B, 0x02, 0xA1, 0x00}, ErrInvalid},
		{"children not a collection", root(node(1, ctx(2, app(5)))), ErrInvalid},
		{"value of unknown type", root(parameter(1, ctx(1, set(ctx(2, oid(1)))))), ErrInvalid},
		{"wrong field type", root(parameter(1, ctx(1, set(ctx(0, integer(1)))))), nil},
	}
	for _, tt := range tests {
		_, err := Decode(tt.data)
		if err == nil {
			t.Errorf("%s: no error", tt.name)
			continue
		}
		if tt.err != nil && !errors.Is(err, tt.err) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.err)
		}
	}
	_, err := Decode(root(parameter(1, ctx(1, set(ctx(0, integer(1)))))))
	if _, ok := errors.AsType[*ber.TagError](err); !ok {
		t.Errorf("wrong field type: got %v", err)
	}
}

// ---- Model helpers --------------------------------------------------------

func TestMergeAndClone(t *testing.T) {
	p := &Parameter{Path: Path{1}, Identifier: new("gain"), Value: Int(1), Access: new(AccessRead)}
	u := &Parameter{Path: Path{1}, Value: Octets{1}, Minimum: Null{}, Enumeration: []string{}}
	p.Merge(u)
	if *p.Identifier != "gain" || *p.Access != AccessRead || p.Minimum != (Null{}) || p.Enumeration == nil {
		t.Fatalf("merged %+v", p)
	}
	u.Value.(Octets)[0] = 9
	if p.Value.(Octets)[0] != 1 {
		t.Fatal("Merge shares octets with the update")
	}

	c := p.Clone()
	if !reflect.DeepEqual(c, p) {
		t.Fatalf("clone %+v", c)
	}
	*c.Identifier = "changed"
	c.Path[0] = 9
	if *p.Identifier != "gain" || p.Path[0] != 1 {
		t.Fatal("Clone shares memory")
	}

	n := &Node{Path: Path{2}, IsOnline: new(true), HasChildren: true}
	n.Merge(&Node{Path: Path{2}, Description: new("x"), HasChildren: false})
	if !*n.IsOnline || *n.Description != "x" || !n.HasChildren {
		t.Fatalf("merged %+v", n)
	}
	if nc := n.Clone(); !reflect.DeepEqual(nc, n) {
		t.Fatalf("clone %+v", nc)
	}
	cmd := &Command{Path: Path{1}, Target: KindNode, Number: CmdGetDirectory}
	if cc := cmd.Clone(); !reflect.DeepEqual(cc, cmd) || &cc.Path[0] == &cmd.Path[0] {
		t.Fatal("command clone")
	}
}

func TestEffectiveType(t *testing.T) {
	tests := []struct {
		p    Parameter
		want ParameterType
		ok   bool
	}{
		{Parameter{Type: new(TypeTrigger), Value: Int(1)}, TypeTrigger, true},
		{Parameter{Enumeration: []string{"a"}, Value: Int(0)}, TypeEnum, true},
		{Parameter{EnumMap: []EnumMapEntry{}}, TypeEnum, true},
		{Parameter{Value: Real(1), Type: new(TypeInteger)}, TypeReal, true},
		{Parameter{Value: Null{}, Type: new(TypeInteger)}, TypeInteger, true},
		{Parameter{Type: new(TypeOctets)}, TypeOctets, true},
		{Parameter{}, 0, false},
	}
	for i, tt := range tests {
		if got, ok := tt.p.EffectiveType(); got != tt.want || ok != tt.ok {
			t.Errorf("%d: got %v, %v", i, got, ok)
		}
	}
}

func TestEnumEntries(t *testing.T) {
	p := &Parameter{Enumeration: []string{"Off", "~Debug", "On"}}
	want := []EnumEntry{{"Off", 0, false}, {"Debug", 1, true}, {"On", 2, false}}
	if got := p.EnumEntries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if name, ok := p.EnumName(1); !ok || name != "Debug" {
		t.Fatalf("EnumName: %q, %v", name, ok)
	}

	p.EnumMap = []EnumMapEntry{{"Low", 10}, {"Alias", 10}, {"High", 20}}
	if name, ok := p.EnumName(10); !ok || name != "Low" {
		t.Fatalf("EnumMap precedence/alias: %q, %v", name, ok)
	}
	if _, ok := p.EnumName(1); ok {
		t.Fatal("Enumeration used although EnumMap is present")
	}
	if (&Parameter{}).EnumEntries() != nil {
		t.Fatal("entries without enumeration")
	}
}

func TestStreamDescriptionExtract(t *testing.T) {
	data := []byte{0xFF, 0xFE, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	f32 := math.Float32bits(-1.5)
	f64 := math.Float64bits(0.25)
	floats := []byte{byte(f32 >> 24), byte(f32 >> 16), byte(f32 >> 8), byte(f32)}
	floats = append(floats, byte(f64), byte(f64>>8), byte(f64>>16), byte(f64>>24),
		byte(f64>>32), byte(f64>>40), byte(f64>>48), byte(f64>>56))

	tests := []struct {
		d    StreamDescription
		data []byte
		want Value
	}{
		{StreamDescription{UInt8, 0}, data, Int(0xFF)},
		{StreamDescription{Int8, 0}, data, Int(-1)},
		{StreamDescription{UInt16BE, 0}, data, Int(0xFFFE)},
		{StreamDescription{Int16BE, 0}, data, Int(-2)},
		{StreamDescription{Int16LE, 0}, data, Int(-257)},
		{StreamDescription{UInt32LE, 2}, data, Int(0x04030201)},
		{StreamDescription{Int32BE, 2}, data, Int(0x01020304)},
		{StreamDescription{UInt64BE, 2}, data, Int(0x0102030405060708)},
		{StreamDescription{Int64LE, 2}, data, Int(0x0807060504030201)},
		{StreamDescription{Float32BE, 0}, floats, Real(-1.5)},
		{StreamDescription{Float64LE, 4}, floats, Real(0.25)},
	}
	for _, tt := range tests {
		got, err := tt.d.Extract(tt.data)
		if err != nil || got != tt.want {
			t.Errorf("%+v: got %v, %v; want %v", tt.d, got, err, tt.want)
		}
	}
	for _, d := range []StreamDescription{{UInt32BE, 7}, {UInt8, -1}, {16, 0}, {1, 0}, {24, 0}} {
		if _, err := d.Extract(data); err == nil {
			t.Errorf("%+v: no error", d)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(root(node(1, ctx(1, set(ctx(0, utf8("Device")))), children(parameter(2, ctx(1, set(ctx(2, integer(5)))))))))
	f.Add([]byte{0x60, 0x0B, 0x6B, 0x09, 0xA0, 0x07, 0x62, 0x05, 0xA0, 0x03, 0x02, 0x01, 0x20})
	f.Fuzz(func(t *testing.T, data []byte) {
		msg, err := Decode(data)
		if err != nil {
			return
		}
		if els, ok := msg.(Elements); ok {
			for _, el := range els {
				if el.ElementPath() == nil {
					t.Fatalf("element without path: %#v", el)
				}
			}
			var b ber.Builder
			if err := Encode(&b, els...); err != nil {
				t.Fatalf("re-encoding decoded elements: %v", err)
			}
		}
	})
}

func TestNodeIsEmptyAndParameterIsStream(t *testing.T) {
	if !(&Node{Path: Path{1}}).IsEmpty() {
		t.Error("bare node not empty")
	}
	if (&Node{Path: Path{1}, HasChildren: true}).IsEmpty() || (&Node{Path: Path{1}, IsOnline: new(true)}).IsEmpty() {
		t.Error("node with children or contents is empty")
	}
	if (&Parameter{}).IsStream() || !(&Parameter{StreamIdentifier: new(int32(0))}).IsStream() {
		t.Error("IsStream")
	}
}
