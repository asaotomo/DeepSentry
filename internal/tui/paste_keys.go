package tui

import (
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func isClipboardPasteShortcut(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyCtrlV {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(msg.String())) {
	case "ctrl+v", "ctrl+shift+v", "cmd+v", "cmd+shift+v", "super+v", "super+shift+v":
		return true
	default:
		return false
	}
}

func pasteShortcutHelp() string {
	if runtime.GOOS == "darwin" {
		return "⌘V 粘贴图片/文本"
	}
	return "Ctrl+V 粘贴图片/文本"
}

func editShortcutHelp() string {
	if runtime.GOOS == "darwin" {
		return "⌘A 全选 · ⌘Z 撤销"
	}
	return "Ctrl+A 全选 · Ctrl+Z 撤销"
}
