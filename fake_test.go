package emberplus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
	"github.com/lukirs95/goemberplusclient/pkg/s101"
)

// request is a command received by the fake provider.
type request struct {
	tag     ber.Tag // qualified element carrying the command; APPLICATION 2 at root level
	path    glow.Path
	number  glow.CommandType
	mask    glow.FieldFlags
	hasMask bool

	invocationID *int32
	arguments    []glow.Value
	element      glow.Element // decoded element of requests without command
}

// fakeProvider is the provider side of a net.Pipe. Requests are answered by
// respond if set, otherwise they are delivered to manual.
type fakeProvider struct {
	t          *testing.T
	conn       net.Conn
	wmu        sync.Mutex
	enc        *s101.Encoder
	keepAlives chan s101.Frame
	manual     chan request
	incoming   chan request
	respond    func(i int, r request)
	answerKA   atomic.Bool

	mu   sync.Mutex
	reqs []request
}

func startFake(t *testing.T, respond func(p *fakeProvider, i int, r request), opts ...Option) (*fakeProvider, *Client) {
	t.Helper()
	server, client := net.Pipe()
	p := &fakeProvider{
		t:          t,
		conn:       server,
		enc:        s101.NewEncoder(server),
		keepAlives: make(chan s101.Frame, 100),
		manual:     make(chan request, 100),
		incoming:   make(chan request, 100),
	}
	if respond != nil {
		p.respond = func(i int, r request) { respond(p, i, r) }
	}
	p.answerKA.Store(true)
	c := NewClient(client, append([]Option{WithKeepAlive(0, 0)}, opts...)...)
	go p.readLoop()
	go p.serve()
	t.Cleanup(func() {
		c.Close()
		server.Close()
	})
	return p, c
}

func (p *fakeProvider) readLoop() {
	defer close(p.incoming)
	dec := s101.NewDecoder(p.conn)
	var a s101.Assembler
	var f s101.Frame
	for {
		if err := dec.ReadFrame(&f); err != nil {
			return
		}
		switch f.Command {
		case s101.CommandKeepAliveRequest:
			p.keepAlives <- f
			if p.answerKA.Load() {
				resp := s101.NewKeepAliveResponse()
				go p.writeFrame(&resp)
			}
			continue
		case s101.CommandKeepAliveResponse:
			p.keepAlives <- f
			continue
		}
		payload, done, err := a.Add(&f)
		if err != nil || !done {
			continue
		}
		reqs, err := parseRequests(payload)
		if err != nil {
			p.t.Errorf("fake provider: %v", err)
			continue
		}
		for _, r := range reqs {
			p.incoming <- r
		}
	}
}

func (p *fakeProvider) serve() {
	i := 0
	for r := range p.incoming {
		p.mu.Lock()
		p.reqs = append(p.reqs, r)
		p.mu.Unlock()
		if p.respond != nil {
			p.respond(i, r)
		} else {
			p.manual <- r
		}
		i++
	}
}

func (p *fakeProvider) requests() []request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.reqs)
}

func (p *fakeProvider) expect() request {
	p.t.Helper()
	select {
	case r := <-p.manual:
		return r
	case <-time.After(2 * time.Second):
		p.t.Fatal("no request received")
		return request{}
	}
}

func (p *fakeProvider) expectNone(d time.Duration) {
	p.t.Helper()
	select {
	case r := <-p.manual:
		p.t.Fatalf("unexpected request %+v", r)
	case <-time.After(d):
	}
}

func (p *fakeProvider) writeFrame(f *s101.Frame) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.enc.WriteFrame(f)
}

// send writes elems as one message.
func (p *fakeProvider) send(elems ...glow.Element) {
	var b ber.Builder
	if err := glow.Encode(&b, elems...); err != nil {
		p.t.Errorf("fake provider: %v", err)
		return
	}
	payload, _ := b.Bytes()
	p.sendPayload(payload)
}

func (p *fakeProvider) sendPayload(payload []byte) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	tmpl := s101.Frame{Message: s101.MessageEmBER, Version: s101.VersionS101, DTD: s101.DTDGlow, AppBytes: []byte{0x1F, 0x02}}
	s101.Split(tmpl, payload, p.enc.WriteFrame)
}

func (p *fakeProvider) sendRaw(b []byte) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.conn.Write(b)
}

// parseRequests extracts the commands of a consumer message.
func parseRequests(payload []byte) ([]request, error) {
	r := ber.NewReader(payload)
	root, err := r.Next()
	if err != nil {
		return nil, err
	}
	coll, err := root.Explicit()
	if err != nil {
		return nil, err
	}
	entries, err := coll.Children()
	if err != nil {
		return nil, err
	}
	var reqs []request
	for entries.More() {
		e, err := entries.Next()
		if err != nil {
			return nil, err
		}
		el, err := e.Explicit()
		if err != nil {
			return nil, err
		}
		req := request{tag: el.Tag}
		if el.Tag == ber.Application(2) {
			if err := parseCommand(el, &req); err != nil {
				return nil, err
			}
			reqs = append(reqs, req)
			continue
		}
		fields, err := el.Children()
		if err != nil {
			return nil, err
		}
		for fields.More() {
			f, err := fields.Next()
			if err != nil {
				return nil, err
			}
			v, err := f.Explicit()
			if err != nil {
				return nil, err
			}
			switch f.Tag {
			case ber.Context(0):
				oid, err := v.RelativeOID(nil)
				if err != nil {
					return nil, err
				}
				req.path = glow.Path(oid)
			case ber.Context(2):
				cmds, err := v.Children()
				if err != nil {
					return nil, err
				}
				for cmds.More() {
					c, _ := cmds.Next()
					cmd, err := c.Explicit()
					if err != nil {
						return nil, err
					}
					if err := parseCommand(cmd, &req); err != nil {
						return nil, err
					}
				}
			}
		}
		reqs = append(reqs, req)
	}
	// Attach the decoded elements of requests that carry no command, such as
	// value changes and connects.
	if msg, err := glow.Decode(payload); err == nil {
		for i := range reqs {
			if reqs[i].number != 0 {
				continue
			}
			for _, el := range msg.(glow.Elements) {
				if slices.Equal(el.ElementPath(), reqs[i].path) {
					reqs[i].element = el
				}
			}
		}
	}
	return reqs, nil
}

func parseCommand(t ber.TLV, req *request) error {
	fields, err := t.Children()
	if err != nil {
		return err
	}
	for fields.More() {
		f, err := fields.Next()
		if err != nil {
			return err
		}
		v, err := f.Explicit()
		if err != nil {
			return err
		}
		if f.Tag == ber.Context(2) {
			if err := parseInvocation(v, req); err != nil {
				return err
			}
			continue
		}
		n, err := v.Int()
		if err != nil {
			continue
		}
		switch f.Tag {
		case ber.Context(0):
			req.number = glow.CommandType(n)
		case ber.Context(1):
			req.mask = glow.FieldFlags(n)
			req.hasMask = true
		}
	}
	return nil
}

func parseInvocation(t ber.TLV, req *request) error {
	fields, err := t.Children()
	if err != nil {
		return err
	}
	for fields.More() {
		f, _ := fields.Next()
		v, err := f.Explicit()
		if err != nil {
			return err
		}
		switch f.Tag {
		case ber.Context(0):
			id, err := v.Int32()
			if err != nil {
				return err
			}
			req.invocationID = &id
		case ber.Context(1):
			values, err := v.Children()
			if err != nil {
				return err
			}
			req.arguments = []glow.Value{}
			for values.More() {
				e, _ := values.Next()
				x, err := e.Explicit()
				if err != nil {
					return err
				}
				switch x.Tag {
				case ber.TagInteger:
					n, _ := x.Int()
					req.arguments = append(req.arguments, glow.Int(n))
				case ber.TagBoolean:
					b, _ := x.Bool()
					req.arguments = append(req.arguments, glow.Bool(b))
				case ber.TagUTF8String:
					s, _ := x.UTF8()
					req.arguments = append(req.arguments, glow.String(s))
				}
			}
		}
	}
	return nil
}

// sendInvocationResult writes an InvocationResult with a single boolean result.
func (p *fakeProvider) sendInvocationResult(id int32, success, result bool) {
	var b ber.Builder
	b.Constructed(ber.Application(0), func(b *ber.Builder) {
		b.Constructed(ber.Application(23), func(b *ber.Builder) {
			b.Constructed(ber.Context(0), func(b *ber.Builder) { b.Int(int64(id)) })
			b.Constructed(ber.Context(1), func(b *ber.Builder) { b.Bool(success) })
			b.Constructed(ber.Context(2), func(b *ber.Builder) {
				b.Constructed(ber.TagSequence, func(b *ber.Builder) {
					b.Constructed(ber.Context(0), func(b *ber.Builder) { b.Bool(result) })
				})
			})
		})
	})
	payload, _ := b.Bytes()
	p.sendPayload(payload)
}

// staticTree answers GetDirectory requests from a fixed list of elements:
// the root and nodes with their children, other elements with themselves.
func staticTree(elems ...glow.Element) func(p *fakeProvider, i int, r request) {
	return func(p *fakeProvider, _ int, r request) {
		if r.number != glow.CmdGetDirectory {
			return
		}
		var self glow.Element
		var children []glow.Element
		for _, el := range elems {
			ep := el.ElementPath()
			if slices.Equal(ep, r.path) {
				self = el
			}
			if slices.Equal(ep.Parent(), r.path) {
				children = append(children, el)
			}
		}
		switch {
		case len(r.path) == 0 || isNode(self):
			if len(children) == 0 {
				p.send(&glow.Node{Path: r.path})
				return
			}
			p.send(children...)
		case self != nil:
			p.send(self)
		}
	}
}

func isNode(el glow.Element) bool {
	_, ok := el.(*glow.Node)
	return ok
}

// result runs f in a goroutine.
func result[T any](f func() (T, error)) <-chan struct {
	v   T
	err error
} {
	ch := make(chan struct {
		v   T
		err error
	}, 1)
	go func() {
		v, err := f()
		ch <- struct {
			v   T
			err error
		}{v, err}
	}()
	return ch
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(time.Millisecond)
	}
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// splitFrames splits a recorded S101 byte stream into raw frames.
func splitFrames(data []byte) [][]byte {
	var frames [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, s101.EOF)
		if i < 0 {
			break
		}
		frames = append(frames, data[:i+1])
		data = data[i+1:]
	}
	return frames
}

func decodeFrame(raw []byte) (s101.Frame, error) {
	var f s101.Frame
	err := s101.NewDecoder(bytes.NewReader(raw)).ReadFrame(&f)
	if errors.Is(err, io.EOF) {
		return f, io.ErrUnexpectedEOF
	}
	return f, err
}
