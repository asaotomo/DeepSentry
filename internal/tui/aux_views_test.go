package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/benchmark"
	"ai-edr/internal/harness"

	"github.com/charmbracelet/lipgloss"
)

func TestSessionPickerFitsTerminalAndWindowsLongLists(t *testing.T) {
	items := make([]harness.SessionSummary, 40)
	for i := range items {
		items[i] = harness.SessionSummary{
			ID:      fmt.Sprintf("session_very_long_identifier_%03d", i),
			Goal:    strings.Repeat("排查远程主机安全告警", 4),
			StepNum: i,
			SavedAt: time.Date(2026, 7, 10, 12, i%60, 0, 0, time.Local),
		}
	}
	for _, size := range [][2]int{{1, 2}, {4, 3}, {7, 5}, {30, 12}, {48, 18}, {100, 30}} {
		m := newSessionPicker(items, make(chan pickResultMsg, 1))
		m.width, m.height = size[0], size[1]
		m.cursor = 30
		assertRenderedSize(t, m.View(), size[0], size[1])
		plain := stripANSIForTest(m.View())
		if size[0] >= 8 && size[1] >= 4 && !strings.Contains(plain, "030") {
			t.Fatalf("picker should window around selected row:\n%s", plain)
		}
	}
}

func TestBenchmarkViewFitsTerminalWithLogsAndReport(t *testing.T) {
	for _, size := range [][2]int{{1, 2}, {4, 3}, {7, 5}, {30, 12}, {48, 18}, {100, 30}} {
		m := NewBenchmarkModel()
		m.width, m.height = size[0], size[1]
		m.running = true
		m.current, m.total = 8, 10
		for i := 0; i < 100; i++ {
			m.lines = append(m.lines, fmt.Sprintf("case-%03d %s", i, strings.Repeat("long-output ", 8)))
		}
		assertRenderedSize(t, m.View(), size[0], size[1])

		m.running = false
		m.report = &benchmark.SuiteReport{OverallScore: 92.5, Grade: "A", CategoryAvg: map[benchmark.Category]float64{}}
		assertRenderedSize(t, m.View(), size[0], size[1])
	}
}

func TestProgressBarClampsInvalidPercentages(t *testing.T) {
	for _, pct := range []float64{-50, 0, 100, 150} {
		bar := progressBar(pct, 10)
		plain := stripANSIForTest(bar)
		if got := len([]rune(plain)); got != 10 {
			t.Fatalf("pct=%v glyphs=%d want 10: %q", pct, got, plain)
		}
	}
}

func assertRenderedSize(t *testing.T, view string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(view); got > height {
		t.Fatalf("height=%d exceeds %d:\n%s", got, height, stripANSIForTest(view))
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); (width > 1 && got >= width) || got > width {
			t.Fatalf("row=%d width=%d reaches terminal autowrap column %d: %q", row, got, width, stripANSIForTest(line))
		}
	}
}
