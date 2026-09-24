package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func (m *AgentModel) mouseToContentCoord(x, y int) (row, col int, ok bool) {
	bodyH := m.viewport.Height
	if bodyH <= 0 {
		bodyH = max(4, m.height-6)
	}
	if y < 1 || y > bodyH {
		return 0, 0, false
	}
	row = m.viewport.YOffset + (y - 1)
	col = x - 1
	if col < 0 {
		col = 0
	}
	lines := strings.Split(m.viewportPlain, "\n")
	if row < 0 || row >= len(lines) {
		return 0, 0, false
	}
	lineW := runewidth.StringWidth(lines[row])
	if lineW == 0 {
		return row, 0, true
	}
	if col >= lineW {
		col = lineW - 1
	}
	return row, col, true
}

func (m *AgentModel) hasCopySelection() bool {
	if !m.selActive {
		return false
	}
	return strings.TrimSpace(m.selectedPlainText()) != ""
}

func (m *AgentModel) selectedPlainText() string {
	return extractPlainSelection(m.viewportPlain, m.selRow1, m.selCol1, m.selRow2, m.selCol2)
}

func (m *AgentModel) copyTextCmd(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return tea.Batch(
		func() tea.Msg {
			if err := copyToClipboard(text); err != nil {
				return copyToastMsg{err: err.Error()}
			}
			return copyToastMsg{chars: selectionCharCount(text)}
		},
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return copyToastClearMsg{} }),
	)
}

func (m *AgentModel) copyToastText(msg copyToastMsg) string {
	if msg.err != "" {
		return fmt.Sprintf("copy failed: %s", msg.err)
	}
	return fmt.Sprintf("copied %d chars to clipboard · 鼠标拖选或 Ctrl+C 复制", msg.chars)
}

func (m AgentModel) viewportView() string {
	if !m.selActive {
		return m.viewport.View()
	}
	return renderSelectionStyled(m.viewport.View(), m.viewportPlain, m.viewport.YOffset, m.viewport.Height, m.selRow1, m.selCol1, m.selRow2, m.selCol2)
}
