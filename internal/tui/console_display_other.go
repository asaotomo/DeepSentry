//go:build !windows

package tui

import tea "github.com/charmbracelet/bubbletea"

func prepareConsoleDisplay() func()                   { return func() {} }
func startConsoleResizeWatcher(p *tea.Program) func() { return func() {} }
