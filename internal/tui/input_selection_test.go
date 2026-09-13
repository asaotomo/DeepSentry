package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func selectionModel() AgentModel {
	m := NewAgentModel(nil, "test", "local", 50, true, false, StartupInfo{})
	m.width, m.height = 100, 30
	m.recalcLayout()
	return m
}

func inputKey(m AgentModel, key tea.KeyMsg) AgentModel {
	next, _ := m.Update(key)
	return next.(AgentModel)
}

func TestInputSelectAllDeleteDraft(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyBackspace, tea.KeyDelete} {
		m := selectionModel()
		m.acceptPaste(strings.Repeat("长文本\n", 100))
		m.acceptPaste("另一行\n中文abc")
		before := m.currentInputValue()
		m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlA})
		if !m.inputAllSelected || m.currentInputValue() != before {
			t.Fatal("select all changed draft or failed")
		}
		m = inputKey(m, tea.KeyMsg{Type: key})
		if m.inputAllSelected || m.currentInputValue() != "" || len(m.draftParts) != 0 {
			t.Fatal("delete did not clear complete draft")
		}
	}
}

func TestInputSelectAllReplaceAndCancel(t *testing.T) {
	for _, paste := range []bool{false, true} {
		m := selectionModel()
		m.acceptPaste("旧内容\n第二行")
		m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlA})
		m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("替换"), Paste: paste})
		if m.currentInputValue() != "替换" || m.inputAllSelected {
			t.Fatalf("replacement: %q", m.currentInputValue())
		}
	}
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyLeft} {
		m := selectionModel()
		m.acceptPaste("保持内容")
		m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlA})
		m = inputKey(m, tea.KeyMsg{Type: key})
		if m.inputAllSelected || m.currentInputValue() != "保持内容" || !m.inputFocused() {
			t.Fatal("cancel changed contents/focus")
		}
	}
}

func TestSelectAllKeyRecognizesCtrlA(t *testing.T) {
	if !isSelectAllKey(tea.KeyMsg{Type: tea.KeyCtrlA}) {
		t.Fatal("ctrl+a should be select-all")
	}
	if isSelectAllKey(tea.KeyMsg{Type: tea.KeyCtrlU}) {
		t.Fatal("ctrl+u is clear, not select-all")
	}
}

func TestInputLeakedMouseReportIsNotTyped(t *testing.T) {
	report := "[<65;105;37M[<65;105;37M"
	m := selectionModel()
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(report)})
	if m.currentInputValue() != "" {
		t.Fatal("mouse reports leaked into input")
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(report), Paste: true})
	if m.currentInputValue() != report {
		t.Fatal("literal pasted text was modified")
	}
}
