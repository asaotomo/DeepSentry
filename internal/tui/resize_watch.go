package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"time"
)

// Windows has no SIGWINCH. Query the visible window, not the scrollback
// buffer, and notify both the model and Bubble Tea renderer only on changes.
func watchTerminalSize(ctx context.Context, ticks <-chan time.Time, query func() (int, int, error), send func(tea.Msg)) {
	lastW, lastH := 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			w, h, err := query()
			if err != nil || w <= 0 || h <= 0 || (w == lastW && h == lastH) {
				continue
			}
			send(tea.WindowSizeMsg{Width: w, Height: h})
			lastW, lastH = w, h
		}
	}
}
