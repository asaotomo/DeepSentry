package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"time"
)

type filteredInputKey tea.KeyMsg
type mouseInputTimeout uint64

// The terminal parser may deliver an SGR report as several KeyRunes messages.
// Buffer only a bounded possible prefix; bracketed paste bypasses this filter.
type mouseInputFilter struct {
	pending    string
	alt        bool
	generation uint64
}

func mouseReportPrefix(s string) bool {
	s = strings.TrimPrefix(s, "\x1b")
	if s == "" || s == "[" || s == "[<" || s == "<" {
		return true
	}
	if strings.HasPrefix(s, "[<") {
		s = s[2:]
	} else if strings.HasPrefix(s, "<") {
		s = s[1:]
	} else {
		return false
	}
	parts := strings.Split(s, ";")
	if len(parts) > 3 {
		return false
	}
	for i, p := range parts {
		limit := 6
		if i == 0 {
			limit = 3
		}
		if len(p) > limit || (p == "" && i < len(parts)-1) {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func (f *mouseInputFilter) flush() []tea.KeyMsg {
	if f.pending == "" {
		return nil
	}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(f.pending), Alt: f.alt}
	f.pending = ""
	f.generation++
	return []tea.KeyMsg{msg}
}

func (f *mouseInputFilter) filter(msg tea.KeyMsg) ([]tea.KeyMsg, tea.Cmd) {
	if msg.Paste || msg.Type != tea.KeyRunes {
		out := f.flush()
		return append(out, msg), nil
	}
	var out []tea.KeyMsg
	emit := func(s string, alt bool) {
		if len(out) > 0 && out[len(out)-1].Alt == alt {
			out[len(out)-1].Runes = append(out[len(out)-1].Runes, []rune(s)...)
		} else {
			out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: alt})
		}
	}
	for _, r := range msg.Runes {
		if f.pending == "" {
			if r == '[' || r == '\x1b' || r == '<' {
				f.pending = string(r)
				f.alt = msg.Alt
			} else {
				emit(string(r), msg.Alt)
			}
			continue
		}
		candidate := f.pending + string(r)
		if match := leakedMouseReport.FindString(candidate); match == candidate {
			f.pending = ""
		} else if mouseReportPrefix(candidate) {
			f.pending = candidate
		} else {
			emit(f.pending, f.alt)
			f.pending = ""
			if r == '[' || r == '\x1b' || r == '<' {
				f.pending = string(r)
				f.alt = msg.Alt
			} else {
				emit(string(r), msg.Alt)
			}
		}
	}
	f.generation++
	if f.pending == "" {
		return out, nil
	}
	generation := f.generation
	return out, tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return mouseInputTimeout(generation) })
}

func (m AgentModel) applyFilteredKeys(keys []tea.KeyMsg, timer tea.Cmd) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{timer}
	for _, key := range keys {
		next, cmd := m.Update(filteredInputKey(key))
		m = next.(AgentModel)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}
