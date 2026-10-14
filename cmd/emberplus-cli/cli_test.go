package main

import (
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	emberplus "github.com/lukirs95/goemberplusclient"
	"github.com/lukirs95/goemberplusclient/pkg/ber"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
	"github.com/lukirs95/goemberplusclient/pkg/s101"
)

// testTree:
//
//	1 root "Device"
//	├── 1 settings "Settings"
//	│   ├── 1 gain   -6, format "%.1f°dB", -128 … 15, values above 10 are rejected
//	│   ├── 2 mode   enum Off/On/~Debug/Auto, value On
//	│   ├── 3 level  stream 3
//	│   ├── 4 mute   boolean
//	│   ├── 5 serial read-only string
//	│   ├── 6 delay  integer with factor 10
//	│   └── 7 reboot trigger
//	├── 2 router     matrix
//	├── 3 reset      function (ch) → (ok), fails for ch 99
//	└── 4 save       function without arguments
var testTree = []glow.Element{
	&glow.Node{Path: glow.Path{1}, Identifier: new("root"), Description: new("Device")},
	&glow.Node{Path: glow.Path{1, 1}, Identifier: new("settings"), Description: new("Settings")},
	&glow.Parameter{Path: glow.Path{1, 1, 1}, Identifier: new("gain"), Value: glow.Real(-6), Format: new("%.1f°dB"),
		Minimum: glow.Real(-128), Maximum: glow.Real(15), Access: new(glow.AccessReadWrite)},
	&glow.Parameter{Path: glow.Path{1, 1, 2}, Identifier: new("mode"), Value: glow.Int(1), Enumeration: []string{"Off", "On", "~Debug", "Auto"}},
	&glow.Parameter{Path: glow.Path{1, 1, 3}, Identifier: new("level"), StreamIdentifier: new(int32(3))},
	&glow.Parameter{Path: glow.Path{1, 1, 4}, Identifier: new("mute"), Value: glow.Bool(false)},
	&glow.Parameter{Path: glow.Path{1, 1, 5}, Identifier: new("serial"), Value: glow.String("A1"), Access: new(glow.AccessRead)},
	&glow.Parameter{Path: glow.Path{1, 1, 6}, Identifier: new("delay"), Value: glow.Int(150), Factor: new(int32(10))},
	&glow.Parameter{Path: glow.Path{1, 1, 7}, Identifier: new("reboot"), Type: new(glow.TypeTrigger)},
	&glow.Matrix{Path: glow.Path{1, 2}, Identifier: new("router"), TargetCount: new(int32(2)), SourceCount: new(int32(2)),
		Connections: []glow.Connection{{Target: 0, Sources: []uint32{1}}, {Target: 1}}},
	&glow.Function{Path: glow.Path{1, 3}, Identifier: new("reset"), Arguments: []glow.TupleItem{{Type: glow.TypeInteger, Name: "ch"}},
		Result: []glow.TupleItem{{Type: glow.TypeBoolean, Name: "ok"}}},
	&glow.Function{Path: glow.Path{1, 4}, Identifier: new("save")},
}

type command struct {
	path   glow.Path
	number glow.CommandType
	id     int32
	args   []glow.Value
	value  glow.Value // value of a SetValue request
}

// provider answers GetDirectory from testTree and records all commands.
type provider struct {
	conn net.Conn
	wmu  sync.Mutex
	enc  *s101.Encoder

	mu       sync.Mutex
	commands []command
}

func (p *provider) recorded() []command {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.commands)
}

func (p *provider) send(els ...glow.Element) {
	var b ber.Builder
	glow.Encode(&b, els...)
	payload, _ := b.Bytes()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	s101.Split(s101.Frame{Message: s101.MessageEmBER, Version: s101.VersionS101, DTD: s101.DTDGlow}, payload, p.enc.WriteFrame)
}

func (p *provider) sendResult(id int32, ok, withResult bool) {
	var b ber.Builder
	b.Constructed(ber.Application(0), func(b *ber.Builder) {
		b.Constructed(ber.Application(23), func(b *ber.Builder) {
			b.Constructed(ber.Context(0), func(b *ber.Builder) { b.Int(int64(id)) })
			b.Constructed(ber.Context(1), func(b *ber.Builder) { b.Bool(ok) })
			if withResult {
				b.Constructed(ber.Context(2), func(b *ber.Builder) {
					b.Constructed(ber.TagSequence, func(b *ber.Builder) {
						b.Constructed(ber.Context(0), func(b *ber.Builder) { b.Bool(ok) })
					})
				})
			}
		})
	})
	payload, _ := b.Bytes()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	s101.Split(s101.Frame{Message: s101.MessageEmBER, Version: s101.VersionS101, DTD: s101.DTDGlow}, payload, p.enc.WriteFrame)
}

func (p *provider) serve(requests <-chan command) {
	for r := range requests {
		p.mu.Lock()
		p.commands = append(p.commands, r)
		p.mu.Unlock()
		switch r.number {
		case glow.CmdInvoke:
			// Functions fail for channel 99.
			// Only reset returns a result.
			p.sendResult(r.id, !slices.Equal(r.args, []glow.Value{glow.Int(99)}), slices.Equal(r.path, glow.Path{1, 3}))
			continue
		case 0:
			// Values above 10 are rejected: the provider keeps -6.
			v := r.value
			if real, ok := v.(glow.Real); ok && real > 10 {
				v = glow.Real(-6)
			}
			p.send(&glow.Parameter{Path: r.path, Value: v})
			continue
		case glow.CmdGetDirectory:
		default:
			continue
		}
		var answer []glow.Element
		for _, el := range testTree {
			if slices.Equal(el.ElementPath().Parent(), r.path) {
				answer = append(answer, el)
			}
		}
		if len(answer) == 0 {
			for _, el := range testTree {
				if slices.Equal(el.ElementPath(), r.path) {
					answer = append(answer, el)
				}
			}
		}
		p.send(answer...)
	}
}

func (p *provider) read(requests chan<- command) {
	defer close(requests)
	dec := s101.NewDecoder(p.conn)
	var a s101.Assembler
	var f s101.Frame
	for dec.ReadFrame(&f) == nil {
		payload, done, err := a.Add(&f)
		if err != nil || !done {
			continue
		}
		cmds := parseCommands(payload)
		if len(cmds) == 0 {
			// A request without command is a value change.
			if msg, err := glow.Decode(payload); err == nil {
				for _, el := range msg.(glow.Elements) {
					if par, ok := el.(*glow.Parameter); ok {
						cmds = append(cmds, command{path: par.Path, value: par.Value})
					}
				}
			}
		}
		for _, c := range cmds {
			requests <- c
		}
	}
}

// parseCommands extracts the commands of a request message.
func parseCommands(payload []byte) []command {
	var cmds []command
	r := ber.NewReader(payload)
	root, _ := r.Next()
	coll, _ := root.Explicit()
	entries, _ := coll.Children()
	for entries.More() {
		e, _ := entries.Next()
		el, _ := e.Explicit()
		var c command
		var cmd ber.TLV
		if el.Tag == ber.Application(2) {
			cmd = el
		} else {
			fields, _ := el.Children()
			for fields.More() {
				f, _ := fields.Next()
				v, _ := f.Explicit()
				switch f.Tag {
				case ber.Context(0):
					oid, _ := v.RelativeOID(nil)
					c.path = glow.Path(oid)
				case ber.Context(2):
					list, _ := v.Children()
					item, _ := list.Next()
					cmd, _ = item.Explicit()
				}
			}
		}
		if cmd.Tag != ber.Application(2) {
			continue
		}
		fields, _ := cmd.Children()
		for fields.More() {
			f, _ := fields.Next()
			v, _ := f.Explicit()
			switch f.Tag {
			case ber.Context(0):
				n, _ := v.Int()
				c.number = glow.CommandType(n)
			case ber.Context(2): // invocation
				inv, _ := v.Children()
				for inv.More() {
					x, _ := inv.Next()
					y, _ := x.Explicit()
					switch x.Tag {
					case ber.Context(0):
						c.id, _ = y.Int32()
					case ber.Context(1):
						values, _ := y.Children()
						for values.More() {
							e, _ := values.Next()
							val, _ := e.Explicit()
							n, _ := val.Int()
							c.args = append(c.args, glow.Int(n))
						}
					}
				}
			}
		}
		cmds = append(cmds, c)
	}
	return cmds
}

// start connects a model to the test provider. Watch events are delivered
// to the returned channel instead of a running program.
func start(t *testing.T) (model, *provider, chan tea.Msg) {
	t.Helper()
	server, client := net.Pipe()
	p := &provider{conn: server, enc: s101.NewEncoder(server)}
	requests := make(chan command, 100)
	go p.read(requests)
	go p.serve(requests)

	c := emberplus.NewClient(client, emberplus.WithKeepAlive(0, 0))
	t.Cleanup(func() {
		c.Close()
		server.Close()
	})
	events := make(chan tea.Msg, 100)
	a := &app{client: c, addr: "test:9000", timeout: 2 * time.Second, send: func(msg tea.Msg) { events <- msg }}
	m := newModel(a)
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	return drive(t, m, m.loadChildren("")), p, events
}

// drive runs cmd and feeds the resulting messages back into the model, as
// the Bubble Tea runtime would.
func drive(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, c := range msg {
			m = drive(t, m, c)
		}
		return m
	default:
		return update(t, m, msg)
	}
}

func update(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, cmd := m.Update(msg)
	return drive(t, next.(model), cmd)
}

func press(t *testing.T, m model, keys ...string) model {
	t.Helper()
	special := map[string]tea.KeyType{"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "ctrl+u": tea.KeyCtrlU}
	for _, k := range keys {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		if kt, ok := special[k]; ok {
			msg = tea.KeyMsg{Type: kt}
		}
		m = update(t, m, msg)
	}
	return m
}

func selectedLabel(m model) string {
	return m.label(m.selected())
}

func TestNavigation(t *testing.T) {
	m, _, _ := start(t)
	if len(m.rows) != 1 || selectedLabel(m) != "Device" {
		t.Fatalf("initial rows %d, selected %q", len(m.rows), selectedLabel(m))
	}

	m = press(t, m, "l") // open root
	if len(m.rows) != 5 {
		t.Fatalf("rows after opening root: %d", len(m.rows))
	}
	m = press(t, m, "j", "right") // open settings
	if selectedLabel(m) != "Settings" || len(m.rows) != 12 {
		t.Fatalf("selected %q, rows %d", selectedLabel(m), len(m.rows))
	}
	m = press(t, m, "l") // move into settings
	if selectedLabel(m) != "gain" {
		t.Fatalf("first child: %q", selectedLabel(m))
	}
	m = press(t, m, "j", "down")
	if selectedLabel(m) != "level" {
		t.Fatalf("after j, down: %q", selectedLabel(m))
	}
	m = press(t, m, "h") // to parent
	if selectedLabel(m) != "Settings" {
		t.Fatalf("parent: %q", selectedLabel(m))
	}
	m = press(t, m, "left") // close settings
	if len(m.rows) != 5 || selectedLabel(m) != "Settings" {
		t.Fatalf("after closing: rows %d, selected %q", len(m.rows), selectedLabel(m))
	}
	m = press(t, m, "k", "k")
	if selectedLabel(m) != "Device" || m.cursor != 0 {
		t.Fatalf("top: %q", selectedLabel(m))
	}
	m = press(t, m, "G")
	if selectedLabel(m) != "save" {
		t.Fatalf("end: %q", selectedLabel(m))
	}
}

func TestReopenUsesMirror(t *testing.T) {
	m, p, _ := start(t)
	m = press(t, m, "l", "j", "l", "h", "l")
	var dirs int
	for _, c := range p.recorded() {
		if c.number == glow.CmdGetDirectory {
			dirs++
		}
	}
	// Root directory, node 1 and node 1.1; reopening loads nothing.
	if dirs != 3 {
		t.Fatalf("%d GetDirectory requests", dirs)
	}
	if len(m.rows) != 12 {
		t.Fatalf("rows %d", len(m.rows))
	}
}

func TestView(t *testing.T) {
	m, _, _ := start(t)
	m = press(t, m, "l", "j", "l", "l")
	view := m.View()
	for _, want := range []string{"Device", "Settings", "-6.0 dB", "On", "≈ level", "▦ router", "ƒ reset", "Path: root/settings/gain"} {
		if !strings.Contains(view, want) {
			t.Errorf("view does not contain %q:\n%s", want, view)
		}
	}
	m = press(t, m, "G", "k")
	if view := m.View(); !strings.Contains(view, "reset(ch integer) → (ok boolean)") {
		t.Errorf("function signature missing:\n%s", view)
	}
}

func TestLiveUpdate(t *testing.T) {
	m, p, events := start(t)
	m = press(t, m, "l", "j", "l")
	p.send(&glow.Parameter{Path: glow.Path{1, 1, 1}, Value: glow.Real(-3)})
	select {
	case msg := <-events:
		m = update(t, m, msg)
	case <-time.After(2 * time.Second):
		t.Fatal("no watch event")
	}
	if !strings.Contains(m.View(), "-3.0 dB") {
		t.Fatalf("value not updated:\n%s", m.View())
	}
}

func TestSubscribe(t *testing.T) {
	m, p, _ := start(t)
	m = press(t, m, "l", "j", "l", "l") // gain selected
	m = press(t, m, "s")
	if m.status != "not a stream parameter" {
		t.Fatalf("status %q", m.status)
	}

	streamCommands := func() []glow.CommandType {
		var cmds []glow.CommandType
		for _, c := range p.recorded() {
			if c.number == glow.CmdSubscribe || c.number == glow.CmdUnsubscribe {
				if !slices.Equal(c.path, glow.Path{1, 1, 3}) {
					t.Fatalf("command on %v", c.path)
				}
				cmds = append(cmds, c.number)
			}
		}
		return cmds
	}
	if len(streamCommands()) != 0 {
		t.Fatal("subscribed a normal parameter")
	}

	m = press(t, m, "j", "j", "s") // level
	if !strings.HasPrefix(m.status, "subscribed") || !strings.Contains(m.View(), "≋ level") {
		t.Fatalf("status %q", m.status)
	}
	m = press(t, m, "s")
	deadline := time.Now().Add(2 * time.Second)
	for len(streamCommands()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := streamCommands(); !slices.Equal(got, []glow.CommandType{glow.CmdSubscribe, glow.CmdUnsubscribe}) {
		t.Fatalf("commands %v", got)
	}
}

func TestDisconnect(t *testing.T) {
	m, p, _ := start(t)
	p.conn.Close()
	done := make(chan tea.Msg, 1)
	go func() { done <- m.waitDisconnect()() }()
	select {
	case msg := <-done:
		m = update(t, m, msg)
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect not detected")
	}
	if !strings.Contains(m.View(), "connection closed") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		p    glow.Parameter
		want string
	}{
		{glow.Parameter{Value: glow.Real(-6), Format: new("%.2f°dB")}, "-6.00 dB"},
		{glow.Parameter{Value: glow.Int(5), Format: new("%d Units")}, "5 Units"},
		{glow.Parameter{Value: glow.Int(255), Format: new("0x%x")}, "0xff"},
		{glow.Parameter{Value: glow.Int(-60), Factor: new(int32(10)), Format: new("%.1f°dB")}, "-6.0 dB"},
		{glow.Parameter{Value: glow.Int(-65), Factor: new(int32(10))}, "-6.5"},
		{glow.Parameter{Value: glow.Real(1.5), Format: new("%s")}, "1.5"},
		{glow.Parameter{Value: glow.Int(1), Enumeration: []string{"Off", "~Debug"}}, "Debug"},
		{glow.Parameter{Value: glow.Int(20), EnumMap: []glow.EnumMapEntry{{Name: "High", Value: 20}}}, "High"},
		{glow.Parameter{Type: new(glow.TypeTrigger)}, "trigger"},
		{glow.Parameter{Value: glow.String("box")}, `"box"`},
		{glow.Parameter{Value: glow.Null{}}, "null"},
		{glow.Parameter{StreamIdentifier: new(int32(1))}, "—"},
	}
	for _, tt := range tests {
		if got := formatValue(&tt.p); got != tt.want {
			t.Errorf("%+v: got %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestAddress(t *testing.T) {
	for args, want := range map[string]string{
		"10.0.0.5:9000":    "10.0.0.5:9000",
		"10.0.0.5 9000":    "10.0.0.5:9000",
		"fe80::1 9000":     "[fe80::1]:9000",
		"device.local 900": "device.local:900",
	} {
		if got, err := address(strings.Fields(args)); err != nil || got != want {
			t.Errorf("%q: got %q, %v", args, got, err)
		}
	}
	for _, args := range []string{"", "10.0.0.5", "a b c"} {
		if _, err := address(strings.Fields(args)); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

// selectPath opens the tree and selects the element with the given label.
func selectPath(t *testing.T, m model, label string) model {
	t.Helper()
	m = press(t, m, "l", "j", "l") // open root and settings
	for i, r := range m.rows {
		if m.label(r) == label {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("%q not found", label)
	return m
}

func lastCommand(p *provider) command {
	cmds := p.recorded()
	return cmds[len(cmds)-1]
}

func TestEditReal(t *testing.T) {
	m, p, _ := start(t)
	m = selectPath(t, m, "gain")
	m = press(t, m, "e")
	if m.mode != modeInput || m.edit.input.Value() != "-6" {
		t.Fatalf("mode %v, input %q", m.mode, m.edit.input.Value())
	}
	m = press(t, m, "ctrl+u", "-", "3", ".", "5", "enter")
	if m.mode != modeBrowse || m.status != "gain set to -3.5 dB" {
		t.Fatalf("mode %v, status %q", m.mode, m.status)
	}
	if c := lastCommand(p); !slices.Equal(c.path, glow.Path{1, 1, 1}) || c.value != glow.Real(-3.5) {
		t.Fatalf("request %+v", c)
	}
	if !strings.Contains(m.View(), "gain  -3.5 dB") {
		t.Fatalf("view not updated:\n%s", m.View())
	}

	// Rejected by the provider.
	m = press(t, m, "e", "ctrl+u", "1", "2", "enter")
	if m.status != "the provider kept gain at -6.0 dB" {
		t.Fatalf("status %q", m.status)
	}
}

func TestEditValidation(t *testing.T) {
	m, p, _ := start(t)
	m = selectPath(t, m, "gain")
	n := len(p.recorded())

	m = press(t, m, "e", "ctrl+u", "2", "0", "enter")
	if m.mode != modeInput || m.status != "the maximum is 15" {
		t.Fatalf("out of range: mode %v, status %q", m.mode, m.status)
	}
	m = press(t, m, "ctrl+u", "a", "b", "c", "enter")
	if m.mode != modeInput || m.status != `"abc" is not a number` {
		t.Fatalf("invalid: mode %v, status %q", m.mode, m.status)
	}
	m = press(t, m, "esc")
	if m.mode != modeBrowse || m.status != "cancelled" {
		t.Fatalf("esc: mode %v, status %q", m.mode, m.status)
	}

	m = selectPath(t, m, "serial")
	m = press(t, m, "e")
	if m.mode != modeBrowse || m.status != "serial is read-only" {
		t.Fatalf("read-only: mode %v, status %q", m.mode, m.status)
	}
	if len(p.recorded()) != n {
		t.Fatal("request sent for an invalid edit")
	}
}

func TestEditFactor(t *testing.T) {
	m, p, _ := start(t)
	m = selectPath(t, m, "delay")
	m = press(t, m, "e")
	if m.edit.input.Value() != "15" {
		t.Fatalf("input %q", m.edit.input.Value())
	}
	m = press(t, m, "ctrl+u", "2", ".", "5", "enter")
	if c := lastCommand(p); c.value != glow.Int(25) {
		t.Fatalf("request %+v", c)
	}
	if m.status != "delay set to 2.5" {
		t.Fatalf("status %q", m.status)
	}
}

func TestEditEnum(t *testing.T) {
	m, p, _ := start(t)
	m = selectPath(t, m, "mode")
	m = press(t, m, "enter")
	if m.mode != modeEnum || m.edit.choice != 1 {
		t.Fatalf("mode %v, choice %d", m.mode, m.edit.choice)
	}
	// The hidden entry "~Debug" is not offered.
	if names := []string{m.edit.entries[0].Name, m.edit.entries[1].Name, m.edit.entries[2].Name}; !slices.Equal(names, []string{"Off", "On", "Auto"}) || len(m.edit.entries) != 3 {
		t.Fatalf("entries %+v", m.edit.entries)
	}
	if !strings.Contains(m.View(), "› On") {
		t.Fatalf("enum list not shown:\n%s", m.View())
	}
	m = press(t, m, "j", "j", "enter")
	if c := lastCommand(p); c.value != glow.Int(3) {
		t.Fatalf("request %+v", c)
	}
	if m.status != "mode set to Auto" {
		t.Fatalf("status %q", m.status)
	}
}

func TestToggleBoolean(t *testing.T) {
	m, p, _ := start(t)
	m = selectPath(t, m, "mute")
	m = press(t, m, "e")
	if c := lastCommand(p); c.value != glow.Bool(true) {
		t.Fatalf("request %+v", c)
	}
	if m.status != "mute set to true" {
		t.Fatalf("status %q", m.status)
	}
}

func TestTrigger(t *testing.T) {
	m, p, _ := start(t)
	m = selectPath(t, m, "reboot")
	n := len(p.recorded())
	m = press(t, m, "e", "n")
	if len(p.recorded()) != n || m.status != "cancelled" {
		t.Fatalf("declined trigger: status %q", m.status)
	}
	m = press(t, m, "e")
	if !strings.Contains(m.View(), "fire trigger reboot? [y/N]") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
	m = press(t, m, "y")
	if c := lastCommand(p); c.value != glow.Int(1) {
		t.Fatalf("request %+v", c)
	}
}

func TestInvoke(t *testing.T) {
	m, p, _ := start(t)
	m = press(t, m, "l", "G", "k") // reset
	m = press(t, m, "enter")
	if m.mode != modeInput || !strings.Contains(m.View(), "ch (integer) =") {
		t.Fatalf("no argument prompt:\n%s", m.View())
	}
	m = press(t, m, "x", "enter")
	if m.mode != modeInput || m.status != `"x" is not an integer` {
		t.Fatalf("invalid argument: status %q", m.status)
	}
	m = press(t, m, "ctrl+u", "4", "enter")
	c := lastCommand(p)
	if c.number != glow.CmdInvoke || !slices.Equal(c.path, glow.Path{1, 3}) || !slices.Equal(c.args, []glow.Value{glow.Int(4)}) {
		t.Fatalf("request %+v", c)
	}
	if m.status != "reset: ok, ok=true" || !strings.Contains(m.View(), "Last result: ok, ok=true") {
		t.Fatalf("status %q\n%s", m.status, m.View())
	}

	m = press(t, m, "e", "9", "9", "enter")
	if m.status != "reset: failed, ok=false" {
		t.Fatalf("failed invocation: status %q", m.status)
	}
}

func TestInvokeWithoutArguments(t *testing.T) {
	m, p, _ := start(t)
	m = press(t, m, "l", "G") // save
	n := len(p.recorded())
	m = press(t, m, "e")
	if m.mode != modeConfirm || !strings.Contains(m.View(), "invoke save()? [y/N]") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
	m = press(t, m, "enter")
	if len(p.recorded()) != n {
		t.Fatal("invoked without confirmation")
	}
	m = press(t, m, "e", "y")
	if c := lastCommand(p); c.number != glow.CmdInvoke || len(c.args) != 0 {
		t.Fatalf("request %+v", c)
	}
	if m.status != "save: ok" {
		t.Fatalf("status %q", m.status)
	}
}

func TestSpaceToggles(t *testing.T) {
	m, _, _ := start(t)
	m = press(t, m, " ") // open root, cursor stays
	if len(m.rows) != 5 || selectedLabel(m) != "Device" {
		t.Fatalf("after opening: rows %d, selected %q", len(m.rows), selectedLabel(m))
	}
	m = press(t, m, "j", " ") // open settings
	if len(m.rows) != 12 || selectedLabel(m) != "Settings" {
		t.Fatalf("after opening settings: rows %d, selected %q", len(m.rows), selectedLabel(m))
	}
	m = press(t, m, " ") // close settings
	if len(m.rows) != 5 || selectedLabel(m) != "Settings" {
		t.Fatalf("after closing settings: rows %d, selected %q", len(m.rows), selectedLabel(m))
	}
	m = press(t, m, "k", " ") // close root
	if len(m.rows) != 1 || selectedLabel(m) != "Device" {
		t.Fatalf("after closing root: rows %d, selected %q", len(m.rows), selectedLabel(m))
	}

	// Space on a leaf does nothing.
	m = press(t, m, " ", "j", "j", " ")
	if len(m.rows) != 5 || selectedLabel(m) != "router" || m.mode != modeBrowse {
		t.Fatalf("on leaf: rows %d, selected %q", len(m.rows), selectedLabel(m))
	}
}
