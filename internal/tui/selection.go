package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

var styleSelection = lipgloss.NewStyle().
	Foreground(colorBg).
	Background(colorAccent)

// extractPlainSelection returns plain text for a row/display-column rectangle in viewportPlain lines.
func extractPlainSelection(plain string, r1, c1, r2, c2 int) string {
	lines := strings.Split(plain, "\n")
	if len(lines) == 0 {
		return ""
	}
	if r1 > r2 {
		r1, r2 = r2, r1
		c1, c2 = c2, c1
	} else if r1 == r2 && c1 > c2 {
		c1, c2 = c2, c1
	}
	if r1 < 0 {
		r1 = 0
	}
	if r2 >= len(lines) {
		r2 = len(lines) - 1
	}
	if r1 > r2 {
		return ""
	}

	var parts []string
	for row := r1; row <= r2; row++ {
		line := lines[row]
		start, end := 0, runewidth.StringWidth(line)
		if row == r1 {
			start = clampCol(c1, runewidth.StringWidth(line))
		}
		if row == r2 {
			end = clampCol(c2+1, runewidth.StringWidth(line))
		}
		if start >= end {
			continue
		}
		parts = append(parts, sliceDisplayColumns(line, start, end))
	}
	return strings.Join(parts, "\n")
}

func clampCol(col, lineLen int) int {
	if col < 0 {
		return 0
	}
	if col > lineLen {
		return lineLen
	}
	return col
}

func normalizeSelection(r1, c1, r2, c2 int) (int, int, int, int) {
	if r1 > r2 || (r1 == r2 && c1 > c2) {
		return r2, c2, r1, c1
	}
	return r1, c1, r2, c2
}

func selectionCharCount(text string) int {
	return len([]rune(text))
}

func sliceDisplayColumns(line string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end <= start {
		return ""
	}
	var b strings.Builder
	col := 0
	for _, r := range line {
		w := runewidth.RuneWidth(r)
		if w <= 0 {
			w = 1
		}
		next := col + w
		if next > start && col < end {
			b.WriteRune(r)
		}
		if col >= end {
			break
		}
		col = next
	}
	return b.String()
}

func renderSelectionPlain(plain string, visibleStart, visibleHeight, r1, c1, r2, c2 int) string {
	lines := strings.Split(plain, "\n")
	if visibleStart >= len(lines) || visibleHeight <= 0 {
		return ""
	}
	r1, c1, r2, c2 = normalizeSelection(r1, c1, r2, c2)
	visibleEnd := visibleStart + visibleHeight
	if visibleEnd > len(lines) {
		visibleEnd = len(lines)
	}
	out := make([]string, 0, visibleEnd-visibleStart)
	for row := visibleStart; row < visibleEnd; row++ {
		line := lines[row]
		lineW := runewidth.StringWidth(line)
		if row < r1 || row > r2 || lineW == 0 {
			out = append(out, line)
			continue
		}
		start, end := 0, lineW
		if row == r1 {
			start = clampCol(c1, lineW)
		}
		if row == r2 {
			end = clampCol(c2+1, lineW)
		}
		if start >= end {
			out = append(out, line)
			continue
		}
		before := sliceDisplayColumns(line, 0, start)
		selected := sliceDisplayColumns(line, start, end)
		after := sliceDisplayColumns(line, end, lineW)
		out = append(out, before+styleSelection.Render(selected)+after)
	}
	return strings.Join(out, "\n")
}

func renderSelectionStyled(styled, plain string, visibleStart, visibleHeight, r1, c1, r2, c2 int) string {
	plainLines := strings.Split(plain, "\n")
	styledLines := strings.Split(styled, "\n")
	if visibleStart >= len(plainLines) || visibleHeight <= 0 {
		return styled
	}
	r1, c1, r2, c2 = normalizeSelection(r1, c1, r2, c2)
	visibleEnd := visibleStart + visibleHeight
	if visibleEnd > len(plainLines) {
		visibleEnd = len(plainLines)
	}
	out := make([]string, 0, len(styledLines))
	for i, styledLine := range styledLines {
		row := visibleStart + i
		if row >= visibleEnd || row >= len(plainLines) {
			out = append(out, styledLine)
			continue
		}
		line := plainLines[row]
		lineW := runewidth.StringWidth(line)
		if row < r1 || row > r2 || lineW == 0 {
			out = append(out, styledLine)
			continue
		}
		start, end := 0, lineW
		if row == r1 {
			start = clampCol(c1, lineW)
		}
		if row == r2 {
			end = clampCol(c2+1, lineW)
		}
		if start >= end {
			out = append(out, styledLine)
			continue
		}
		out = append(out, highlightStyledLine(styledLine, line, start, end))
	}
	return strings.Join(out, "\n")
}

func highlightStyledLine(styledLine, plainLine string, start, end int) string {
	var before, after strings.Builder
	activeSGR := ""
	col := 0
	for i := 0; i < len(styledLine); {
		if seq, next, ok := readANSISeq(styledLine, i); ok {
			if col < start {
				before.WriteString(seq)
			} else if col >= end {
				after.WriteString(seq)
			}
			if col < end {
				activeSGR = updateActiveSGR(activeSGR, seq)
			}
			i = next
			continue
		}
		r, size := utf8.DecodeRuneInString(styledLine[i:])
		w := runewidth.RuneWidth(r)
		if w <= 0 {
			w = 1
		}
		nextCol := col + w
		chunk := styledLine[i : i+size]
		switch {
		case nextCol <= start:
			before.WriteString(chunk)
		case col >= end:
			after.WriteString(chunk)
		}
		col = nextCol
		i += size
	}
	selected := sliceDisplayColumns(plainLine, start, end)
	if selected == "" {
		return styledLine
	}
	afterText := after.String()
	if afterText != "" && activeSGR != "" {
		afterText = activeSGR + afterText
	}
	return before.String() + styleSelection.Render(selected) + afterText
}

func readANSISeq(s string, i int) (seq string, next int, ok bool) {
	if i+2 >= len(s) || s[i] != 0x1b || s[i+1] != '[' {
		return "", i, false
	}
	for j := i + 2; j < len(s); j++ {
		ch := s[j]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
			return s[i : j+1], j + 1, true
		}
	}
	return "", i, false
}

func updateActiveSGR(active, seq string) string {
	if !strings.HasSuffix(seq, "m") {
		return active
	}
	if seq == "\x1b[m" || seq == "\x1b[0m" {
		return ""
	}
	return active + seq
}
