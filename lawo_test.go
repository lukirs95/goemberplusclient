package emberplus

import (
	"bytes"
	"os"
	"slices"
	"testing"

	"github.com/lukirs95/goemberplusclient/pkg/glow"
	"github.com/lukirs95/goemberplusclient/pkg/s101"
)

// The Lawo testdata hold a session between Lawo's Ember+ consumer and a Lawo
// GP-I/O-Box. The replay test answers the client's requests with the
// recorded provider frames and checks that the client sends the same
// requests as Lawo's consumer did.

// lawoResponseSizes is the number of provider messages answering each of
// the 14 recorded requests: the matrix GetDirectory is answered with one
// message per connection.
var lawoResponseSizes = []int{1, 1, 1, 64, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("pkg/glow/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// lawoResponses groups the raw provider frames by the request they answer.
func lawoResponses(t *testing.T) [][]byte {
	t.Helper()
	var messages [][]byte
	var current []byte
	for _, raw := range splitFrames(readTestdata(t, "lawo-gpio-box.provider.s101")) {
		f, err := decodeFrame(raw)
		if err != nil {
			t.Fatal(err)
		}
		if f.Command != s101.CommandEmBER {
			continue
		}
		current = append(current, raw...)
		if f.Flags&s101.FlagLast != 0 {
			messages = append(messages, current)
			current = nil
		}
	}
	var groups [][]byte
	for _, n := range lawoResponseSizes {
		if len(messages) < n {
			t.Fatalf("only %d messages left, need %d", len(messages), n)
		}
		groups = append(groups, bytes.Join(messages[:n], nil))
		messages = messages[n:]
	}
	if len(messages) != 0 {
		t.Fatalf("%d messages not assigned to a request", len(messages))
	}
	return groups
}

// lawoRequests returns the requests recorded from Lawo's consumer.
func lawoRequests(t *testing.T) []request {
	t.Helper()
	dec := s101.NewDecoder(bytes.NewReader(readTestdata(t, "lawo-gpio-box.consumer.s101")))
	var a s101.Assembler
	var f s101.Frame
	var reqs []request
	for dec.ReadFrame(&f) == nil {
		if f.Command != s101.CommandEmBER {
			continue
		}
		payload, done, err := a.Add(&f)
		if err != nil {
			t.Fatal(err)
		}
		if !done || len(payload) == 0 {
			continue
		}
		r, err := parseRequests(payload)
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, r...)
	}
	return reqs
}

func TestLawoReplay(t *testing.T) {
	responses := lawoResponses(t)
	recorded := lawoRequests(t)
	if len(recorded) != len(responses) {
		t.Fatalf("%d recorded requests, %d responses", len(recorded), len(responses))
	}

	_, c := startFake(t, func(p *fakeProvider, i int, r request) {
		if i >= len(recorded) {
			t.Errorf("unexpected request %d: %+v", i, r)
			return
		}
		want := recorded[i]
		if r.tag != want.tag || !slices.Equal(r.path, want.path) || r.number != want.number ||
			r.hasMask != want.hasMask || r.mask != want.mask {
			t.Errorf("request %d: got %+v, Lawo sent %+v", i, r, want)
		}
		p.sendRaw(responses[i])
	})
	ctx := testContext(t)

	// The same browsing sequence as Lawo's consumer, using identifier paths.
	for _, path := range []string{"", "root", "root/gpio", "root/gpio/inputs", "root/settings", "root/settings/info"} {
		if _, err := c.GetDirectory(ctx, path); err != nil {
			t.Fatalf("GetDirectory %q: %v", path, err)
		}
	}
	params := []string{"moniker", "version", "temperature", "hwRev", "hwID", "swID", "uartMode", "osVersion"}
	for _, id := range params {
		if _, err := c.GetDirectory(ctx, "root/settings/info/"+id); err != nil {
			t.Fatalf("GetDirectory %q: %v", id, err)
		}
	}

	root, err := c.Get(ctx, "root")
	if err != nil || *root.(*glow.Node).Description != "GP-I/O-Box" {
		t.Fatalf("root: %+v, %v", root, err)
	}
	el, err := c.Get(ctx, "root/gpio/inputs")
	if err != nil {
		t.Fatal(err)
	}
	m := el.(*glow.Matrix)
	if *m.Description != "GPIs" || *m.TargetCount != 64 || len(m.Connections) != 64 {
		t.Fatalf("matrix: %+v", m)
	}
	el, err = c.Get(ctx, "root/settings/info/version")
	if err != nil || el.(*glow.Parameter).Value != glow.String("02.46") {
		t.Fatalf("version: %+v, %v", el, err)
	}
	if id, err := c.IdentifierPath(ctx, "1.100.3.3"); err != nil || id != "root/settings/info/temperature" {
		t.Fatalf("IdentifierPath: %q, %v", id, err)
	}
	children, err := c.Children(ctx, "root/settings/info")
	if err != nil || len(children) != len(params) {
		t.Fatalf("children: %d, %v", len(children), err)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}
