package emberplus

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

// dirRequest is a GetDirectory in flight. Concurrent requests for the same
// path share it.
type dirRequest struct {
	done    chan struct{}
	err     error
	waiting int
	w       *waiter
}

// GetDirectory requests the children of a node, or the element itself for
// parameters, matrices and functions, and returns a snapshot from the
// mirror. For the root, path is "".
//
// The request is answered by the first message that reports a child of a
// node (or the node without properties, meaning it has no children). If a
// provider spreads the children of a node over several messages, the
// snapshot may be incomplete; later messages still update the mirror. A
// matrix is answered once a connection is known for every target.
func (c *Client) GetDirectory(ctx context.Context, path string) ([]glow.Element, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, err := c.locate(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := c.loadDirectory(ctx, p); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.tree.lookup(p)
	if e == nil {
		return []glow.Element{}, nil
	}
	switch e.el.(type) {
	case nil, *glow.Node:
		return childSnapshots(e), nil
	}
	return []glow.Element{snapshot(e)}, nil
}

// loadDirectory sends a GetDirectory for p and waits for its answer.
func (c *Client) loadDirectory(ctx context.Context, p glow.Path) error {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	key := p.String()
	c.mu.Lock()
	req, inFlight := c.dirs[key]
	if !inFlight {
		req = &dirRequest{done: make(chan struct{})}
		kind := c.tree.kind(p)
		if len(p) == 0 {
			kind = glow.KindUnknown
		}
		req.w = &waiter{
			match: c.directoryMatcher(p, kind),
			fire: func() {
				if e := c.tree.lookup(p); e != nil {
					e.loaded = true
				}
				delete(c.dirs, key)
				close(req.done)
			},
		}
		c.dirs[key] = req
		c.waiters = append(c.waiters, req.w)
		req.waiting++
		c.mu.Unlock()

		var cmd glow.Command
		if len(p) == 0 {
			cmd = glow.Command{Number: glow.CmdGetDirectory, DirFieldMask: glow.FieldsAll}
		} else {
			cmd = glow.Command{Path: p, Target: kind, Number: glow.CmdGetDirectory, DirFieldMask: glow.FieldsAll}
		}
		if err := c.send(ctx, &cmd); err != nil {
			c.mu.Lock()
			if c.dirs[key] == req {
				delete(c.dirs, key)
				c.removeWaiter(req.w)
				req.err = err
				close(req.done)
			}
			c.mu.Unlock()
		}
	} else {
		req.waiting++
		c.mu.Unlock()
	}

	select {
	case <-req.done:
		return req.err
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		c.mu.Lock()
		req.waiting--
		// Without anyone waiting, a later request must be sent again instead
		// of waiting for an answer that may never come.
		if req.waiting == 0 && c.dirs[key] == req {
			delete(c.dirs, key)
			c.removeWaiter(req.w)
		}
		c.mu.Unlock()
		return fmt.Errorf("emberplus: GetDirectory %q: %w", key, ctx.Err())
	}
}

// directoryMatcher returns the rule that recognizes the answer to a
// GetDirectory on p. It runs with c.mu held, after the message was applied.
func (c *Client) directoryMatcher(p glow.Path, kind glow.ElementKind) func(glow.Message) bool {
	return func(msg glow.Message) bool {
		els, ok := msg.(glow.Elements)
		if !ok {
			return false
		}
		for _, el := range els {
			ep := el.ElementPath()
			switch kind {
			case glow.KindUnknown: // root
				if len(ep) == 1 {
					return true
				}
			case glow.KindNode:
				if slices.Equal(ep.Parent(), p) {
					return true
				}
				if slices.Equal(ep, p) {
					// Either the node has no children, or the path
					// turned out not to be a node.
					if n, ok := el.(*glow.Node); !ok || n.IsEmpty() {
						return true
					}
				}
			case glow.KindMatrix:
				if m, ok := el.(*glow.Matrix); ok && slices.Equal(ep, p) && c.matrixComplete(p, m) {
					return true
				}
			default:
				if slices.Equal(ep, p) {
					return true
				}
			}
		}
		return false
	}
}

// matrixComplete reports whether the mirror knows a connection for every
// target of the matrix at p. If the number of targets is unknown, any
// reported connection list completes the matrix.
func (c *Client) matrixComplete(p glow.Path, update *glow.Matrix) bool {
	e := c.tree.lookup(p)
	if e == nil {
		return false
	}
	m, ok := e.el.(*glow.Matrix)
	if !ok {
		return false
	}
	var targets []uint32
	switch {
	case m.Targets != nil:
		targets = m.Targets
	case m.TargetCount != nil && (m.AddressingMode == nil || *m.AddressingMode == glow.Linear):
		for t := range uint32(max(*m.TargetCount, 0)) {
			targets = append(targets, t)
		}
	default:
		return update.Connections != nil
	}
	for _, t := range targets {
		if _, ok := m.Connection(t); !ok {
			return false
		}
	}
	return true
}

// Get returns a snapshot of the element at path. Elements that are not in
// the mirror yet are loaded with GetDirectory on their ancestors; afterwards
// Get needs no network access.
//
// The value of a stream parameter is only kept current while it is
// subscribed; otherwise Get returns the last value received.
func (c *Client) Get(ctx context.Context, path string) (glow.Element, error) {
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
	defer c.mu.Unlock()
	return snapshot(c.tree.lookup(p)), nil
}

// Children returns snapshots of the children of the element at path. Unlike
// GetDirectory, it only sends a request if the children have not been
// loaded yet.
func (c *Client) Children(ctx context.Context, path string) ([]glow.Element, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, err := c.locate(ctx, path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	loaded := c.tree.lookup(p).loaded
	c.mu.Unlock()
	if !loaded {
		if err := c.loadDirectory(ctx, p); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return childSnapshots(c.tree.lookup(p)), nil
}

// IsOnline reports whether the element at path is online: an element is
// offline if it or one of its ancestors is marked offline (spec: "Glow
// specific properties").
func (c *Client) IsOnline(ctx context.Context, path string) (bool, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, err := c.locate(ctx, path)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tree.isOnline(p), nil
}

// IdentifierPath returns the identifier path of the element at path, such as
// "root/settings/info", which stays valid across sessions.
func (c *Client) IdentifierPath(ctx context.Context, path string) (string, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	p, err := c.locate(ctx, path)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, len(p))
	e := &c.tree.root
	for i, n := range p {
		e = e.child(n)
		id := identifierOf(e.el)
		if id == nil {
			return "", fmt.Errorf("%w: identifier of %v", ErrNotFound, p[:i+1])
		}
		ids[i] = *id
	}
	return strings.Join(ids, "/"), nil
}
