//go:build !darwin

package tui

import tea "github.com/charmbracelet/bubbletea"

func startMacOSCmdVWatcher(*tea.Program) func() {
	return func() {}
}
