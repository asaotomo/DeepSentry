package tui

import (
	"ai-edr/internal/harness"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSanitizeTUITextNeutralizesTerminalInjection(t *testing.T) {
	in := "safe\x1b[2J\x1b[999;1H moved\x1b]0;owned\x07\a\x00 end"
	got := sanitizeTUIText(in)
	if strings.ContainsAny(got, "\x1b\a\x00") {
		t.Fatalf("terminal controls survived: %q", got)
	}
	if got != "safe moved end" {
		t.Fatalf("sanitized text=%q", got)
	}
}

func TestSanitizeTUITextKeepsLatestCarriageReturnProgress(t *testing.T) {
	in := "[10%] testing\r[55%] testing\r[100%] done\nnext\b!\tcolumn"
	got := sanitizeTUIText(in)
	want := "[100%] done\nnex!    column"
	if got != want {
		t.Fatalf("sanitized progress=%q want=%q", got, want)
	}
}

func TestSanitizeTUITextPreservesJoinedEmojiButDropsBidiOverrides(t *testing.T) {
	got := sanitizeTUIText("👨‍💻 safe\u202eevil")
	if got != "👨‍💻 safeevil" {
		t.Fatalf("unicode sanitization=%q", got)
	}
}

func TestCommandOutputIsSanitizedAndFitsViewport(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width = 56
	m.height = 20
	m.recalcLayout()
	m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "old\r\x1b[2Jsqlmap result: parameter id is injectable\x1b[999;1H\n"})
	m.refreshViewport()

	view := m.viewport.View()
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b[999;1H") {
		t.Fatalf("unsafe child-process escape reached viewport: %q", view)
	}
	if !strings.Contains(stripANSIForTest(view), "sqlmap result") {
		t.Fatalf("readable command result missing: %q", stripANSIForTest(view))
	}
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > m.viewport.Width {
			t.Fatalf("line width=%d want <=%d: %q", got, m.viewport.Width, stripANSIForTest(line))
		}
	}
}
