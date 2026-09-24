package chat

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-edr/internal/ui"
)

var (
	stepBannerRe  = regexp.MustCompile(`(?m)^[-—=\s]*\[?Step\s*\d+\s*/\s*\d+\]?[-—=\s]*$`)
	noisePrefixRe = regexp.MustCompile(`(?m)^(?:💡\s*想法|💻\s*命令|⚡\s*结果|✅\s*结果|🔧\s*工具|📂\s*读取|✏️\s*编辑|🔎\s*搜索|\[Batch\])(?:[:：\[].*)?$`)
	ansiOrCtrlRe  = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	multiBlankRe  = regexp.MustCompile(`\n{3,}`)
	sepLineRe     = regexp.MustCompile(`(?m)^[-=_]{3,}\s*$`)
	fenceRe       = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n?(.*?)```")
	headingRe     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	linkRe        = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	boldRe        = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	italicRe      = regexp.MustCompile(`\*([^*\n]+)\*`)
	inlineCodeRe  = regexp.MustCompile("`([^`]+)`")
	bulletRe      = regexp.MustCompile(`(?m)^[-*+]\s+`)
)

const (
	weixinBubbleLimit  = 800  // WeChat / WeCom phone bubbles
	feishuBubbleLimit  = 1500 // Feishu text is more tolerant
	defaultBubbleLimit = 1000
)

// formatTaskNotify builds a short, phone-friendly chat reply from the agent conclusion.
// Successful replies are plain conversation text only; meta/status is omitted unless failed.
func formatTaskNotify(status, id, raw string) string {
	body := formatMobileText(extractConclusion(raw))
	if body == "" && status == "completed" {
		body = "好的，已完成。"
	}
	if status == "completed" {
		return body
	}
	var b strings.Builder
	b.WriteString(statusIcon(status))
	b.WriteString(" ")
	b.WriteString(statusLabel(status))
	if body != "" && body != statusLabel(status) {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	return b.String()
}

func formatResultPage(status, id, pageText string, page, total int) string {
	body := formatMobileText(extractConclusion(pageText))
	if body == "" {
		body = formatMobileText(pageText)
	}
	if body == "" {
		body = "（无更多内容）"
	}
	if total <= 1 && status == "completed" {
		return body
	}
	var b strings.Builder
	if total > 1 {
		b.WriteString(strconv.Itoa(page))
		b.WriteString("/")
		b.WriteString(strconv.Itoa(total))
		b.WriteString("\n\n")
	}
	b.WriteString(body)
	if page < total {
		b.WriteString("\n\n下一页发：/ds result ")
		b.WriteString(id)
		b.WriteString(" ")
		b.WriteString(strconv.Itoa(page + 1))
	}
	return b.String()
}

func extractConclusion(raw string) string {
	raw = strings.TrimSpace(ui.StripANSI(raw))
	if raw == "" {
		return ""
	}
	for _, marker := range []string{"最终报告:", "最终报告：", "[REPORT]最终报告:", "📝最终报告:"} {
		if i := strings.Index(raw, marker); i >= 0 {
			rest := strings.TrimSpace(raw[i+len(marker):])
			rest = sepLineRe.ReplaceAllString(rest, "")
			if j := strings.Index(rest, "日志:"); j > 0 {
				rest = rest[:j]
			}
			if j := strings.Index(rest, "[LOG]"); j > 0 {
				rest = rest[:j]
			}
			rest = strings.TrimSpace(rest)
			if rest != "" {
				return rest
			}
		}
	}
	return raw
}

func formatMobileText(s string) string {
	s = ansiOrCtrlRe.ReplaceAllString(s, "")
	s = ui.StripANSI(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = stepBannerRe.ReplaceAllString(s, "")
	s = noisePrefixRe.ReplaceAllString(s, "")
	s = sepLineRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			out = append(out, "")
			continue
		}
		if isChatMetaLine(trim) {
			continue
		}
		out = append(out, trim)
	}
	s = strings.Join(out, "\n")
	s = convertMarkdownForIM(s)
	s = multiBlankRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func convertMarkdownForIM(s string) string {
	s = fenceRe.ReplaceAllStringFunc(s, func(block string) string {
		m := fenceRe.FindStringSubmatch(block)
		inner := strings.TrimSpace(block)
		if len(m) > 1 {
			inner = strings.TrimSpace(m[1])
		}
		return "\n" + inner + "\n"
	})
	s = headingRe.ReplaceAllString(s, "")
	s = linkRe.ReplaceAllStringFunc(s, func(link string) string {
		m := linkRe.FindStringSubmatch(link)
		if m[1] == m[2] {
			return m[2]
		}
		return m[1] + "（" + m[2] + "）"
	})
	s = boldRe.ReplaceAllString(s, "$1$2")
	s = inlineCodeRe.ReplaceAllString(s, "$1")
	s = italicRe.ReplaceAllString(s, "$1")
	s = bulletRe.ReplaceAllString(s, "• ")
	return s
}

func isChatMetaLine(line string) bool {
	switch {
	case strings.HasPrefix(line, "--- [Step"), strings.HasPrefix(line, "---[Step"):
		return true
	case strings.HasPrefix(line, "♻️已恢复会话 session_"):
		return true
	case strings.HasPrefix(line, "💡已追加本次 --task/参数"):
		return true
	case strings.HasPrefix(line, "[模式切换]"), strings.HasPrefix(line, "🔌 [模式切换]"):
		return true
	case strings.HasPrefix(line, "[RESUME]"), strings.HasPrefix(line, "[TIP]"), strings.HasPrefix(line, "[CFG]"):
		return true
	case strings.Contains(line, "MCP连接失败") && strings.Contains(line, "格式应为"):
		return true
	default:
		return false
	}
}

func platformBubbleLimit(platform string) int {
	switch platform {
	case "weixin", "wechat", "wecom", "wecom_ws":
		return weixinBubbleLimit
	case "feishu", "feishu_ws", "dingtalk":
		return feishuBubbleLimit
	default:
		return defaultBubbleLimit
	}
}

// chunkMobile splits IM text at paragraph / sentence boundaries, like OpenClaw auto-reply chunking.
func chunkMobile(s string, limit int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return []string{s}
	}
	var chunks []string
	for _, para := range strings.Split(s, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if utf8.RuneCountInString(para) <= limit {
			chunks = appendPacked(chunks, para, limit)
			continue
		}
		chunks = append(chunks, splitRunes(para, limit)...)
	}
	if len(chunks) == 0 {
		return []string{s}
	}
	return chunks
}

func appendPacked(chunks []string, para string, limit int) []string {
	if len(chunks) == 0 {
		return []string{para}
	}
	last := chunks[len(chunks)-1]
	joined := last + "\n\n" + para
	if utf8.RuneCountInString(joined) <= limit {
		chunks[len(chunks)-1] = joined
		return chunks
	}
	return append(chunks, para)
}

func splitRunes(s string, limit int) []string {
	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		if len(runes) <= limit {
			out = append(out, string(runes))
			break
		}
		cut := limit
		window := runes[:limit]
		if i := lastIndexAny(window, []rune("\n。！？；;!?")); i >= limit/3 {
			cut = i + 1
		} else if i := lastIndexAny(window, []rune("，,、 ")); i >= limit/2 {
			cut = i + 1
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
		for len(runes) > 0 && (runes[0] == ' ' || runes[0] == '\n') {
			runes = runes[1:]
		}
	}
	return out
}

func lastIndexAny(runes []rune, needles []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		for _, n := range needles {
			if runes[i] == n {
				return i
			}
		}
	}
	return -1
}

func statusIcon(status string) string {
	switch status {
	case "completed":
		return "✅"
	case "failed":
		return "❌"
	case "cancelled", "cancelling":
		return "⏹️"
	case "timed_out":
		return "⏰"
	case "interrupted":
		return "⚠️"
	default:
		return "ℹ️"
	}
}

func statusLabel(status string) string {
	switch status {
	case "completed":
		return "任务完成"
	case "failed":
		return "任务失败"
	case "cancelled", "cancelling":
		return "已取消"
	case "timed_out":
		return "已超时"
	case "interrupted":
		return "已中断"
	case "running":
		return "执行中"
	case "queued":
		return "排队中"
	default:
		return status
	}
}
