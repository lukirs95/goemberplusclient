package glow

import "bytes"

// Value is one of Int, Real, String, Bool, Octets or Null. A nil Value means
// "not transmitted", Null{} an explicit null value (Glow 2.50).
type Value interface {
	isValue()
}

type (
	Int    int64
	Real   float64
	String string
	Bool   bool
	Octets []byte
	Null   struct{}
)

func (Int) isValue()    {}
func (Real) isValue()   {}
func (String) isValue() {}
func (Bool) isValue()   {}
func (Octets) isValue() {}
func (Null) isValue()   {}

func cloneValue(v Value) Value {
	if o, ok := v.(Octets); ok {
		return Octets(bytes.Clone(o))
	}
	return v
}

// valueType returns the parameter type matching v.
func valueType(v Value) (ParameterType, bool) {
	switch v.(type) {
	case Int:
		return TypeInteger, true
	case Real:
		return TypeReal, true
	case String:
		return TypeString, true
	case Bool:
		return TypeBoolean, true
	case Octets:
		return TypeOctets, true
	}
	return 0, false
}
