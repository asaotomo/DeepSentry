package ui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestRobotLogoLinesAligned(t *testing.T) {
	tests := []struct {
		name, fancy, plain string
	}{
		{name: "fancy", fancy: "1"},
		{name: "plain", plain: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DEEPSENTRY_FANCY", test.fancy)
			t.Setenv("DEEPSENTRY_PLAIN", test.plain)
			lines := RobotLogoLines()
			if len(lines) != 9 {
				t.Fatalf("expected 9 lines, got %d", len(lines))
			}
			for i, line := range lines {
				if got := runewidth.StringWidth(line); got != robotLogoWidth {
					t.Fatalf("line %d width=%d want %d: %q", i, got, robotLogoWidth, line)
				}
			}
			assertSentryCentered(t, lines[5])
		})
	}
}

func assertSentryCentered(t *testing.T, line string) {
	t.Helper()
	if !strings.Contains(line, "SENTRY") {
		t.Fatalf("missing SENTRY in body line: %q", line)
	}
	start := strings.Index(line, "SENTRY")
	border := "|"
	if strings.Contains(line, "\u2502") {
		border = "\u2502"
	}
	leftBorder := strings.Index(line, border)
	rightBorder := strings.LastIndex(line, border)
	if start < 0 || leftBorder < 0 || rightBorder <= leftBorder {
		t.Fatalf("invalid SENTRY body line: %q", line)
	}
	leftPadding := runewidth.StringWidth(line[leftBorder+len(border) : start])
	rightPadding := runewidth.StringWidth(line[start+len("SENTRY") : rightBorder])
	if leftPadding != rightPadding {
		t.Fatalf("SENTRY is not centered: left=%d right=%d line=%q", leftPadding, rightPadding, line)
	}
}

func TestLogoArtIsASCIIAndAligned(t *testing.T) {
	lines := strings.Split(strings.Trim(LogoArt, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("logo should not be empty")
	}
	want := runewidth.StringWidth(lines[0])
	for i, line := range lines {
		if got := runewidth.StringWidth(line); got != want {
			t.Fatalf("line %d width=%d want %d: %q", i, got, want, line)
		}
		for _, r := range line {
			if r > 127 {
				t.Fatalf("logo should stay ASCII for no-tui compatibility, got %q in %q", r, line)
			}
		}
	}
	if !strings.Contains(LogoArt, "S E N T R Y") {
		t.Fatalf("missing SENTRY in logo: %q", LogoArt)
	}
}
