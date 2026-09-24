package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	charmansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestConhostTrueColorKeepsDesktopTheme(t *testing.T) {
	t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", "1")
	t.Setenv("DEEPSENTRY_COLOR", "1")
	old := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(old); applyTerminalPalette(darkTerminalPalette()) })
	lipgloss.SetColorProfile(termenv.TrueColor)
	ConfigureTerminalPreferences("dark")
	if colorBg != darkTerminalPalette().bg || colorAccent != darkTerminalPalette().accent {
		t.Fatal("truecolor conhost was downgraded to neon ANSI colors")
	}
	ConfigureTerminalPreferences("light")
	if colorBg != lightTerminalPalette().bg {
		t.Fatal("explicit light theme lost")
	}
}

func TestConhostFrameUsesAbsoluteRowsAndPreservesDiffSkips(t *testing.T) {
	t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", "1")
	input := charmansi.CursorHomePosition + "中文 · x≈1\r\n\n第三行│…" + charmansi.CursorPosition(0, 3)
	got := string(conhostFrame([]byte(input)))
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("relative newline remains: %q", got)
	}
	for _, want := range []string{"\x1b[?7l", charmansi.CursorPosition(1, 1), charmansi.CursorPosition(1, 2), charmansi.CursorPosition(1, 3), "中文 . x=1", "第三行|~"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\x1b[?7h") {
		t.Fatal("wrap mode not restored")
	}
	cleanup := []byte("\x1b[?1049l\x1b[?25h")
	if string(conhostFrame(cleanup)) != string(cleanup) {
		t.Fatal("cleanup modified")
	}
}

func TestConhostFullFrameKeepsHeaderAndFitsCJKCells(t *testing.T) {
	t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", "1")
	t.Setenv("DEEPSENTRY_FANCY", "")
	t.Setenv("DEEPSENTRY_EMOJI", "")
	t.Setenv("DEEPSENTRY_COLOR", "1")
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old); applyTerminalPalette(darkTerminalPalette()) })
	ConfigureTerminalPreferences("dark")
	for _, size := range [][2]int{{80, 24}, {120, 30}, {160, 38}, {100, 20}} {
		m := NewAgentModel(nil, "deepseek / deepseek-flash . ctx≈1.00M[官方模型目录]", "本地模式", 100, true, false, StartupInfo{Version: ui.Version, ModelInfo: "deepseek / deepseek-flash . ctx≈1.00M[官方模型目录]", OS: "Windows", WorkDir: `E:\Deepsentry`, ChatSummary: "已启用：企业微信", AwaitGoal: true})
		m.width, m.height = size[0], size[1]
		m.recalcLayout()
		m.refreshViewport()
		_, frame := stripFrameMarkers([]byte(m.View()))
		if !strings.Contains(strings.Split(string(frame), "\n")[0], "DeepSentry") {
			t.Fatal("header clipped")
		}
		lines := strings.Split(string(frame), "\n")
		if len(lines) > size[1] {
			t.Fatalf("frame overflows %v: %d", size, len(lines))
		}
		for row, line := range lines {
			physical := conhostSymbols.Replace(charmansi.Strip(line))
			if charmansi.StringWidth(physical) >= size[0] {
				t.Fatalf("row %d overflows %v: %q", row, size, physical)
			}
			if strings.ContainsAny(physical, "·≈…│─↑↓") {
				t.Fatalf("ambiguous chrome remains: %q", physical)
			}
			for _, r := range physical {
				if r > 0xFFFF {
					t.Fatalf("emoji survives into the legacy frame: %q", physical)
				}
			}
		}
	}
}

func TestResizeWatchOnlySendsValidChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 6)
	for i := 0; i < 6; i++ {
		ticks <- time.Now()
	}
	queries := 0
	sizes := [][2]int{{120, 30}, {120, 30}, {0, 0}, {100, 20}, {100, 20}, {80, 24}}
	var got []tea.WindowSizeMsg
	watchTerminalSize(ctx, ticks, func() (int, int, error) {
		s := sizes[queries]
		queries++
		if queries == 6 {
			cancel()
		}
		if s[0] == 0 {
			return 0, 0, errors.New("minimized")
		}
		return s[0], s[1], nil
	}, func(msg tea.Msg) { got = append(got, msg.(tea.WindowSizeMsg)) })
	if len(got) != 3 || got[0].Width != 120 || got[1].Width != 100 || got[2].Width != 80 {
		t.Fatalf("wrong resize stream: %v", got)
	}
}

// Exercise the real Bubble Tea diff renderer, including its empty skipped
// rows, rather than only asserting the width of a model's View string.
type consoleRenderFixture struct{ AgentModel }

func (m consoleRenderFixture) Init() tea.Cmd {
	return tea.Sequence(
		func() tea.Msg { return tea.WindowSizeMsg{Width: 120, Height: 30} },
		tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 24} }),
		tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tea.QuitMsg{} }),
	)
}

func TestConhostActualRendererOutput(t *testing.T) {
	t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", "1")
	t.Setenv("DEEPSENTRY_COLOR", "1")
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old); applyTerminalPalette(darkTerminalPalette()) })
	ConfigureTerminalPreferences("dark")
	f, err := os.CreateTemp(t.TempDir(), "console-*.ansi")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := NewAgentModel(nil, "deepseek / deepseek-flash", "本地模式", 100, true, false, StartupInfo{Version: ui.Version, ModelInfo: "deepseek / deepseek-flash", OS: "Windows", AwaitGoal: true})
	p := tea.NewProgram(consoleRenderFixture{m}, tea.WithInput(nil), tea.WithOutput(newInputCursorOutput(f, m.cursorAnchor)), tea.WithAltScreen(), tea.WithoutSignalHandler())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, cursorFrameMarkerPrefix) || strings.Contains(text, footerFrameMarkerPrefix) {
		t.Fatal("private markers leaked")
	}
	if !strings.Contains(text, "DeepSentry") || !strings.Contains(text, "\x1b[?7l") || !strings.Contains(text, charmansi.CursorPosition(1, 24)) {
		t.Fatal("actual renderer did not use absolute conhost frames")
	}
	for _, frame := range strings.Split(text, "\x1b[?7l")[1:] {
		stop := strings.Index(frame, "\x1b[?7h")
		if stop < 0 || strings.ContainsAny(frame[:stop], "\r\n") {
			t.Fatalf("relative line advance in physical frame: %q", frame)
		}
	}
}

func TestConhostTrailingNewlineDoesNotAddressExtraRow(t *testing.T) {
	t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", "1")
	got := string(conhostFrame([]byte(charmansi.CursorHomePosition + "header\nfooter\n")))
	if strings.Contains(got, charmansi.CursorPosition(1, 3)) {
		t.Fatalf("extra row after final LF: %q", got)
	}
	if !strings.Contains(got, charmansi.CursorPosition(1, 2)+"footer") {
		t.Fatalf("footer missing: %q", got)
	}
}
