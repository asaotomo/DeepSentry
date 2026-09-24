package harness

import (
	"fmt"
	"sort"
	"strings"

	"ai-edr/internal/config"
	"ai-edr/internal/executor"
)

const confirmPreviewLimit = 1500

// ConfirmPreview is the concrete action shown in chat confirmations. The
// caller passes an already redacted action, and this preview keeps that text.
func ConfirmPreview(action *AgentAction) string {
	if action == nil {
		return ""
	}
	var text string
	switch action.Type {
	case ActionExecute:
		text = strings.TrimSpace(action.Command)
	case ActionTool:
		text = previewTool(action.ToolName, action.ToolArgs)
	case ActionWriteFile:
		text = previewWrite(action.Path, action.Content)
	case ActionEditFile:
		text = previewEdit(action.Path, action.OldString, action.NewString)
	case ActionToolBatch:
		text = previewToolBatch(action.ToolCalls)
	default:
		text = strings.TrimSpace(string(action.Type))
		if path := strings.TrimSpace(action.Path); path != "" {
			text = joinPreview(text, "path: "+path)
		}
		if command := strings.TrimSpace(action.Command); command != "" {
			text = joinPreview(text, command)
		}
	}
	return clipConfirmPreview(text, confirmPreviewLimit)
}

func previewTool(name string, args map[string]string) string {
	var b strings.Builder
	if name = strings.TrimSpace(name); name != "" {
		b.WriteString(name)
	}
	if name == "fleet_exec" || name == "fleet_file" {
		selector := strings.TrimSpace(args["selector"])
		if selector == "" {
			selector = strings.TrimSpace(args["target"])
		}
		if selector == "" {
			selector = strings.TrimSpace(args["targets"])
		}
		selected := executor.MatchTargets(config.GlobalConfig.Targets, selector)
		writePreviewLine(&b, "matched_targets", fmt.Sprintf("%d", len(selected)))
	}
	priority := []string{"selector", "target", "targets", "action", "remote_path", "local_path", "concurrency", "command", "cmd", "content", "script", "code", "path", "url", "operation"}
	used := map[string]bool{}
	hasPayload := false
	for _, key := range priority {
		value, original, ok := takePreviewArg(args, key, used)
		if !ok {
			continue
		}
		used[original] = true
		if key == "command" || key == "cmd" || key == "content" || key == "script" || key == "code" {
			hasPayload = true
		}
		writePreviewLine(&b, key, value)
	}
	if !hasPayload {
		keys := make([]string, 0, len(args))
		for key, value := range args {
			if used[key] || strings.TrimSpace(value) == "" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			writePreviewLine(&b, key, strings.TrimSpace(args[key]))
		}
	}
	return b.String()
}

func takePreviewArg(args map[string]string, want string, used map[string]bool) (value, original string, ok bool) {
	wantValue, found := lookupApprovalArg(args, want)
	wantValue = strings.TrimSpace(wantValue)
	if !found || wantValue == "" {
		return "", "", false
	}
	for key, raw := range args {
		if used[key] || strings.TrimSpace(raw) == "" {
			continue
		}
		got, match := lookupApprovalArg(map[string]string{key: raw}, want)
		if match && strings.TrimSpace(got) == strings.TrimSpace(raw) {
			return strings.TrimSpace(raw), key, true
		}
	}
	return wantValue, want, true
}

func previewWrite(path, content string) string {
	var b strings.Builder
	if path = strings.TrimSpace(path); path != "" {
		b.WriteString("path: ")
		b.WriteString(path)
	}
	if content = strings.TrimSpace(content); content != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(content)
	}
	return b.String()
}

func previewEdit(path, oldString, newString string) string {
	var b strings.Builder
	if path = strings.TrimSpace(path); path != "" {
		b.WriteString("path: ")
		b.WriteString(path)
	}
	oldString = strings.TrimSpace(oldString)
	newString = strings.TrimSpace(newString)
	if oldString != "" || newString != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(oldString)
		b.WriteString("\n->\n")
		b.WriteString(newString)
	}
	return b.String()
}

func previewToolBatch(calls []ToolCallAction) string {
	var b strings.Builder
	n := 0
	for _, call := range calls {
		line := previewTool(call.Name, call.Args)
		if line == "" {
			continue
		}
		if n > 0 {
			b.WriteByte('\n')
		}
		n++
		fmt.Fprintf(&b, "%d. %s", n, line)
	}
	if n == 0 {
		return "tool_batch"
	}
	return b.String()
}

func writePreviewLine(b *strings.Builder, key, value string) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
}

func joinPreview(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "\n" + right
}

func clipConfirmPreview(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	note := "…（已截断）"
	keep := limit - len([]rune(note))
	if keep < 1 {
		keep = 1
	}
	if keep > len(runes) {
		keep = len(runes)
	}
	return strings.TrimRight(string(runes[:keep]), "\n") + note
}
