package chat

import (
	"strings"
	"testing"
)

func TestFormatTaskNotifyKeepsConclusionOnly(t *testing.T) {
	raw := strings.Join([]string{
		"♻️已恢复会话 session_chat_abc_g0 (step 3)",
		"💡已追加本次 --task/参数作为用户补充并继续执行",
		"🔌 [模式切换] 本地执行模式",
		"💡想法: 检查磁盘",
		"💻命令[控制端本机]: df -h",
		"在的在的！小呆呆一直蹲在这里等卡卡呢～ (｡•̀ᴗ-)✧",
		"随传随到，永远在线！有啥活儿要派尽管说：查服务器、抓日志、开网页、刷CTF、写脚本……一句话的事儿！🎀",
	}, "\n")
	out := formatTaskNotify("completed", "abc123", raw)
	for _, ban := range []string{"任务完成", "已恢复会话", "已追加本次", "模式切换", "想法", "命令", "df -h", "/ds result", "——"} {
		if strings.Contains(out, ban) {
			t.Fatalf("notify leaked %q:\n%s", ban, out)
		}
	}
	want := "在的在的！小呆呆一直蹲在这里等卡卡呢～ (｡•̀ᴗ-)✧\n随传随到，永远在线！有啥活儿要派尽管说：查服务器、抓日志、开网页、刷CTF、写脚本……一句话的事儿！🎀"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestConvertMarkdownForIM(t *testing.T) {
	in := "# 标题\n\n我是**小呆呆**，看 [文档](https://example.com)\n- 巡检\n- 取证\n\n`df -h`"
	got := formatMobileText(in)
	for _, ban := range []string{"# 标题", "**", "[", "](", "- 巡检"} {
		if strings.Contains(got, ban) {
			t.Fatalf("markdown leftover %q in %q", ban, got)
		}
	}
	for _, want := range []string{"标题", "小呆呆", "文档", "• 巡检", "df -h"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestChunkMobileSplitsParagraphs(t *testing.T) {
	long := strings.Repeat("结论。", 200)
	chunks := chunkMobile(long+"\n\n"+long, 80)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple bubbles, got %d %#v", len(chunks), chunks)
	}
	for i, c := range chunks {
		if n := []rune(c); len(n) > 80 {
			t.Fatalf("chunk %d too long: %d", i, len(n))
		}
	}
}

func TestFormatTaskNotifyFailedKeepsStatus(t *testing.T) {
	out := formatTaskNotify("failed", "abc", "执行中断")
	if !strings.Contains(out, "任务失败") || !strings.Contains(out, "执行中断") {
		t.Fatalf("got %q", out)
	}
}

func TestExtractConclusionFromReplyFinalOutput(t *testing.T) {
	got := extractConclusion("主机负载正常，无异常进程。\n")
	if got != "主机负载正常，无异常进程。" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatMobileTextDropsLegacyMCPSpecWarning(t *testing.T) {
	raw := "⚠️ MCP连接失败 [/opt/hawkeye/hawkeye-mcp-server.mjs,--port,19016]: 格式应为 name:command:arg1,arg2\nB站现在开着会员购和直播间。"
	got := formatMobileText(raw)
	if strings.Contains(got, "MCP连接失败") || strings.Contains(got, "格式应为") {
		t.Fatalf("legacy MCP warning leaked: %q", got)
	}
	if !strings.Contains(got, "会员购") {
		t.Fatalf("conclusion dropped: %q", got)
	}
}
