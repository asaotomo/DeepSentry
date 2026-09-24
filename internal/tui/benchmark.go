package tui

import (
	"fmt"
	"os"
	"strings"

	"ai-edr/internal/benchmark"
	"ai-edr/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type benchProgressMsg struct {
	id, name string
	score    float64
	passed   bool
	total    int
	current  int
}

type benchDoneMsg struct {
	report *benchmark.SuiteReport
	err    error
}

// BenchmarkModel Benchmark 可视化 TUI
type BenchmarkModel struct {
	width, height int
	lines         []string
	running       bool
	report        *benchmark.SuiteReport
	err           error
	current       int
	total         int
}

func NewBenchmarkModel() BenchmarkModel {
	return BenchmarkModel{
		lines: []string{ui.Prefix("🚀", "[BENCH]") + "Benchmark 启动中..."},
		total: len(benchmark.AllScenarios()),
	}
}

type benchStartMsg struct{}

func (m BenchmarkModel) Init() tea.Cmd {
	return func() tea.Msg { return benchStartMsg{} }
}

func (m BenchmarkModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case benchStartMsg:
		m.running = true
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case benchProgressMsg:
		m.current = msg.current
		icon := ui.Prefix("❌", "[ERR]")
		if msg.passed {
			icon = ui.Prefix("✅", "[OK]")
		}
		m.lines = append(m.lines, fmt.Sprintf("%s %s %s  %.0f分", icon, msg.id, msg.name, msg.score))
		if len(m.lines) > 80 {
			m.lines = m.lines[len(m.lines)-80:]
		}
		return m, nil
	case benchDoneMsg:
		m.running = false
		m.report = msg.report
		m.err = msg.err
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if !m.running && m.report != nil {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m BenchmarkModel) View() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	renderW := TerminalRenderWidth(w)
	if w < 8 || h < 4 {
		return styleApp.Width(renderW).Height(h).MaxHeight(h).Render(runewidth.Truncate("Benchmark", renderW, ""))
	}
	var b strings.Builder
	headerW := max(1, renderW-styleHeader.GetHorizontalFrameSize())
	header := runewidth.Truncate(ui.Prefix("📊", "[BENCH]")+"DeepSentry Benchmark", headerW, "…")
	b.WriteString(styleHeader.Width(renderW).Render(header))
	b.WriteString("\n\n")
	if m.running && m.total > 0 {
		pct := float64(m.current) / float64(m.total) * 100
		bar := progressBar(pct, max(4, min(40, renderW-20)))
		b.WriteString(styleInfo.Render(fmt.Sprintf("  进度 %d/%d  %s", m.current, m.total, bar)) + "\n\n")
	}
	logLines := m.lines
	if available := max(1, h-7); len(logLines) > available {
		logLines = logLines[len(logLines)-available:]
	}
	for _, ln := range logLines {
		b.WriteString("  " + ln + "\n")
	}
	if m.report != nil {
		divider := strings.Repeat("─", max(1, min(50, renderW)))
		if ui.PlainTextMode() {
			divider = strings.Repeat("-", max(1, min(50, renderW)))
		}
		b.WriteString("\n" + divider + "\n")
		b.WriteString(styleSuccess.Render(fmt.Sprintf("  综合得分: %.1f / 100  %s\n", m.report.OverallScore, m.report.Grade)))
		order := []benchmark.Category{
			benchmark.CatLLM, benchmark.CatLocalTool, benchmark.CatRemoteTool,
			benchmark.CatLinkage, benchmark.CatFilesystem, benchmark.CatForensics,
			benchmark.CatIncident, benchmark.CatAgent, benchmark.CatHarness,
			benchmark.CatResilience, benchmark.CatSecurity,
		}
		for _, cat := range order {
			meta := benchmark.CategoryMeta[cat]
			avg := m.report.CategoryAvg[cat]
			b.WriteString(fmt.Sprintf("  %-22s %5.1f/100  %s\n", meta.DisplayName, avg, progressBar(avg, 20)))
		}
		b.WriteString("\n" + styleHelp.Render("  按 Q 退出"))
	} else if m.err != nil {
		b.WriteString(styleError.Render("  错误: " + m.err.Error()))
	}
	rows := strings.Split(b.String(), "\n")
	if len(rows) > h {
		rows = append(rows[:1], rows[len(rows)-(h-1):]...)
	}
	for i := range rows {
		rows[i] = fitStyledLine(rows[i], renderW)
	}
	return styleApp.Width(renderW).Height(h).MaxHeight(h).Render(strings.Join(rows, "\n"))
}

func progressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if ui.PlainTextMode() {
		inner := max(0, width-2)
		n := min(inner, int(pct/100*float64(inner)))
		return "[" + strings.Repeat("#", n) + strings.Repeat(".", inner-n) + "]"
	}
	n := min(width, int(pct/100*float64(width)))
	return lipgloss.NewStyle().Foreground(colorGreen).Render(strings.Repeat("█", n)) +
		lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("░", width-n))
}

// RunBenchmark 在 TUI 中运行 benchmark
func RunBenchmark(cfgPath string, skipLLM, skipRemote bool) error {
	restoreConsole := prepareConsoleDisplay()
	defer restoreConsole()
	m := NewBenchmarkModel()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(newInputCursorOutput(os.Stdout, nil)))
	defer startConsoleResizeWatcher(p)()

	go func() {
		report, err := benchmark.RunSuiteWithProgress(cfgPath, skipLLM, skipRemote, func(id, name string, score float64, passed bool, cur, total int) {
			p.Send(benchProgressMsg{id: id, name: name, score: score, passed: passed, current: cur, total: total})
		})
		p.Send(benchDoneMsg{report: report, err: err})
	}()

	_, err := p.Run()
	return err
}
