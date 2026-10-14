package glow

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
)

func function(n int64, fs ...bf) bf { return app(19, append([]bf{ctx(0, integer(n))}, fs...)...) }

func tupleItem(t ParameterType, name string) bf {
	if name == "" {
		return app(21, ctx(0, integer(int64(t))))
	}
	return app(21, ctx(0, integer(int64(t))), ctx(1, utf8(name)))
}

func rootWith(f bf) []byte {
	var b ber.Builder
	b.Constructed(ber.Application(0), f)
	data, _ := b.Bytes()
	return bytes.Clone(data)
}

func TestDecodeFunctionDescription(t *testing.T) {
	// Specification, "Transmission of the Function Description".
	data := root(app(10, ctx(0, oid(1, 2)), children(function(1, ctx(1, set(
		ctx(0, utf8("setObjectNameRecursive")),
		ctx(1, utf8("Sample Function")),
		ctx(2, sequenceOf(
			tupleItem(TypeInteger, "objectId"),
			tupleItem(TypeBoolean, "isRecursive"),
			tupleItem(TypeString, "newName"),
		)),
		ctx(3, sequenceOf(tupleItem(TypeInteger, "changeCount"))),
	))))))
	els := decodeElements(t, data)
	if len(els) != 2 {
		t.Fatalf("got %d elements", len(els))
	}
	want := &Function{
		Path:        Path{1, 2, 1},
		Identifier:  new("setObjectNameRecursive"),
		Description: new("Sample Function"),
		Arguments: []TupleItem{
			{TypeInteger, "objectId"},
			{TypeBoolean, "isRecursive"},
			{TypeString, "newName"},
		},
		Result: []TupleItem{{TypeInteger, "changeCount"}},
	}
	if !reflect.DeepEqual(els[1], want) {
		t.Fatalf("got  %+v\nwant %+v", els[1], want)
	}
}

func TestEncodeInvoke(t *testing.T) {
	// Specification, "Invocation of Functions".
	got := encode(t, &Command{
		Path:   Path{1, 2, 1},
		Target: KindFunction,
		Number: CmdInvoke,
		Invocation: &Invocation{
			ID:        new(int32(1)),
			Arguments: []Value{Int(123), Bool(true), String("Herbert")},
		},
	})
	want := root(app(20, ctx(0, oid(1, 2, 1)), children(app(2,
		ctx(0, integer(33)),
		ctx(2, app(22,
			ctx(0, integer(1)),
			ctx(1, sequenceOf(integer(123), boolean(true), utf8("Herbert"))),
		)),
	))))
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X\nwant % X", got, want)
	}
}

func TestEncodeInvokeFireAndForget(t *testing.T) {
	got := encode(t, &Command{Path: Path{4}, Target: KindFunction, Number: CmdInvoke, Invocation: &Invocation{}})
	want := root(app(20, ctx(0, oid(4)), children(app(2, ctx(0, integer(33)), ctx(2, app(22))))))
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X\nwant % X", got, want)
	}
}

func TestEncodeInvokeErrors(t *testing.T) {
	for _, c := range []*Command{
		{Number: CmdInvoke},
		{Path: Path{1}, Target: KindNode, Number: CmdInvoke},
		{Path: Path{1}, Target: KindFunction, Number: CmdGetDirectory, Invocation: &Invocation{}},
	} {
		var b ber.Builder
		if err := Encode(&b, c); err == nil {
			t.Errorf("%+v: no error", c)
		}
	}
}

func TestDecodeInvocationResult(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want *InvocationResult
	}{
		{
			// Specification, "Invocation of Functions": 74 objects have been changed through invocation 1.
			"spec example",
			rootWith(app(23, ctx(0, integer(1)), ctx(2, sequenceOf(integer(74))))),
			&InvocationResult{ID: 1, Success: true, Result: []Value{Int(74)}},
		},
		{
			"failure without result",
			rootWith(app(23, ctx(0, integer(7)), ctx(1, boolean(false)))),
			&InvocationResult{ID: 7, Success: false},
		},
		{
			"empty result tuple",
			rootWith(app(23, ctx(0, integer(2)), ctx(2, sequenceOf()))),
			&InvocationResult{ID: 2, Success: true, Result: []Value{}},
		},
	}
	for _, tt := range tests {
		msg, err := Decode(tt.data)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if !reflect.DeepEqual(msg, tt.want) {
			t.Errorf("%s: got %+v, want %+v", tt.name, msg, tt.want)
		}
	}
}

func TestDecodeInvocationResultErrors(t *testing.T) {
	for name, data := range map[string][]byte{
		"missing id":       rootWith(app(23, ctx(1, boolean(true)))),
		"value of oid":     rootWith(app(23, ctx(0, integer(1)), ctx(2, sequenceOf(oid(1))))),
		"result not tuple": rootWith(app(23, ctx(0, integer(1)), ctx(2, integer(5)))),
	} {
		if _, err := Decode(data); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Decode(rootWith(app(23, ctx(1, boolean(true))))); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing id: got %v", err)
	}
}

func TestFunctionRoundTrip(t *testing.T) {
	elems := []Element{
		&Function{
			Path:              Path{1, 5},
			Identifier:        new("reset"),
			Description:       new("Reset"),
			Arguments:         []TupleItem{{Type: TypeInteger}},
			Result:            []TupleItem{},
			TemplateReference: Path{9},
		},
		&Function{Path: Path{2}},
	}
	got := decodeElements(t, encode(t, elems...))
	if !reflect.DeepEqual([]Element(got), elems) {
		t.Fatalf("got  %+v\nwant %+v", got, elems)
	}
}

func TestFunctionMergeAndClone(t *testing.T) {
	f := &Function{Path: Path{1}, Identifier: new("f"), Arguments: []TupleItem{{TypeInteger, "a"}}}
	f.Merge(&Function{Path: Path{1}, Result: []TupleItem{{TypeBoolean, "ok"}}})
	if *f.Identifier != "f" || len(f.Arguments) != 1 || len(f.Result) != 1 {
		t.Fatalf("merged %+v", f)
	}
	c := f.Clone()
	if !reflect.DeepEqual(c, f) {
		t.Fatalf("clone %+v", c)
	}
	c.Arguments[0].Name = "changed"
	if f.Arguments[0].Name != "a" {
		t.Fatal("Clone shares arguments")
	}
}

func TestCommandCloneInvocation(t *testing.T) {
	c := &Command{Path: Path{1}, Target: KindFunction, Number: CmdInvoke, Invocation: &Invocation{
		ID:        new(int32(3)),
		Arguments: []Value{Octets{1}},
	}}
	cc := c.Clone()
	if !reflect.DeepEqual(cc, c) {
		t.Fatalf("clone %+v", cc)
	}
	*cc.Invocation.ID = 9
	cc.Invocation.Arguments[0].(Octets)[0] = 9
	if *c.Invocation.ID != 3 || c.Invocation.Arguments[0].(Octets)[0] != 1 {
		t.Fatal("Clone shares the invocation")
	}
}
