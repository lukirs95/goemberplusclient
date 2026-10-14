package s101

import "errors"

var (
	ErrUnexpectedFirst        = errors.New("s101: first packet while message in progress")
	ErrUnexpectedContinuation = errors.New("s101: continuation without first packet")
)

// Assembler joins the payloads of multi-packet EmBER messages (spec: "S101
// Messages").
// It is not safe for concurrent use.
type Assembler struct {
	buf        []byte
	inProgress bool
}

// Add processes an EmBER frame. When f completes a message, done is true and
// payload holds the joined payload, valid until the next call to Add. The
// payload of f is copied, so f may point into a reused decoder buffer.
//
// Frames with FlagEmpty contribute no payload. On ErrUnexpectedFirst, both
// the incomplete message and f are discarded. A non-EmBER frame returns
// ErrInvalidFrame and leaves the state untouched.
func (a *Assembler) Add(f *Frame) (payload []byte, done bool, err error) {
	if f.Command != CommandEmBER {
		return nil, false, ErrInvalidFrame
	}
	first := f.Flags&FlagFirst != 0
	if first && a.inProgress {
		a.Reset()
		return nil, false, ErrUnexpectedFirst
	}
	if !first && !a.inProgress {
		return nil, false, ErrUnexpectedContinuation
	}
	if first {
		a.buf = a.buf[:0]
		a.inProgress = true
	}
	if f.Flags&FlagEmpty == 0 {
		a.buf = append(a.buf, f.Payload...)
	}
	if f.Flags&FlagLast == 0 {
		return nil, false, nil
	}
	a.inProgress = false
	return a.buf, true, nil
}

// Reset discards an incomplete message.
func (a *Assembler) Reset() {
	a.buf = a.buf[:0]
	a.inProgress = false
}

// Split divides payload into EmBER frames of at most MaxPayloadSize bytes and
// calls fn for each of them in order. Slot, Message, Version, DTD and
// AppBytes are taken from template; Command, Flags and Payload are set by
// Split. An empty payload yields a single frame with FlagEmpty. fn receives
// the same *Frame on every call and must not retain it. Split stops at the
// first error returned by fn.
func Split(template Frame, payload []byte, fn func(*Frame) error) error {
	f := template
	f.Command = CommandEmBER
	if len(payload) == 0 {
		f.Flags = FlagSingle | FlagEmpty
		f.Payload = nil
		return fn(&f)
	}
	for off := 0; off < len(payload); off += MaxPayloadSize {
		end := min(off+MaxPayloadSize, len(payload))
		f.Flags = 0
		if off == 0 {
			f.Flags |= FlagFirst
		}
		if end == len(payload) {
			f.Flags |= FlagLast
		}
		f.Payload = payload[off:end]
		if err := fn(&f); err != nil {
			return err
		}
	}
	return nil
}
