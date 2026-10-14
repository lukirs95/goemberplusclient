package glow

import (
	"fmt"
	"strings"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
)

// Encode appends a Glow root containing elems to b. All elements are written
// as qualified elements; a Command with an empty Path is written at root
// level. Node.HasChildren is ignored.
func Encode(b *ber.Builder, elems ...Element) error {
	for _, el := range elems {
		if err := validate(el); err != nil {
			return err
		}
	}
	b.Constructed(tagRoot, func(b *ber.Builder) {
		b.Constructed(tagRootElementCollection, func(b *ber.Builder) {
			for _, el := range elems {
				b.Constructed(ber.Context(0), func(b *ber.Builder) {
					encodeElement(b, el)
				})
			}
		})
	})
	_, err := b.Bytes()
	return err
}

func validate(el Element) error {
	switch e := el.(type) {
	case *Command:
		if len(e.Path) > 0 && qualifiedTag(e.Target) == (ber.Tag{}) {
			return fmt.Errorf("glow: command on %v: invalid target %v", e.Path, e.Target)
		}
		if e.Number == CmdInvoke && (len(e.Path) == 0 || e.Target != KindFunction) {
			return fmt.Errorf("glow: invoke on %v: target must be a function", e.Path)
		}
		if e.Invocation != nil && e.Number != CmdInvoke {
			return fmt.Errorf("glow: invocation with command %v", e.Number)
		}
		return nil
	case *Node, *Parameter, *Matrix, *Function:
		if len(el.ElementPath()) == 0 {
			return fmt.Errorf("glow: %T without path", el)
		}
		return nil
	}
	return fmt.Errorf("glow: cannot encode %T", el)
}

func qualifiedTag(k ElementKind) ber.Tag {
	switch k {
	case KindNode:
		return tagQualifiedNode
	case KindParameter:
		return tagQualifiedParameter
	case KindMatrix:
		return tagQualifiedMatrix
	case KindFunction:
		return tagQualifiedFunction
	}
	return ber.Tag{}
}

func encodeElement(b *ber.Builder, el Element) {
	switch e := el.(type) {
	case *Command:
		if len(e.Path) == 0 {
			e.encode(b)
			return
		}
		b.Constructed(qualifiedTag(e.Target), func(b *ber.Builder) {
			field(b, 0, func(b *ber.Builder) { b.RelativeOID(e.Path) })
			field(b, 2, func(b *ber.Builder) {
				b.Constructed(tagElementCollection, func(b *ber.Builder) {
					field(b, 0, e.encode)
				})
			})
		})
	case *Node:
		b.Constructed(tagQualifiedNode, func(b *ber.Builder) {
			field(b, 0, func(b *ber.Builder) { b.RelativeOID(e.Path) })
			if e.hasContents() {
				field(b, 1, func(b *ber.Builder) { b.Constructed(ber.TagSet, e.encodeContents) })
			}
		})
	case *Matrix:
		e.encode(b)
	case *Function:
		e.encode(b)
	case *Parameter:
		b.Constructed(tagQualifiedParameter, func(b *ber.Builder) {
			field(b, 0, func(b *ber.Builder) { b.RelativeOID(e.Path) })
			if e.hasContents() {
				field(b, 1, func(b *ber.Builder) { b.Constructed(ber.TagSet, e.encodeContents) })
			}
		})
	}
}

func (m *Matrix) encode(b *ber.Builder) {
	b.Constructed(tagQualifiedMatrix, func(b *ber.Builder) {
		field(b, 0, func(b *ber.Builder) { b.RelativeOID(m.Path) })
		if m.hasContents() {
			field(b, 1, func(b *ber.Builder) { b.Constructed(ber.TagSet, m.encodeContents) })
		}
		if m.Targets != nil {
			field(b, 3, func(b *ber.Builder) { encodeSignals(b, tagTarget, m.Targets) })
		}
		if m.Sources != nil {
			field(b, 4, func(b *ber.Builder) { encodeSignals(b, tagSource, m.Sources) })
		}
		if m.Connections != nil {
			field(b, 5, func(b *ber.Builder) {
				b.Constructed(ber.TagSequence, func(b *ber.Builder) {
					for _, c := range m.Connections {
						field(b, 0, c.encode)
					}
				})
			})
		}
	})
}

func (m *Matrix) hasContents() bool {
	return m.Identifier != nil || m.Description != nil || m.Type != nil || m.AddressingMode != nil ||
		m.TargetCount != nil || m.SourceCount != nil || m.MaximumTotalConnects != nil ||
		m.MaximumConnectsPerTarget != nil || m.ParametersLocation != nil ||
		m.GainParameterNumber != nil || m.Labels != nil || m.SchemaIdentifiers != nil ||
		m.TemplateReference != nil
}

func (m *Matrix) encodeContents(b *ber.Builder) {
	stringField(b, 0, m.Identifier)
	stringField(b, 1, m.Description)
	intField(b, 2, m.Type)
	intField(b, 3, m.AddressingMode)
	intField(b, 4, m.TargetCount)
	intField(b, 5, m.SourceCount)
	intField(b, 6, m.MaximumTotalConnects)
	intField(b, 7, m.MaximumConnectsPerTarget)
	switch l := m.ParametersLocation.(type) {
	case BasePath:
		field(b, 8, func(b *ber.Builder) { b.RelativeOID(l) })
	case Inline:
		field(b, 8, func(b *ber.Builder) { b.Int(int64(l)) })
	}
	intField(b, 9, m.GainParameterNumber)
	if m.Labels != nil {
		field(b, 10, func(b *ber.Builder) {
			b.Constructed(ber.TagSequence, func(b *ber.Builder) {
				for _, l := range m.Labels {
					field(b, 0, func(b *ber.Builder) {
						b.Constructed(tagLabel, func(b *ber.Builder) {
							field(b, 0, func(b *ber.Builder) { b.RelativeOID(l.BasePath) })
							field(b, 1, func(b *ber.Builder) { b.UTF8(l.Description) })
						})
					})
				}
			})
		})
	}
	stringField(b, 11, m.SchemaIdentifiers)
	pathField(b, 12, m.TemplateReference)
}

func encodeSignals(b *ber.Builder, tag ber.Tag, signals []uint32) {
	b.Constructed(ber.TagSequence, func(b *ber.Builder) {
		for _, n := range signals {
			field(b, 0, func(b *ber.Builder) {
				b.Constructed(tag, func(b *ber.Builder) {
					field(b, 0, func(b *ber.Builder) { b.Int(int64(n)) })
				})
			})
		}
	})
}

func (c Connection) encode(b *ber.Builder) {
	b.Constructed(tagConnection, func(b *ber.Builder) {
		field(b, 0, func(b *ber.Builder) { b.Int(int64(c.Target)) })
		if c.Sources != nil {
			field(b, 1, func(b *ber.Builder) { b.RelativeOID(c.Sources) })
		}
		if c.Operation != OpAbsolute {
			field(b, 2, func(b *ber.Builder) { b.Int(int64(c.Operation)) })
		}
		if c.Disposition != Tally {
			field(b, 3, func(b *ber.Builder) { b.Int(int64(c.Disposition)) })
		}
	})
}

func (c *Command) encode(b *ber.Builder) {
	b.Constructed(tagCommand, func(b *ber.Builder) {
		field(b, 0, func(b *ber.Builder) { b.Int(int64(c.Number)) })
		if c.Number == CmdGetDirectory && c.DirFieldMask != FieldsDefault {
			field(b, 1, func(b *ber.Builder) { b.Int(int64(c.DirFieldMask)) })
		}
		if inv := c.Invocation; inv != nil {
			field(b, 2, func(b *ber.Builder) {
				b.Constructed(tagInvocation, func(b *ber.Builder) {
					intField(b, 0, inv.ID)
					if inv.Arguments != nil {
						field(b, 1, func(b *ber.Builder) { encodeTuple(b, inv.Arguments) })
					}
				})
			})
		}
	})
}

func (f *Function) encode(b *ber.Builder) {
	b.Constructed(tagQualifiedFunction, func(b *ber.Builder) {
		field(b, 0, func(b *ber.Builder) { b.RelativeOID(f.Path) })
		if f.Identifier == nil && f.Description == nil && f.Arguments == nil && f.Result == nil && f.TemplateReference == nil {
			return
		}
		field(b, 1, func(b *ber.Builder) {
			b.Constructed(ber.TagSet, func(b *ber.Builder) {
				stringField(b, 0, f.Identifier)
				stringField(b, 1, f.Description)
				if f.Arguments != nil {
					field(b, 2, func(b *ber.Builder) { encodeTupleDescription(b, f.Arguments) })
				}
				if f.Result != nil {
					field(b, 3, func(b *ber.Builder) { encodeTupleDescription(b, f.Result) })
				}
				pathField(b, 4, f.TemplateReference)
			})
		})
	})
}

func encodeTupleDescription(b *ber.Builder, items []TupleItem) {
	b.Constructed(ber.TagSequence, func(b *ber.Builder) {
		for _, it := range items {
			field(b, 0, func(b *ber.Builder) {
				b.Constructed(tagTupleItemDescription, func(b *ber.Builder) {
					field(b, 0, func(b *ber.Builder) { b.Int(int64(it.Type)) })
					if it.Name != "" {
						field(b, 1, func(b *ber.Builder) { b.UTF8(it.Name) })
					}
				})
			})
		}
	})
}

func encodeTuple(b *ber.Builder, values []Value) {
	b.Constructed(ber.TagSequence, func(b *ber.Builder) {
		for _, v := range values {
			field(b, 0, func(b *ber.Builder) { encodeValue(b, v) })
		}
	})
}

func (n *Node) hasContents() bool {
	return n.Identifier != nil || n.Description != nil || n.IsRoot != nil || n.IsOnline != nil ||
		n.SchemaIdentifiers != nil || n.TemplateReference != nil
}

func (n *Node) encodeContents(b *ber.Builder) {
	stringField(b, 0, n.Identifier)
	stringField(b, 1, n.Description)
	boolField(b, 2, n.IsRoot)
	boolField(b, 3, n.IsOnline)
	stringField(b, 4, n.SchemaIdentifiers)
	pathField(b, 5, n.TemplateReference)
}

func (p *Parameter) hasContents() bool {
	return p.Identifier != nil || p.Description != nil || p.Value != nil || p.Minimum != nil ||
		p.Maximum != nil || p.Access != nil || p.Format != nil || p.Enumeration != nil ||
		p.Factor != nil || p.IsOnline != nil || p.Formula != nil || p.Step != nil ||
		p.Default != nil || p.Type != nil || p.StreamIdentifier != nil || p.EnumMap != nil ||
		p.StreamDescriptor != nil || p.SchemaIdentifiers != nil || p.TemplateReference != nil
}

func (p *Parameter) encodeContents(b *ber.Builder) {
	stringField(b, 0, p.Identifier)
	stringField(b, 1, p.Description)
	valueField(b, 2, p.Value)
	valueField(b, 3, p.Minimum)
	valueField(b, 4, p.Maximum)
	intField(b, 5, p.Access)
	stringField(b, 6, p.Format)
	if p.Enumeration != nil {
		field(b, 7, func(b *ber.Builder) { b.UTF8(strings.Join(p.Enumeration, "\n")) })
	}
	intField(b, 8, p.Factor)
	boolField(b, 9, p.IsOnline)
	stringField(b, 10, p.Formula)
	intField(b, 11, p.Step)
	valueField(b, 12, p.Default)
	intField(b, 13, p.Type)
	intField(b, 14, p.StreamIdentifier)
	if p.EnumMap != nil {
		field(b, 15, func(b *ber.Builder) {
			b.Constructed(tagStringIntegerColl, func(b *ber.Builder) {
				for _, e := range p.EnumMap {
					field(b, 0, func(b *ber.Builder) {
						b.Constructed(tagStringIntegerPair, func(b *ber.Builder) {
							field(b, 0, func(b *ber.Builder) { b.UTF8(e.Name) })
							field(b, 1, func(b *ber.Builder) { b.Int(int64(e.Value)) })
						})
					})
				}
			})
		})
	}
	if d := p.StreamDescriptor; d != nil {
		field(b, 16, func(b *ber.Builder) {
			b.Constructed(tagStreamDescription, func(b *ber.Builder) {
				field(b, 0, func(b *ber.Builder) { b.Int(int64(d.Format)) })
				field(b, 1, func(b *ber.Builder) { b.Int(int64(d.Offset)) })
			})
		})
	}
	stringField(b, 17, p.SchemaIdentifiers)
	pathField(b, 18, p.TemplateReference)
}

// field writes an explicitly tagged context field.
func field(b *ber.Builder, n uint32, f func(*ber.Builder)) {
	b.Constructed(ber.Context(n), f)
}

func stringField(b *ber.Builder, n uint32, s *string) {
	if s != nil {
		field(b, n, func(b *ber.Builder) { b.UTF8(*s) })
	}
}

func boolField(b *ber.Builder, n uint32, v *bool) {
	if v != nil {
		field(b, n, func(b *ber.Builder) { b.Bool(*v) })
	}
}

func intField[T ~int32](b *ber.Builder, n uint32, v *T) {
	if v != nil {
		field(b, n, func(b *ber.Builder) { b.Int(int64(*v)) })
	}
}

func pathField(b *ber.Builder, n uint32, p Path) {
	if p != nil {
		field(b, n, func(b *ber.Builder) { b.RelativeOID(p) })
	}
}

func valueField(b *ber.Builder, n uint32, v Value) {
	if v != nil {
		field(b, n, func(b *ber.Builder) { encodeValue(b, v) })
	}
}

func encodeValue(b *ber.Builder, v Value) {
	switch v := v.(type) {
	case Int:
		b.Int(int64(v))
	case Real:
		b.Real(float64(v))
	case String:
		b.UTF8(string(v))
	case Bool:
		b.Bool(bool(v))
	case Octets:
		b.Octets(v)
	case Null:
		b.Null()
	}
}
