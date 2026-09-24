package tui

// TerminalRenderWidth leaves the terminal's last physical column unused.
// Writing into that cell enables delayed autowrap in several terminals; a
// following CRLF can then consume an extra row and overwrite Bubble Tea's
// fixed footer while its diff renderer still believes that footer is intact.
func TerminalRenderWidth(termW int) int {
	if termW <= 0 {
		termW = 80
	}
	if termW == 1 {
		return 1
	}
	return termW - 1
}

// ChromeContentWidth 与 viewport / 输入框对齐的可视内容宽度（body 左右各 1 列 padding）。
func ChromeContentWidth(termW int) int {
	if termW <= 0 {
		termW = 80
	}
	// Never render wider than the terminal. The previous 40-column floor made
	// resize/minimized windows wrap every TUI row and corrupt Bubble Tea's
	// vertical diff, which looked like duplicated or displaced content.
	return max(1, termW-2)
}
