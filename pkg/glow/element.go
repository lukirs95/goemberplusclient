package glow

import (
	"slices"
	"strings"
)

// Element is one of *Node, *Parameter, *Matrix, *Function or *Command.
type Element interface {
	ElementPath() Path
	isElement()
}

// Node is a node of the provider tree. Pointer and slice fields are nil when
// they were not transmitted.
type Node struct {
	Path              Path
	Identifier        *string
	Description       *string
	IsRoot            *bool
	IsOnline          *bool
	SchemaIdentifiers *string
	TemplateReference Path

	// HasChildren reports whether a children collection was transmitted. A
	// node without properties and without children is the response to a
	// GetDirectory on a node that has no children (spec: "GetDirectory").
	// HasChildren is a
	// property of the message and is ignored by Merge and Encode.
	HasChildren bool
}

// Parameter is a parameter of the provider tree. Pointer, slice and Value
// fields are nil when they were not transmitted.
type Parameter struct {
	Path        Path
	Identifier  *string
	Description *string
	Value       Value
	Minimum     Value // Int, Real or Null
	Maximum     Value // Int, Real or Null
	Access      *Access
	Format      *string
	// Enumeration holds the entries in provider order. Hidden entries keep
	// their leading "~"; see EnumEntries.
	Enumeration       []string
	Factor            *int32
	IsOnline          *bool
	Formula           *string
	Step              *int32
	Default           Value
	Type              *ParameterType
	StreamIdentifier  *int32
	EnumMap           []EnumMapEntry
	StreamDescriptor  *StreamDescription
	SchemaIdentifiers *string
	TemplateReference Path
}

type EnumMapEntry struct {
	Name  string
	Value int32
}

// Command is only used in requests. Target selects the qualified type the
// path is sent with; a Command with an empty Path is sent at root level.
type Command struct {
	Path         Path
	Target       ElementKind
	Number       CommandType
	DirFieldMask FieldFlags  // GetDirectory only; FieldsDefault is not transmitted
	Invocation   *Invocation // Invoke only
}

func (n *Node) ElementPath() Path      { return n.Path }
func (p *Parameter) ElementPath() Path { return p.Path }
func (c *Command) ElementPath() Path   { return c.Path }

func (*Node) isElement()      {}
func (*Parameter) isElement() {}
func (*Command) isElement()   {}

// IsEmpty reports whether n carries neither properties nor children. This is
// how a provider answers a GetDirectory on a node without children (spec:
// "GetDirectory").
func (n *Node) IsEmpty() bool {
	return !n.hasContents() && !n.HasChildren
}

// IsStream reports whether p is a stream parameter, whose value updates must
// be requested with a Subscribe command (spec: "Subscribe").
func (p *Parameter) IsStream() bool {
	return p.StreamIdentifier != nil
}

// EffectiveType determines the type of a parameter following the rule of the
// Glow DTD 2.5 change log. ok is false if the type cannot be determined.
func (p *Parameter) EffectiveType() (t ParameterType, ok bool) {
	switch {
	case p.Type != nil && *p.Type == TypeTrigger:
		return TypeTrigger, true
	case p.Enumeration != nil || p.EnumMap != nil:
		return TypeEnum, true
	}
	if t, ok := valueType(p.Value); ok {
		return t, true
	}
	if p.Type != nil {
		return *p.Type, true
	}
	return 0, false
}

type EnumEntry struct {
	Name   string
	Value  int64
	Hidden bool
}

// EnumEntries returns the entries of an enum parameter in provider order. If
// EnumMap is present it takes precedence over Enumeration. For Enumeration,
// a leading "~" marks a hidden entry and is removed from the name.
func (p *Parameter) EnumEntries() []EnumEntry {
	if p.EnumMap != nil {
		entries := make([]EnumEntry, len(p.EnumMap))
		for i, e := range p.EnumMap {
			entries[i] = EnumEntry{Name: e.Name, Value: int64(e.Value)}
		}
		return entries
	}
	if p.Enumeration == nil {
		return nil
	}
	entries := make([]EnumEntry, len(p.Enumeration))
	for i, name := range p.Enumeration {
		hidden := strings.HasPrefix(name, "~")
		entries[i] = EnumEntry{Name: strings.TrimPrefix(name, "~"), Value: int64(i), Hidden: hidden}
	}
	return entries
}

// EnumName returns the name of value v, using the first match for aliases.
func (p *Parameter) EnumName(v int64) (string, bool) {
	for _, e := range p.EnumEntries() {
		if e.Value == v {
			return e.Name, true
		}
	}
	return "", false
}

// Merge applies all transmitted fields of u to n.
func (n *Node) Merge(u *Node) {
	mergePtr(&n.Identifier, u.Identifier)
	mergePtr(&n.Description, u.Description)
	mergePtr(&n.IsRoot, u.IsRoot)
	mergePtr(&n.IsOnline, u.IsOnline)
	mergePtr(&n.SchemaIdentifiers, u.SchemaIdentifiers)
	mergeSlice(&n.TemplateReference, u.TemplateReference)
}

// Clone returns a deep copy of n.
func (n *Node) Clone() *Node {
	c := &Node{Path: slices.Clone(n.Path), HasChildren: n.HasChildren}
	c.Merge(n)
	return c
}

// Merge applies all transmitted fields of u to p.
func (p *Parameter) Merge(u *Parameter) {
	mergePtr(&p.Identifier, u.Identifier)
	mergePtr(&p.Description, u.Description)
	mergeValue(&p.Value, u.Value)
	mergeValue(&p.Minimum, u.Minimum)
	mergeValue(&p.Maximum, u.Maximum)
	mergePtr(&p.Access, u.Access)
	mergePtr(&p.Format, u.Format)
	mergeSlice(&p.Enumeration, u.Enumeration)
	mergePtr(&p.Factor, u.Factor)
	mergePtr(&p.IsOnline, u.IsOnline)
	mergePtr(&p.Formula, u.Formula)
	mergePtr(&p.Step, u.Step)
	mergeValue(&p.Default, u.Default)
	mergePtr(&p.Type, u.Type)
	mergePtr(&p.StreamIdentifier, u.StreamIdentifier)
	mergeSlice(&p.EnumMap, u.EnumMap)
	mergePtr(&p.StreamDescriptor, u.StreamDescriptor)
	mergePtr(&p.SchemaIdentifiers, u.SchemaIdentifiers)
	mergeSlice(&p.TemplateReference, u.TemplateReference)
}

// Clone returns a deep copy of p.
func (p *Parameter) Clone() *Parameter {
	c := &Parameter{Path: slices.Clone(p.Path)}
	c.Merge(p)
	return c
}

// Clone returns a deep copy of c.
func (c *Command) Clone() *Command {
	cc := *c
	cc.Path = slices.Clone(c.Path)
	cc.Invocation = cloneInvocation(c.Invocation)
	return &cc
}

func mergePtr[T any](dst **T, src *T) {
	if src != nil {
		*dst = new(*src)
	}
}

func mergeSlice[S ~[]E, E any](dst *S, src S) {
	if src != nil {
		*dst = slices.Clone(src)
	}
}

func mergeValue(dst *Value, src Value) {
	if src != nil {
		*dst = cloneValue(src)
	}
}
