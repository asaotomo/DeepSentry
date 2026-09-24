package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// displayWidth follows grapheme clusters instead of adding rune widths. This
// keeps emoji presentation selectors, skin tones, keycaps and ZWJ sequences
// aligned with how terminals render them.
func displayWidth(s string) int {
	return ansi.StringWidth(s)
}

func truncateDisplay(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, tail)
}

func wrapDisplayClusters(s string, width int) string {
	if width <= 0 {
		return s
	}
	// Hardwrap with preserveSpace keeps every byte of commands/code intact;
	// only display newlines are inserted.
	return ansi.Hardwrap(s, width, true)
}

func firstDisplayCluster(s string) (cluster, rest string) {
	cluster, rest, _, _ = uniseg.FirstGraphemeClusterInString(s, -1)
	return cluster, rest
}

// normalizeEmojiSpacing adds a single visual separator where an emoji touches
// human-readable text. It operates on grapheme clusters so keycaps, variation
// selectors, flags, skin tones and ZWJ emoji are never split. Commands and raw
// evidence deliberately bypass this helper; it is only used by Markdown UI.
func normalizeEmojiSpacing(s string) string {
	if s == "" {
		return s
	}
	clusters := make([]string, 0, len(s))
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		clusters = append(clusters, g.Str())
	}
	if len(clusters) < 2 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, cluster := range clusters {
		if i > 0 {
			prev := clusters[i-1]
			if !clusterHasWhitespace(prev) && !clusterHasWhitespace(cluster) &&
				((isEmojiCluster(prev) && isWordCluster(cluster)) ||
					(isWordCluster(prev) && isEmojiCluster(cluster))) {
				b.WriteByte(' ')
			}
		}
		b.WriteString(cluster)
	}
	return b.String()
}

func clusterHasWhitespace(cluster string) bool {
	for _, r := range cluster {
		if unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func isWordCluster(cluster string) bool {
	for _, r := range cluster {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	return false
}

func isEmojiCluster(cluster string) bool {
	for _, r := range cluster {
		switch {
		case r == '\u20e3', r == '\u200d', r == '\ufe0f':
			return true
		case r >= 0x1f000 && r <= 0x1faff:
			return true
		case r >= 0x2600 && r <= 0x27bf:
			return true
		case r >= 0x1f1e6 && r <= 0x1f1ff:
			return true
		case r >= 0x1f3fb && r <= 0x1f3ff:
			return true
		}
	}
	return false
}

// Width styles do not truncate content. This final guard prevents one
// overlong grapheme from pushing a bordered panel's right edge to a new row.
func fitRenderedBlock(s string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = truncateDisplay(line, width, "")
	}
	return strings.Join(lines, "\n")
}

// Lipgloss Width includes padding but excludes borders and margins. Keep this
// separate from the actual content width (which excludes the entire frame).
func styleRenderWidth(style lipgloss.Style, totalWidth int) int {
	return max(1, totalWidth-style.GetHorizontalMargins()-style.GetHorizontalBorderSize())
}
