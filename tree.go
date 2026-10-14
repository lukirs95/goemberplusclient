package emberplus

import (
	"slices"

	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

// entry is an element of the mirror. el is nil for placeholders created for
// ancestors of qualified elements that have not been reported yet.
type entry struct {
	el       glow.Element
	children map[uint32]*entry
	order    []uint32 // child numbers in the order they were first reported
	loaded   bool     // a GetDirectory on this element was answered
}

func (e *entry) child(n uint32) *entry {
	return e.children[n]
}

// childByIdentifier returns the number of the child with identifier id.
func (e *entry) childByIdentifier(id string) (uint32, bool) {
	for _, n := range e.order {
		if ident := identifierOf(e.children[n].el); ident != nil && *ident == id {
			return n, true
		}
	}
	return 0, false
}

type tree struct {
	root    entry
	streams map[int32][]*entry
}

func newTree() tree {
	return tree{streams: map[int32][]*entry{}}
}

func (t *tree) lookup(p glow.Path) *entry {
	e := &t.root
	for _, n := range p {
		if e = e.child(n); e == nil {
			return nil
		}
	}
	return e
}

// ensure returns the entry for p, creating placeholders as needed.
func (t *tree) ensure(p glow.Path) *entry {
	e := &t.root
	for _, n := range p {
		c := e.child(n)
		if c == nil {
			c = &entry{}
			if e.children == nil {
				e.children = map[uint32]*entry{}
			}
			e.children[n] = c
			e.order = append(e.order, n)
		}
		e = c
	}
	return e
}

// kind returns the element kind at p; unknown elements are assumed to be
// nodes.
func (t *tree) kind(p glow.Path) glow.ElementKind {
	if e := t.lookup(p); e != nil {
		switch e.el.(type) {
		case *glow.Parameter:
			return glow.KindParameter
		case *glow.Matrix:
			return glow.KindMatrix
		case *glow.Function:
			return glow.KindFunction
		}
	}
	return glow.KindNode
}

type mergeable[T any] interface {
	glow.Element
	Merge(T)
	Clone() T
}

func mergeInto[T mergeable[T]](e *entry, u T) {
	if cur, ok := e.el.(T); ok {
		cur.Merge(u)
		return
	}
	e.el = u.Clone()
}

// apply merges an element received from the provider into the mirror and
// returns its entry, or nil for elements that are not mirrored.
func (t *tree) apply(el glow.Element) *entry {
	p := el.ElementPath()
	switch u := el.(type) {
	case *glow.Node:
		e := t.ensure(p)
		mergeInto(e, u)
		return e
	case *glow.Parameter:
		e := t.ensure(p)
		mergeInto(e, u)
		if u.StreamIdentifier != nil {
			t.indexStream(*u.StreamIdentifier, e)
		}
		if u.Value != nil {
			t.markOnline(p)
		}
		return e
	case *glow.Matrix:
		e := t.ensure(p)
		mergeInto(e, u)
		return e
	case *glow.Function:
		e := t.ensure(p)
		mergeInto(e, u)
		return e
	}
	return nil
}

// streamUpdate is a parameter value set from a stream entry.
type streamUpdate struct {
	entry  *entry
	update *glow.Parameter
}

// markOnline marks the element at p and its ancestors as online: a value
// implies that its element is online (spec: "Glow specific properties").
func (t *tree) markOnline(p glow.Path) {
	e := &t.root
	for _, n := range p {
		e = e.child(n)
		switch el := e.el.(type) {
		case *glow.Node:
			if el.IsOnline != nil && !*el.IsOnline {
				el.IsOnline = new(true)
			}
		case *glow.Parameter:
			if el.IsOnline != nil && !*el.IsOnline {
				el.IsOnline = new(true)
			}
		}
	}
}

// isOnline applies the inheritance rule of the IsOnline property (spec:
// "Glow specific properties"): an element is offline if it
// or one of its ancestors is marked offline.
func (t *tree) isOnline(p glow.Path) bool {
	e := &t.root
	for _, n := range p {
		if e = e.child(n); e == nil {
			return false
		}
		var online *bool
		switch el := e.el.(type) {
		case *glow.Node:
			online = el.IsOnline
		case *glow.Parameter:
			online = el.IsOnline
		}
		if online != nil && !*online {
			return false
		}
	}
	return true
}

func (t *tree) indexStream(id int32, e *entry) {
	if !slices.Contains(t.streams[id], e) {
		t.streams[id] = append(t.streams[id], e)
	}
}

// applyStreams sets the values of all parameters sharing a stream identifier.
// Octet values are split according to each parameter's stream descriptor.
func (t *tree) applyStreams(streams glow.Streams) []streamUpdate {
	var updates []streamUpdate
	for _, s := range streams {
		for _, e := range t.streams[s.Identifier] {
			p, ok := e.el.(*glow.Parameter)
			if !ok || p.StreamIdentifier == nil || *p.StreamIdentifier != s.Identifier {
				continue
			}
			v := s.Value
			if octets, ok := v.(glow.Octets); ok && p.StreamDescriptor != nil {
				var err error
				if v, err = p.StreamDescriptor.Extract(octets); err != nil {
					continue
				}
			}
			u := &glow.Parameter{Path: p.Path, Value: v}
			p.Merge(u)
			updates = append(updates, streamUpdate{e, u})
		}
	}
	return updates
}

// snapshot returns a copy of the element of e. For nodes, HasChildren reports
// whether children are known.
func snapshot(e *entry) glow.Element {
	switch el := e.el.(type) {
	case *glow.Node:
		n := el.Clone()
		n.HasChildren = len(e.children) > 0
		return n
	case *glow.Parameter:
		return el.Clone()
	case *glow.Matrix:
		return el.Clone()
	case *glow.Function:
		return el.Clone()
	}
	return nil
}

// childSnapshots returns copies of all reported children of e in order.
func childSnapshots(e *entry) []glow.Element {
	els := []glow.Element{}
	for _, n := range e.order {
		if c := e.children[n]; c.el != nil {
			els = append(els, snapshot(c))
		}
	}
	return els
}

func identifierOf(el glow.Element) *string {
	switch e := el.(type) {
	case *glow.Node:
		return e.Identifier
	case *glow.Parameter:
		return e.Identifier
	case *glow.Matrix:
		return e.Identifier
	case *glow.Function:
		return e.Identifier
	}
	return nil
}
