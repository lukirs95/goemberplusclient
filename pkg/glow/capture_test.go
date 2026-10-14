package glow_test

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/lukirs95/goemberplusclient/pkg/ber"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
	"github.com/lukirs95/goemberplusclient/pkg/s101"
)

// The testdata files hold both directions of a TCP session between Lawo's
// Ember+ consumer and a Lawo GP-I/O-Box, extracted from a network capture.

type capture struct {
	messages   [][]byte
	keepAlives []s101.Frame
	multi      int // messages spanning more than one packet
}

func readCapture(t *testing.T, name string) capture {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var c capture
	dec := s101.NewDecoder(bytes.NewReader(data))
	var a s101.Assembler
	var f s101.Frame
	packets := 0
	for {
		err := dec.ReadFrame(&f)
		if err == io.EOF {
			return c
		}
		if err != nil {
			t.Fatalf("frame: %v", err)
		}
		if f.Command != s101.CommandEmBER {
			f.Payload = nil
			c.keepAlives = append(c.keepAlives, f)
			continue
		}
		packets++
		payload, done, err := a.Add(&f)
		if err != nil {
			t.Fatalf("assembler: %v", err)
		}
		if done {
			if packets > 1 && len(payload) > 0 && f.Flags&s101.FlagFirst == 0 {
				c.multi++
			}
			packets = 0
			if len(payload) > 0 {
				c.messages = append(c.messages, bytes.Clone(payload))
			}
		}
	}
}

func TestCaptureConsumer(t *testing.T) {
	c := readCapture(t, "lawo-gpio-box.consumer.s101")
	if len(c.keepAlives) != 1 || c.keepAlives[0].Command != s101.CommandKeepAliveRequest {
		t.Fatalf("keep-alives: %+v", c.keepAlives)
	}
	if len(c.messages) != 14 {
		t.Fatalf("got %d messages", len(c.messages))
	}
	for i, m := range c.messages {
		if _, err := glow.Decode(m); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}

	// Lawo's consumer sends GetDirectory with indefinite lengths and
	// dirFieldMask=all. Decoding the root command must yield no elements.
	msg, _ := glow.Decode(c.messages[0])
	if els := msg.(glow.Elements); len(els) != 0 {
		t.Fatalf("root GetDirectory decoded to %+v", els)
	}
	if !bytes.Contains(c.messages[0], []byte{0xA1, 0x03, 0x02, 0x01, 0xFF}) {
		t.Fatal("expected dirFieldMask=all in the root GetDirectory")
	}
}

func TestCaptureProvider(t *testing.T) {
	c := readCapture(t, "lawo-gpio-box.provider.s101")

	// The keep-alive response uses slot 0xFF although the request used 0x00.
	if len(c.keepAlives) != 1 || c.keepAlives[0].Command != s101.CommandKeepAliveResponse || c.keepAlives[0].Slot != 0xFF {
		t.Fatalf("keep-alives: %+v", c.keepAlives)
	}
	if c.multi != 2 {
		t.Fatalf("got %d multi-packet messages", c.multi)
	}

	nodes := map[string]*glow.Node{}
	params := map[string]*glow.Parameter{}
	matrices := map[string]*glow.Matrix{}
	connectionMessages := 0
	for i, m := range c.messages {
		msg, err := glow.Decode(m)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		els := msg.(glow.Elements)
		if len(els) == 0 {
			t.Fatalf("message %d: no elements", i)
		}
		for _, el := range els {
			key := el.ElementPath().String()
			switch e := el.(type) {
			case *glow.Node:
				if nodes[key] == nil {
					nodes[key] = &glow.Node{Path: e.Path}
				}
				nodes[key].Merge(e)
			case *glow.Parameter:
				if params[key] == nil {
					params[key] = &glow.Parameter{Path: e.Path}
				}
				params[key].Merge(e)
			case *glow.Matrix:
				if matrices[key] == nil {
					matrices[key] = &glow.Matrix{Path: e.Path}
				}
				matrices[key].Merge(e)
				if e.Connections != nil {
					connectionMessages++
				}
			}
		}
		assertReencodes(t, els)
	}

	for path, want := range map[string][2]string{
		"1":       {"root", "GP-I/O-Box"},
		"1.1":     {"gpio", "GP-I/O"},
		"1.100":   {"settings", "Main Menu"},
		"1.100.3": {"info", "Info"},
	} {
		n := nodes[path]
		if n == nil || *n.Identifier != want[0] || *n.Description != want[1] {
			t.Errorf("node %s: %+v", path, n)
		}
	}
	if len(params) != 8 {
		t.Errorf("got %d parameters", len(params))
	}
	if p := params["1.100.3.2"]; p == nil || *p.Identifier != "version" || p.Value != glow.String("02.46") {
		t.Errorf("version: %+v", p)
	}
	// The device reports streamIdentifier 0 together with a string value,
	// which the specification does not intend for streams ("StreamIdentifier").
	if p := params["1.100.3.3"]; p == nil || p.StreamIdentifier == nil || *p.StreamIdentifier != 0 || p.Value != glow.String("48.56°C") {
		t.Errorf("temperature: %+v", p)
	}

	// Lawo reports the matrix contents with the parent's GetDirectory and
	// then every connection in a message of its own.
	m := matrices["1.1.1"]
	if m == nil || *m.Identifier != "inputs" || *m.Description != "GPIs" || *m.TargetCount != 64 || *m.SourceCount != 1 {
		t.Fatalf("matrix: %+v", m)
	}
	if connectionMessages != 64 || len(m.Connections) != 64 {
		t.Fatalf("got %d connection messages, %d connections", connectionMessages, len(m.Connections))
	}
	for i, conn := range m.Connections {
		if conn.Target != uint32(i) || conn.Sources != nil {
			t.Fatalf("connection %d: %+v", i, conn)
		}
	}
}

func assertReencodes(t *testing.T, els glow.Elements) {
	t.Helper()
	var b ber.Builder
	if err := glow.Encode(&b, els...); err != nil {
		t.Fatal(err)
	}
	data, _ := b.Bytes()
	msg, err := glow.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	// HasChildren is a property of the original message and is not encoded.
	want := slices.Clone(els)
	for i, el := range want {
		if n, ok := el.(*glow.Node); ok && n.HasChildren {
			c := n.Clone()
			c.HasChildren = false
			want[i] = c
		}
	}
	if !reflect.DeepEqual(msg, want) {
		t.Fatalf("re-encoded %+v\nwant %+v", msg, want)
	}
}
