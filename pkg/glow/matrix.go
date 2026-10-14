package glow

import "slices"

// Matrix is a matrix of the provider tree (Ember+ 1.1). Pointer, slice and
// interface fields are nil when they were not transmitted.
type Matrix struct {
	Path                     Path
	Identifier               *string
	Description              *string
	Type                     *MatrixType
	AddressingMode           *AddressingMode
	TargetCount              *int32
	SourceCount              *int32
	MaximumTotalConnects     *int32
	MaximumConnectsPerTarget *int32
	ParametersLocation       ParametersLocation
	GainParameterNumber      *int32
	Labels                   []Label
	SchemaIdentifiers        *string
	TemplateReference        Path

	Targets     []uint32 // signal numbers
	Sources     []uint32 // signal numbers
	Connections []Connection
}

// ParametersLocation is BasePath or Inline.
type ParametersLocation interface {
	isParametersLocation()
}

// BasePath is the absolute path of the node holding signal and connection
// parameters.
type BasePath Path

// Inline is a sub-identifier appended to the matrix path; the node it refers
// to is not reported by GetDirectory on the matrix's parent (spec:
// "ParametersLocation").
type Inline uint32

func (BasePath) isParametersLocation() {}
func (Inline) isParametersLocation()   {}

type Label struct {
	BasePath    Path
	Description string
}

// Connection describes the sources connected to a target. Operation and
// Disposition default to OpAbsolute and Tally, which are their zero values.
// Sources is nil if the target has no connections.
type Connection struct {
	Target      uint32
	Sources     []uint32
	Operation   ConnectionOperation
	Disposition ConnectionDisposition
}

func (m *Matrix) ElementPath() Path { return m.Path }
func (*Matrix) isElement()          {}

// Merge applies all transmitted fields of u to m. Connections are merged per
// target: a connection in u replaces the connection of the same target in m.
// u is expected to come from the provider, i.e. to contain absolute sources.
// If u changes targetCount, targets or addressingMode, connections of targets
// that no longer exist are removed.
func (m *Matrix) Merge(u *Matrix) {
	mergePtr(&m.Identifier, u.Identifier)
	mergePtr(&m.Description, u.Description)
	mergePtr(&m.Type, u.Type)
	mergePtr(&m.AddressingMode, u.AddressingMode)
	mergePtr(&m.TargetCount, u.TargetCount)
	mergePtr(&m.SourceCount, u.SourceCount)
	mergePtr(&m.MaximumTotalConnects, u.MaximumTotalConnects)
	mergePtr(&m.MaximumConnectsPerTarget, u.MaximumConnectsPerTarget)
	if u.ParametersLocation != nil {
		m.ParametersLocation = cloneLocation(u.ParametersLocation)
	}
	mergePtr(&m.GainParameterNumber, u.GainParameterNumber)
	if u.Labels != nil {
		m.Labels = make([]Label, len(u.Labels))
		for i, l := range u.Labels {
			m.Labels[i] = Label{BasePath: slices.Clone(l.BasePath), Description: l.Description}
		}
	}
	mergePtr(&m.SchemaIdentifiers, u.SchemaIdentifiers)
	mergeSlice(&m.TemplateReference, u.TemplateReference)
	mergeSlice(&m.Targets, u.Targets)
	mergeSlice(&m.Sources, u.Sources)
	for _, c := range u.Connections {
		c.Sources = slices.Clone(c.Sources)
		i := slices.IndexFunc(m.Connections, func(x Connection) bool { return x.Target == c.Target })
		if i >= 0 {
			m.Connections[i] = c
		} else {
			m.Connections = append(m.Connections, c)
		}
	}
	if u.TargetCount != nil || u.Targets != nil || u.AddressingMode != nil {
		m.pruneConnections()
	}
}

// pruneConnections removes connections of targets that no longer exist: with
// a targets list, only listed targets exist (spec: "Matrix"); with linear
// addressing,
// only targets below targetCount.
func (m *Matrix) pruneConnections() {
	var listed map[uint32]bool
	if m.Targets != nil {
		listed = make(map[uint32]bool, len(m.Targets))
		for _, t := range m.Targets {
			listed[t] = true
		}
	}
	linear := m.AddressingMode == nil || *m.AddressingMode == Linear
	m.Connections = slices.DeleteFunc(m.Connections, func(c Connection) bool {
		if listed != nil && !listed[c.Target] {
			return true
		}
		return linear && m.TargetCount != nil && int64(c.Target) >= int64(*m.TargetCount)
	})
}

// Clone returns a deep copy of m.
func (m *Matrix) Clone() *Matrix {
	c := &Matrix{Path: slices.Clone(m.Path)}
	c.Merge(m)
	if m.Connections != nil && c.Connections == nil {
		c.Connections = []Connection{}
	}
	return c
}

// Connection returns the connection of target, if it was reported.
func (m *Matrix) Connection(target uint32) (Connection, bool) {
	i := slices.IndexFunc(m.Connections, func(x Connection) bool { return x.Target == target })
	if i < 0 {
		return Connection{}, false
	}
	return m.Connections[i], true
}

func cloneLocation(l ParametersLocation) ParametersLocation {
	if p, ok := l.(BasePath); ok {
		return BasePath(slices.Clone(p))
	}
	return l
}
