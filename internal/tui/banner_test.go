package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"ai-edr/internal/ui"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func TestRenderWelcomeBannerFitsWidth(t *testing.T) {
	info := StartupInfo{
		Version:     "2.0",
		BuildTime:   "2026-06-26",
		ModelInfo:   "lmstudio / local-model",
		ConnInfo:    "Fleet 多目标: 2 台",
		ConfigPath:  "/opt/deepsentry/config.yaml",
		ReportPath:  "/opt/deepsentry/reports/report.md",
		OS:          "Darwin",
		Arch:        "arm64",
		Username:    "demo",
		WorkDir:     "/opt/deepsentry",
		ModeLine:    "🔌 [模式切换] 本地执行模式",
		ToolCount:   51,
		MCPCount:    2,
		MCPSummary:  "2 个已连接 · fofamap 15 · hx0-hawkeye 51",
		ChatSummary: "已启用：微信、QQ、企业微信",
		Tip:         "Tab 聚焦输入框，Enter 发送安全任务",
	}
	for _, w := range []int{96, 120, 140} {
		cw := ChromeContentWidth(w)
		out := renderWelcomeBanner(info, cw)
		plain := stripANSIForTest(out)
		lines := strings.Split(plain, "\n")
		sideBorder := "│"
		if ui.PlainTextMode() {
			sideBorder = "|"
		}
		if len(lines) > 22 {
			t.Fatalf("width=%d banner too tall: %d lines", w, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got != cw {
				t.Fatalf("width=%d line %d width mismatch: got=%d want=%d line=%q", w, i, got, cw, line)
			}
			if i > 0 && i < len(lines)-1 && !strings.HasPrefix(line, sideBorder) {
				t.Fatalf("width=%d broken side border at line %d: %q", w, i, line)
			}
		}
		if !strings.Contains(plain, "DeepSentry") {
			t.Fatalf("banner should include brand title: %q", plain)
		}
		if !strings.Contains(plain, "Hx0 Studio") || !strings.Contains(plain, "官网 · https://hx0studio.com/") {
			t.Fatalf("banner should include Hx0 Studio and 官网 URL: %q", plain)
		}
		if !strings.Contains(plain, "深海哨兵") {
			t.Fatalf("banner should include brand tagline: %q", plain)
		}
		if !strings.Contains(plain, "小技巧") {
			t.Fatalf("banner should include usage tip: %q", plain)
		}
		if !strings.Contains(plain, "MCP") || !strings.Contains(plain, "fofamap 15") {
			t.Fatalf("banner should list MCP connections and tool counts: %q", plain)
		}
		if !strings.Contains(plain, "聊天") || !strings.Contains(plain, "已启用：微信、QQ、企业微信") {
			t.Fatalf("banner should list enabled chat services: %q", plain)
		}
		if !strings.Contains(plain, "目录") || !strings.Contains(plain, "报告") {
			t.Fatalf("banner should include workdir and report paths: %q", plain)
		}
	}
}

func TestBannerAndInputBoxAlign(t *testing.T) {
	w := 120
	cw := ChromeContentWidth(w)
	info := StartupInfo{
		Version:   "2.0",
		ModelInfo: "lmstudio / local-model",
		ConnInfo:  "local",
		Tip:       "test tip",
	}
	banner := stripANSIForTest(renderWelcomeBanner(info, cw))
	input := stripANSIForTest(renderChromeBox([]string{"hello"}, cw, styleBannerBorder))
	bLines := strings.Split(banner, "\n")
	iLines := strings.Split(input, "\n")
	if lipgloss.Width(bLines[0]) != lipgloss.Width(iLines[0]) {
		t.Fatalf("top border mismatch: banner=%d input=%d", lipgloss.Width(bLines[0]), lipgloss.Width(iLines[0]))
	}
	if lipgloss.Width(bLines[len(bLines)-1]) != lipgloss.Width(iLines[len(iLines)-1]) {
		t.Fatalf("bottom border mismatch")
	}
}

func TestCompactWelcomeBannerNeverExceedsNarrowWidth(t *testing.T) {
	info := StartupInfo{
		Version: "2.0", ModelInfo: "provider / very-long-model-name", ConnInfo: "Fleet 多目标: 12 台", AwaitGoal: true,
		Tip: "这是一条很长的使用提示，需要在窄窗口内截断",
	}
	for _, width := range []int{20, 30, 48, 79} {
		out := renderWelcomeBanner(info, width)
		for row, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got != width {
				t.Fatalf("width=%d row=%d got=%d line=%q", width, row, got, stripANSIForTest(line))
			}
		}
	}
}

func TestBannerColumnRatio(t *testing.T) {
	cw := ChromeContentWidth(100)
	inner := cw - 2
	leftW := inner * 40 / 100
	rightW := inner - leftW - 1
	if leftW < 30 {
		t.Fatalf("left column too narrow: %d", leftW)
	}
	if rightW < leftW {
		t.Fatalf("expected right wider than left, left=%d right=%d", leftW, rightW)
	}
}

func TestPadLinesCenteredPutsLogoBelowTop(t *testing.T) {
	even := padLinesCentered([]string{"logo", "title"}, 6)
	if strings.Join(even, ",") != ",,logo,title,," {
		t.Fatalf("even gap should be vertically centered: %#v", even)
	}
	odd := padLinesCentered([]string{"logo", "title"}, 5)
	if strings.Join(odd, ",") != ",,logo,title," {
		t.Fatalf("odd gap should bias extra space above: %#v", odd)
	}

	info := StartupInfo{
		Version: "2.0.3", BuildTime: "2026-09-03", ModelInfo: "deepseek / demo",
		ConnInfo: "本地模式", OS: "Darwin", Arch: "arm64", Username: "demo",
		Hostname: "host", WorkDir: "/opt/deepsentry", ReportPath: "reports/a.md",
		ToolCount: 71, SubAgentCount: 8, SkillCount: 3, NativeTools: true,
		MCPCount: 2, MCPSummary: "2 个已连接 · fofamap 15 · hx0-hawkeye 51",
		Tip: "Tab 聚焦输入框", AwaitGoal: true, StartedAt: "2026-09-03 14:56:02",
		Notices: []string{"定时任务调度已启用"},
	}
	plain := stripANSIForTest(renderWelcomeBanner(info, 120))
	rows := strings.Split(plain, "\n")
	contentTop := 1
	contentBottom := len(rows) - 2
	leftFirst, leftLast := bannerColumnSpan(rows, true)
	rightFirst, rightLast := bannerColumnSpan(rows, false)
	if leftFirst < 0 || leftLast < 0 {
		t.Fatalf("logo column missing in banner:\n%s", plain)
	}
	if rightFirst < 0 || rightLast < 0 {
		t.Fatalf("info column missing in banner:\n%s", plain)
	}
	if !strings.Contains(plain, "Hx0 Studio") || !strings.Contains(plain, "官网 · https://hx0studio.com/") {
		t.Fatalf("left column should show Hx0 Studio and 官网 URL:\n%s", plain)
	}
	for name, span := range map[string][2]int{
		"left":  {leftFirst, leftLast},
		"right": {rightFirst, rightLast},
	} {
		first, last := span[0], span[1]
		if first <= contentTop {
			t.Fatalf("%s column still top-aligned: first=%d", name, first)
		}
		if last >= contentBottom {
			t.Fatalf("%s column should leave space below, last=%d bottom=%d", name, last, contentBottom)
		}
		above := first - contentTop
		below := contentBottom - last
		if above < below {
			t.Fatalf("%s column still too high: %d rows above, %d below", name, above, below)
		}
	}
}

func bannerColumnSpan(rows []string, left bool) (first, last int) {
	first, last = -1, -1
	for i, row := range rows {
		if !strings.HasPrefix(row, "│") && !strings.HasPrefix(row, "|") {
			continue
		}
		inner := strings.TrimPrefix(strings.TrimPrefix(row, "│"), "|")
		inner = strings.TrimSuffix(strings.TrimSuffix(inner, "│"), "|")
		cut := strings.IndexAny(inner, "│|")
		if cut <= 0 {
			continue
		}
		_, size := utf8.DecodeRuneInString(inner[cut:])
		cell := strings.TrimSpace(inner[:cut])
		if !left {
			cell = strings.TrimSpace(inner[cut+size:])
		}
		if cell == "" {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	return first, last
}

func TestRobotLogoSymmetric(t *testing.T) {
	lines := ui.RobotLogoLines()
	if len(lines) != 9 {
		t.Fatalf("expected 9 robot lines, got %d", len(lines))
	}
	wantW := 16
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("empty robot line at %d", i)
		}
		if got := runewidth.StringWidth(line); got != wantW {
			t.Fatalf("line %d width=%d want %d: %q", i, got, wantW, line)
		}
		if i == 5 && !strings.Contains(line, "SENTRY") {
			t.Fatalf("robot body should include SENTRY: %q", line)
		}
	}
	if len(robotLogoLines()) != len(lines) {
		t.Fatalf("styled line count mismatch")
	}
}

func TestRandomUsageTipStablePool(t *testing.T) {
	if len(bannerTips) < 120 {
		t.Fatalf("expected a rich tip pool, got %d", len(bannerTips))
	}
	joined := strings.Join(bannerTips, "\n")
	for _, feature := range []string{
		"ctx=", "64K、128K", "context_window_tokens", "tool_catalog", "config_manage",
		"fleet_inventory", "parallel_tasks", "核心线索", "schedule_task", "headless_browser",
		"pcap_analyze", "db_config_audit", "MCP", "Native Tool schema", "checkpoint",
		"zip_password_recover", "fofa_rules", "bilibili-play", "ssh_legacy_compat",
		"nuclei_plan", "mcp_server_configs", "⌘V", "Ctrl+A", "生成报告",
		"inspection_run", "重启会话", "kind=inspection", "captcha_assist",
		".evidence.jsonl", "允许本次", "reply_text", "--computer-check", "computer_use",
	} {
		if !strings.Contains(joined, feature) {
			t.Fatalf("tip pool missing latest capability %q", feature)
		}
	}
	seenExact := map[string]bool{}
	for index, tip := range bannerTips {
		if strings.TrimSpace(tip) == "" {
			t.Fatalf("empty tip at index %d", index)
		}
		if seenExact[tip] {
			t.Fatalf("duplicate tip: %q", tip)
		}
		seenExact[tip] = true
	}
	seen := map[string]struct{}{}
	for i := 0; i < 80; i++ {
		tip := randomUsageTip()
		if tip == "" {
			t.Fatal("empty tip")
		}
		seen[tip] = struct{}{}
	}
	if len(seen) < 10 {
		t.Fatalf("expected varied tips, got %d unique in 80 draws", len(seen))
	}
}

func TestStartupTipPinnedAtInit(t *testing.T) {
	m := NewAgentModel(nil, "lmstudio / local-model", "local", 30, true, false, StartupInfo{
		Version:   "2.0",
		ModelInfo: "lmstudio / local-model",
	})
	if m.startupInfo.Tip == "" {
		t.Fatal("tip should be pinned once at model init")
	}
	if m.startupInfo.StartedAt == "" {
		t.Fatal("StartedAt should be pinned once at model init")
	}
	pinned := m.startupInfo.Tip
	startedAt := m.startupInfo.StartedAt
	m.width = 120
	m.height = 32
	m.recalcLayout()
	var firstBanner string
	for i := 0; i < 12; i++ {
		m.appendLine("info", "log line", "log")
		m.thinking = i%2 == 0
		m.refreshViewport()
		if m.startupInfo.Tip != pinned {
			t.Fatalf("refresh %d: startupInfo.Tip changed", i)
		}
		banner := stripANSIForTest(m.cachedWelcomeBanner(m.viewport.Width))
		visiblePrefix := runewidth.Truncate(pinned, 12, "")
		if !strings.Contains(banner, visiblePrefix) {
			t.Fatalf("refresh %d: cached banner lost pinned tip prefix %q (tip=%q)", i, visiblePrefix, pinned)
		}
		if !strings.Contains(banner, startedAt) {
			t.Fatalf("refresh %d: cached banner lost pinned StartedAt %q", i, startedAt)
		}
		if firstBanner == "" {
			firstBanner = banner
		} else if banner != firstBanner {
			t.Fatalf("refresh %d: cached banner changed", i)
		}
	}
}

func TestStartupInfoShowsInViewport(t *testing.T) {
	m := NewAgentModel(nil, "lmstudio / local-model", "Fleet 多目标: 2 台", 30, true, false, StartupInfo{
		Version:   "2.0",
		ModelInfo: "lmstudio / local-model",
		ConnInfo:  "Fleet 多目标: 2 台",
		OS:        "Darwin",
		Arch:      "arm64",
		Tip:       "输入 /help 查看命令",
	})
	m.width = 120
	m.height = 32
	m.recalcLayout()
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "DeepSentry") {
		t.Fatalf("viewport should render startup banner, got %q", view)
	}
}
