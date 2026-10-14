package glow

import "slices"

// Function is a function of the provider tree (Ember+ 1.2). Pointer and
// slice fields are nil when they were not transmitted.
type Function struct {
	Path        Path
	Identifier  *string
	Description *string
	// Arguments describes the arguments in order. If it is not transmitted,
	// the function takes no arguments.
	Arguments         []TupleItem
	Result            []TupleItem
	TemplateReference Path
}

// TupleItem describes one argument or result value. Name may be empty if the
// function only takes one argument.
type TupleItem struct {
	Type ParameterType
	Name string
}

// Invocation holds the arguments of an Invoke command.
type Invocation struct {
	// ID is repeated by the provider in the InvocationResult. A nil ID makes
	// the call fire-and-forget, which is only allowed for functions without
	// a result (spec: "Ember+ 1.2: Function Extensions").
	ID        *int32
	Arguments []Value
}

// InvocationResult is the provider's response to an Invoke command.
type InvocationResult struct {
	ID int32
	// Success is true if the provider reported success or omitted the field.
	Success bool
	Result  []Value
}

func (f *Function) ElementPath() Path { return f.Path }
func (*Function) isElement()          {}

func (*InvocationResult) isMessage() {}

// Merge applies all transmitted fields of u to f.
func (f *Function) Merge(u *Function) {
	mergePtr(&f.Identifier, u.Identifier)
	mergePtr(&f.Description, u.Description)
	mergeSlice(&f.Arguments, u.Arguments)
	mergeSlice(&f.Result, u.Result)
	mergeSlice(&f.TemplateReference, u.TemplateReference)
}

// Clone returns a deep copy of f.
func (f *Function) Clone() *Function {
	c := &Function{Path: slices.Clone(f.Path)}
	c.Merge(f)
	return c
}

func cloneInvocation(i *Invocation) *Invocation {
	if i == nil {
		return nil
	}
	c := &Invocation{}
	mergePtr(&c.ID, i.ID)
	if i.Arguments != nil {
		c.Arguments = make([]Value, len(i.Arguments))
		for n, v := range i.Arguments {
			c.Arguments[n] = cloneValue(v)
		}
	}
	return c
}
