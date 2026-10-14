package emberplus

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
	"github.com/lukirs95/goemberplusclient/pkg/s101"
)

// deviceTree is a small provider tree:
//
//	1 root
//	├── 1 gpio (node without children)
//	├── 2 dsp
//	│   └── 1 gain.left (parameter, identifier contains a dot)
//	└── 100 settings
//	    └── 3 info
//	        ├── 1 moniker
//	        └── 2 version
func deviceTree() []glow.Element {
	return []glow.Element{
		&glow.Node{Path: glow.Path{1}, Identifier: new("root"), Description: new("Device")},
		&glow.Node{Path: glow.Path{1, 1}, Identifier: new("gpio")},
		&glow.Node{Path: glow.Path{1, 2}, Identifier: new("dsp")},
		&glow.Parameter{Path: glow.Path{1, 2, 1}, Identifier: new("gain.left"), Value: glow.Real(-6)},
		&glow.Node{Path: glow.Path{1, 100}, Identifier: new("settings")},
		&glow.Node{Path: glow.Path{1, 100, 3}, Identifier: new("info"), Description: new("Info")},
		&glow.Parameter{Path: glow.Path{1, 100, 3, 1}, Identifier: new("moniker"), Value: glow.String("box")},
		&glow.Parameter{Path: glow.Path{1, 100, 3, 2}, Identifier: new("version"), Value: glow.String("02.46"), Access: new(glow.AccessRead)},
	}
}

func requestedPaths(p *fakeProvider) []string {
	var paths []string
	for _, r := range p.requests() {
		paths = append(paths, r.path.String())
	}
	return paths
}

func TestParsePath(t *testing.T) {
	tests := []struct {
		path string
		want []segment
	}{
		{"", nil},
		{"/", nil},
		{"1.100.3", []segment{{number: 1}, {number: 100}, {number: 3}}},
		{"7", []segment{{number: 7}}},
		{"root/settings/info", []segment{{identifier: "root"}, {identifier: "settings"}, {identifier: "info"}}},
		{"/root/100/info", []segment{{identifier: "root"}, {number: 100}, {identifier: "info"}}},
		{"1/100/3", []segment{{number: 1}, {number: 100}, {number: 3}}},
		{"dsp/gain.left", []segment{{identifier: "dsp"}, {identifier: "gain.left"}}},
		// "." only separates numeric paths.
		{"root.settings.info", []segment{{identifier: "root.settings.info"}}},
		{"gain.left", []segment{{identifier: "gain.left"}}},
	}
	for _, tt := range tests {
		got, err := parsePath(tt.path)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%q: got %v, %v; want %v", tt.path, got, err, tt.want)
		}
	}
	for _, path := range []string{"root//info", "1..2", "root/", "1.", "1.99999999999"} {
		if _, err := parsePath(path); err == nil {
			t.Errorf("%q: no error", path)
		}
	}
}

func TestGetDirectoryRoot(t *testing.T) {
	p, c := startFake(t, nil)
	res := result(func() ([]glow.Element, error) { return c.GetDirectory(testContext(t), "") })

	r := p.expect()
	if r.tag != ber.Application(2) || len(r.path) != 0 || r.number != glow.CmdGetDirectory || !r.hasMask || r.mask != glow.FieldsAll {
		t.Fatalf("request %+v", r)
	}
	p.send(&glow.Node{Path: glow.Path{1}, Identifier: new("root"), Description: new("Device")})

	got := <-res
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.v) != 1 || *got.v[0].(*glow.Node).Identifier != "root" {
		t.Fatalf("got %+v", got.v)
	}
}

func TestPathForms(t *testing.T) {
	p, c := startFake(t, staticTree(deviceTree()...))
	ctx := testContext(t)
	want := glow.Path{1, 100, 3}

	for _, path := range []string{"root/settings/info", "/root/settings/info", "root/100/info", "1/settings/3", "1.100.3", "1/100/3"} {
		el, err := c.Get(ctx, path)
		if err != nil || !slices.Equal(el.ElementPath(), want) {
			t.Errorf("%q: got %v, %v", path, el, err)
		}
	}
	// Only the first resolution needs the directories of root, 1 and 1.100.
	if got := requestedPaths(p); !slices.Equal(got, []string{"", "1", "1.100"}) {
		t.Fatalf("requests for %q", got)
	}
	if _, err := c.Get(ctx, "root.settings.info"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dotted identifier path: %v", err)
	}
}

func TestNumericPathLoadsAncestors(t *testing.T) {
	p, c := startFake(t, staticTree(deviceTree()...))
	// The client loads the ancestors of an unknown numeric path, so it knows
	// that 1.100.3.1 is a parameter and requests it as such.
	els, err := c.GetDirectory(testContext(t), "1.100.3.1")
	if err != nil || len(els) != 1 || els[0].(*glow.Parameter).Value != glow.String("box") {
		t.Fatalf("got %+v, %v", els, err)
	}
	if got := requestedPaths(p); !slices.Equal(got, []string{"", "1", "1.100", "1.100.3", "1.100.3.1"}) {
		t.Fatalf("requests for %q", got)
	}
	if r := p.requests()[4]; r.tag != ber.Application(9) {
		t.Fatalf("parameter requested as %v", r.tag)
	}
}

func TestIdentifierWithDot(t *testing.T) {
	_, c := startFake(t, staticTree(deviceTree()...))
	el, err := c.Get(testContext(t), "root/dsp/gain.left")
	if err != nil || !slices.Equal(el.ElementPath(), glow.Path{1, 2, 1}) {
		t.Fatalf("got %v, %v", el, err)
	}
}

func TestNotFound(t *testing.T) {
	p, c := startFake(t, staticTree(deviceTree()...))
	ctx := testContext(t)
	for _, path := range []string{"root/missing", "1.5", "root/settings/info/version/deeper"} {
		if _, err := c.Get(ctx, path); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: got %v", path, err)
		}
	}
	n := len(p.requests())
	// The directory of root is loaded now, so no further request is needed.
	if _, err := c.Get(ctx, "root/other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if len(p.requests()) != n {
		t.Fatal("request sent for a loaded directory")
	}
}

func TestGetDirectoryKinds(t *testing.T) {
	p, c := startFake(t, staticTree(deviceTree()...))
	ctx := testContext(t)

	els, err := c.GetDirectory(ctx, "root/settings/info")
	if err != nil || len(els) != 2 || *els[1].(*glow.Parameter).Identifier != "version" {
		t.Fatalf("info: %+v, %v", els, err)
	}

	// A node without children is answered with an empty node.
	els, err = c.GetDirectory(ctx, "root/gpio")
	if err != nil || len(els) != 0 {
		t.Fatalf("gpio: %+v, %v", els, err)
	}

	// A known parameter is requested as qualified parameter and returned itself.
	els, err = c.GetDirectory(ctx, "root/settings/info/version")
	if err != nil || len(els) != 1 || els[0].(*glow.Parameter).Value != glow.String("02.46") {
		t.Fatalf("version: %+v, %v", els, err)
	}
	reqs := p.requests()
	if last := reqs[len(reqs)-1]; last.tag != ber.Application(9) {
		t.Fatalf("parameter requested as %v", last.tag)
	}

	// GetDirectory always sends a request, even for loaded directories.
	n := len(p.requests())
	if _, err := c.GetDirectory(ctx, "root/settings/info"); err != nil || len(p.requests()) != n+1 {
		t.Fatalf("refresh: %v, %d requests", err, len(p.requests())-n)
	}
}

func TestGetLoadsAsNeeded(t *testing.T) {
	p, c := startFake(t, staticTree(deviceTree()...))
	ctx := testContext(t)

	el, err := c.Get(ctx, "root/settings/info/version")
	if err != nil {
		t.Fatal(err)
	}
	v := el.(*glow.Parameter)
	if v.Value != glow.String("02.46") || *v.Access != glow.AccessRead {
		t.Fatalf("got %+v", v)
	}
	if got := requestedPaths(p); !slices.Equal(got, []string{"", "1", "1.100", "1.100.3"}) {
		t.Fatalf("requests for %q", got)
	}

	// Further reads come from the mirror.
	n := len(p.requests())
	if _, err := c.Get(ctx, "1.100.3.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "root/settings/info"); err != nil {
		t.Fatal(err)
	}
	if len(p.requests()) != n {
		t.Fatal("request sent for a loaded element")
	}

	// Snapshots are copies.
	*v.Access = glow.AccessReadWrite
	if el, _ := c.Get(ctx, "1.100.3.2"); *el.(*glow.Parameter).Access != glow.AccessRead {
		t.Fatal("snapshot shares memory with the mirror")
	}
	if _, err := c.Get(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("root: %v", err)
	}
}

func TestChildrenAndIdentifierPath(t *testing.T) {
	p, c := startFake(t, staticTree(deviceTree()...))
	ctx := testContext(t)

	children, err := c.Children(ctx, "root/settings/info")
	if err != nil || len(children) != 2 {
		t.Fatalf("children %+v, %v", children, err)
	}
	// Children of a loaded directory need no request.
	n := len(p.requests())
	if _, err := c.Children(ctx, "root/settings/info"); err != nil || len(p.requests()) != n {
		t.Fatalf("second Children: %v, %d new requests", err, len(p.requests())-n)
	}
	if children, err := c.Children(ctx, "root/gpio"); err != nil || len(children) != 0 {
		t.Fatalf("gpio children %+v, %v", children, err)
	}

	settings, _ := c.Get(ctx, "root/settings")
	if !settings.(*glow.Node).HasChildren {
		t.Fatal("HasChildren of a loaded node")
	}

	// IdentifierPath of a numeric path that is not loaded yet.
	_, c2 := startFake(t, staticTree(deviceTree()...))
	id, err := c2.IdentifierPath(ctx, "1.100.3.2")
	if err != nil || id != "root/settings/info/version" {
		t.Fatalf("IdentifierPath: %q, %v", id, err)
	}
}

func TestIsOnline(t *testing.T) {
	p, c := startFake(t, staticTree(deviceTree()...))
	ctx := testContext(t)
	if on, err := c.IsOnline(ctx, "root/settings/info/version"); err != nil || !on {
		t.Fatalf("got %v, %v", on, err)
	}

	p.send(&glow.Node{Path: glow.Path{1, 100}, IsOnline: new(false)})
	waitFor(t, func() bool { on, _ := c.IsOnline(ctx, "root/settings/info/version"); return !on })

	// A value implies that its element and all parents are online.
	p.send(&glow.Parameter{Path: glow.Path{1, 100, 3, 1}, Value: glow.String("new")})
	waitFor(t, func() bool { on, _ := c.IsOnline(ctx, "root/settings"); return on })
}

func TestGetDirectoryCoalesces(t *testing.T) {
	p, c := startFake(t, nil)
	ctx := testContext(t)
	a := result(func() ([]glow.Element, error) { return c.GetDirectory(ctx, "") })
	p.expect()
	b := result(func() ([]glow.Element, error) { return c.GetDirectory(ctx, "") })
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.dirs[""] != nil && c.dirs[""].waiting == 2
	})
	p.send(&glow.Node{Path: glow.Path{1}})
	for _, ch := range []<-chan struct {
		v   []glow.Element
		err error
	}{a, b} {
		if r := <-ch; r.err != nil || len(r.v) != 1 {
			t.Fatalf("got %+v, %v", r.v, r.err)
		}
	}
	p.expectNone(50 * time.Millisecond)
}

func TestGetDirectoryTimeout(t *testing.T) {
	p, c := startFake(t, nil, WithRequestTimeout(50*time.Millisecond))
	if _, err := c.GetDirectory(context.Background(), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	p.expect()
	// The abandoned request must not swallow the next one.
	res := result(func() ([]glow.Element, error) { return c.GetDirectory(testContext(t), "") })
	p.expect()
	p.send(&glow.Node{Path: glow.Path{1}})
	if r := <-res; r.err != nil {
		t.Fatal(r.err)
	}
}

func TestMatrixDirectory(t *testing.T) {
	matrix := &glow.Matrix{Path: glow.Path{1, 1}, Identifier: new("inputs"), TargetCount: new(int32(4)), SourceCount: new(int32(1))}
	static := staticTree(&glow.Node{Path: glow.Path{1}, Identifier: new("gpio")}, matrix)
	// The matrix GetDirectory is answered manually, everything else statically.
	p, c := startFake(t, func(p *fakeProvider, i int, r request) {
		if slices.Equal(r.path, matrix.Path) {
			p.manual <- r
			return
		}
		static(p, i, r)
	})
	ctx := testContext(t)

	// Lawo style: one connection per message. Only the last one completes.
	res := result(func() ([]glow.Element, error) { return c.GetDirectory(ctx, "gpio/inputs") })
	if r := p.expect(); r.tag != ber.Application(17) {
		t.Fatalf("matrix requested as %v", r.tag)
	}
	for target := range uint32(3) {
		p.send(&glow.Matrix{Path: matrix.Path, Connections: []glow.Connection{{Target: target}}})
	}
	select {
	case r := <-res:
		t.Fatalf("completed early: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	p.send(&glow.Matrix{Path: matrix.Path, Connections: []glow.Connection{{Target: 3, Sources: []uint32{0}}}})
	r := <-res
	if r.err != nil || len(r.v[0].(*glow.Matrix).Connections) != 4 {
		t.Fatalf("got %+v, %v", r.v, r.err)
	}

	// Spec style: the complete matrix in one message.
	res = result(func() ([]glow.Element, error) { return c.GetDirectory(ctx, "gpio/inputs") })
	p.expect()
	p.send(&glow.Matrix{Path: matrix.Path, Connections: []glow.Connection{{Target: 0}, {Target: 1}, {Target: 2}, {Target: 3}}})
	if r := <-res; r.err != nil {
		t.Fatal(r.err)
	}
}

func TestStreams(t *testing.T) {
	p, c := startFake(t, staticTree(
		&glow.Node{Path: glow.Path{1}, Identifier: new("root")},
		&glow.Parameter{Path: glow.Path{1, 1}, Identifier: new("level"), StreamIdentifier: new(int32(7))},
		&glow.Parameter{Path: glow.Path{1, 2}, Identifier: new("left"), StreamIdentifier: new(int32(9)),
			StreamDescriptor: &glow.StreamDescription{Format: glow.Int16BE, Offset: 0}},
		&glow.Parameter{Path: glow.Path{1, 3}, Identifier: new("right"), StreamIdentifier: new(int32(9)),
			StreamDescriptor: &glow.StreamDescription{Format: glow.Int16BE, Offset: 2}},
	))
	ctx := testContext(t)
	if _, err := c.Children(ctx, "root"); err != nil {
		t.Fatal(err)
	}

	var b ber.Builder
	b.Constructed(ber.Application(0), func(b *ber.Builder) {
		b.Constructed(ber.Application(6), func(b *ber.Builder) {
			entry := func(id int64, value func(*ber.Builder)) {
				b.Constructed(ber.Context(0), func(b *ber.Builder) {
					b.Constructed(ber.Application(5), func(b *ber.Builder) {
						b.Constructed(ber.Context(0), func(b *ber.Builder) { b.Int(id) })
						b.Constructed(ber.Context(1), value)
					})
				})
			}
			entry(7, func(b *ber.Builder) { b.Int(-20) })
			entry(9, func(b *ber.Builder) { b.Octets([]byte{0xFF, 0xF6, 0x00, 0x05}) })
		})
	})
	payload, _ := b.Bytes()
	p.sendPayload(payload)

	want := map[string]glow.Value{"root/level": glow.Int(-20), "root/left": glow.Int(-10), "root/right": glow.Int(5)}
	waitFor(t, func() bool {
		for path, v := range want {
			el, err := c.Get(ctx, path)
			if err != nil || el.(*glow.Parameter).Value != v {
				return false
			}
		}
		return true
	})
}

func TestInvalidMessageKeepsConnection(t *testing.T) {
	p, c := startFake(t, nil)
	res := result(func() ([]glow.Element, error) { return c.GetDirectory(testContext(t), "") })
	p.expect()
	p.sendPayload([]byte{0x60, 0x03, 0x6B, 0x01, 0xFF})
	p.send(&glow.Node{Path: glow.Path{5}})
	if r := <-res; r.err != nil {
		t.Fatal(r.err)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

func TestClose(t *testing.T) {
	p, c := startFake(t, nil)
	res := result(func() ([]glow.Element, error) { return c.GetDirectory(testContext(t), "") })
	p.expect()
	c.Close()
	if r := <-res; !errors.Is(r.err, ErrClosed) {
		t.Fatalf("pending request: %v", r.err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done not closed")
	}
	if !errors.Is(c.Err(), ErrClosed) {
		t.Fatalf("Err: %v", c.Err())
	}
	if _, err := c.GetDirectory(testContext(t), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestProviderCloses(t *testing.T) {
	p, c := startFake(t, nil)
	res := result(func() ([]glow.Element, error) { return c.GetDirectory(testContext(t), "") })
	p.expect()
	p.conn.Close()
	if r := <-res; r.err == nil {
		t.Fatal("pending request succeeded")
	}
	<-c.Done()
	if !errors.Is(c.Err(), io.EOF) {
		t.Fatalf("Err: %v", c.Err())
	}
}

func TestAnswersKeepAlive(t *testing.T) {
	p, _ := startFake(t, nil)
	req := s101.NewKeepAliveRequest()
	req.Slot = 0x05
	p.writeFrame(&req)
	select {
	case f := <-p.keepAlives:
		if f.Command != s101.CommandKeepAliveResponse || f.Slot != 0x05 {
			t.Fatalf("got %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no keep-alive response")
	}
}

func TestSendsKeepAlive(t *testing.T) {
	p, c := startFake(t, nil, WithKeepAlive(20*time.Millisecond, 50*time.Millisecond))
	for range 3 {
		select {
		case f := <-p.keepAlives:
			if f.Command != s101.CommandKeepAliveRequest {
				t.Fatalf("got %+v", f)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no keep-alive request")
		}
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

func TestKeepAliveTimeout(t *testing.T) {
	p, c := startFake(t, nil, WithKeepAlive(20*time.Millisecond, 30*time.Millisecond))
	p.answerKA.Store(false)
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("connection not closed")
	}
	if !errors.Is(c.Err(), ErrKeepAliveTimeout) {
		t.Fatalf("Err: %v", c.Err())
	}
}
