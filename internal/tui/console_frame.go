package tui

import (
	"bytes"
	"strings"

	"ai-edr/internal/ui"
	charmansi "github.com/charmbracelet/x/ansi"
)

// conhostSymbols are one-cell decorations in our layout but may occupy two
// cells with a CJK conhost font. Keep source text and copied content unchanged.
var conhostSymbols = strings.NewReplacer(
	"·", ".", "≈", "=", "…", "~", "│", "|", "─", "-",
	"╭", "+", "╮", "+", "╰", "+", "╯", "+",
	"┌", "+", "┐", "+", "└", "+", "┘", "+",
	"├", "+", "┤", "+", "┬", "+", "┴", "+", "┼", "+",
	"↑", "^", "↓", "v", "←", "<", "→", ">", "↵", "<",
	"▸", ">", "▶", ">", "●", "o", "•", "*",
)

// conhostFrame preserves Bubble Tea's row-diff semantics but uses absolute
// addressing instead of LF/CRLF. With wrapping disabled, a font disagreement
// cannot scroll the screen and displace all subsequent rows. Control-only
// writes (enter/leave alternate screen, cleanup) pass through unchanged.
func conhostFrame(p []byte) []byte {
	if !ui.LegacyConsole() || !bytes.HasPrefix(p, []byte(charmansi.CursorHomePosition)) {
		return p
	}
	var out strings.Builder
	out.WriteString("\x1b[?7l")
	row := 1
	// A trailing LF terminates the final rendered row; it does not start
	// another row. Addressing one past the viewport scrolls conhost.
	lines := strings.Split(strings.TrimSuffix(string(p), "\n"), "\n")
	for _, line := range lines {
		out.WriteString(charmansi.CursorPosition(1, row))
		// Drop emoji after layout as well. A glyph the font cannot draw would
		// otherwise paint more cells than the row was wrapped for.
		out.WriteString(ui.CollapseLegacyEmoji(conhostSymbols.Replace(strings.ReplaceAll(line, "\r", ""))))
		row++
	}
	out.WriteString("\x1b[?7h")
	return []byte(out.String())
}
