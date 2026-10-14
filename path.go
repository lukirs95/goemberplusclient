package emberplus

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

// segment is one step of a path: a number or an identifier.
type segment struct {
	number     uint32
	identifier string
}

func (s segment) isNumber() bool { return s.identifier == "" }

func (s segment) String() string {
	if s.isNumber() {
		return strconv.FormatUint(uint64(s.number), 10)
	}
	return s.identifier
}

// parsePath splits a path string into segments. A path made of digits and
// dots only is a numeric path such as "1.100.3". Any other path is split at
// "/"; its segments are numbers if they consist of digits only and
// identifiers otherwise. A leading "/" is ignored.
func parsePath(s string) ([]segment, error) {
	s = strings.TrimPrefix(s, "/")
	if s == "" {
		return nil, nil
	}
	sep := "/"
	if strings.Trim(s, "0123456789.") == "" {
		sep = "."
	}
	parts := strings.Split(s, sep)
	segs := make([]segment, len(parts))
	for i, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("emberplus: invalid path %q: empty segment", s)
		}
		if strings.Trim(part, "0123456789") != "" {
			segs[i] = segment{identifier: part}
			continue
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("emberplus: invalid path %q: number %s out of range", s, part)
		}
		segs[i] = segment{number: uint32(n)}
	}
	return segs, nil
}

// locate resolves path to a numeric path and makes sure that every element
// on it is in the mirror. Missing elements are loaded with GetDirectory on
// their parent, starting at the root as needed.
func (c *Client) locate(ctx context.Context, path string) (glow.Path, error) {
	segs, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	p := glow.Path{}
	for _, seg := range segs {
		n, found, loaded := c.lookupChild(p, seg)
		if !found && !loaded {
			if err := c.loadDirectory(ctx, p); err != nil {
				return nil, err
			}
			n, found, _ = c.lookupChild(p, seg)
		}
		if !found {
			return nil, fmt.Errorf("%w: %q below %q", ErrNotFound, seg, p)
		}
		p = p.Child(n)
	}
	return p, nil
}

// lookupChild looks up a child of p in the mirror and reports whether the
// directory of p has been loaded.
func (c *Client) lookupChild(p glow.Path, seg segment) (n uint32, found, loaded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.tree.lookup(p)
	if e == nil {
		return 0, false, false
	}
	if seg.isNumber() {
		child := e.child(seg.number)
		return seg.number, child != nil && child.el != nil, e.loaded
	}
	n, found = e.childByIdentifier(seg.identifier)
	return n, found, e.loaded
}
