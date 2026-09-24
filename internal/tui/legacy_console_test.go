package tui

import (
	"strings"
	"testing"

	"ai-edr/internal/ui"
	"github.com/charmbracelet/lipgloss"
	charmansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestLegacyConsoleUsesASCIIThinkingSpinner(t *testing.T) {
	t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", "1")
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	for _, frame := range m.spinner.Spinner.Frames {
		for _, r := range frame {
			if r > 127 {
				t.Fatalf("thinking spinner is not ASCII: %q", frame)
			}
		}
	}
	if m.spinner.View() == "" || strings.ContainsAny(m.spinner.View(), "⣾⣽⣻⢿") {
		t.Fatalf("spinner view: %q", m.spinner.View())
	}
}

func TestLegacyConsoleFrameAndPalette(t *testing.T) {
	t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", "1")
	t.Setenv("DEEPSENTRY_FANCY", "")
	t.Setenv("DEEPSENTRY_EMOJI", "")
	t.Setenv("DEEPSENTRY_COLOR", "1")
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(old); applyTerminalPalette(darkTerminalPalette()) })
	ConfigureTerminalPreferences("auto")
	if !ui.PlainTextMode() || colorBg != "0" || colorText != "15" {
		t.Fatal("legacy palette not selected")
	}
	frame := paintFrameBackground("中文\x1b[0m padding\nnext")
	if !strings.Contains(frame, "\x1b[0m\x1b[40m padding") {
		t.Fatalf("background reset not restored: %q", frame)
	}
	if strings.Contains(frame, "48;2") {
		t.Fatal("legacy frame uses RGB")
	}
	if terminalBorder().TopLeft != "+" {
		t.Fatal("legacy border must be ASCII")
	}
	thought := renderWrapped(lipgloss.NewStyle(), "[17:18:39] 💭 内存不是瓶颈，因此补查CPU 逻辑核数", 36)
	if strings.Contains(thought, "💭") {
		t.Fatalf("thought icon still wide: %q", thought)
	}
	for _, line := range strings.Split(thought, "\n") {
		if charmansi.StringWidth(charmansi.Strip(line)) > 36 {
			t.Fatalf("legacy thought row overflows: %q", line)
		}
	}
	TestAgentViewFitsTerminalAcrossSizesAndStates(t)
}

func TestNoColorFrameDoesNotAddEscapes(t *testing.T) {
	t.Setenv("DEEPSENTRY_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("DEEPSENTRY_NO_COLOR", "1")
	if got := paintFrameBackground("text\n中文"); got != "text\n中文" {
		t.Fatalf("unexpected escapes: %q", got)
	}
}
