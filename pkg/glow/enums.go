package glow

import "fmt"

type Access int32

const (
	AccessNone      Access = 0
	AccessRead      Access = 1
	AccessWrite     Access = 2
	AccessReadWrite Access = 3
)

func (a Access) String() string {
	switch a {
	case AccessNone:
		return "none"
	case AccessRead:
		return "read"
	case AccessWrite:
		return "write"
	case AccessReadWrite:
		return "readWrite"
	}
	return fmt.Sprintf("Access(%d)", int32(a))
}

type ParameterType int32

const (
	TypeNull    ParameterType = 0
	TypeInteger ParameterType = 1
	TypeReal    ParameterType = 2
	TypeString  ParameterType = 3
	TypeBoolean ParameterType = 4
	TypeTrigger ParameterType = 5
	TypeEnum    ParameterType = 6
	TypeOctets  ParameterType = 7
)

func (t ParameterType) String() string {
	switch t {
	case TypeNull:
		return "null"
	case TypeInteger:
		return "integer"
	case TypeReal:
		return "real"
	case TypeString:
		return "string"
	case TypeBoolean:
		return "boolean"
	case TypeTrigger:
		return "trigger"
	case TypeEnum:
		return "enum"
	case TypeOctets:
		return "octets"
	}
	return fmt.Sprintf("ParameterType(%d)", int32(t))
}

type CommandType int32

const (
	CmdSubscribe    CommandType = 30
	CmdUnsubscribe  CommandType = 31
	CmdGetDirectory CommandType = 32
	CmdInvoke       CommandType = 33
)

func (c CommandType) String() string {
	switch c {
	case CmdSubscribe:
		return "subscribe"
	case CmdUnsubscribe:
		return "unsubscribe"
	case CmdGetDirectory:
		return "getDirectory"
	case CmdInvoke:
		return "invoke"
	}
	return fmt.Sprintf("CommandType(%d)", int32(c))
}

// FieldFlags is the dirFieldMask of a GetDirectory command.
type FieldFlags int32

const (
	FieldsSparse      FieldFlags = -2
	FieldsAll         FieldFlags = -1
	FieldsDefault     FieldFlags = 0 // same as FieldsAll; not transmitted
	FieldsIdentifier  FieldFlags = 1
	FieldsDescription FieldFlags = 2
	FieldsTree        FieldFlags = 3
	FieldsValue       FieldFlags = 4
	FieldsConnections FieldFlags = 5
)

func (f FieldFlags) String() string {
	switch f {
	case FieldsSparse:
		return "sparse"
	case FieldsAll:
		return "all"
	case FieldsDefault:
		return "default"
	case FieldsIdentifier:
		return "identifier"
	case FieldsDescription:
		return "description"
	case FieldsTree:
		return "tree"
	case FieldsValue:
		return "value"
	case FieldsConnections:
		return "connections"
	}
	return fmt.Sprintf("FieldFlags(%d)", int32(f))
}

// ElementKind selects the qualified type a Command is sent with.
type ElementKind uint8

const (
	KindUnknown ElementKind = iota
	KindNode
	KindParameter
	KindMatrix
	KindFunction
)

func (k ElementKind) String() string {
	switch k {
	case KindNode:
		return "node"
	case KindParameter:
		return "parameter"
	case KindMatrix:
		return "matrix"
	case KindFunction:
		return "function"
	}
	return fmt.Sprintf("ElementKind(%d)", uint8(k))
}

// StreamFormat describes a value inside an octet stream (DTD StreamFormat):
// bits 3-7 type (0 uint, 1 int, 2 float), bits 1-2 size (1, 2, 4, 8 bytes),
// bit 0 endianness (0 big, 1 little).
type StreamFormat int32

const (
	UInt8     StreamFormat = 0
	UInt16BE  StreamFormat = 2
	UInt16LE  StreamFormat = 3
	UInt32BE  StreamFormat = 4
	UInt32LE  StreamFormat = 5
	UInt64BE  StreamFormat = 6
	UInt64LE  StreamFormat = 7
	Int8      StreamFormat = 8
	Int16BE   StreamFormat = 10
	Int16LE   StreamFormat = 11
	Int32BE   StreamFormat = 12
	Int32LE   StreamFormat = 13
	Int64BE   StreamFormat = 14
	Int64LE   StreamFormat = 15
	Float32BE StreamFormat = 20
	Float32LE StreamFormat = 21
	Float64BE StreamFormat = 22
	Float64LE StreamFormat = 23
)

type MatrixType int32

const (
	OneToN   MatrixType = 0
	OneToOne MatrixType = 1
	NToN     MatrixType = 2
)

func (t MatrixType) String() string {
	switch t {
	case OneToN:
		return "oneToN"
	case OneToOne:
		return "oneToOne"
	case NToN:
		return "nToN"
	}
	return fmt.Sprintf("MatrixType(%d)", int32(t))
}

type AddressingMode int32

const (
	Linear    AddressingMode = 0
	NonLinear AddressingMode = 1
)

func (m AddressingMode) String() string {
	switch m {
	case Linear:
		return "linear"
	case NonLinear:
		return "nonLinear"
	}
	return fmt.Sprintf("AddressingMode(%d)", int32(m))
}

type ConnectionOperation int32

const (
	OpAbsolute   ConnectionOperation = 0
	OpConnect    ConnectionOperation = 1
	OpDisconnect ConnectionOperation = 2
)

func (o ConnectionOperation) String() string {
	switch o {
	case OpAbsolute:
		return "absolute"
	case OpConnect:
		return "connect"
	case OpDisconnect:
		return "disconnect"
	}
	return fmt.Sprintf("ConnectionOperation(%d)", int32(o))
}

type ConnectionDisposition int32

const (
	Tally    ConnectionDisposition = 0
	Modified ConnectionDisposition = 1
	Pending  ConnectionDisposition = 2
	Locked   ConnectionDisposition = 3
)

func (d ConnectionDisposition) String() string {
	switch d {
	case Tally:
		return "tally"
	case Modified:
		return "modified"
	case Pending:
		return "pending"
	case Locked:
		return "locked"
	}
	return fmt.Sprintf("ConnectionDisposition(%d)", int32(d))
}
