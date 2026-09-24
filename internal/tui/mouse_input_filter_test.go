package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func TestMouseReportsAcrossEverySplit(t *testing.T) {
	for _, report := range []string{"[<65;43;15M", "[<64;43;15M", "\x1b[<65;43;15M", "[<0;123;456m", "<35;43;15M", "<35;1;1m"} {
		for split := 1; split < len(report); split++ {
			m := selectionModel()
			m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("你好")})
			for _, chunk := range []string{report[:split], report[split:], "世界"} {
				m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(chunk)})
			}
			if got := m.currentInputValue(); got != "你好世界" {
				t.Fatalf("split %d of %q leaked: %q", split, report, got)
			}
		}
	}
}
func TestMouseReportsOneRuneAtATime(t *testing.T) {
	m := selectionModel()
	for _, r := range strings.Repeat("[<65;43;15M", 100) {
		m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.currentInputValue() != "" || m.mouseInput.pending != "" {
		t.Fatal("fragmented scroll leaked")
	}
}
func TestOrdinaryBracketsAndTimeout(t *testing.T) {
	m := selectionModel()
	for _, r := range "a[测试][123];[" {
		m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	generation := m.mouseInput.generation
	next, _ := m.Update(mouseInputTimeout(generation - 1))
	m = next.(AgentModel)
	if m.mouseInput.pending != "[" {
		t.Fatal("old timer flushed newer prefix")
	}
	next, _ = m.Update(mouseInputTimeout(generation))
	m = next.(AgentModel)
	if got := m.currentInputValue(); got != "a[测试][123];[" {
		t.Fatalf("ordinary input changed: %q", got)
	}
	if m.mouseInput.pending != "" {
		t.Fatal("flushed prefix retained")
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := m.currentInputValue(); got != "a[测试][123];[x" {
		t.Fatalf("prefix duplicated: %q", got)
	}
}
func TestPendingBracketPreservesPasteAndBackspace(t *testing.T) {
	m := selectionModel()
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.currentInputValue() != "" {
		t.Fatal("backspace did not remove pending bracket")
	}
	pasted := "[<65;43;15M"
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	if m.currentInputValue() != pasted {
		t.Fatal("literal paste modified")
	}
}
