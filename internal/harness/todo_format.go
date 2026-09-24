package harness

import (
	"ai-edr/internal/ui"
	"fmt"
	"strings"
)

func todoStatusIcon(status string) string {
	if ui.PlainTextMode() {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "in_progress", "running":
			return "[*]"
		case "completed", "done", "finished":
			return "[x]"
		case "cancelled", "canceled":
			return "[!]"
		default:
			return "[ ]"
		}
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in_progress", "running":
		return "🔄"
	case "completed", "done", "finished":
		return "✅"
	case "cancelled", "canceled":
		return "❌"
	default:
		return "⬜"
	}
}

func todoStatusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in_progress", "running":
		return "进行中"
	case "completed", "done", "finished":
		return "已完成"
	case "cancelled", "canceled":
		return "已取消"
	default:
		return "待办"
	}
}

// FormatTodoList 格式化任务清单（stdout / TUI 共用）
func FormatTodoList(todos []TodoItem) string {
	if len(todos) == 0 {
		return ui.Prefix("📋", "[TODO]") + "任务清单为空"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s任务清单 (%d 项)\n", ui.Prefix("📋", "[TODO]"), len(todos)))
	for i, t := range todos {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			id = fmt.Sprintf("%d", i+1)
		}
		content := strings.TrimSpace(t.Content)
		if content == "" {
			content = "(未描述)"
		}
		status := todoStatusLabel(t.Status)
		b.WriteString(fmt.Sprintf("  %s [%s] %s  (%s)\n",
			todoStatusIcon(t.Status), id, content, status))
	}
	return strings.TrimRight(b.String(), "\n")
}
