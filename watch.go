package emberplus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

var ErrNotStream = errors.New("emberplus: parameter is not a stream")

// Event reports a change of a watched element.
type Event struct {
	// Element is a snapshot of the element after the change was applied.
	Element glow.Element
	// Update holds only the fields the provider transmitted.
	Update glow.Element
}

type watcher struct {
	fn      func(Event)
	stopped atomic.Bool
}

// Watch calls fn for every change of the element at path. Watching a
// parameter or node loads the directory of its parent if needed, which makes
// the provider report changes (spec: "Application defined commands").
// Watching a matrix additionally loads the matrix itself, which subscribes to
// its connection changes (spec: "Ember+ 1.1: Matrix Extensions").
//
// Watch never subscribes to streams: a stream parameter only reports values
// while it is subscribed with Subscribe.
//
// fn is called from a single goroutine in the order the changes arrived. A
// slow fn delays later events but never the connection itself; events are
// queued without limit until fn catches up. After stop returns, fn is not
// called again.
func (c *Client) Watch(ctx context.Context, path string, fn func(Event)) (stop func(), err error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, err := c.locate(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, fmt.Errorf("%w: the root is not an element", ErrNotFound)
	}
	c.mu.Lock()
	e := c.tree.lookup(p)
	_, isMatrix := e.el.(*glow.Matrix)
	loaded := e.loaded
	c.mu.Unlock()
	if isMatrix && !loaded {
		if err := c.loadDirectory(ctx, p); err != nil {
			return nil, err
		}
	}

	w := &watcher{fn: fn}
	key := p.String()
	c.mu.Lock()
	c.watchers[key] = append(c.watchers[key], w)
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.stopped.Store(true)
			c.mu.Lock()
			defer c.mu.Unlock()
			ws := c.watchers[key]
			for i, x := range ws {
				if x == w {
					ws = append(ws[:i], ws[i+1:]...)
					break
				}
			}
			if len(ws) == 0 {
				delete(c.watchers, key)
			} else {
				c.watchers[key] = ws
			}
		})
	}, nil
}

// notify queues an event for the watchers of e. Callers must hold c.mu.
func (c *Client) notify(e *entry, update glow.Element) {
	ws := c.watchers[update.ElementPath().String()]
	if len(ws) == 0 {
		return
	}
	ev := Event{Element: snapshot(e), Update: cloneElement(update)}
	for _, w := range ws {
		c.events.push(func() {
			if !w.stopped.Load() {
				w.fn(ev)
			}
		})
	}
}

func cloneElement(el glow.Element) glow.Element {
	switch e := el.(type) {
	case *glow.Node:
		return e.Clone()
	case *glow.Parameter:
		return e.Clone()
	case *glow.Matrix:
		return e.Clone()
	case *glow.Function:
		return e.Clone()
	}
	return el
}

// Subscribe requests the values of a stream parameter, such as audio level
// meters (spec: "Application defined commands"). Streams typically report values every 50 to 80 ms, so they
// should only be subscribed while needed. Use Watch to receive the values.
//
// Parameters that are not streams return ErrNotStream without sending
// anything; glow.Parameter.IsStream reports whether a parameter is a stream.
// Subscriptions are counted: the provider is unsubscribed when the last
// unsubscribe function is called.
func (c *Client) Subscribe(ctx context.Context, path string) (unsubscribe func() error, err error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, par, err := elementAt[*glow.Parameter](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if !par.IsStream() {
		return nil, fmt.Errorf("%w: %q", ErrNotStream, path)
	}

	key := p.String()
	c.mu.Lock()
	c.subs[key]++
	first := c.subs[key] == 1
	c.mu.Unlock()
	if first {
		if err := c.send(ctx, &glow.Command{Path: p, Target: glow.KindParameter, Number: glow.CmdSubscribe}); err != nil {
			c.mu.Lock()
			c.subs[key]--
			c.mu.Unlock()
			return nil, err
		}
	}

	var once sync.Once
	return func() (err error) {
		once.Do(func() {
			c.mu.Lock()
			c.subs[key]--
			last := c.subs[key] == 0
			if last {
				delete(c.subs, key)
			}
			c.mu.Unlock()
			if last {
				ctx, cancel := c.withTimeout(context.Background())
				defer cancel()
				err = c.send(ctx, &glow.Command{Path: p, Target: glow.KindParameter, Number: glow.CmdUnsubscribe})
			}
		})
		return err
	}, nil
}

// dispatcher runs queued functions in order on a single goroutine. The queue
// is unbounded so that pushing never blocks the read loop.
type dispatcher struct {
	mu    sync.Mutex
	queue []func()
	wake  chan struct{}
}

func newDispatcher() *dispatcher {
	return &dispatcher{wake: make(chan struct{}, 1)}
}

func (d *dispatcher) push(f func()) {
	d.mu.Lock()
	d.queue = append(d.queue, f)
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *dispatcher) run(done <-chan struct{}) {
	for {
		d.mu.Lock()
		queue := d.queue
		d.queue = nil
		d.mu.Unlock()
		for _, f := range queue {
			select {
			case <-done:
				return
			default:
			}
			f()
		}
		if len(queue) == 0 {
			select {
			case <-d.wake:
			case <-done:
				return
			}
		}
	}
}
