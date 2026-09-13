package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderMarkdownReportRendersTableAndInlineStyles(t *testing.T) {
	md := strings.Join([]string{
		"# 总结",
		"",
		"✅ 已完成以下操作：",
		"",
		"| 步骤 | 操作 | 结果 |",
		"|------|------|------|",
		"| 1 | 创建 `/tmp/test_flag.txt` | **成功** |",
		"| 2 | 下载到本机 | 成功 |",
		"",
		"解压密码是 **123456**。",
	}, "\n")

	rendered := renderMarkdownReport(md, 96)
	plain := stripANSIForTest(rendered)
	for _, bad := range []string{"|------|", "**123456**", "`/tmp/test_flag.txt`", "# 总结"} {
		if strings.Contains(plain, bad) {
			t.Fatalf("markdown marker %q should not remain in rendered report:\n%s", bad, plain)
		}
	}
	for _, want := range []string{"总结", "步骤", "操作", "结果", "123456"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered report missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "╭") && !strings.Contains(plain, "+") {
		t.Fatalf("table should render with borders:\n%s", plain)
	}
}

func TestRenderMarkdownReportFitsWidth(t *testing.T) {
	md := "| 字段 | 很长的说明 |\n|---|---|\n| 路径 | /var/www/html/uploads/reports/report_YYYYMMDD_HHMMSS.md |"
	rendered := renderMarkdownReport(md, 48)
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 48 {
			t.Fatalf("rendered markdown line width=%d want <=48: %q", got, line)
		}
	}
}

func TestRenderMarkdownHeadingSeparatesEmojiFromText(t *testing.T) {
	rendered := renderMarkdownReport("## 🖥当前服务器配置概览", 48)
	plain := stripANSIForTest(rendered)
	if strings.Contains(plain, "🖥当前") {
		t.Fatalf("heading emoji should not touch following text:\n%s", plain)
	}
	if !strings.Contains(plain, "-- 🖥 当前服务器配置概览") {
		t.Fatalf("heading should use stable marker and spacing:\n%s", plain)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 48 {
			t.Fatalf("heading line width=%d want <=48: %q", got, line)
		}
	}
}

func TestRenderMarkdownHeadingKeepsEmojiGraphemeTogether(t *testing.T) {
	got := normalizeMarkdownHeadingText("⚠️执行提醒")
	if got != "⚠️ 执行提醒" {
		t.Fatalf("emoji cluster should stay intact and be separated from text: %q", got)
	}
	if strings.Contains(got, "⚠ ️") {
		t.Fatalf("variation selector must not be split from its emoji: %q", got)
	}
}

func TestNormalizeEmojiSpacingKeepsClustersAndAddsReadableGap(t *testing.T) {
	tests := map[string]string{
		"1️⃣重命名": "1️⃣ 重命名",
		"✅本机搞定":  "✅ 本机搞定",
		"完成✅验证":  "完成 ✅ 验证",
		"完成🎭✨":   "完成 🎭✨",
		"👩‍💻排查":  "👩‍💻 排查",
		"✅ 已有间距": "✅ 已有间距",
		"✅，已完成":  "✅，已完成",
	}
	for input, want := range tests {
		if got := normalizeEmojiSpacing(input); got != want {
			t.Fatalf("normalizeEmojiSpacing(%q)=%q want %q", input, got, want)
		}
		if got := normalizeEmojiSpacing(want); got != want {
			t.Fatalf("emoji spacing must be idempotent: %q -> %q", want, got)
		}
	}
}

func TestMarkdownTableSeparatesEmojiAndTextWithoutBreakingWidth(t *testing.T) {
	t.Setenv("DEEPSENTRY_FANCY", "1")
	t.Setenv("TERM", "xterm-256color")
	md := strings.Join([]string{
		"| 步骤 | 操作 | 结果 |",
		"|---|---|---|",
		"| 1️⃣重命名 | 修改文件 | ✅本机搞定 |",
		"| 2️⃣上传 | 传送文件 | ✅原汁原味 |",
	}, "\n")
	rendered := renderMarkdownReport(md, 64)
	plain := stripANSIForTest(rendered)
	for _, want := range []string{"1️⃣ 重命名", "2️⃣ 上传", "✅ 本机搞定", "✅ 原汁原味"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered table missing spaced value %q:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(rendered, "\n") {
		if got := displayWidth(line); got > 64 {
			t.Fatalf("emoji table overflow width=%d: %q", got, line)
		}
	}
}

func TestRenderMarkdownAskRendersRichQuestionInsideViewport(t *testing.T) {
	md := strings.Join([]string{
		"📊 **当前战况：**",
		"",
		"| 题目 | 难度 | 状态 |",
		"|---|---|---|",
		"| `a-05` | easy | **available** |",
		"",
		"请选择下一步：",
		"1. 启动 a-05",
		"2. 暂不启动",
	}, "\n")

	rendered := renderMarkdownAsk(md, "[13:32:27] ", 78)
	plain := stripANSIForTest(rendered)
	for _, bad := range []string{"**当前战况：**", "`a-05`", "|---|---|---|"} {
		if strings.Contains(plain, bad) {
			t.Fatalf("ask prompt still contains markdown marker %q:\n%s", bad, plain)
		}
	}
	for _, want := range []string{"需要用户补充", "当前战况", "a-05", "available", "1. 启动"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("ask prompt missing %q:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 78 {
			t.Fatalf("ask line width=%d want <=78: %q", got, line)
		}
	}
}

func TestMarkdownTableFallsBackWithoutOverflowAtNarrowWidth(t *testing.T) {
	md := "| 题目 | 难度 | 状态 |\n|---|---|---|\n| a-05 | easy | available |"
	for _, width := range []int{8, 12, 18} {
		rendered := renderMarkdownBlocks(md, width)
		for _, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width=%d rendered line width=%d: %q", width, got, line)
			}
		}
		plain := strings.ReplaceAll(stripANSIForTest(rendered), "\n", "")
		if !strings.Contains(plain, "a-05") || !strings.Contains(plain, "available") {
			t.Fatalf("narrow table lost data at width=%d:\n%s", width, plain)
		}
	}
}

func TestMarkdownSummaryTableUsesNaturalLabelValueRows(t *testing.T) {
	md := strings.Join([]string{
		"| 项目 | 内容 |",
		"|---|---|",
		"| 任务成果 | 安徽邮电职业技术学院到 ahptc.cn 的资产调查已经完成 |",
		"| 技能成长 | 学会使用 domain 查询语法 |",
	}, "\n")
	rendered := renderMarkdownBlocks(md, 34)
	plain := stripANSIForTest(rendered)
	for _, bad := range []string{"项目:", "项目：", "内容:", "内容：", " · "} {
		if strings.Contains(plain, bad) {
			t.Fatalf("generic table headers should not repeat in summary rows (%q):\n%s", bad, plain)
		}
	}
	for _, want := range []string{"• 任务成果：", "• 技能成长：", "ahptc.cn", "domain"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("natural summary row missing %q:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 34 {
			t.Fatalf("summary row width=%d want <=34: %q", got, line)
		}
	}
}

func TestMarkdownCodeBlockWrapsWithoutLosingCommand(t *testing.T) {
	command := "sqlmap --batch --url http://10.0.0.1/item?id=1 --level=5 --risk=3 --tamper=space2comment"
	rendered := renderMarkdownBlocks("```sh\n"+command+"\n```", 24)
	plain := strings.ReplaceAll(stripANSIForTest(rendered), "\n", "")
	if !strings.Contains(plain, command) {
		t.Fatalf("wrapped code block lost command text:\n%s", stripANSIForTest(rendered))
	}
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 24 {
			t.Fatalf("wrapped code line width=%d: %q", got, line)
		}
	}
}

func TestMarkdownConfirmationShowsOnceSessionAndDenyChoices(t *testing.T) {
	rendered := stripANSIForTest(renderMarkdownConfirm("编辑文件", "[08:15:00] ", 80))
	for _, want := range []string{"Y 本次", "A 会话同类", "S 本次会话允许所有高危操作", "N/Esc 拒绝", "Enter 拒绝"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("confirmation panel missing %q:\n%s", want, rendered)
		}
	}
}

func TestMarkdownEmojiTableKeepsColumnsAndPanelBorderAligned(t *testing.T) {
	t.Setenv("DEEPSENTRY_FANCY", "1")
	t.Setenv("DEEPSENTRY_PLAIN", "")
	t.Setenv("TERM", "xterm-256color")
	md := strings.Join([]string{
		"🎉 请选择下一步：",
		"",
		"| 状态 | 项目 | 说明 |",
		"|---|---|---|",
		"| ⚠️ | 执行出错 | 请重试 |",
		"| ✅ | 已完成 | 正常 |",
	}, "\n")
	rendered := renderMarkdownAsk(md, "[16:42:31] ", 52)
	for _, line := range strings.Split(rendered, "\n") {
		if got := displayWidth(line); got != 52 {
			t.Fatalf("emoji panel row is not aligned: width=%d want=52 line=%q", got, line)
		}
	}
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "⚠️") || !strings.Contains(plain, "执行出错") {
		t.Fatalf("emoji table lost content:\n%s", plain)
	}
}

func TestWrapDisplayDoesNotSplitEmojiPresentationSequence(t *testing.T) {
	wrapped := wrapDisplay("1234567⚠️执行出错", 8)
	if strings.Contains(wrapped, "⚠\n️") || strings.Contains(wrapped, "⚠\n") {
		t.Fatalf("emoji grapheme was split across rows: %q", wrapped)
	}
	for _, line := range strings.Split(wrapped, "\n") {
		if got := displayWidth(line); got > 8 {
			t.Fatalf("wrapped line width=%d: %q", got, line)
		}
	}
}
