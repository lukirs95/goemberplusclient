package main

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	emberplus "github.com/lukirs95/goemberplusclient"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

// app holds what the model shares with callbacks running outside of it.
type app struct {
	client  *emberplus.Client
	addr    string
	timeout time.Duration
	send    func(tea.Msg) // delivers messages from Watch callbacks
}

// node is an element in the displayed tree. The root node has no element.
type node struct {
	path     string // numeric path
	el       glow.Element
	parent   *node
	children []*node
	depth    int
	expanded bool
	loaded   bool
	loading  bool
	stops    []func() // watches of the children while expanded
}

func (n *node) isContainer() bool {
	_, ok := n.el.(*glow.Node)
	return n.el == nil || ok
}

type model struct {
	app    *app
	root   *node
	nodes  map[string]*node
	rows   []*node
	cursor int
	offset int
	width  int
	height int

	subs    map[string]func() error
	results map[string]string // last invocation result per function
	status  string
	closed  error

	mode   mode
	edit   *editState
	matrix *matrixState
}

type childrenMsg struct {
	path     string
	children []glow.Element
	err      error
}

type watchesMsg struct {
	path  string
	stops []func()
	err   error
}

type updateMsg struct{ el glow.Element }

type subscribedMsg struct {
	path        string
	unsubscribe func() error
	err         error
}

type disconnectedMsg struct{ err error }

func newModel(a *app) model {
	root := &node{path: "", expanded: true, loading: true, depth: -1}
	return model{
		app:     a,
		root:    root,
		nodes:   map[string]*node{"": root},
		subs:    map[string]func() error{},
		results: map[string]string{},
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.loadChildren(""), m.waitDisconnect())
}

func (m model) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), m.app.timeout)
}

func (m model) loadChildren(path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		children, err := m.app.client.Children(ctx, path)
		return childrenMsg{path: path, children: children, err: err}
	}
}

// watchChildren watches parameters and nodes below path so that their
// changes appear while path is expanded. It never subscribes to streams.
func (m model) watchChildren(path string, children []glow.Element) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		var stops []func()
		for _, el := range children {
			switch el.(type) {
			case *glow.Parameter, *glow.Node:
			default:
				continue
			}
			stop, err := m.app.client.Watch(ctx, el.ElementPath().String(), func(e emberplus.Event) {
				m.app.send(updateMsg{e.Element})
			})
			if err != nil {
				return watchesMsg{path: path, stops: stops, err: err}
			}
			stops = append(stops, stop)
		}
		return watchesMsg{path: path, stops: stops}
	}
}

func (m model) waitDisconnect() tea.Cmd {
	return func() tea.Msg {
		<-m.app.client.Done()
		return disconnectedMsg{m.app.client.Err()}
	}
}

func (m model) toggleSubscription(n *node) tea.Cmd {
	if unsubscribe, ok := m.subs[n.path]; ok {
		delete(m.subs, n.path)
		return func() tea.Msg {
			if err := unsubscribe(); err != nil {
				return subscribedMsg{path: n.path, err: err}
			}
			return nil
		}
	}
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		unsubscribe, err := m.app.client.Subscribe(ctx, n.path)
		return subscribedMsg{path: n.path, unsubscribe: unsubscribe, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.scroll()
		return m, nil

	case tea.KeyMsg:
		return m.key(msg)

	case childrenMsg:
		n := m.nodes[msg.path]
		if n == nil {
			return m, nil
		}
		n.loading = false
		if msg.err != nil {
			n.expanded = false
			m.status = msg.err.Error()
			m.flatten()
			return m, nil
		}
		m.setChildren(n, msg.children)
		m.flatten()
		if n.expanded {
			return m, m.watchChildren(n.path, msg.children)
		}
		return m, nil

	case watchesMsg:
		n := m.nodes[msg.path]
		if n == nil || !n.expanded {
			for _, stop := range msg.stops {
				stop()
			}
		} else {
			n.stops = append(n.stops, msg.stops...)
		}
		if msg.err != nil {
			m.status = msg.err.Error()
		}
		return m, nil

	case updateMsg:
		if n := m.nodes[msg.el.ElementPath().String()]; n != nil {
			n.el = msg.el
		}
		return m, nil

	case setValueMsg, invokeMsg:
		m.handleResult(msg)
		return m, nil

	case matrixLoadedMsg, connectMsg:
		m.handleMatrix(msg)
		return m, nil

	case subscribedMsg:
		switch {
		case msg.err != nil:
			m.status = msg.err.Error()
		case msg.unsubscribe != nil:
			m.subs[msg.path] = msg.unsubscribe
			m.status = "subscribed " + m.label(m.nodes[msg.path])
		}
		return m, nil

	case disconnectedMsg:
		m.closed = msg.err
		if m.closed == nil {
			m.closed = fmt.Errorf("disconnected")
		}
		return m, nil
	}
	return m, nil
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeMatrix:
		return m.matrixKey(msg)
	case modeInput, modeEnum, modeConfirm:
		return m.editKey(msg)
	}
	m.status = ""
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+u":
		m.move(-m.pageSize())
	case "pgdown", "ctrl+d":
		m.move(m.pageSize())
	case "home", "g":
		m.move(-len(m.rows))
	case "end", "G":
		m.move(len(m.rows))
	case "right", "l":
		return m, m.expand()
	case "enter":
		if n := m.selected(); n != nil && !n.isContainer() {
			return m, m.beginEdit()
		}
		return m, m.expand()
	case " ":
		return m, m.toggle()
	case "e":
		return m, m.beginEdit()
	case "left", "h":
		m.collapse()
	case "s":
		if n := m.selected(); n != nil {
			if p, ok := n.el.(*glow.Parameter); ok && p.IsStream() {
				return m, m.toggleSubscription(n)
			}
			m.status = "not a stream parameter"
		}
	case "r":
		if n := m.selected(); n != nil && n.isContainer() && n.expanded {
			m.stopWatches(n)
			n.loading = true
			return m, m.reload(n.path)
		}
	}
	return m, nil
}

// reload requests the children of path again, even if they are loaded.
func (m model) reload(path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		if _, err := m.app.client.GetDirectory(ctx, path); err != nil {
			return childrenMsg{path: path, err: err}
		}
		children, err := m.app.client.Children(ctx, path)
		return childrenMsg{path: path, children: children, err: err}
	}
}

func (m *model) selected() *node {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor]
}

func (m *model) move(delta int) {
	m.cursor = max(0, min(len(m.rows)-1, m.cursor+delta))
	m.scroll()
}

// expand opens the selected node, or moves to its first child if it is open.
func (m *model) expand() tea.Cmd {
	n := m.selected()
	if n == nil || !n.isContainer() {
		return nil
	}
	if n.expanded {
		if len(n.children) > 0 {
			m.move(1)
		}
		return nil
	}
	n.expanded = true
	if n.loaded {
		m.flatten()
		return m.watchChildren(n.path, elements(n.children))
	}
	n.loading = true
	m.flatten()
	return m.loadChildren(n.path)
}

// toggle opens or closes the selected node without moving the cursor.
func (m *model) toggle() tea.Cmd {
	n := m.selected()
	if n == nil || !n.isContainer() {
		return nil
	}
	if n.expanded {
		m.close(n)
		m.flatten()
		return nil
	}
	return m.expand()
}

// collapse closes the selected node, or moves to its parent if it is closed.
func (m *model) collapse() {
	n := m.selected()
	if n == nil {
		return
	}
	if n.expanded {
		m.close(n)
		m.flatten()
		return
	}
	if n.parent != nil && n.parent != m.root {
		for i, r := range m.rows {
			if r == n.parent {
				m.cursor = i
				break
			}
		}
		m.scroll()
	}
}

// close collapses n and its descendants and stops their watches.
func (m *model) close(n *node) {
	n.expanded = false
	m.stopWatches(n)
	for _, c := range n.children {
		if c.expanded {
			m.close(c)
		}
	}
}

func (m *model) stopWatches(n *node) {
	for _, stop := range n.stops {
		stop()
	}
	n.stops = nil
}

func (m *model) setChildren(n *node, els []glow.Element) {
	old := map[string]*node{}
	for _, c := range n.children {
		old[c.path] = c
	}
	n.children = n.children[:0]
	for _, el := range els {
		path := el.ElementPath().String()
		c := old[path]
		if c == nil {
			c = &node{path: path, parent: n, depth: n.depth + 1}
			m.nodes[path] = c
		}
		c.el = el
		n.children = append(n.children, c)
	}
	n.loaded = true
}

// flatten rebuilds the visible rows and keeps the cursor on its node.
func (m *model) flatten() {
	sel := m.selected()
	m.rows = m.rows[:0]
	var walk func(n *node)
	walk = func(n *node) {
		for _, c := range n.children {
			m.rows = append(m.rows, c)
			if c.expanded {
				walk(c)
			}
		}
	}
	walk(m.root)
	m.cursor = 0
	for i, r := range m.rows {
		if r == sel {
			m.cursor = i
		}
	}
	m.scroll()
}

func (m *model) pageSize() int {
	return max(1, m.treeHeight()-1)
}

func (m *model) scroll() {
	h := m.treeHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if h > 0 && m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.rows)-h)))
}

func elements(nodes []*node) []glow.Element {
	els := make([]glow.Element, len(nodes))
	for i, n := range nodes {
		els[i] = n.el
	}
	return els
}
