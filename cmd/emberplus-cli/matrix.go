package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	emberplus "github.com/lukirs95/goemberplusclient"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

// matrixState is the crosspoint view of a matrix: sources are rows, targets
// are columns (spec: "Matrix Extensions").
type matrixState struct {
	node    *node
	loading bool
	source  int // cursor row
	target  int // cursor column
	rowOff  int
	colOff  int
	stop    func()

	targetLabels map[uint32]string
	sourceLabels map[uint32]string
}

type matrixLoadedMsg struct {
	path         string
	matrix       glow.Element
	stop         func()
	targetLabels map[uint32]string
	sourceLabels map[uint32]string
	err          error
}

type connectMsg struct {
	conn glow.Connection
	err  error
}

const matrixLabelWidth = 16

// signals returns the target and source numbers of a matrix: the listed
// signals for non-linear matrices, otherwise 0 to count-1 (spec: "Matrix").
func signals(m *glow.Matrix) (targets, sources []uint32) {
	numbers := func(listed []uint32, count *int32) []uint32 {
		if listed != nil {
			return listed
		}
		if count == nil {
			return nil
		}
		ns := make([]uint32, max(0, *count))
		for i := range ns {
			ns[i] = uint32(i)
		}
		return ns
	}
	return numbers(m.Targets, m.TargetCount), numbers(m.Sources, m.SourceCount)
}

func connected(m *glow.Matrix, target, source uint32) bool {
	c, ok := m.Connection(target)
	return ok && slices.Contains(c.Sources, source)
}

// openMatrix loads the connections and labels of the matrix and watches it.
func (m *model) openMatrix(n *node) tea.Cmd {
	m.matrix = &matrixState{node: n, loading: true}
	m.mode = modeMatrix
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		// GetDirectory on the matrix reports its connections and subscribes
		// to their changes.
		els, err := m.app.client.GetDirectory(ctx, n.path)
		if err != nil {
			return matrixLoadedMsg{path: n.path, err: err}
		}
		msg := matrixLoadedMsg{path: n.path, matrix: els[0]}
		if mat, ok := els[0].(*glow.Matrix); ok && len(mat.Labels) > 0 {
			base := mat.Labels[0].BasePath.String()
			msg.targetLabels = m.labels(base + ".1")
			msg.sourceLabels = m.labels(base + ".2")
		}
		msg.stop, msg.err = m.app.client.Watch(ctx, n.path, func(e emberplus.Event) {
			m.app.send(updateMsg{e.Element})
		})
		return msg
	}
}

// labels reads the label parameters below path: one string parameter per
// signal, numbered like the signal (spec: "Label"). Labels are optional, so
// errors only leave them out.
func (m model) labels(path string) map[uint32]string {
	ctx, cancel := m.context()
	defer cancel()
	children, err := m.app.client.Children(ctx, path)
	if err != nil {
		return nil
	}
	labels := map[uint32]string{}
	for _, el := range children {
		if p, ok := el.(*glow.Parameter); ok {
			if s, ok := p.Value.(glow.String); ok {
				ep := p.Path
				labels[ep[len(ep)-1]] = string(s)
			}
		}
	}
	return labels
}

func (m *model) closeMatrix() {
	if m.matrix != nil && m.matrix.stop != nil {
		m.matrix.stop()
	}
	m.matrix = nil
	m.mode = modeBrowse
}

func (m model) matrixKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ms := m.matrix
	mat, _ := ms.node.el.(*glow.Matrix)
	targets, sources := signals(mat)
	m.status = ""
	switch msg.String() {
	case "esc", "q":
		m.closeMatrix()
	case "ctrl+c":
		m.closeMatrix()
		return m, tea.Quit
	case "up", "k":
		ms.source = max(0, ms.source-1)
	case "down", "j":
		ms.source = max(0, min(len(sources)-1, ms.source+1))
	case "left", "h":
		ms.target = max(0, ms.target-1)
	case "right", "l":
		ms.target = max(0, min(len(targets)-1, ms.target+1))
	case " ", "pgdown", "ctrl+f":
		ms.source = max(0, min(len(sources)-1, ms.source+m.pageRows()))
	case "b", "pgup", "ctrl+b":
		ms.source = max(0, ms.source-m.pageRows())
	case "L", "shift+right":
		ms.target = max(0, min(len(targets)-1, ms.target+m.pageCols()))
	case "H", "shift+left":
		ms.target = max(0, ms.target-m.pageCols())
	case "g":
		ms.source = 0
	case "G":
		ms.source = max(0, len(sources)-1)
	case "0":
		ms.target = 0
	case "$":
		ms.target = max(0, len(targets)-1)
	case "enter":
		if ms.loading || ms.target >= len(targets) || ms.source >= len(sources) {
			return m, nil
		}
		return m, m.toggleCrosspoint(mat, targets[ms.target], sources[ms.source])
	}
	return m, nil
}

// toggleCrosspoint connects or disconnects source and target. Matrices
// with one source per target replace the source; N:N matrices add or remove
// it (spec: "ConnectionOperation").
func (m model) toggleCrosspoint(mat *glow.Matrix, target, source uint32) tea.Cmd {
	on := connected(mat, target, source)
	nToN := mat.Type != nil && *mat.Type == glow.NToN
	sources, op := []uint32{source}, glow.OpAbsolute
	switch {
	case nToN && on:
		op = glow.OpDisconnect
	case nToN:
		op = glow.OpConnect
	case on:
		sources = []uint32{}
	}
	path := m.matrix.node.path
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		conn, err := m.app.client.Connect(ctx, path, target, sources, op)
		return connectMsg{conn: conn, err: err}
	}
}

func (m *model) handleMatrix(msg tea.Msg) {
	switch msg := msg.(type) {
	case matrixLoadedMsg:
		if m.matrix == nil || m.matrix.node.path != msg.path {
			if msg.stop != nil {
				msg.stop()
			}
			return
		}
		m.matrix.loading = false
		m.matrix.stop = msg.stop
		m.matrix.targetLabels = msg.targetLabels
		m.matrix.sourceLabels = msg.sourceLabels
		if msg.err != nil {
			m.status = msg.err.Error()
		}
		if msg.matrix != nil {
			m.matrix.node.el = msg.matrix
		}
	case connectMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return
		}
		switch msg.conn.Disposition {
		case glow.Locked:
			m.status = fmt.Sprintf("target %d is locked", msg.conn.Target)
		case glow.Pending:
			m.status = fmt.Sprintf("connection of target %d is pending", msg.conn.Target)
		default:
			m.status = fmt.Sprintf("target %d: %s", msg.conn.Target, m.sourceList(msg.conn.Sources))
		}
		if m.matrix != nil {
			if mat, ok := m.matrix.node.el.(*glow.Matrix); ok {
				updated := mat.Clone()
				updated.Merge(&glow.Matrix{Connections: []glow.Connection{msg.conn}})
				m.matrix.node.el = updated
			}
		}
	}
}

func (m model) sourceList(sources []uint32) string {
	if len(sources) == 0 {
		return "disconnected"
	}
	var names []string
	for _, s := range sources {
		names = append(names, m.signalName(m.matrix.sourceLabels, s))
	}
	return "← " + strings.Join(names, ", ")
}

func (m model) signalName(labels map[uint32]string, n uint32) string {
	if l := labels[n]; l != "" {
		return l
	}
	return strconv.FormatUint(uint64(n), 10)
}

// matrixView renders the crosspoint grid in place of the tree and details.
func (m *model) matrixView() string {
	ms := m.matrix
	if ms.loading {
		return dimStyle.Render("loading " + m.label(ms.node) + "…")
	}
	mat, ok := ms.node.el.(*glow.Matrix)
	if !ok {
		return ""
	}
	targets, sources := signals(mat)
	if len(targets) == 0 || len(sources) == 0 {
		return dimStyle.Render("the matrix reports no targets or sources")
	}
	ms.target = min(ms.target, len(targets)-1)
	ms.source = min(ms.source, len(sources)-1)

	// Two lines for title and column header, one for the cursor info.
	rows, cols := m.pageRows(), m.pageCols()
	ms.rowOff = scrollTo(ms.rowOff, ms.source, rows, len(sources))
	ms.colOff = scrollTo(ms.colOff, ms.target, cols, len(targets))

	kind := glow.OneToN
	if mat.Type != nil {
		kind = *mat.Type
	}
	lines := []string{keyStyle.Render(m.label(ms.node)) + dimStyle.Render(fmt.Sprintf("  %s, %d sources × %d targets", kind, len(sources), len(targets)))}

	// The column header marks the target of the cursor.
	lines = append(lines, strings.Repeat(" ", matrixLabelWidth+1+2*(ms.target-ms.colOff))+"▼")

	for r := ms.rowOff; r < min(len(sources), ms.rowOff+rows); r++ {
		s := sources[r]
		label := m.signalName(ms.sourceLabels, s)
		label = fmt.Sprintf("%-*s", matrixLabelWidth, truncate(label, matrixLabelWidth))
		if r == ms.source {
			label = selectedStyle.Render(label)
		}
		var cells strings.Builder
		for c := ms.colOff; c < min(len(targets), ms.colOff+cols); c++ {
			on := connected(mat, targets[c], s)
			cell := "·"
			if on {
				cell = valueStyle.Render("●")
			}
			if r == ms.source && c == ms.target {
				cell = selectedStyle.Render(stripCell(on))
			}
			cells.WriteString(cell)
			cells.WriteByte(' ')
		}
		lines = append(lines, label+" "+cells.String())
	}

	t, s := targets[ms.target], sources[ms.source]
	state := "not connected"
	if connected(mat, t, s) {
		state = "connected"
	}
	lines = append(lines, dimStyle.Render(fmt.Sprintf("target %s, source %s: %s",
		m.signalName(ms.targetLabels, t), m.signalName(ms.sourceLabels, s), state)))
	return strings.Join(lines, "\n")
}

func stripCell(on bool) string {
	if on {
		return "●"
	}
	return "·"
}

// pageRows is the number of sources visible at once: the matrix view
// shares the screen with the header, a title, the column marker, the cursor
// info and the status line.
func (m *model) pageRows() int {
	return max(1, m.treeHeight()-3)
}

// pageCols is the number of targets visible at once; each takes two columns.
func (m *model) pageCols() int {
	return max(1, (m.width-matrixLabelWidth-1)/2)
}

// scrollTo returns an offset that keeps cursor within a window of size n.
func scrollTo(offset, cursor, n, total int) int {
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+n {
		offset = cursor - n + 1
	}
	return max(0, min(offset, max(0, total-n)))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
