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

func TestCtrlZUndoesInputEdits(t *testing.T) {
	m := selectionModel()
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("大三")})
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("大四")})
	if m.currentInputValue() != "大三大四" {
		t.Fatalf("typed %q", m.currentInputValue())
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if m.currentInputValue() != "大三" {
		t.Fatalf("undo last edit: %q", m.currentInputValue())
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.currentInputValue() != "" {
		t.Fatal("ctrl+u did not clear")
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if m.currentInputValue() != "大三" {
		t.Fatalf("undo clear: %q", m.currentInputValue())
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("尾部"), Paste: true})
	if m.currentInputValue() != "大三尾部" {
		t.Fatalf("paste: %q", m.currentInputValue())
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if m.currentInputValue() != "大三" {
		t.Fatalf("undo paste: %q", m.currentInputValue())
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if m.currentInputValue() != "" {
		t.Fatalf("undo to empty: %q", m.currentInputValue())
	}
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	if m.currentInputValue() != "" {
		t.Fatal("extra ctrl+z changed empty input")
	}
}

func TestMacEditChordStaysInsideInput(t *testing.T) {
	msg, ok := macEditChordToSwallow(cgEventFlagCommand, 0x00)
	if !ok {
		t.Fatal("command+a should be handled by the input")
	}
	if _, isSelect := msg.(macosCmdAMsg); !isSelect {
		t.Fatalf("command+a message: %#v", msg)
	}
	msg, ok = macEditChordToSwallow(cgEventFlagCommand, 0x06)
	if _, isUndo := msg.(macosCmdZMsg); !ok || !isUndo {
		t.Fatalf("command+z message: %#v %v", msg, ok)
	}
	if _, ok := macEditChordToSwallow(cgEventFlagCommand, 0x09); ok {
		t.Fatal("command+v must stay with the terminal paste path")
	}
	if _, ok := macEditChordToSwallow(cgEventFlagCommand|cgEventFlagShift, 0x00); ok {
		t.Fatal("shifted command+a is not input select-all")
	}
}

func TestMacCommandSelectAllAndUndo(t *testing.T) {
	m := selectionModel()
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("大三")})
	m = inputKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("大四")})
	next, _ := m.Update(macosCmdAMsg{})
	m = next.(AgentModel)
	if !m.inputAllSelected || m.currentInputValue() != "大三大四" {
		t.Fatalf("command+a select-all: selected=%v value=%q", m.inputAllSelected, m.currentInputValue())
	}
	next, _ = m.Update(macosCmdZMsg{})
	m = next.(AgentModel)
	if m.currentInputValue() != "大三" {
		t.Fatalf("command+z undo: %q", m.currentInputValue())
	}
	if !isSelectAllKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}) && !isSelectAllKey(tea.KeyMsg{Type: tea.KeyCtrlA}) {
		t.Fatal("ctrl+a should still select all")
	}
}
