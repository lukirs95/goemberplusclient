package glow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
)

var (
	ErrInvalid   = errors.New("glow: invalid message")
	ErrTooDeep   = errors.New("glow: nesting too deep")
	ErrNoMessage = errors.New("glow: unsupported root type")
)

// MaxDepth limits the nesting of elements when decoding.
const MaxDepth = 64

var (
	tagRoot                  = ber.Application(0)
	tagParameter             = ber.Application(1)
	tagCommand               = ber.Application(2)
	tagNode                  = ber.Application(3)
	tagElementCollection     = ber.Application(4)
	tagStreamEntry           = ber.Application(5)
	tagStreamCollection      = ber.Application(6)
	tagStringIntegerPair     = ber.Application(7)
	tagStringIntegerColl     = ber.Application(8)
	tagQualifiedParameter    = ber.Application(9)
	tagQualifiedNode         = ber.Application(10)
	tagRootElementCollection = ber.Application(11)
	tagStreamDescription     = ber.Application(12)
	tagMatrix                = ber.Application(13)
	tagTarget                = ber.Application(14)
	tagSource                = ber.Application(15)
	tagConnection            = ber.Application(16)
	tagQualifiedMatrix       = ber.Application(17)
	tagLabel                 = ber.Application(18)
	tagFunction              = ber.Application(19)
	tagTupleItemDescription  = ber.Application(21)
	tagInvocation            = ber.Application(22)
	tagInvocationResult      = ber.Application(23)
	tagQualifiedFunction     = ber.Application(20)
)

// Decode decodes an EmBER payload. Elements are returned as a flat list with
// absolute paths, parents before children. Commands and element types that
// are not supported (templates) are skipped, as are
// unknown fields. The result does not reference data.
func Decode(data []byte) (Message, error) {
	r := ber.NewReader(data)
	root, err := r.Next()
	if err == io.EOF {
		return nil, fmt.Errorf("%w: empty payload", ErrInvalid)
	}
	if err != nil {
		return nil, fmt.Errorf("glow: %w", err)
	}
	if root.Tag != tagRoot {
		return nil, fmt.Errorf("%w: %v", ErrNoMessage, root.Tag)
	}
	inner, err := root.Explicit()
	if err != nil {
		return nil, fmt.Errorf("glow: root: %w", err)
	}
	switch inner.Tag {
	case tagRootElementCollection:
		var d decoder
		if err := d.collection(inner, nil, 0); err != nil {
			return nil, err
		}
		return d.out, nil
	case tagInvocationResult:
		return decodeInvocationResult(inner)
	case tagStreamCollection:
		return decodeStreams(inner)
	}
	return nil, fmt.Errorf("%w: %v", ErrNoMessage, inner.Tag)
}

type decoder struct {
	out Elements
}

// sequence calls f for each entry of a SEQUENCE OF [0] T.
func sequence(t ber.TLV, f func(ber.TLV) error) error {
	r, err := t.Children()
	if err != nil {
		return err
	}
	for r.More() {
		entry, err := r.Next()
		if err != nil {
			return err
		}
		if entry.Tag != ber.Context(0) {
			return fmt.Errorf("%w: entry %v in %v", ErrInvalid, entry.Tag, t.Tag)
		}
		v, err := entry.Explicit()
		if err != nil {
			return err
		}
		if err := f(v); err != nil {
			return err
		}
	}
	return nil
}

// fields calls f with the tag number and the unwrapped value of each
// context-tagged field of a SEQUENCE or SET. Other fields are skipped.
func fields(t ber.TLV, f func(n uint32, v ber.TLV) error) error {
	r, err := t.Children()
	if err != nil {
		return err
	}
	for r.More() {
		field, err := r.Next()
		if err != nil {
			return err
		}
		if field.Tag.Class != ber.ClassContext {
			continue
		}
		v, err := field.Explicit()
		if err != nil {
			return fmt.Errorf("field %d: %w", field.Tag.Number, err)
		}
		if err := f(field.Tag.Number, v); err != nil {
			return fmt.Errorf("field %d: %w", field.Tag.Number, err)
		}
	}
	return nil
}

func (d *decoder) collection(t ber.TLV, parent Path, depth int) error {
	if depth >= MaxDepth {
		return fmt.Errorf("%w: below %v", ErrTooDeep, parent)
	}
	return sequence(t, func(el ber.TLV) error {
		switch el.Tag {
		case tagParameter:
			return d.parameter(el, parent, false)
		case tagQualifiedParameter:
			return d.parameter(el, nil, true)
		case tagNode:
			return d.node(el, parent, false, depth)
		case tagQualifiedNode:
			return d.node(el, nil, true, depth)
		case tagMatrix:
			return d.matrix(el, parent, false)
		case tagQualifiedMatrix:
			return d.matrix(el, nil, true)
		case tagFunction:
			return d.function(el, parent, false)
		case tagQualifiedFunction:
			return d.function(el, nil, true)
		}
		return nil
	})
}

// path decodes the number or, for qualified elements, the path field.
func path(v ber.TLV, parent Path, qualified bool) (Path, error) {
	if qualified {
		p, err := decodePath(v)
		if err == nil && len(p) == 0 {
			err = fmt.Errorf("%w: empty path", ErrInvalid)
		}
		return p, err
	}
	n, err := decodeNumber(v)
	if err != nil {
		return nil, err
	}
	return parent.Child(n), nil
}

func (d *decoder) node(t ber.TLV, parent Path, qualified bool, depth int) error {
	n := &Node{}
	var children ber.TLV
	err := fields(t, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			n.Path, err = path(v, parent, qualified)
		case 1:
			err = n.decodeContents(v)
		case 2:
			if v.Tag != tagElementCollection {
				return fmt.Errorf("%w: children of type %v", ErrInvalid, v.Tag)
			}
			children = v
			n.HasChildren = true
		}
		return err
	})
	if err == nil && n.Path == nil {
		err = fmt.Errorf("%w: missing number", ErrInvalid)
	}
	if err != nil {
		return fmt.Errorf("glow: node below %v: %w", parent, err)
	}
	d.out = append(d.out, n)
	if n.HasChildren {
		return d.collection(children, n.Path, depth+1)
	}
	return nil
}

func (n *Node) decodeContents(set ber.TLV) error {
	return fields(set, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			n.Identifier, err = decodeString(v)
		case 1:
			n.Description, err = decodeString(v)
		case 2:
			n.IsRoot, err = decodeBool(v)
		case 3:
			n.IsOnline, err = decodeBool(v)
		case 4:
			n.SchemaIdentifiers, err = decodeString(v)
		case 5:
			n.TemplateReference, err = decodePath(v)
		}
		return err
	})
}

func (d *decoder) parameter(t ber.TLV, parent Path, qualified bool) error {
	p := &Parameter{}
	err := fields(t, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			p.Path, err = path(v, parent, qualified)
		case 1:
			err = p.decodeContents(v)
		}
		return err
	})
	if err == nil && p.Path == nil {
		err = fmt.Errorf("%w: missing number", ErrInvalid)
	}
	if err != nil {
		return fmt.Errorf("glow: parameter below %v: %w", parent, err)
	}
	d.out = append(d.out, p)
	return nil
}

func (p *Parameter) decodeContents(set ber.TLV) error {
	return fields(set, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			p.Identifier, err = decodeString(v)
		case 1:
			p.Description, err = decodeString(v)
		case 2:
			p.Value, err = decodeValue(v)
		case 3:
			p.Minimum, err = decodeValue(v)
		case 4:
			p.Maximum, err = decodeValue(v)
		case 5:
			p.Access, err = decodeEnum[Access](v)
		case 6:
			p.Format, err = decodeString(v)
		case 7:
			var s *string
			if s, err = decodeString(v); err == nil {
				p.Enumeration = splitLines(*s)
			}
		case 8:
			p.Factor, err = decodeInt32(v)
		case 9:
			p.IsOnline, err = decodeBool(v)
		case 10:
			p.Formula, err = decodeString(v)
		case 11:
			p.Step, err = decodeInt32(v)
		case 12:
			p.Default, err = decodeValue(v)
		case 13:
			p.Type, err = decodeEnum[ParameterType](v)
		case 14:
			p.StreamIdentifier, err = decodeInt32(v)
		case 15:
			p.EnumMap, err = decodeEnumMap(v)
		case 16:
			p.StreamDescriptor, err = decodeStreamDescription(v)
		case 17:
			p.SchemaIdentifiers, err = decodeString(v)
		case 18:
			p.TemplateReference, err = decodePath(v)
		}
		return err
	})
}

// splitLines splits a line feed separated list. The empty string yields an
// empty, non-nil list.
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

func decodeEnumMap(v ber.TLV) ([]EnumMapEntry, error) {
	if v.Tag != tagStringIntegerColl {
		return nil, &ber.TagError{Want: tagStringIntegerColl, Got: v.Tag}
	}
	entries := []EnumMapEntry{}
	err := sequence(v, func(pair ber.TLV) error {
		if pair.Tag != tagStringIntegerPair {
			return &ber.TagError{Want: tagStringIntegerPair, Got: pair.Tag}
		}
		var e EnumMapEntry
		err := fields(pair, func(num uint32, v ber.TLV) (err error) {
			switch num {
			case 0:
				e.Name, err = v.UTF8()
			case 1:
				e.Value, err = v.Int32()
			}
			return err
		})
		entries = append(entries, e)
		return err
	})
	return entries, err
}

func decodeStreamDescription(v ber.TLV) (*StreamDescription, error) {
	if v.Tag != tagStreamDescription {
		return nil, &ber.TagError{Want: tagStreamDescription, Got: v.Tag}
	}
	var d StreamDescription
	err := fields(v, func(num uint32, v ber.TLV) error {
		switch num {
		case 0:
			f, err := v.Int32()
			d.Format = StreamFormat(f)
			return err
		case 1:
			o, err := v.Int32()
			d.Offset = o
			return err
		}
		return nil
	})
	return &d, err
}

func decodeStreams(t ber.TLV) (Streams, error) {
	streams := Streams{}
	err := sequence(t, func(v ber.TLV) error {
		if v.Tag != tagStreamEntry {
			return &ber.TagError{Want: tagStreamEntry, Got: v.Tag}
		}
		var e StreamEntry
		err := fields(v, func(num uint32, v ber.TLV) (err error) {
			switch num {
			case 0:
				e.Identifier, err = v.Int32()
			case 1:
				e.Value, err = decodeValue(v)
			}
			return err
		})
		streams = append(streams, e)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("glow: streams: %w", err)
	}
	return streams, nil
}

func decodeValue(v ber.TLV) (Value, error) {
	switch v.Tag {
	case ber.TagInteger:
		i, err := v.Int()
		return Int(i), err
	case ber.TagReal:
		f, err := v.Real()
		return Real(f), err
	case ber.TagUTF8String:
		s, err := v.UTF8()
		return String(s), err
	case ber.TagBoolean:
		b, err := v.Bool()
		return Bool(b), err
	case ber.TagOctetString:
		o, err := v.Octets()
		return Octets(bytes.Clone(o)), err
	case ber.TagNull:
		return Null{}, v.Null()
	}
	return nil, fmt.Errorf("%w: value of type %v", ErrInvalid, v.Tag)
}

func decodeString(v ber.TLV) (*string, error) {
	s, err := v.UTF8()
	if err != nil {
		return nil, err
	}
	return new(s), nil
}

func decodeBool(v ber.TLV) (*bool, error) {
	b, err := v.Bool()
	if err != nil {
		return nil, err
	}
	return new(b), nil
}

func decodeInt32(v ber.TLV) (*int32, error) {
	i, err := v.Int32()
	if err != nil {
		return nil, err
	}
	return new(i), nil
}

func decodeEnum[T ~int32](v ber.TLV) (*T, error) {
	i, err := v.Int32()
	if err != nil {
		return nil, err
	}
	return new(T(i)), nil
}

func (d *decoder) matrix(t ber.TLV, parent Path, qualified bool) error {
	m := &Matrix{}
	err := fields(t, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			m.Path, err = path(v, parent, qualified)
		case 1:
			err = m.decodeContents(v)
		case 3:
			m.Targets, err = decodeSignals(v, tagTarget)
		case 4:
			m.Sources, err = decodeSignals(v, tagSource)
		case 5:
			m.Connections, err = decodeConnections(v)
		}
		return err
	})
	if err == nil && m.Path == nil {
		err = fmt.Errorf("%w: missing number", ErrInvalid)
	}
	if err != nil {
		return fmt.Errorf("glow: matrix below %v: %w", parent, err)
	}
	d.out = append(d.out, m)
	return nil
}

func (m *Matrix) decodeContents(set ber.TLV) error {
	return fields(set, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			m.Identifier, err = decodeString(v)
		case 1:
			m.Description, err = decodeString(v)
		case 2:
			m.Type, err = decodeEnum[MatrixType](v)
		case 3:
			m.AddressingMode, err = decodeEnum[AddressingMode](v)
		case 4:
			m.TargetCount, err = decodeInt32(v)
		case 5:
			m.SourceCount, err = decodeInt32(v)
		case 6:
			m.MaximumTotalConnects, err = decodeInt32(v)
		case 7:
			m.MaximumConnectsPerTarget, err = decodeInt32(v)
		case 8:
			m.ParametersLocation, err = decodeParametersLocation(v)
		case 9:
			m.GainParameterNumber, err = decodeInt32(v)
		case 10:
			m.Labels, err = decodeLabels(v)
		case 11:
			m.SchemaIdentifiers, err = decodeString(v)
		case 12:
			m.TemplateReference, err = decodePath(v)
		}
		return err
	})
}

func decodeParametersLocation(v ber.TLV) (ParametersLocation, error) {
	switch v.Tag {
	case ber.TagRelativeOID:
		p, err := decodePath(v)
		return BasePath(p), err
	case ber.TagInteger:
		n, err := decodeNumber(v)
		return Inline(n), err
	}
	return nil, fmt.Errorf("%w: parametersLocation of type %v", ErrInvalid, v.Tag)
}

func decodeLabels(v ber.TLV) ([]Label, error) {
	labels := []Label{}
	err := sequence(v, func(l ber.TLV) error {
		if l.Tag != tagLabel {
			return &ber.TagError{Want: tagLabel, Got: l.Tag}
		}
		var label Label
		err := fields(l, func(num uint32, v ber.TLV) (err error) {
			switch num {
			case 0:
				label.BasePath, err = decodePath(v)
			case 1:
				label.Description, err = v.UTF8()
			}
			return err
		})
		labels = append(labels, label)
		return err
	})
	return labels, err
}

// decodeSignals decodes a TargetCollection or SourceCollection.
func decodeSignals(v ber.TLV, tag ber.Tag) ([]uint32, error) {
	signals := []uint32{}
	err := sequence(v, func(s ber.TLV) error {
		if s.Tag != tag {
			return &ber.TagError{Want: tag, Got: s.Tag}
		}
		return fields(s, func(num uint32, v ber.TLV) error {
			if num != 0 {
				return nil
			}
			n, err := decodeNumber(v)
			signals = append(signals, n)
			return err
		})
	})
	return signals, err
}

func decodeConnections(v ber.TLV) ([]Connection, error) {
	connections := []Connection{}
	err := sequence(v, func(c ber.TLV) error {
		if c.Tag != tagConnection {
			return &ber.TagError{Want: tagConnection, Got: c.Tag}
		}
		var conn Connection
		err := fields(c, func(num uint32, v ber.TLV) (err error) {
			switch num {
			case 0:
				conn.Target, err = decodeNumber(v)
			case 1:
				var p Path
				p, err = decodePath(v)
				conn.Sources = []uint32(p)
			case 2:
				var op *ConnectionOperation
				if op, err = decodeEnum[ConnectionOperation](v); err == nil {
					conn.Operation = *op
				}
			case 3:
				var d *ConnectionDisposition
				if d, err = decodeEnum[ConnectionDisposition](v); err == nil {
					conn.Disposition = *d
				}
			}
			return err
		})
		connections = append(connections, conn)
		return err
	})
	return connections, err
}

func (d *decoder) function(t ber.TLV, parent Path, qualified bool) error {
	f := &Function{}
	err := fields(t, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			f.Path, err = path(v, parent, qualified)
		case 1:
			err = fields(v, func(num uint32, v ber.TLV) (err error) {
				switch num {
				case 0:
					f.Identifier, err = decodeString(v)
				case 1:
					f.Description, err = decodeString(v)
				case 2:
					f.Arguments, err = decodeTupleDescription(v)
				case 3:
					f.Result, err = decodeTupleDescription(v)
				case 4:
					f.TemplateReference, err = decodePath(v)
				}
				return err
			})
		}
		return err
	})
	if err == nil && f.Path == nil {
		err = fmt.Errorf("%w: missing number", ErrInvalid)
	}
	if err != nil {
		return fmt.Errorf("glow: function below %v: %w", parent, err)
	}
	d.out = append(d.out, f)
	return nil
}

func decodeTupleDescription(v ber.TLV) ([]TupleItem, error) {
	items := []TupleItem{}
	err := sequence(v, func(it ber.TLV) error {
		if it.Tag != tagTupleItemDescription {
			return &ber.TagError{Want: tagTupleItemDescription, Got: it.Tag}
		}
		var item TupleItem
		err := fields(it, func(num uint32, v ber.TLV) error {
			switch num {
			case 0:
				t, err := v.Int32()
				item.Type = ParameterType(t)
				return err
			case 1:
				name, err := v.UTF8()
				item.Name = name
				return err
			}
			return nil
		})
		items = append(items, item)
		return err
	})
	return items, err
}

func decodeTuple(v ber.TLV) ([]Value, error) {
	values := []Value{}
	err := sequence(v, func(v ber.TLV) error {
		value, err := decodeValue(v)
		values = append(values, value)
		return err
	})
	return values, err
}

func decodeInvocationResult(t ber.TLV) (*InvocationResult, error) {
	r := &InvocationResult{Success: true}
	hasID := false
	err := fields(t, func(num uint32, v ber.TLV) (err error) {
		switch num {
		case 0:
			r.ID, err = v.Int32()
			hasID = true
		case 1:
			r.Success, err = v.Bool()
		case 2:
			r.Result, err = decodeTuple(v)
		}
		return err
	})
	if err == nil && !hasID {
		err = fmt.Errorf("%w: missing invocationId", ErrInvalid)
	}
	if err != nil {
		return nil, fmt.Errorf("glow: invocation result: %w", err)
	}
	return r, nil
}

// decodeNumber decodes a non-negative Integer32.
func decodeNumber(v ber.TLV) (uint32, error) {
	n, err := v.Int32()
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("%w: negative number %d", ErrInvalid, n)
	}
	return uint32(n), nil
}

func decodePath(v ber.TLV) (Path, error) {
	oid, err := v.RelativeOID(nil)
	if err != nil {
		return nil, err
	}
	return append(Path{}, oid...), nil
}
