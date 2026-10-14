package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

// matrixTree:
//
//	1 root
//	├── 1 router   1:N, 3 sources × 4 targets, labels at 1.2; target 0 ← 1
//	├── 2 labels
//	│   ├── 1 targets  Out A … Out D
//	│   └── 2 sources  In A … In C
//	└── 3 mixer    N:N, 2 × 2; target 0 ← 0
func matrixTree() []glow.Element {
	els := []glow.Element{
		&glow.Node{Path: glow.Path{1}, Identifier: new("root")},
		&glow.Matrix{Path: glow.Path{1, 1}, Identifier: new("router"), TargetCount: new(int32(4)), SourceCount: new(int32(3)),
			Labels:      []glow.Label{{BasePath: glow.Path{1, 2}, Description: "Primary"}},
			Connections: []glow.Connection{{Target: 0, Sources: []uint32{1}}, {Target: 1}, {Target: 2}, {Target: 3}}},
		&glow.Node{Path: glow.Path{1, 2}, Identifier: new("labels")},
		&glow.Node{Path: glow.Path{1, 2, 1}, Identifier: new("targets")},
		&glow.Node{Path: glow.Path{1, 2, 2}, Identifier: new("sources")},
		&glow.Matrix{Path: glow.Path{1, 3}, Identifier: new("mixer"), Type: new(glow.NToN), TargetCount: new(int32(2)), SourceCount: new(int32(2)),
			Connections: []glow.Connection{{Target: 0, Sources: []uint32{0}}, {Target: 1}}},
	}
	for i, name := range []string{"Out A", "Out B", "Out C", "Out D"} {
		els = append(els, &glow.Parameter{Path: glow.Path{1, 2, 1, uint32(i)}, Identifier: new("t"), Value: glow.String(name)})
	}
	for i, name := range []string{"In A", "In B", "In C"} {
		els = append(els, &glow.Parameter{Path: glow.Path{1, 2, 2, uint32(i)}, Identifier: new("s"), Value: glow.String(name)})
	}
	return els
}

func openMatrix(t *testing.T, label string) (model, *provider, chan tea.Msg) {
	t.Helper()
	m, p, events := startWith(t, matrixTree())
	m = press(t, m, "l")
	for i, r := range m.rows {
		if m.label(r) == label {
			m.cursor = i
		}
	}
	m = press(t, m, "e")
	if m.mode != modeMatrix || m.matrix.loading || m.status != "" {
		t.Fatalf("matrix not open: mode %v, status %q", m.mode, m.status)
	}
	return m, p, events
}

func lastConnect(t *testing.T, p *provider) glow.Connection {
	t.Helper()
	for _, c := range slices.Backward(p.recorded()) {
		if c.conn != nil {
			return *c.conn
		}
	}
	t.Fatal("no connect request")
	return glow.Connection{}
}

func TestMatrixView(t *testing.T) {
	m, _, _ := openMatrix(t, "router")
	view := m.View()
	for _, want := range []string{"router", "oneToN, 3 sources × 4 targets", "In A", "In C", "target Out A, source In A: not connected"} {
		if !strings.Contains(view, want) {
			t.Errorf("view does not contain %q:\n%s", want, view)
		}
	}
	// Sources are rows: source 1 is connected to target 0, the first column.
	for line := range strings.SplitSeq(view, "\n") {
		if strings.HasPrefix(line, "In B") && (strings.Count(line, "●") != 1 || !strings.HasPrefix(strings.TrimSpace(line[len("In B"):]), "●")) {
			t.Errorf("row of In B: %q", line)
		}
		if strings.HasPrefix(line, "In A") && strings.Contains(line, "●") {
			t.Errorf("row of In A: %q", line)
		}
	}
	m = press(t, m, "j")
	if !strings.Contains(m.View(), "target Out A, source In B: connected") {
		t.Errorf("cursor info:\n%s", m.View())
	}
	m = press(t, m, "esc")
	if m.mode != modeBrowse || m.matrix != nil {
		t.Fatalf("esc: mode %v", m.mode)
	}
}

func TestMatrixToggleOneToN(t *testing.T) {
	m, p, _ := openMatrix(t, "router")

	// Disconnect the connected crosspoint source 1 / target 0.
	m = press(t, m, "j", "enter")
	if c := lastConnect(t, p); c.Target != 0 || len(c.Sources) != 0 || c.Operation != glow.OpAbsolute {
		t.Fatalf("request %+v", c)
	}
	if m.status != "target 0: disconnected" || strings.Contains(m.View(), "●") {
		t.Fatalf("status %q\n%s", m.status, m.View())
	}

	// Connect target 1 to source 2, replacing its sources.
	m = press(t, m, "j", "l", "enter")
	if c := lastConnect(t, p); c.Target != 1 || !slices.Equal(c.Sources, []uint32{2}) || c.Operation != glow.OpAbsolute {
		t.Fatalf("request %+v", c)
	}
	if m.status != "target 1: ← In C" {
		t.Fatalf("status %q", m.status)
	}
}

func TestMatrixLocked(t *testing.T) {
	m, p, _ := openMatrix(t, "router")
	p.mu.Lock()
	p.locked = 3
	p.mu.Unlock()
	m = press(t, m, "l", "l", "l", "enter")
	if m.status != "target 3 is locked" {
		t.Fatalf("status %q", m.status)
	}
}

func TestMatrixToggleNToN(t *testing.T) {
	m, p, _ := openMatrix(t, "mixer")
	m = press(t, m, "j", "enter") // source 1 / target 0: add
	if c := lastConnect(t, p); c.Operation != glow.OpConnect || !slices.Equal(c.Sources, []uint32{1}) {
		t.Fatalf("request %+v", c)
	}
	if m.status != "target 0: ← 0, 1" {
		t.Fatalf("status %q", m.status)
	}
	m = press(t, m, "k", "enter") // source 0 / target 0: remove
	if c := lastConnect(t, p); c.Operation != glow.OpDisconnect || !slices.Equal(c.Sources, []uint32{0}) {
		t.Fatalf("request %+v", c)
	}
	if m.status != "target 0: ← 1" {
		t.Fatalf("status %q", m.status)
	}
}

func TestMatrixLiveUpdate(t *testing.T) {
	m, p, events := openMatrix(t, "router")
	p.send(&glow.Matrix{Path: glow.Path{1, 1}, Connections: []glow.Connection{{Target: 2, Sources: []uint32{0}, Disposition: glow.Modified}}})
	select {
	case msg := <-events:
		m = update(t, m, msg)
	case <-time.After(2 * time.Second):
		t.Fatal("no watch event")
	}
	// Target 2 is now connected to source 0: a marker in the row of In A.
	for line := range strings.SplitSeq(m.View(), "\n") {
		if strings.HasPrefix(line, "In A") && strings.Count(line, "●") != 1 {
			t.Fatalf("row of In A: %q", line)
		}
	}
}

func openBigMatrix(t *testing.T) model {
	t.Helper()
	// A provider reports one connection per target (spec: "Matrix").
	big := &glow.Matrix{Path: glow.Path{1, 1}, Identifier: new("big"), TargetCount: new(int32(200)), SourceCount: new(int32(200))}
	for target := range uint32(200) {
		big.Connections = append(big.Connections, glow.Connection{Target: target})
	}
	m, _, _ := startWith(t, []glow.Element{&glow.Node{Path: glow.Path{1}, Identifier: new("root")}, big})
	m = press(t, m, "l", "j", "e")
	if m.status != "" {
		t.Fatalf("loading the matrix: %s", m.status)
	}
	return m
}

func TestMatrixScrolls(t *testing.T) {
	m := openBigMatrix(t)
	for range 60 {
		m = press(t, m, "j", "l")
	}
	if m.matrix.target != 60 || m.matrix.source != 60 {
		t.Fatalf("cursor %d/%d", m.matrix.target, m.matrix.source)
	}
	if !strings.Contains(m.View(), "target 60, source 60") || m.matrix.rowOff == 0 || m.matrix.colOff == 0 {
		t.Fatalf("not scrolled: offsets %d/%d\n%s", m.matrix.rowOff, m.matrix.colOff, m.View())
	}
	for line := range strings.SplitSeq(m.View(), "\n") {
		if len([]rune(line)) > 120 {
			t.Fatalf("line wider than the terminal: %d", len([]rune(line)))
		}
	}
}

func TestMatrixPageJumps(t *testing.T) {
	m := openBigMatrix(t)
	// The test terminal is 120×20: 15 sources and 51 targets fit on a page.
	rows, cols := m.pageRows(), m.pageCols()
	if rows != 15 || cols != 51 {
		t.Fatalf("page %d×%d", rows, cols)
	}
	tests := []struct {
		keys           []string
		source, target int
	}{
		{[]string{" "}, 15, 0},
		{[]string{" ", "pgdown", "ctrl+f"}, 60, 0},
		{[]string{"b"}, 45, 0},
		{[]string{"pgup", "ctrl+b", "b", "b"}, 0, 0},
		{[]string{"L"}, 0, 51},
		{[]string{"shift+right", "L"}, 0, 153},
		{[]string{"L"}, 0, 199},
		{[]string{"H", "shift+left"}, 0, 97},
		{[]string{"0"}, 0, 0},
		{[]string{"$"}, 0, 199},
		{[]string{"G"}, 199, 199},
		{[]string{" "}, 199, 199},
		{[]string{"g"}, 0, 199},
	}
	for _, tt := range tests {
		m = press(t, m, tt.keys...)
		if m.matrix.source != tt.source || m.matrix.target != tt.target {
			t.Fatalf("after %q: source %d, target %d; want %d, %d", tt.keys, m.matrix.source, m.matrix.target, tt.source, tt.target)
		}
		// The cursor stays visible.
		if !strings.Contains(m.View(), fmt.Sprintf("target %d, source %d", tt.target, tt.source)) {
			t.Fatalf("after %q: cursor info missing\n%s", tt.keys, m.View())
		}
	}
}
