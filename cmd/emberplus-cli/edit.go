package main

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

type mode int

const (
	modeBrowse  mode = iota
	modeInput        // text input for a parameter value or function argument
	modeEnum         // selection of an enum entry
	modeConfirm      // y/N question
)

// editState describes an edit in progress.
type editState struct {
	node  *node
	input textinput.Model

	// Parameter edits.
	param   *glow.Parameter
	typ     glow.ParameterType
	entries []glow.EnumEntry
	choice  int
	confirm glow.Value // value sent when a confirmation is accepted

	// Function invocations.
	fn   *glow.Function
	args []glow.Value
}

type setValueMsg struct {
	path      string
	requested glow.Value
	confirmed glow.Value
	err       error
}

type invokeMsg struct {
	path   string
	result *glow.InvocationResult
	err    error
}

func newInput(prompt, value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.SetValue(value)
	ti.CursorEnd()
	ti.Focus()
	return ti
}

// beginEdit starts editing the selected parameter or invoking the selected
// function.
func (m *model) beginEdit() tea.Cmd {
	n := m.selected()
	if n == nil {
		return nil
	}
	switch el := n.el.(type) {
	case *glow.Parameter:
		return m.beginParameter(n, el)
	case *glow.Function:
		m.edit = &editState{node: n, fn: el}
		return m.nextArgument()
	}
	return nil
}

func (m *model) beginParameter(n *node, p *glow.Parameter) tea.Cmd {
	if p.Access != nil && (*p.Access == glow.AccessRead || *p.Access == glow.AccessNone) {
		m.status = m.label(n) + " is read-only"
		return nil
	}
	typ, ok := p.EffectiveType()
	if !ok {
		m.status = "the type of " + m.label(n) + " is unknown"
		return nil
	}
	e := &editState{node: n, param: p, typ: typ}
	switch typ {
	case glow.TypeBoolean:
		current, _ := p.Value.(glow.Bool)
		return m.setValue(n, !current)
	case glow.TypeTrigger:
		e.confirm = triggerValue(p)
		m.edit, m.mode = e, modeConfirm
		return nil
	case glow.TypeEnum:
		for _, entry := range p.EnumEntries() {
			if !entry.Hidden {
				e.entries = append(e.entries, entry)
			}
		}
		if current, ok := p.Value.(glow.Int); ok {
			for i, entry := range e.entries {
				if entry.Value == int64(current) {
					e.choice = i
				}
			}
		}
		m.edit, m.mode = e, modeEnum
		return nil
	}
	e.input = newInput(m.label(n)+" = ", editableValue(p))
	m.edit, m.mode = e, modeInput
	return nil
}

// triggerValue is sent to fire a trigger parameter. The specification does
// not define it; the default value is used if the provider reports one.
func triggerValue(p *glow.Parameter) glow.Value {
	if p.Default != nil {
		return p.Default
	}
	return glow.Int(1)
}

// nextArgument asks for the next function argument, or for a confirmation
// if the function takes none, or invokes the function.
func (m *model) nextArgument() tea.Cmd {
	e := m.edit
	switch {
	case len(e.fn.Arguments) == 0:
		m.mode = modeConfirm
		return nil
	case len(e.args) < len(e.fn.Arguments):
		item := e.fn.Arguments[len(e.args)]
		name := item.Name
		if name == "" {
			name = fmt.Sprintf("argument %d", len(e.args)+1)
		}
		e.input = newInput(fmt.Sprintf("%s: %s (%s) = ", signature(e.fn), name, item.Type), "")
		m.mode = modeInput
		return nil
	}
	n, args := e.node, e.args
	m.endEdit()
	return m.invoke(n, args)
}

func (m *model) endEdit() {
	m.edit = nil
	m.mode = modeBrowse
}

func (m model) editKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := m.edit
	if msg.String() == "esc" || msg.String() == "ctrl+c" {
		m.endEdit()
		m.status = "cancelled"
		return m, nil
	}
	switch m.mode {
	case modeConfirm:
		switch msg.String() {
		case "y", "Y":
			n := e.node
			if e.fn != nil {
				m.endEdit()
				return m, m.invoke(n, nil)
			}
			v := e.confirm
			m.endEdit()
			return m, m.setValue(n, v)
		case "n", "N", "enter":
			m.endEdit()
			m.status = "cancelled"
		}
		return m, nil

	case modeEnum:
		switch msg.String() {
		case "up", "k":
			e.choice = max(0, e.choice-1)
		case "down", "j":
			e.choice = min(len(e.entries)-1, e.choice+1)
		case "enter":
			if len(e.entries) == 0 {
				m.endEdit()
				return m, nil
			}
			n, v := e.node, glow.Int(e.entries[e.choice].Value)
			m.endEdit()
			return m, m.setValue(n, v)
		}
		return m, nil

	case modeInput:
		if msg.String() != "enter" {
			e.input, _ = e.input.Update(msg)
			return m, nil
		}
		if e.fn != nil {
			v, err := parseValue(e.input.Value(), e.fn.Arguments[len(e.args)].Type, nil)
			if err != nil {
				m.status = err.Error()
				return m, nil
			}
			e.args = append(e.args, v)
			return m, m.nextArgument()
		}
		v, err := parseValue(e.input.Value(), e.typ, e.param)
		if err == nil {
			err = checkRange(e.param, v)
		}
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		n := e.node
		m.endEdit()
		return m, m.setValue(n, v)
	}
	return m, nil
}

func (m model) setValue(n *node, v glow.Value) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		confirmed, err := m.app.client.SetValue(ctx, n.path, v)
		return setValueMsg{path: n.path, requested: v, confirmed: confirmed, err: err}
	}
}

func (m model) invoke(n *node, args []glow.Value) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.context()
		defer cancel()
		res, err := m.app.client.Invoke(ctx, n.path, args...)
		return invokeMsg{path: n.path, result: res, err: err}
	}
}

// handleResult updates the model with the outcome of SetValue or Invoke.
func (m *model) handleResult(msg tea.Msg) {
	switch msg := msg.(type) {
	case setValueMsg:
		n := m.nodes[msg.path]
		if msg.err != nil {
			m.status = msg.err.Error()
			return
		}
		p, ok := n.el.(*glow.Parameter)
		if !ok {
			return
		}
		updated := p.Clone()
		updated.Value = msg.confirmed
		n.el = updated
		if equalValues(msg.confirmed, msg.requested) {
			m.status = m.label(n) + " set to " + formatValue(updated)
		} else {
			m.status = "the provider kept " + m.label(n) + " at " + formatValue(updated)
		}

	case invokeMsg:
		n := m.nodes[msg.path]
		if msg.err != nil {
			m.status = msg.err.Error()
			return
		}
		m.results[msg.path] = formatResult(n.el.(*glow.Function), msg.result)
		m.status = m.label(n) + ": " + m.results[msg.path]
	}
}

func formatResult(f *glow.Function, r *glow.InvocationResult) string {
	s := "ok"
	if !r.Success {
		s = "failed"
	}
	var parts []string
	for i, v := range r.Result {
		name := ""
		if i < len(f.Result) && f.Result[i].Name != "" {
			name = f.Result[i].Name + "="
		}
		parts = append(parts, name+valueString(v))
	}
	if len(parts) > 0 {
		s += ", " + strings.Join(parts, ", ")
	}
	return s
}

func equalValues(a, b glow.Value) bool {
	if ao, ok := a.(glow.Octets); ok {
		bo, ok := b.(glow.Octets)
		return ok && string(ao) == string(bo)
	}
	return a == b
}

// editableValue is the current value in the form parseValue accepts.
func editableValue(p *glow.Parameter) string {
	switch v := p.Value.(type) {
	case glow.Int:
		if p.Factor != nil && *p.Factor != 0 {
			return strconv.FormatFloat(float64(v)/float64(*p.Factor), 'g', -1, 64)
		}
		return strconv.FormatInt(int64(v), 10)
	case glow.Real:
		return strconv.FormatFloat(float64(v), 'g', -1, 64)
	case glow.String:
		return string(v)
	case glow.Octets:
		return fmt.Sprintf("% x", []byte(v))
	}
	return ""
}

// parseValue converts user input to a value of type t. For integer
// parameters with a factor, the input is the displayed value, which is
// multiplied by the factor (spec: "Factor").
func parseValue(s string, t glow.ParameterType, p *glow.Parameter) (glow.Value, error) {
	s = strings.TrimSpace(s)
	switch t {
	case glow.TypeInteger:
		if p != nil && p.Factor != nil && *p.Factor != 0 {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", s)
			}
			return glow.Int(math.Round(f * float64(*p.Factor))), nil
		}
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", s)
		}
		return glow.Int(i), nil
	case glow.TypeReal:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", s)
		}
		return glow.Real(f), nil
	case glow.TypeBoolean:
		switch strings.ToLower(s) {
		case "true", "1", "on", "yes":
			return glow.Bool(true), nil
		case "false", "0", "off", "no":
			return glow.Bool(false), nil
		}
		return nil, fmt.Errorf("%q is not a boolean", s)
	case glow.TypeOctets:
		b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
		if err != nil {
			return nil, fmt.Errorf("%q is not hexadecimal", s)
		}
		return glow.Octets(b), nil
	case glow.TypeString:
		return glow.String(s), nil
	}
	return nil, fmt.Errorf("values of type %v cannot be entered", t)
}

// checkRange checks a value against minimum and maximum. Both are in the
// same unit as the value (spec: "Factor").
func checkRange(p *glow.Parameter, v glow.Value) error {
	num := func(v glow.Value) (float64, bool) {
		switch v := v.(type) {
		case glow.Int:
			return float64(v), true
		case glow.Real:
			return float64(v), true
		}
		return 0, false
	}
	x, ok := num(v)
	if !ok {
		return nil
	}
	if lo, ok := num(p.Minimum); ok && x < lo {
		return fmt.Errorf("the minimum is %s", valueString(p.Minimum))
	}
	if hi, ok := num(p.Maximum); ok && x > hi {
		return fmt.Errorf("the maximum is %s", valueString(p.Maximum))
	}
	return nil
}

// editView renders the editor for the status line, or "" when browsing.
func (m model) editView() string {
	e := m.edit
	switch m.mode {
	case modeInput:
		return e.input.View()
	case modeConfirm:
		if e.fn != nil {
			return "invoke " + signature(e.fn) + "? [y/N]"
		}
		return "fire trigger " + m.label(e.node) + "? [y/N]"
	case modeEnum:
		return "select " + m.label(e.node) + " with ↑↓, Enter to set, Esc to cancel"
	}
	return ""
}

// enumView renders the enum choices in place of the details.
func (m model) enumView() string {
	lines := []string{keyStyle.Render(m.label(m.edit.node))}
	for i, e := range m.edit.entries {
		line := "  " + e.Name
		if i == m.edit.choice {
			line = selectedStyle.Render("› " + e.Name)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
