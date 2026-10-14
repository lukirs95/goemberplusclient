package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	valueStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	streamStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	keyStyle      = lipgloss.NewStyle().Bold(true)
	detailsStyle  = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).PaddingLeft(1)
)

// treeHeight is the number of tree rows that fit between header and status.
func (m *model) treeHeight() int {
	return max(0, m.height-2)
}

func (m model) View() string {
	if m.width == 0 {
		return "connecting…"
	}
	header := headerStyle.Width(m.width).Render("emberplus-cli  " + m.app.addr)

	treeWidth := m.width
	detailsWidth := 0
	if m.width >= 80 {
		detailsWidth = m.width * 2 / 5
		treeWidth = m.width - detailsWidth
	}
	h := m.treeHeight()
	tree := lipgloss.NewStyle().Width(treeWidth).Height(h).MaxHeight(h).Render(m.treeView(treeWidth, h))
	body := tree
	if detailsWidth > 0 {
		content := m.detailsView(detailsWidth - 2)
		if m.mode == modeEnum {
			content = m.enumView()
		}
		details := detailsStyle.Width(detailsWidth - 1).Height(h).MaxHeight(h).Render(content)
		body = lipgloss.JoinHorizontal(lipgloss.Top, tree, details)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, m.statusView())
}

func (m model) treeView(width, height int) string {
	if m.root.loading && len(m.rows) == 0 {
		return dimStyle.Render("loading…")
	}
	if len(m.rows) == 0 {
		return dimStyle.Render("the provider reported no elements")
	}
	var lines []string
	for i := m.offset; i < len(m.rows) && i < m.offset+height; i++ {
		line := m.row(m.rows[i])
		if i == m.cursor {
			line = selectedStyle.Render(m.plainRow(m.rows[i]))
		}
		lines = append(lines, lipgloss.NewStyle().MaxWidth(width).Render(line))
	}
	return strings.Join(lines, "\n")
}

// row renders a tree row with styles; plainRow without.
func (m model) row(n *node) string {
	if !m.online(n) {
		return dimStyle.Render(m.plainRow(n))
	}
	line := m.plainRowPrefix(n) + m.label(n)
	if p, ok := n.el.(*glow.Parameter); ok {
		v := formatValue(p)
		if _, subscribed := m.subs[n.path]; subscribed {
			v = streamStyle.Render(v)
		} else {
			v = valueStyle.Render(v)
		}
		line += "  " + v
	}
	return line
}

func (m model) plainRow(n *node) string {
	line := m.plainRowPrefix(n) + m.label(n)
	if p, ok := n.el.(*glow.Parameter); ok {
		line += "  " + formatValue(p)
	}
	return line
}

func (m model) plainRowPrefix(n *node) string {
	return strings.Repeat("  ", n.depth) + icon(n, m.subs) + " "
}

func icon(n *node, subs map[string]func() error) string {
	switch e := n.el.(type) {
	case *glow.Node:
		switch {
		case n.loading:
			return "…"
		case n.expanded:
			return "▾"
		}
		return "▸"
	case *glow.Parameter:
		if e.IsStream() {
			if _, ok := subs[n.path]; ok {
				return "≋"
			}
			return "≈"
		}
		return "•"
	case *glow.Matrix:
		return "▦"
	case *glow.Function:
		return "ƒ"
	}
	return "?"
}

// label is the description of an element, or its identifier (spec: "The
// description property").
func (m model) label(n *node) string {
	if n == nil {
		return ""
	}
	desc, id := names(n.el)
	switch {
	case desc != "":
		return desc
	case id != "":
		return id
	}
	return n.path
}

func names(el glow.Element) (description, identifier string) {
	get := func(d, i *string) (string, string) {
		var ds, is string
		if d != nil {
			ds = *d
		}
		if i != nil {
			is = *i
		}
		return ds, is
	}
	switch e := el.(type) {
	case *glow.Node:
		return get(e.Description, e.Identifier)
	case *glow.Parameter:
		return get(e.Description, e.Identifier)
	case *glow.Matrix:
		return get(e.Description, e.Identifier)
	case *glow.Function:
		return get(e.Description, e.Identifier)
	}
	return "", ""
}

// online applies the inheritance of the online state within the tree.
func (m model) online(n *node) bool {
	for ; n != nil; n = n.parent {
		var on *bool
		switch e := n.el.(type) {
		case *glow.Node:
			on = e.IsOnline
		case *glow.Parameter:
			on = e.IsOnline
		}
		if on != nil && !*on {
			return false
		}
	}
	return true
}

// identifierPath builds the identifier path of n from the tree.
func identifierPath(n *node) string {
	var ids []string
	for ; n != nil && n.el != nil; n = n.parent {
		_, id := names(n.el)
		if id == "" {
			id = n.path[strings.LastIndex(n.path, ".")+1:]
		}
		ids = append(ids, id)
	}
	slices.Reverse(ids)
	return strings.Join(ids, "/")
}

func (m model) detailsView(width int) string {
	n := m.selected()
	if n == nil {
		return ""
	}
	var b strings.Builder
	field := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s %s\n", keyStyle.Render(name+":"), value)
		}
	}
	ptr := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	field("Path", identifierPath(n))
	field("Number", n.path)
	if !m.online(n) {
		field("Online", "no")
	}

	switch e := n.el.(type) {
	case *glow.Node:
		field("Type", "node")
		field("Identifier", ptr(e.Identifier))
		field("Description", ptr(e.Description))
		if e.IsRoot != nil && *e.IsRoot {
			field("Root", "yes")
		}
		field("Schemas", ptr(e.SchemaIdentifiers))
		if n.loaded {
			field("Children", strconv.Itoa(len(n.children)))
		}

	case *glow.Parameter:
		kind := "parameter"
		if t, ok := e.EffectiveType(); ok {
			kind += " (" + t.String() + ")"
		}
		field("Type", kind)
		field("Identifier", ptr(e.Identifier))
		field("Description", ptr(e.Description))
		field("Value", formatValue(e))
		if writable(e) {
			field("Edit", "press e")
		}
		if e.Access != nil {
			field("Access", e.Access.String())
		} else {
			field("Access", "read (default)")
		}
		if e.Minimum != nil || e.Maximum != nil {
			field("Range", valueString(e.Minimum)+" … "+valueString(e.Maximum))
		}
		if e.Default != nil {
			field("Default", valueString(e.Default))
		}
		field("Format", ptr(e.Format))
		field("Formula", strings.ReplaceAll(ptr(e.Formula), "\n", " | "))
		if e.Factor != nil {
			field("Factor", strconv.Itoa(int(*e.Factor)))
		}
		if entries := e.EnumEntries(); entries != nil {
			var names []string
			for _, en := range entries {
				if !en.Hidden {
					names = append(names, fmt.Sprintf("%d=%s", en.Value, en.Name))
				}
			}
			field("Enum", strings.Join(names, ", "))
		}
		if e.IsStream() {
			state := "not subscribed, press s"
			if _, ok := m.subs[n.path]; ok {
				state = "subscribed, press s to stop"
			}
			field("Stream", fmt.Sprintf("%d (%s)", *e.StreamIdentifier, state))
		}

	case *glow.Matrix:
		field("Type", "matrix")
		field("Identifier", ptr(e.Identifier))
		field("Description", ptr(e.Description))
		mt := glow.OneToN
		if e.Type != nil {
			mt = *e.Type
		}
		field("Kind", mt.String())
		if e.TargetCount != nil && e.SourceCount != nil {
			field("Size", fmt.Sprintf("%d targets × %d sources", *e.TargetCount, *e.SourceCount))
		}
		if len(e.Connections) > 0 {
			var conns []string
			for _, c := range e.Connections {
				if len(c.Sources) > 0 {
					conns = append(conns, fmt.Sprintf("%d←%s", c.Target, glow.Path(c.Sources)))
				}
			}
			if len(conns) == 0 {
				conns = []string{"none"}
			}
			field("Connections", strings.Join(conns, " "))
		}

	case *glow.Function:
		field("Type", "function")
		field("Identifier", ptr(e.Identifier))
		field("Description", ptr(e.Description))
		field("Signature", signature(e))
		field("Last result", m.results[n.path])
		field("Invoke", "press e")
	}
	return lipgloss.NewStyle().Width(width).Render(strings.TrimRight(b.String(), "\n"))
}

func signature(f *glow.Function) string {
	tuple := func(items []glow.TupleItem) string {
		var parts []string
		for _, it := range items {
			parts = append(parts, strings.TrimSpace(it.Name+" "+it.Type.String()))
		}
		return strings.Join(parts, ", ")
	}
	_, id := names(f)
	s := id + "(" + tuple(f.Arguments) + ")"
	if len(f.Result) > 0 {
		s += " → (" + tuple(f.Result) + ")"
	}
	return s
}

func (m model) statusView() string {
	help := "↑↓/jk move  →/l open  ←/h close  space toggle  e edit/invoke  s subscribe  r reload  q quit"
	switch {
	case m.mode != modeBrowse:
		return m.editView()
	case m.closed != nil:
		return errorStyle.Render("connection closed: " + m.closed.Error())
	case m.status != "":
		return m.status
	}
	return dimStyle.Render(help)
}

// formatValue renders the value of a parameter for display, applying enum
// names, factor and format string.
func formatValue(p *glow.Parameter) string {
	if t, ok := p.EffectiveType(); ok && t == glow.TypeTrigger {
		return "trigger"
	}
	if p.Value == nil {
		if p.IsStream() {
			return "—"
		}
		return ""
	}
	if i, ok := p.Value.(glow.Int); ok {
		if name, found := p.EnumName(int64(i)); found {
			return name
		}
	}
	var number float64
	isNumber := true
	switch v := p.Value.(type) {
	case glow.Int:
		number = float64(v)
	case glow.Real:
		number = float64(v)
	default:
		isNumber = false
	}
	if !isNumber {
		return valueString(p.Value)
	}
	if p.Factor != nil && *p.Factor != 0 {
		number /= float64(*p.Factor)
	}
	if p.Format != nil {
		if s, ok := applyFormat(*p.Format, number); ok {
			return s
		}
	}
	if p.Factor != nil {
		return strconv.FormatFloat(number, 'g', -1, 64)
	}
	return valueString(p.Value)
}

// applyFormat applies a C-style format string such as "%.2f°dB", where "°"
// separates the unit (spec: "Format"). Only d, f and x are allowed.
func applyFormat(format string, v float64) (string, bool) {
	format, unit, _ := strings.Cut(format, "°")
	unit = strings.ReplaceAll(unit, "\n", "")
	i := strings.IndexByte(format, '%')
	if i < 0 {
		return "", false
	}
	verb := strings.IndexAny(format[i+1:], "dfx")
	if verb < 0 {
		return "", false
	}
	var arg any = v
	if c := format[i+1+verb]; c == 'd' || c == 'x' {
		arg = int64(v)
	}
	s := fmt.Sprintf(format, arg)
	if strings.Contains(s, "%!") {
		return "", false
	}
	if unit != "" {
		s = strings.TrimSpace(s) + " " + strings.TrimSpace(unit)
	}
	return s, true
}

func valueString(v glow.Value) string {
	switch v := v.(type) {
	case nil:
		return ""
	case glow.Int:
		return strconv.FormatInt(int64(v), 10)
	case glow.Real:
		return strconv.FormatFloat(float64(v), 'g', -1, 64)
	case glow.String:
		return strconv.Quote(string(v))
	case glow.Bool:
		return strconv.FormatBool(bool(v))
	case glow.Octets:
		return fmt.Sprintf("% X", []byte(v))
	case glow.Null:
		return "null"
	}
	return fmt.Sprint(v)
}

// writable reports whether a parameter may be edited. Parameters without an
// access property are treated as writable and the provider decides.
func writable(p *glow.Parameter) bool {
	return p.Access == nil || *p.Access == glow.AccessWrite || *p.Access == glow.AccessReadWrite
}
