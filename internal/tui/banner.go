package tui

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"ai-edr/internal/ui"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// StartupInfo TUI 首次载入欢迎面板数据。
type StartupInfo struct {
	Version       string
	BuildTime     string
	ModelInfo     string
	ConnInfo      string
	ConfigPath    string
	ReportPath    string
	OS            string
	Arch          string
	Username      string
	Hostname      string
	WorkDir       string
	ModeLine      string
	ToolCount     int
	SubAgentCount int
	SkillCount    int
	MCPCount      int
	ChatSummary   string
	MCPSummary    string // 例如 "2 个已连接 · fofamap 15 · hx0-hawkeye 51"
	TargetCount   int
	MaxSteps      int
	BatchMode     bool
	NativeTools   bool
	SessionID     string
	AwaitGoal     bool
	Notices       []string
	Tip           string // 进入 TUI 时固定；空则在 NewAgentModel 中随机抽取一次
	StartedAt     string // 进入 TUI 时固定，Banner 时间行不随刷新变化
}

func (s StartupInfo) isEmpty() bool {
	return s.Version == "" && s.ModelInfo == "" && s.ConfigPath == "" && s.ConnInfo == ""
}

var (
	styleBannerBorder  = lipgloss.NewStyle().Foreground(colorAccent)
	styleBannerRobot   = lipgloss.NewStyle().Foreground(lipgloss.Color("#5c7cfa"))
	styleBannerRobotHi = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleBannerBrand   = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	styleBannerBadge   = lipgloss.NewStyle().Bold(true).Foreground(colorBg).Background(colorAccent).Padding(0, 1)
	styleBannerTagline = lipgloss.NewStyle().Foreground(colorMuted)
	styleBannerHeading = lipgloss.NewStyle().Bold(true).Foreground(colorYellow)
	styleBannerLabel   = lipgloss.NewStyle().Foreground(colorMuted)
	styleBannerText    = lipgloss.NewStyle().Foreground(colorText)
	styleBannerDivider = lipgloss.NewStyle().Foreground(colorBorder)
)

const brandTagline = "深海哨兵 · AI 驱动的安全应急与智能运维 Agent"

func renderWelcomeBanner(info StartupInfo, width int) string {
	width = max(4, width)
	if width < 80 {
		return renderCompactWelcomeBanner(info, width)
	}
	innerW := width - 2
	leftW := innerW * 40 / 100
	rightW := innerW - leftW - 1

	tip := strings.TrimSpace(info.Tip)

	left := buildBannerLeft(info, leftW)
	right := buildBannerRight(info, rightW, tip)
	rows := joinBannerRows(left, right, leftW, rightW, innerW)
	return renderChromeBox(rows, width, styleBannerBorder)
}

func renderCompactWelcomeBanner(info StartupInfo, width int) string {
	innerW := max(1, width-2)
	rows := []string{
		centerStyledLine(brandTitleLine(info.Version), innerW),
	}
	for _, line := range wrapPlain(brandTagline, innerW) {
		rows = append(rows, centerStyledLine(styleBannerTagline.Render(line), innerW))
	}
	rows = append(rows, centerStyledLine(styleBannerLabel.Render(ui.StudioSiteLine), innerW))
	rows = append(rows,
		compactBannerSection("模型", firstNonEmpty(info.ModelInfo, "-"), innerW),
		compactBannerSection("连接", firstNonEmpty(info.ConnInfo, "本地模式"), innerW),
	)
	if summary := strings.TrimSpace(info.MCPSummary); summary != "" {
		rows = append(rows, compactBannerSection("MCP", summary, innerW))
	}
	rows = append(rows,
		compactBannerSection("聊天", firstNonEmpty(info.ChatSummary, "未开启"), innerW),
		compactBannerSection("小技巧", firstNonEmpty(info.Tip, "Tab 聚焦输入框"), innerW),
		compactBannerSection("就绪", compactReadyText(info), innerW),
	)
	return renderChromeBox(rows, width, styleBannerBorder)
}

func compactReadyText(info StartupInfo) string {
	if info.SessionID != "" {
		return "已恢复 checkpoint · 可继续追问"
	}
	if info.AwaitGoal {
		return "描述任务后 Enter 开始"
	}
	return "Enter 发送 · /help 查看命令"
}

func compactBannerSection(title, value string, width int) string {
	title = sanitizeTUIText(title)
	value = sanitizeTUIText(value)
	prefix := styleBannerHeading.Render(title) + styleBannerLabel.Render(" · ")
	valueW := max(1, width-lipgloss.Width(prefix))
	return fitStyledLine(prefix+styleBannerText.Render(truncateStr(value, valueW)), width)
}

func buildBannerLeft(info StartupInfo, colW int) []string {
	var lines []string
	for _, line := range robotLogoLines() {
		lines = append(lines, centerStyledLine(line, colW))
	}
	lines = append(lines, centerStyledLine(brandTitleLine(info.Version), colW))
	for _, line := range wrapPlain(brandTagline, colW) {
		lines = append(lines, centerStyledLine(styleBannerTagline.Render(line), colW))
	}
	meta := fmt.Sprintf("Build · %s · %s", sanitizeTUIText(firstNonEmpty(info.BuildTime, "dev")), ui.StudioName)
	lines = append(lines, centerStyledLine(styleBannerLabel.Render(meta), colW))
	lines = append(lines, centerStyledLine(styleBannerLabel.Render(ui.StudioSiteLine), colW))
	return lines
}

// robotLogoLines 哨兵机器人 ASCII（参考品牌稿：天线 + 方头 + SENTRY）。
func robotLogoLines() []string {
	raw := ui.RobotLogoLines()
	out := make([]string, len(raw))
	frame := styleBannerRobot
	hi := styleBannerRobotHi
	for i, line := range raw {
		switch i {
		case 0:
			out[i] = hi.Render(line)
		case 3, 4:
			out[i] = frame.Render(line)
		case 5:
			out[i] = hi.Render(line)
		default:
			out[i] = frame.Render(line)
		}
	}
	return out
}

func brandTitleLine(version string) string {
	title := styleBannerBrand.Render("DeepSentry")
	badge := styleBannerBadge.Render(sanitizeTUIText(firstNonEmpty(version, ui.Version)))
	return title + " " + badge
}

func wrapPlain(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" || width <= 0 {
		return []string{text}
	}
	if runewidth.StringWidth(text) <= width {
		return []string{text}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	for _, r := range text {
		rw := runewidth.RuneWidth(r)
		if curW+rw > width && cur.Len() > 0 {
			lines = append(lines, strings.TrimSpace(cur.String()))
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(r)
		curW += rw
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		lines = append(lines, s)
	}
	return lines
}

func centerStyledLine(line string, colW int) string {
	w := lipgloss.Width(line)
	if w >= colW {
		return fitStyledLine(line, colW)
	}
	gap := colW - w
	left := gap / 2
	return strings.Repeat(" ", left) + line + strings.Repeat(" ", gap-left)
}

func buildBannerRight(info StartupInfo, width int, tip string) []string {
	mode := shortenModeLine(info.ModeLine)
	runtime := fmt.Sprintf("%s/%s · %s", firstNonEmpty(info.OS, "-"), firstNonEmpty(info.Arch, "-"), firstNonEmpty(info.Username, "-"))
	conn := firstNonEmpty(info.ConnInfo, "本地模式")
	if mode != "" {
		conn += " · " + mode
	}
	if info.TargetCount > 0 {
		conn += fmt.Sprintf(" · %d 目标", info.TargetCount)
	}

	subAgents := info.SubAgentCount
	if subAgents <= 0 {
		subAgents = 0
	}
	capability := fmt.Sprintf("%d 子 Agent", subAgents)
	if info.ToolCount > 0 {
		capability = fmt.Sprintf("%d 工具 · %s", info.ToolCount, capability)
	}
	if info.NativeTools {
		capability += " · Native Tools"
	}
	if info.SkillCount > 0 {
		capability += fmt.Sprintf(" · %d Skills", info.SkillCount)
	}

	mcpSummary := strings.TrimSpace(info.MCPSummary)
	if mcpSummary == "" && info.MCPCount > 0 {
		mcpSummary = fmt.Sprintf("%d 个已连接", info.MCPCount)
	}

	steps := fmt.Sprintf("最多 %d 步/任务", max(info.MaxSteps, 30))
	if info.BatchMode {
		steps += " · Batch 已开"
	}

	ready := "Enter 发送 · Tab 聚焦输入框"
	if info.SessionID != "" {
		ready = "已恢复 checkpoint · 可继续追问"
	} else if info.AwaitGoal {
		ready = "描述安全任务后 Enter 开始"
	}

	lines := bannerLabeledLines("小技巧", tip, width)
	lines = append(lines,
		bannerSection("环境", runtime, width),
		bannerSection("主机", firstNonEmpty(info.Hostname, "-"), width),
		bannerSection("连接", conn, width),
		bannerSection("模型", firstNonEmpty(info.ModelInfo, "-"), width),
		bannerSection("能力", capability, width),
	)
	if mcpSummary != "" {
		lines = append(lines, bannerLabeledLines("MCP", mcpSummary, width)...)
	}
	lines = append(lines,
		bannerSection("聊天", firstNonEmpty(info.ChatSummary, "未开启"), width),
		bannerSection("步数", steps, width),
		bannerSection("时间", firstNonEmpty(info.StartedAt, time.Now().Format("2006-01-02 15:04:05")), width),
		bannerSection("目录", truncatePath(info.WorkDir, width), width),
		bannerSection("报告", truncatePath(info.ReportPath, width), width),
		bannerSection("就绪", ready, width),
		bannerSection("按键", "Tab输入 · /help · e全展/全折 · Y/N确认 · q空闲退出", width),
	)
	if sid := truncateStr(info.SessionID, 24); sid != "" {
		lines = append(lines, bannerSection("会话", sid, width))
	}
	if note := firstNotice(info.Notices); note != "" {
		lines = append(lines, bannerSection("提示", note, width))
	}
	return lines
}

// bannerLabeledLines 给更长的新功能提示或 MCP 清单预留两行。
// 第二行与值列对齐，保持“小技巧 ·”标签和右侧信息列的视觉节奏。
func bannerLabeledLines(title, value string, width int) []string {
	value = sanitizeTUIText(value)
	label := styleBannerHeading.Render(sanitizeTUIText(title))
	sep := styleBannerLabel.Render(" · ")
	prefixW := lipgloss.Width(label) + lipgloss.Width(sep)
	valueW := max(1, width-prefixW)
	wrapped := wrapPlain(value, valueW)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	if len(wrapped) > 2 {
		wrapped[1] = truncateStr(strings.Join(wrapped[1:], ""), valueW)
		wrapped = wrapped[:2]
	}
	lines := []string{label + sep + styleBannerText.Render(wrapped[0])}
	if len(wrapped) == 2 {
		lines = append(lines, strings.Repeat(" ", prefixW)+styleBannerText.Render(wrapped[1]))
	}
	return lines
}

func firstNotice(notices []string) string {
	for _, n := range notices {
		n = strings.TrimSpace(n)
		if n != "" {
			return n
		}
	}
	return ""
}

func bannerSection(title, value string, width int) string {
	title = sanitizeTUIText(title)
	value = sanitizeTUIText(value)
	label := styleBannerHeading.Render(title)
	sep := styleBannerLabel.Render(" · ")
	valW := width - lipgloss.Width(label) - lipgloss.Width(sep)
	valW = max(1, valW)
	return label + sep + styleBannerText.Render(truncateStr(value, valW))
}

func joinBannerRows(left, right []string, leftW, rightW, innerW int) []string {
	height := max(len(left), len(right)) + 2
	left = padLinesCentered(left, height)
	right = padLinesCentered(right, height)
	div := styleBannerDivider.Render("│")
	rows := make([]string, height)
	for i := 0; i < height; i++ {
		l := fitStyledLine(left[i], leftW)
		r := fitStyledLine(right[i], rightW)
		rows[i] = fitStyledLine(l+div+r, innerW)
	}
	return rows
}

// renderChromeBox 绘制与 viewport / 输入框等宽的外框（totalW 含左右边框列）。
func renderChromeBox(rows []string, totalW int, border lipgloss.Style) string {
	if totalW < 4 {
		totalW = 4
	}
	innerW := totalW - 2
	if ui.PlainTextMode() {
		var out []string
		out = append(out, border.Render("+"+strings.Repeat("-", innerW)+"+"))
		for _, row := range rows {
			out = append(out, border.Render("|")+fitStyledLine(ui.TerminalText(row), innerW)+border.Render("|"))
		}
		out = append(out, border.Render("+"+strings.Repeat("-", innerW)+"+"))
		return strings.Join(out, "\n")
	}
	pipe := border.Render("│")
	var out []string
	out = append(out, border.Render("╭"+strings.Repeat("─", innerW)+"╮"))
	for _, row := range rows {
		out = append(out, pipe+fitStyledLine(row, innerW)+pipe)
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return strings.Join(out, "\n")
}

func fitStyledLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	for lipgloss.Width(s) > w {
		plain := stripANSIForWidth(s)
		if plain == "" {
			return runewidth.Truncate(s, w, "")
		}
		s = styleBannerText.Render(truncateStr(plain, w))
	}
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func randomUsageTip() string {
	if len(bannerTips) == 0 {
		return "Tab 聚焦输入框开始任务"
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(bannerTips))))
	if err != nil {
		return bannerTips[0]
	}
	return bannerTips[n.Int64()]
}

func shortenModeLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if i := strings.Index(line, "]"); i >= 0 && i+1 < len(line) {
		return strings.TrimSpace(line[i+1:])
	}
	return truncateStr(line, 20)
}

// padLinesCentered vertically centers a column inside the banner box.
// Odd leftover rows go above so content sits a touch lower than mathematical
// center (the robot antenna otherwise looks top-heavy).
func padLinesCentered(lines []string, n int) []string {
	if len(lines) >= n {
		return lines
	}
	gap := n - len(lines)
	top := (gap + 1) / 2
	out := make([]string, 0, n)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, lines...)
	for len(out) < n {
		out = append(out, "")
	}
	return out
}

func truncatePath(path string, max int) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "-"
	}
	if runewidth.StringWidth(path) <= max {
		return path
	}
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return runewidth.Truncate(path, max, "…")
	}
	short := parts[len(parts)-1]
	for i := len(parts) - 2; i >= 0; i-- {
		candidate := "…/" + strings.Join(parts[i:], "/")
		if runewidth.StringWidth(candidate) <= max {
			return candidate
		}
	}
	return runewidth.Truncate("…/"+short, max, "…")
}

func stripANSIForWidth(s string) string {
	var b strings.Builder
	inSeq := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inSeq {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				inSeq = false
			}
			continue
		}
		if ch == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			inSeq = true
			i++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
