package emberplus

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
	"github.com/lukirs95/goemberplusclient/pkg/s101"
)

// controlTree is a provider tree with writable and invocable elements:
//
//	1 root
//	├── 1 gain     (parameter, -6, values above 15 are rejected)
//	├── 2 level    (stream parameter 7)
//	├── 3 reset    (function: channel int → ok bool; channel 99 fails)
//	├── 4 router   (matrix 2×3; target 1 is locked)
//	└── 5 info
//	    └── 1 name
type controlTree struct {
	mu   sync.Mutex
	gain glow.Real
}

func (ct *controlTree) elements() []glow.Element {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return []glow.Element{
		&glow.Node{Path: glow.Path{1}, Identifier: new("root")},
		&glow.Parameter{Path: glow.Path{1, 1}, Identifier: new("gain"), Value: ct.gain, Access: new(glow.AccessReadWrite)},
		&glow.Parameter{Path: glow.Path{1, 2}, Identifier: new("level"), StreamIdentifier: new(int32(7))},
		&glow.Function{Path: glow.Path{1, 3}, Identifier: new("reset"),
			Arguments: []glow.TupleItem{{Type: glow.TypeInteger, Name: "channel"}},
			Result:    []glow.TupleItem{{Type: glow.TypeBoolean, Name: "ok"}}},
		&glow.Matrix{Path: glow.Path{1, 4}, Identifier: new("router"), TargetCount: new(int32(2)), SourceCount: new(int32(3)),
			Connections: []glow.Connection{{Target: 0}, {Target: 1}}},
		&glow.Node{Path: glow.Path{1, 5}, Identifier: new("info")},
		&glow.Parameter{Path: glow.Path{1, 5, 1}, Identifier: new("name"), Value: glow.String("box")},
	}
}

func (ct *controlTree) respond(p *fakeProvider, i int, r request) {
	switch {
	case r.number == glow.CmdGetDirectory:
		staticTree(ct.elements()...)(p, i, r)
	case r.number == glow.CmdInvoke:
		ok := !slices.Equal(r.arguments, []glow.Value{glow.Int(99)})
		p.sendInvocationResult(*r.invocationID, ok, ok)
	case r.element != nil:
		switch el := r.element.(type) {
		case *glow.Parameter:
			ct.mu.Lock()
			if v, ok := el.Value.(glow.Real); ok && v <= 15 {
				ct.gain = v
			}
			gain := ct.gain
			ct.mu.Unlock()
			p.send(&glow.Parameter{Path: el.Path, Value: gain})
		case *glow.Matrix:
			c := el.Connections[0]
			reply := glow.Connection{Target: c.Target, Sources: c.Sources, Disposition: glow.Modified}
			if c.Target == 1 {
				reply = glow.Connection{Target: 1, Sources: []uint32{}, Disposition: glow.Locked}
			}
			p.send(&glow.Matrix{Path: el.Path, Connections: []glow.Connection{reply}})
		}
	}
}

func startControl(t *testing.T, opts ...Option) (*fakeProvider, *Client) {
	ct := &controlTree{gain: -6}
	return startFake(t, ct.respond, opts...)
}

func TestSetValue(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)

	v, err := c.SetValue(ctx, "root/gain", glow.Real(3))
	if err != nil || v != glow.Real(3) {
		t.Fatalf("got %v, %v", v, err)
	}
	reqs := p.requests()
	last := reqs[len(reqs)-1]
	if last.tag != ber.Application(9) || last.element.(*glow.Parameter).Value != glow.Real(3) {
		t.Fatalf("request %+v", last)
	}
	if el, _ := c.Get(ctx, "root/gain"); el.(*glow.Parameter).Value != glow.Real(3) {
		t.Fatalf("mirror: %+v", el)
	}

	// The provider rejects the value and answers with the current one.
	if v, err := c.SetValue(ctx, "root/gain", glow.Real(20)); err != nil || v != glow.Real(3) {
		t.Fatalf("rejected: got %v, %v", v, err)
	}
}

func TestWrongElementType(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)
	if _, err := c.Get(ctx, "root/info"); err != nil {
		t.Fatal(err)
	}
	n := len(p.requests())
	if _, err := c.SetValue(ctx, "root/info", glow.Int(1)); !errors.Is(err, ErrWrongType) {
		t.Errorf("SetValue on node: %v", err)
	}
	if _, err := c.Invoke(ctx, "root/gain"); !errors.Is(err, ErrWrongType) {
		t.Errorf("Invoke on parameter: %v", err)
	}
	if _, err := c.Connect(ctx, "root/gain", 0, nil, glow.OpAbsolute); !errors.Is(err, ErrWrongType) {
		t.Errorf("Connect on parameter: %v", err)
	}
	if _, err := c.Subscribe(ctx, "root/router"); !errors.Is(err, ErrWrongType) {
		t.Errorf("Subscribe on matrix: %v", err)
	}
	if len(p.requests()) != n {
		t.Fatal("request sent for a wrong element type")
	}
}

func TestInvoke(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)

	res, err := c.Invoke(ctx, "root/reset", glow.Int(3))
	if err != nil || !res.Success || !slices.Equal(res.Result, []glow.Value{glow.Bool(true)}) {
		t.Fatalf("got %+v, %v", res, err)
	}
	reqs := p.requests()
	first := reqs[len(reqs)-1]
	if first.tag != ber.Application(20) || first.number != glow.CmdInvoke ||
		first.invocationID == nil || *first.invocationID != res.ID || !slices.Equal(first.arguments, []glow.Value{glow.Int(3)}) {
		t.Fatalf("request %+v", first)
	}

	res2, err := c.Invoke(ctx, "root/reset", glow.Int(99))
	if err != nil || res2.Success || res2.ID == res.ID {
		t.Fatalf("failing invocation: %+v, %v", res2, err)
	}

	n := len(p.requests())
	if _, err := c.Invoke(ctx, "root/reset"); err == nil {
		t.Fatal("wrong argument count accepted")
	}
	if len(p.requests()) != n {
		t.Fatal("request sent with wrong argument count")
	}
}

func TestConnect(t *testing.T) {
	_, c := startControl(t)
	ctx := testContext(t)

	conn, err := c.Connect(ctx, "root/router", 0, []uint32{2}, glow.OpAbsolute)
	if err != nil || conn.Disposition != glow.Modified || !slices.Equal(conn.Sources, []uint32{2}) {
		t.Fatalf("got %+v, %v", conn, err)
	}
	el, _ := c.Get(ctx, "root/router")
	if got, _ := el.(*glow.Matrix).Connection(0); !slices.Equal(got.Sources, []uint32{2}) {
		t.Fatalf("mirror: %+v", got)
	}

	conn, err = c.Connect(ctx, "root/router", 1, []uint32{0}, glow.OpAbsolute)
	if err != nil || conn.Disposition != glow.Locked || len(conn.Sources) != 0 {
		t.Fatalf("locked: %+v, %v", conn, err)
	}
}

func TestWatchParameter(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)
	events := make(chan Event, 10)
	stop, err := c.Watch(ctx, "root/gain", func(e Event) { events <- e })
	if err != nil {
		t.Fatal(err)
	}

	p.send(&glow.Parameter{Path: glow.Path{1, 1}, Value: glow.Real(1), Description: new("Gain")})
	select {
	case e := <-events:
		el, u := e.Element.(*glow.Parameter), e.Update.(*glow.Parameter)
		if *el.Identifier != "gain" || el.Value != glow.Real(1) || *el.Description != "Gain" {
			t.Fatalf("element %+v", el)
		}
		if u.Identifier != nil || u.Value != glow.Real(1) || *u.Description != "Gain" {
			t.Fatalf("update %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}

	stop()
	stop()
	p.send(&glow.Parameter{Path: glow.Path{1, 1}, Value: glow.Real(2)})
	select {
	case e := <-events:
		t.Fatalf("event after stop: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchLoadsParent(t *testing.T) {
	p, c := startControl(t)
	if _, err := c.Watch(testContext(t), "root/info/name", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := requestedPaths(p); !slices.Equal(got, []string{"", "1", "1.5"}) {
		t.Fatalf("requests for %q", got)
	}
}

func TestWatchMatrixLoadsMatrix(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)
	events := make(chan Event, 10)
	if _, err := c.Watch(ctx, "root/router", func(e Event) { events <- e }); err != nil {
		t.Fatal(err)
	}
	reqs := p.requests()
	if last := reqs[len(reqs)-1]; last.tag != ber.Application(17) || !slices.Equal(last.path, glow.Path{1, 4}) {
		t.Fatalf("last request %+v", last)
	}
	n := len(reqs)
	if _, err := c.Watch(ctx, "root/router", func(Event) {}); err != nil || len(p.requests()) != n {
		t.Fatalf("second Watch: %v, %d new requests", err, len(p.requests())-n)
	}

	p.send(&glow.Matrix{Path: glow.Path{1, 4}, Connections: []glow.Connection{{Target: 0, Sources: []uint32{1}, Disposition: glow.Modified}}})
	select {
	case e := <-events:
		if c, _ := e.Element.(*glow.Matrix).Connection(0); !slices.Equal(c.Sources, []uint32{1}) {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
}

func TestSlowWatcherDoesNotBlockConnection(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)
	release := make(chan struct{})
	var got []glow.Value
	var mu sync.Mutex
	if _, err := c.Watch(ctx, "root/gain", func(e Event) {
		<-release
		mu.Lock()
		got = append(got, e.Update.(*glow.Parameter).Value)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []glow.Real{1, 2, 3} {
		p.send(&glow.Parameter{Path: glow.Path{1, 1}, Value: v})
	}

	// The callback blocks, but keep-alives are still answered and the
	// mirror is still updated.
	req := s101.NewKeepAliveRequest()
	p.writeFrame(&req)
	select {
	case <-p.keepAlives:
	case <-time.After(2 * time.Second):
		t.Fatal("keep-alive not answered while a callback blocks")
	}
	waitFor(t, func() bool { el, _ := c.Get(ctx, "root/gain"); return el.(*glow.Parameter).Value == glow.Real(3) })

	close(release)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 3 })
	if !slices.Equal(got, []glow.Value{glow.Real(1), glow.Real(2), glow.Real(3)}) {
		t.Fatalf("events in wrong order: %v", got)
	}
}

func TestSubscribe(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)

	if _, err := c.Subscribe(ctx, "root/gain"); !errors.Is(err, ErrNotStream) {
		t.Fatalf("non-stream: %v", err)
	}
	commands := func() []glow.CommandType {
		var cmds []glow.CommandType
		for _, r := range p.requests() {
			if r.number == glow.CmdSubscribe || r.number == glow.CmdUnsubscribe {
				if r.tag != ber.Application(9) || !slices.Equal(r.path, glow.Path{1, 2}) {
					t.Fatalf("request %+v", r)
				}
				cmds = append(cmds, r.number)
			}
		}
		return cmds
	}
	if len(commands()) != 0 {
		t.Fatal("subscribed a non-stream parameter")
	}

	unsub1, err := c.Subscribe(ctx, "root/level")
	if err != nil {
		t.Fatal(err)
	}
	unsub2, err := c.Subscribe(ctx, "root/level")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(commands()) == 1 })
	if err := unsub1(); err != nil {
		t.Fatal(err)
	}
	if err := unsub2(); err != nil {
		t.Fatal(err)
	}
	unsub2()
	waitFor(t, func() bool { return len(commands()) == 2 })
	time.Sleep(20 * time.Millisecond)
	if got := commands(); !slices.Equal(got, []glow.CommandType{glow.CmdSubscribe, glow.CmdUnsubscribe}) {
		t.Fatalf("commands %v", got)
	}
}

func TestWatchStream(t *testing.T) {
	p, c := startControl(t)
	ctx := testContext(t)
	events := make(chan Event, 10)
	unsub, err := c.Subscribe(ctx, "root/level")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	if _, err := c.Watch(ctx, "root/level", func(e Event) { events <- e }); err != nil {
		t.Fatal(err)
	}

	var b ber.Builder
	b.Constructed(ber.Application(0), func(b *ber.Builder) {
		b.Constructed(ber.Application(6), func(b *ber.Builder) {
			b.Constructed(ber.Context(0), func(b *ber.Builder) {
				b.Constructed(ber.Application(5), func(b *ber.Builder) {
					b.Constructed(ber.Context(0), func(b *ber.Builder) { b.Int(7) })
					b.Constructed(ber.Context(1), func(b *ber.Builder) { b.Int(-30) })
				})
			})
		})
	})
	payload, _ := b.Bytes()
	p.sendPayload(payload)

	select {
	case e := <-events:
		if e.Update.(*glow.Parameter).Value != glow.Int(-30) || *e.Element.(*glow.Parameter).Identifier != "level" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no stream event")
	}
}
