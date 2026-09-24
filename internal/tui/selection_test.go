package tui

import (
	"strings"
	"testing"
)

func TestExtractPlainSelectionSingleLine(t *testing.T) {
	plain := "hello world"
	got := extractPlainSelection(plain, 0, 0, 0, 4)
	if got != "hello" {
		t.Fatalf("got %q want hello", got)
	}
}

func TestExtractPlainSelectionMultiLine(t *testing.T) {
	plain := "line one\nline two\nline three"
	got := extractPlainSelection(plain, 0, 2, 2, 3)
	want := "ne one\nline two\nline"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExtractPlainSelectionReversed(t *testing.T) {
	plain := "abcdef"
	got := extractPlainSelection(plain, 0, 5, 0, 1)
	if got != "bcdef" {
		t.Fatalf("got %q want bcdef", got)
	}
}

func TestExtractPlainSelectionDisplayColumns(t *testing.T) {
	plain := "a你好🙂z"
	got := extractPlainSelection(plain, 0, 1, 0, 4)
	if got != "你好" {
		t.Fatalf("got %q want 你好", got)
	}
}

func TestRenderSelectionPlainHighlightsVisibleText(t *testing.T) {
	plain := "alpha\n你好 world\nomega"
	got := renderSelectionPlain(plain, 0, 3, 1, 0, 1, 3)
	if stripANSIForTest(got) != plain {
		t.Fatalf("highlight should preserve plain text, got %q", stripANSIForTest(got))
	}
	if selected := extractPlainSelection(plain, 1, 0, 1, 3); selected != "你好" {
		t.Fatalf("selection = %q, want 你好", selected)
	}
}

func TestRenderSelectionStyledPreservesANSIOutsideSelection(t *testing.T) {
	plain := "hello world"
	styled := "\x1b[31m" + plain + "\x1b[0m"
	got := renderSelectionStyled(styled, plain, 0, 1, 0, 0, 0, 4)
	if stripANSIForTest(got) != plain {
		t.Fatalf("highlight should preserve visible text, got %q", stripANSIForTest(got))
	}
	if !strings.Contains(got, "\x1b[31m world") {
		t.Fatalf("style after selection should be restored, got %q", got)
	}
}

func TestSelectionCharCount(t *testing.T) {
	if selectionCharCount("你好") != 2 {
		t.Fatalf("expected rune count 2")
	}
}
