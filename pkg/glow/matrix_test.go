package glow

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
)

func matrix(n int64, fs ...bf) bf { return app(13, append([]bf{ctx(0, integer(n))}, fs...)...) }

func sequenceOf(es ...bf) bf {
	return func(b *ber.Builder) {
		b.Constructed(ber.TagSequence, func(b *ber.Builder) {
			for _, e := range es {
				ctx(0, e)(b)
			}
		})
	}
}

func connection(target int64, fs ...bf) bf {
	return app(16, append([]bf{ctx(0, integer(target))}, fs...)...)
}

func signal(tag uint32, n int64) bf { return app(tag, ctx(0, integer(n))) }

func TestDecodeMatrixDescription(t *testing.T) {
	// Specification, "Transmission of the Matrix Description".
	data := root(app(10, ctx(0, oid(1, 2)), children(matrix(1, ctx(1, set(
		ctx(0, utf8("matrix")),
		ctx(1, utf8("Sample Matrix")),
		ctx(2, integer(2)),
		ctx(4, integer(4)),
		ctx(5, integer(4)),
		ctx(8, oid(1, 2, 2)),
		ctx(9, integer(1)),
		ctx(10, sequenceOf(
			app(18, ctx(0, oid(1, 2, 3, 1)), ctx(1, utf8("Primary"))),
			app(18, ctx(0, oid(1, 2, 3, 2)), ctx(1, utf8("Internal"))),
		)),
	))))))
	els := decodeElements(t, data)
	if len(els) != 2 {
		t.Fatalf("got %d elements", len(els))
	}
	got := els[1].(*Matrix)
	want := &Matrix{
		Path:                Path{1, 2, 1},
		Identifier:          new("matrix"),
		Description:         new("Sample Matrix"),
		Type:                new(NToN),
		TargetCount:         new(int32(4)),
		SourceCount:         new(int32(4)),
		ParametersLocation:  BasePath{1, 2, 2},
		GainParameterNumber: new(int32(1)),
		Labels: []Label{
			{BasePath: Path{1, 2, 3, 1}, Description: "Primary"},
			{BasePath: Path{1, 2, 3, 2}, Description: "Internal"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestDecodeMatrixConnections(t *testing.T) {
	// Specification, "Transmission of Connections".
	data := root(app(17, ctx(0, oid(1, 2, 1)),
		ctx(1, set(ctx(0, utf8("matrix")))),
		ctx(5, sequenceOf(
			connection(0, ctx(1, oid(3))),
			connection(1, ctx(1, oid(0, 1))),
			connection(2, ctx(1, oid(3, 1, 2)), ctx(3, integer(1))),
			connection(3),
		)),
	))
	m := decodeElements(t, data)[0].(*Matrix)
	want := []Connection{
		{Target: 0, Sources: []uint32{3}},
		{Target: 1, Sources: []uint32{0, 1}},
		{Target: 2, Sources: []uint32{3, 1, 2}, Disposition: Modified},
		{Target: 3},
	}
	if !reflect.DeepEqual(m.Connections, want) {
		t.Fatalf("got %+v", m.Connections)
	}
}

func TestDecodeMatrixNonLinear(t *testing.T) {
	data := root(app(17, ctx(0, oid(5)),
		ctx(1, set(ctx(3, integer(1)), ctx(8, integer(7)))),
		ctx(3, sequenceOf(signal(14, 100), signal(14, 205))),
		ctx(4, sequenceOf(signal(15, 9))),
	))
	m := decodeElements(t, data)[0].(*Matrix)
	if *m.AddressingMode != NonLinear || m.ParametersLocation != Inline(7) ||
		!slices.Equal(m.Targets, []uint32{100, 205}) || !slices.Equal(m.Sources, []uint32{9}) || m.Connections != nil {
		t.Fatalf("got %+v", m)
	}
}

func TestEncodeConnect(t *testing.T) {
	// Specification, "Issuing Connects".
	got := encode(t, &Matrix{Path: Path{1, 2, 1}, Connections: []Connection{
		{Target: 0, Sources: []uint32{0, 2}, Operation: OpConnect},
		{Target: 1, Sources: []uint32{2}, Operation: OpConnect},
	}})
	want := root(app(17, ctx(0, oid(1, 2, 1)), ctx(5, sequenceOf(
		connection(0, ctx(1, oid(0, 2)), ctx(2, integer(1))),
		connection(1, ctx(1, oid(2)), ctx(2, integer(1))),
	))))
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X\nwant % X", got, want)
	}
}

func TestMatrixRoundTrip(t *testing.T) {
	elems := []Element{
		&Matrix{
			Path:                     Path{1, 1, 1},
			Identifier:               new("inputs"),
			Description:              new("GPIs"),
			Type:                     new(OneToOne),
			AddressingMode:           new(NonLinear),
			TargetCount:              new(int32(2)),
			SourceCount:              new(int32(1)),
			MaximumTotalConnects:     new(int32(2)),
			MaximumConnectsPerTarget: new(int32(1)),
			ParametersLocation:       Inline(3),
			GainParameterNumber:      new(int32(1)),
			Labels:                   []Label{{BasePath: Path{1, 9}, Description: "Main"}},
			SchemaIdentifiers:        new("de.example.matrix"),
			TemplateReference:        Path{7},
			Targets:                  []uint32{10, 20},
			Sources:                  []uint32{},
			Connections: []Connection{
				{Target: 10, Sources: []uint32{}, Disposition: Locked},
				{Target: 20, Operation: OpDisconnect},
			},
		},
		&Matrix{Path: Path{2}, ParametersLocation: BasePath{3, 4}},
	}
	got := decodeElements(t, encode(t, elems...))
	if !reflect.DeepEqual([]Element(got), elems) {
		t.Fatalf("got  %+v\nwant %+v", got, elems)
	}
}

func TestMatrixMerge(t *testing.T) {
	m := &Matrix{
		Path:        Path{1},
		Identifier:  new("matrix"),
		TargetCount: new(int32(3)),
		Connections: []Connection{{Target: 0, Sources: []uint32{1}}, {Target: 1}},
	}
	u := &Matrix{
		Path:        Path{1},
		Description: new("Matrix"),
		Connections: []Connection{
			{Target: 1, Sources: []uint32{2, 3}, Disposition: Modified},
			{Target: 2, Sources: []uint32{0}},
		},
	}
	m.Merge(u)
	want := []Connection{
		{Target: 0, Sources: []uint32{1}},
		{Target: 1, Sources: []uint32{2, 3}, Disposition: Modified},
		{Target: 2, Sources: []uint32{0}},
	}
	if *m.Identifier != "matrix" || *m.Description != "Matrix" || *m.TargetCount != 3 || !reflect.DeepEqual(m.Connections, want) {
		t.Fatalf("merged %+v", m)
	}
	u.Connections[0].Sources[0] = 99
	if m.Connections[1].Sources[0] != 2 {
		t.Fatal("Merge shares sources with the update")
	}
	if c, ok := m.Connection(2); !ok || !slices.Equal(c.Sources, []uint32{0}) {
		t.Fatalf("Connection(2) = %+v, %v", c, ok)
	}
	if _, ok := m.Connection(7); ok {
		t.Fatal("Connection(7) found")
	}
}

func connections(targets ...uint32) []Connection {
	cs := make([]Connection, len(targets))
	for i, t := range targets {
		cs[i] = Connection{Target: t, Sources: []uint32{0}}
	}
	return cs
}

func targetsOf(cs []Connection) []uint32 {
	var ts []uint32
	for _, c := range cs {
		ts = append(ts, c.Target)
	}
	return ts
}

func TestMatrixMergePrunesRemovedTargets(t *testing.T) {
	tests := []struct {
		name   string
		matrix Matrix
		update Matrix
		want   []uint32
	}{
		{
			name:   "linear targetCount shrinks",
			matrix: Matrix{TargetCount: new(int32(4)), Connections: connections(0, 1, 2, 3)},
			update: Matrix{TargetCount: new(int32(2))},
			want:   []uint32{0, 1},
		},
		{
			name:   "non-linear targets change",
			matrix: Matrix{AddressingMode: new(NonLinear), Targets: []uint32{10, 20, 30}, Connections: connections(10, 20, 30)},
			update: Matrix{Targets: []uint32{10, 30}, TargetCount: new(int32(2))},
			want:   []uint32{10, 30},
		},
		{
			name:   "non-linear targetCount is not a bound",
			matrix: Matrix{AddressingMode: new(NonLinear), Connections: connections(10, 20)},
			update: Matrix{TargetCount: new(int32(2))},
			want:   []uint32{10, 20},
		},
		{
			name:   "linear restricted by targets list",
			matrix: Matrix{TargetCount: new(int32(8)), Connections: connections(0, 3, 5)},
			update: Matrix{Targets: []uint32{3, 5, 9}},
			want:   []uint32{3, 5},
		},
		{
			name:   "switch to non-linear without targets",
			matrix: Matrix{TargetCount: new(int32(2)), Connections: connections(0, 1)},
			update: Matrix{AddressingMode: new(NonLinear)},
			want:   []uint32{0, 1},
		},
		{
			name:   "connection update alone does not prune",
			matrix: Matrix{TargetCount: new(int32(2)), Connections: connections(0, 1)},
			update: Matrix{Connections: connections(5)},
			want:   []uint32{0, 1, 5},
		},
		{
			name:   "new structure and connections in one update",
			matrix: Matrix{TargetCount: new(int32(4)), Connections: connections(0, 1, 2, 3)},
			update: Matrix{TargetCount: new(int32(3)), Connections: connections(1, 7)},
			want:   []uint32{0, 1, 2},
		},
	}
	for _, tt := range tests {
		m := tt.matrix
		m.Merge(&tt.update)
		if got := targetsOf(m.Connections); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got targets %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestMatrixClone(t *testing.T) {
	m := &Matrix{
		Path:               Path{1},
		ParametersLocation: BasePath{1, 2},
		Labels:             []Label{{BasePath: Path{3}, Description: "l"}},
		Connections:        []Connection{{Target: 0, Sources: []uint32{1}}},
	}
	c := m.Clone()
	if !reflect.DeepEqual(c, m) {
		t.Fatalf("clone %+v", c)
	}
	c.ParametersLocation.(BasePath)[0] = 9
	c.Labels[0].BasePath[0] = 9
	c.Connections[0].Sources[0] = 9
	if m.ParametersLocation.(BasePath)[0] != 1 || m.Labels[0].BasePath[0] != 3 || m.Connections[0].Sources[0] != 1 {
		t.Fatal("Clone shares memory")
	}
	if e := (&Matrix{Path: Path{1}, Connections: []Connection{}}).Clone(); e.Connections == nil {
		t.Fatal("Clone loses empty connections")
	}
}

func TestDecodeMatrixErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"negative target", root(app(17, ctx(0, oid(1)), ctx(5, sequenceOf(connection(-1)))))},
		{"wrong connection tag", root(app(17, ctx(0, oid(1)), ctx(5, sequenceOf(app(14, ctx(0, integer(0)))))))},
		{"wrong signal tag", root(app(17, ctx(0, oid(1)), ctx(3, sequenceOf(signal(15, 0)))))},
		{"location type", root(app(17, ctx(0, oid(1)), ctx(1, set(ctx(8, utf8("x"))))))},
		{"missing number", root(app(13, ctx(1, set())))},
	}
	for _, tt := range tests {
		if _, err := Decode(tt.data); err == nil {
			t.Errorf("%s: no error", tt.name)
		}
	}
	if _, err := Decode(root(app(17, ctx(0, oid(1)), ctx(1, set(ctx(8, utf8("x"))))))); !errors.Is(err, ErrInvalid) {
		t.Errorf("location type: got %v", err)
	}
}

func TestEncodeMatrixWithoutPath(t *testing.T) {
	var b ber.Builder
	if err := Encode(&b, &Matrix{}); err == nil {
		t.Fatal("no error")
	}
}
