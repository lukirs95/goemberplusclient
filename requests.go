package emberplus

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

var ErrWrongType = errors.New("emberplus: wrong element type")

// elementAt locates path and returns its element from the mirror.
func elementAt[T glow.Element](ctx context.Context, c *Client, path string) (glow.Path, T, error) {
	var zero T
	p, err := c.locate(ctx, path)
	if err != nil {
		return nil, zero, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var el glow.Element
	if e := c.tree.lookup(p); e != nil {
		el = e.el
	}
	t, ok := el.(T)
	if !ok {
		return nil, zero, fmt.Errorf("%w: %q is a %T, not a %T", ErrWrongType, path, el, zero)
	}
	return p, t, nil
}

// await registers a waiter, sends the request and waits until match accepts
// a message. fire runs with c.mu held, after the message was applied.
func (c *Client) await(ctx context.Context, match func(glow.Message) bool, fire func(), elems ...glow.Element) error {
	done := make(chan struct{})
	w := &waiter{match: match, fire: func() {
		fire()
		close(done)
	}}
	c.mu.Lock()
	c.waiters = append(c.waiters, w)
	c.mu.Unlock()
	remove := func() {
		c.mu.Lock()
		c.removeWaiter(w)
		c.mu.Unlock()
	}

	if err := c.send(ctx, elems...); err != nil {
		remove()
		return err
	}
	select {
	case <-done:
		return nil
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		remove()
		return ctx.Err()
	}
}

// SetValue requests a new value for a parameter and returns the value the
// provider confirms. A provider answers an invalid value with the unchanged
// current value (spec: "Value change requests"), so the result may differ
// from v.
//
// Another value update of the parameter that arrives before the provider's
// answer, e.g. caused by a front panel, is taken as the answer.
func (c *Client) SetValue(ctx context.Context, path string, v glow.Value) (glow.Value, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, _, err := elementAt[*glow.Parameter](ctx, c, path)
	if err != nil {
		return nil, err
	}
	var confirmed glow.Value
	err = c.await(ctx,
		func(msg glow.Message) bool {
			return hasElement(msg, p, func(par *glow.Parameter) bool { return par.Value != nil })
		},
		func() { confirmed = snapshot(c.tree.lookup(p)).(*glow.Parameter).Value },
		&glow.Parameter{Path: p, Value: v},
	)
	if err != nil {
		return nil, fmt.Errorf("emberplus: SetValue %q: %w", path, err)
	}
	return confirmed, nil
}

// Invoke calls a function and waits for its result. The result reports
// whether the provider executed the function successfully. A function may be
// invoked more than once by redundant consumers, so functions should not
// change state relatively (spec: "Ember+ 1.2: Function Extensions").
func (c *Client) Invoke(ctx context.Context, path string, args ...glow.Value) (*glow.InvocationResult, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, fn, err := elementAt[*glow.Function](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if len(args) != len(fn.Arguments) {
		return nil, fmt.Errorf("emberplus: Invoke %q: %d arguments, function takes %d", path, len(args), len(fn.Arguments))
	}
	id := c.invocationID.Add(1)
	var res *glow.InvocationResult
	err = c.await(ctx,
		func(msg glow.Message) bool {
			r, ok := msg.(*glow.InvocationResult)
			if ok && r.ID == id {
				res = r
			}
			return res != nil
		},
		func() {},
		&glow.Command{
			Path:       p,
			Target:     glow.KindFunction,
			Number:     glow.CmdInvoke,
			Invocation: &glow.Invocation{ID: new(id), Arguments: args},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("emberplus: Invoke %q: %w", path, err)
	}
	return res, nil
}

// Connect changes the sources connected to a target of a matrix and returns
// the connection the provider reports. Its Disposition tells whether the
// change was applied (Modified), queued (Pending) or refused because the
// target is locked (Locked). With glow.OpAbsolute, sources replaces all
// connected sources; an empty list disconnects the target.
func (c *Client) Connect(ctx context.Context, path string, target uint32, sources []uint32, op glow.ConnectionOperation) (glow.Connection, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, _, err := elementAt[*glow.Matrix](ctx, c, path)
	if err != nil {
		return glow.Connection{}, err
	}
	if sources == nil {
		sources = []uint32{}
	}
	var conn glow.Connection
	err = c.await(ctx,
		func(msg glow.Message) bool {
			return hasElement(msg, p, func(m *glow.Matrix) bool {
				return slices.ContainsFunc(m.Connections, func(c glow.Connection) bool { return c.Target == target })
			})
		},
		func() {
			conn, _ = c.tree.lookup(p).el.(*glow.Matrix).Connection(target)
			conn.Sources = slices.Clone(conn.Sources)
		},
		&glow.Matrix{Path: p, Connections: []glow.Connection{{Target: target, Sources: sources, Operation: op}}},
	)
	if err != nil {
		return glow.Connection{}, fmt.Errorf("emberplus: Connect %q: %w", path, err)
	}
	return conn, nil
}

// hasElement reports whether msg contains an element of type T at p that
// satisfies ok.
func hasElement[T glow.Element](msg glow.Message, p glow.Path, ok func(T) bool) bool {
	els, isElements := msg.(glow.Elements)
	if !isElements {
		return false
	}
	for _, el := range els {
		if t, isT := el.(T); isT && slices.Equal(el.ElementPath(), p) && ok(t) {
			return true
		}
	}
	return false
}
