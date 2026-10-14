// Package glow implements the Glow data schema of Ember+ (Glow DTD).
//
// Decode turns an EmBER payload into a flat list of elements with absolute
// paths, regardless of whether the provider sent nested or qualified
// elements. Encode writes requests, always using qualified elements.
package glow

import (
	"fmt"
	"strconv"
	"strings"
)

// Path is the numeric path of an element, e.g. 1.3.2. The empty path is the
// root.
type Path []uint32

// ParsePath parses a dotted path such as "1.3.2". The empty string is the root.
func ParsePath(s string) (Path, error) {
	if s == "" {
		return Path{}, nil
	}
	parts := strings.Split(s, ".")
	p := make(Path, len(parts))
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("glow: invalid path %q", s)
		}
		p[i] = uint32(n)
	}
	return p, nil
}

func (p Path) String() string {
	var sb strings.Builder
	for i, n := range p {
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(strconv.FormatUint(uint64(n), 10))
	}
	return sb.String()
}

// Parent returns the path of the parent element, or nil for the root.
func (p Path) Parent() Path {
	if len(p) == 0 {
		return nil
	}
	return p[: len(p)-1 : len(p)-1]
}

// Child returns a new path with n appended. p is not modified.
func (p Path) Child(n uint32) Path {
	c := make(Path, len(p)+1)
	copy(c, p)
	c[len(p)] = n
	return c
}

// HasPrefix reports whether p lies in the subtree rooted at q (or equals q).
func (p Path) HasPrefix(q Path) bool {
	if len(q) > len(p) {
		return false
	}
	for i := range q {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}
