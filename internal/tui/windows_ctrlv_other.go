//go:build !windows

package tui

import tea "github.com/charmbracelet/bubbletea"

func startWindowsCtrlVWatcher(*tea.Program) func() {
	return func() {}
}
