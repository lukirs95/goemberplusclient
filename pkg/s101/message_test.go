package s101

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

type splitFrame struct {
	flags   Flags
	payload []byte
}

func split(t *testing.T, template Frame, payload []byte) []splitFrame {
	t.Helper()
	var out []splitFrame
	err := Split(template, payload, func(f *Frame) error {
		if f.Command != CommandEmBER || f.Slot != template.Slot || f.Message != template.Message ||
			f.Version != template.Version || f.DTD != template.DTD || !bytes.Equal(f.AppBytes, template.AppBytes) {
			t.Fatalf("header not taken from template: %+v", f)
		}
		out = append(out, splitFrame{f.Flags, append([]byte(nil), f.Payload...)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSplit(t *testing.T) {
	tests := []struct {
		name  string
		size  int
		flags []Flags
		sizes []int
	}{
		{"empty", 0, []Flags{FlagSingle | FlagEmpty}, []int{0}},
		{"small", 10, []Flags{FlagSingle}, []int{10}},
		{"exactly max", MaxPayloadSize, []Flags{FlagSingle}, []int{MaxPayloadSize}},
		{"max plus one", MaxPayloadSize + 1, []Flags{FlagFirst, FlagLast}, []int{MaxPayloadSize, 1}},
		{"three packets", 3000, []Flags{FlagFirst, 0, FlagLast}, []int{1024, 1024, 952}},
	}
	template := emberFrame(nil)
	template.Command = CommandKeepAliveRequest // must be overridden
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := pattern(tt.size)
			want := template
			want.Command = CommandEmBER
			frames := split(t, want, payload)
			if len(frames) != len(tt.flags) {
				t.Fatalf("got %d frames, want %d", len(frames), len(tt.flags))
			}
			var joined []byte
			for i, f := range frames {
				if f.flags != tt.flags[i] || len(f.payload) != tt.sizes[i] {
					t.Fatalf("frame %d: flags %#02x size %d, want %#02x size %d",
						i, f.flags, len(f.payload), tt.flags[i], tt.sizes[i])
				}
				joined = append(joined, f.payload...)
			}
			if !bytes.Equal(joined, payload) {
				t.Fatal("joined payload differs")
			}
		})
	}
}

func TestSplitForcesEmBERCommand(t *testing.T) {
	template := NewKeepAliveRequest()
	Split(template, []byte{1}, func(f *Frame) error {
		if f.Command != CommandEmBER {
			t.Fatalf("command = %#02x", f.Command)
		}
		return nil
	})
}

func TestSplitStopsOnError(t *testing.T) {
	stop := errors.New("stop")
	calls := 0
	err := Split(emberFrame(nil), pattern(3000), func(*Frame) error {
		calls++
		return stop
	})
	if err != stop || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func add(t *testing.T, a *Assembler, flags Flags, payload []byte) ([]byte, bool, error) {
	t.Helper()
	f := emberFrame(payload)
	f.Flags = flags
	return a.Add(&f)
}

func TestAssemblerSingle(t *testing.T) {
	var a Assembler
	got, done, err := add(t, &a, FlagSingle, []byte{1, 2, 3})
	if err != nil || !done || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("got % X, %v, %v", got, done, err)
	}
}

func TestAssemblerMulti(t *testing.T) {
	var a Assembler
	steps := []struct {
		flags   Flags
		payload []byte
	}{
		{FlagFirst, []byte{1, 2}},
		{0, []byte{3}},
		{FlagEmpty, nil},
		{0, []byte{4, 5}},
	}
	for _, s := range steps {
		if got, done, err := add(t, &a, s.flags, s.payload); err != nil || done || got != nil {
			t.Fatalf("flags %#02x: got % X, %v, %v", s.flags, got, done, err)
		}
	}
	got, done, err := add(t, &a, FlagLast, []byte{6})
	if err != nil || !done || !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("got % X, %v, %v", got, done, err)
	}
}

func TestAssemblerEmpty(t *testing.T) {
	var a Assembler
	got, done, err := add(t, &a, FlagSingle|FlagEmpty, []byte{0xAA})
	if err != nil || !done || len(got) != 0 {
		t.Fatalf("got % X, %v, %v", got, done, err)
	}
}

func TestAssemblerCopiesFramePayload(t *testing.T) {
	var a Assembler
	buf := []byte{1, 2}
	add(t, &a, FlagFirst, buf)
	buf[0], buf[1] = 9, 9 // decoder reuses its buffer
	got, _, _ := add(t, &a, FlagLast, buf)
	if !bytes.Equal(got, []byte{1, 2, 9, 9}) {
		t.Fatalf("got % X", got)
	}
}

func TestAssemblerErrors(t *testing.T) {
	t.Run("continuation without first", func(t *testing.T) {
		for _, flags := range []Flags{0, FlagLast, FlagEmpty} {
			var a Assembler
			if _, _, err := add(t, &a, flags, []byte{1}); err != ErrUnexpectedContinuation {
				t.Fatalf("flags %#02x: got %v", flags, err)
			}
		}
	})

	t.Run("first while in progress", func(t *testing.T) {
		for _, flags := range []Flags{FlagFirst, FlagSingle} {
			var a Assembler
			add(t, &a, FlagFirst, []byte{1})
			if _, _, err := add(t, &a, flags, []byte{2}); err != ErrUnexpectedFirst {
				t.Fatalf("flags %#02x: got %v", flags, err)
			}
			// Both messages are discarded; the next message starts fresh.
			if _, _, err := add(t, &a, FlagLast, []byte{3}); err != ErrUnexpectedContinuation {
				t.Fatalf("flags %#02x: got %v after discard", flags, err)
			}
			got, done, err := add(t, &a, FlagSingle, []byte{4})
			if err != nil || !done || !bytes.Equal(got, []byte{4}) {
				t.Fatalf("got % X, %v, %v", got, done, err)
			}
		}
	})

	t.Run("non-EmBER frame", func(t *testing.T) {
		var a Assembler
		add(t, &a, FlagFirst, []byte{1})
		ka := NewKeepAliveRequest()
		if _, _, err := a.Add(&ka); err != ErrInvalidFrame {
			t.Fatalf("got %v", err)
		}
		got, done, err := add(t, &a, FlagLast, []byte{2})
		if err != nil || !done || !bytes.Equal(got, []byte{1, 2}) {
			t.Fatalf("keep-alive disturbed message: % X, %v, %v", got, done, err)
		}
	})
}

func TestAssemblerReset(t *testing.T) {
	var a Assembler
	add(t, &a, FlagFirst, []byte{1})
	a.Reset()
	got, done, err := add(t, &a, FlagSingle, []byte{2})
	if err != nil || !done || !bytes.Equal(got, []byte{2}) {
		t.Fatalf("got % X, %v, %v", got, done, err)
	}
}

func TestAssemblerReusesBuffer(t *testing.T) {
	var a Assembler
	frames := []Frame{emberFrame(pattern(1024)), emberFrame(pattern(1024)), emberFrame(pattern(100))}
	frames[0].Flags, frames[1].Flags, frames[2].Flags = FlagFirst, 0, FlagLast
	run := func() {
		for i := range frames {
			a.Add(&frames[i])
		}
	}
	run()
	if n := testing.AllocsPerRun(100, run); n != 0 {
		t.Errorf("%v allocs per message", n)
	}
}

// Split -> Encoder -> Decoder -> Assembler, with keep-alives interleaved.
func TestMessageRoundTrip(t *testing.T) {
	messages := [][]byte{pattern(5000), {}, pattern(1), pattern(MaxPayloadSize)}
	for _, v := range []Variant{VariantEscaping, VariantNonEscaping} {
		var wire bytes.Buffer
		enc := NewEncoder(&wire)
		enc.SetVariant(v)
		ka := NewKeepAliveRequest()
		for _, m := range messages {
			if err := Split(emberFrame(nil), m, enc.WriteFrame); err != nil {
				t.Fatal(err)
			}
			enc.WriteFrame(&ka)
		}

		dec := NewDecoder(&wire)
		var a Assembler
		var f Frame
		var got [][]byte
		for {
			err := dec.ReadFrame(&f)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.Command != CommandEmBER {
				continue
			}
			payload, done, err := a.Add(&f)
			if err != nil {
				t.Fatal(err)
			}
			if done {
				got = append(got, append([]byte{}, payload...))
			}
		}
		if len(got) != len(messages) {
			t.Fatalf("variant %d: got %d messages, want %d", v, len(got), len(messages))
		}
		for i := range messages {
			if !bytes.Equal(got[i], messages[i]) {
				t.Fatalf("variant %d: message %d differs", v, i)
			}
		}
	}
}
