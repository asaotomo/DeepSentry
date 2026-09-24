package tui

import (
	"regexp"
	"strings"
	"unicode"

	charmansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// sanitizeTUIText turns untrusted process/model text into inert terminal text.
//
// Full-screen TUIs must never render cursor movement, screen clearing, OSC
// hyperlinks/titles, bells, or other control sequences received from a child
// process. Programs such as sqlmap also use carriage returns to redraw progress
// on one line; emulate that redraw in memory so the user still sees the latest
// readable progress instead of letting the real terminal cursor move.
func sanitizeTUIText(s string) string {
	if s == "" {
		return ""
	}

	// Strip CSI, OSC, DCS and the other ANSI escape families first. ansi.Strip
	// intentionally preserves C0 execute characters, which are handled below.
	s = charmansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")

	var out strings.Builder
	line := make([]rune, 0, 128)
	lineWidth := 0
	flushLine := func(newline bool) {
		out.WriteString(string(line))
		if newline {
			out.WriteByte('\n')
		}
		line = line[:0]
		lineWidth = 0
	}

	for _, r := range s {
		switch r {
		case '\n':
			flushLine(true)
		case '\r':
			// A bare CR means "replace this progress line". Keep only the
			// newest version from the child-process output chunk.
			line = line[:0]
			lineWidth = 0
		case '\b':
			if len(line) > 0 {
				last := line[len(line)-1]
				line = line[:len(line)-1]
				lineWidth -= runewidth.RuneWidth(last)
				if lineWidth < 0 {
					lineWidth = 0
				}
			}
		case '\t':
			spaces := 4 - lineWidth%4
			line = append(line, []rune(strings.Repeat(" ", spaces))...)
			lineWidth += spaces
		default:
			// Drop C0/C1 controls, DEL, bidi formatting/overrides and other
			// non-printing format characters. They have no useful place in a
			// command log and can make copied/displayed results deceptive.
			if r == 0x7f || unicode.IsControl(r) || isUnsafeFormatRune(r) {
				continue
			}
			line = append(line, r)
			lineWidth += runewidth.RuneWidth(r)
		}
	}
	flushLine(false)
	return out.String()
}

func isUnsafeFormatRune(r rune) bool {
	// Preserve ZWNJ/ZWJ (U+200C/U+200D): they are required for correct Indic,
	// Arabic and joined-emoji rendering. Remove only invisible formatting that
	// can reorder/spoof a terminal log or hide text.
	switch r {
	case '\u061c', '\u200b', '\u200e', '\u200f', '\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069', '\ufeff':
		return true
	default:
		return false
	}
}

// Only applied to non-paste key events: copied code remains unchanged.
// The bracket is optional because Windows consoles sometimes consume ESC[
// and leave the SGR payload (<35;x;yM) as ordinary keystrokes.
var leakedMouseReport = regexp.MustCompile(`(?:\x1b)?\[?<\d{1,3};\d{1,6};\d{1,6}[Mm]`)
