package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestChatHeaderFitsWithChineseAndJoinedEmoji(t *testing.T) {
	for _, legacy := range []string{"0", "1"} {
		t.Run(legacy, func(t *testing.T) {
			t.Setenv("DEEPSENTRY_LEGACY_CONSOLE", legacy)
			m := NewAgentModel(nil, "安全审计 👩‍💻 / Windows Agent · 中文标题与很长的上下文名称", "本地模式", 30, true, false, StartupInfo{})
			for _, width := range []int{24, 40, 80} {
				header := m.renderHeader(TerminalRenderWidth(width))
				for _, line := range strings.Split(header, "\n") {
					plain := ansi.Strip(line)
					if legacy == "1" {
						plain = conhostSymbols.Replace(plain)
					}
					if got := displayWidth(plain); got >= width {
						t.Fatalf("header width %d exceeds terminal %d: %q", got, width, plain)
					}
				}
			}
		})
	}
}
