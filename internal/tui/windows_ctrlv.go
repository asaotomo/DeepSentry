package tui

// windowsPasteTargetFocused reports whether the foreground process is this
// DeepSentry process or one of its ancestors. Windows Terminal swallows
// Ctrl+V, so the image paste watcher has to see the chord itself, but only
// while this session's console is the active window.
func windowsPasteTargetFocused(self int, ancestors []int, foreground int) bool {
	if self <= 0 || foreground <= 0 {
		return false
	}
	if foreground == self {
		return true
	}
	for _, pid := range ancestors {
		if pid == foreground {
			return true
		}
	}
	return false
}
