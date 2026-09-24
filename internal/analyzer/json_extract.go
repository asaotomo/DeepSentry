package analyzer

import (
	"encoding/json"
	"strings"
	"unicode"
)

// recoverLocalControlMessage handles special-token tool messages occasionally
// emitted as plain text by local chat templates. Only complete, anchored JSON
// envelopes become actions; malformed/unknown tokens request a corrected turn.
func recoverLocalControlMessage(raw string) (AgentResponse, bool) {
	text := strings.TrimSpace(raw)
	var header, payload string
	switch {
	case strings.HasPrefix(text, "<|channel|>"):
		var found bool
		header, payload, found = strings.Cut(text, "<|message|>")
		if !found {
			return localControlRetry(), true
		}
	case strings.HasPrefix(text, "<|im_start|>assistant"):
		var found bool
		header, payload, found = strings.Cut(text, "<|im_sep|>")
		if !found {
			return localControlRetry(), true
		}
	default:
		return AgentResponse{}, false
	}
	toolName := ""
	for _, field := range strings.Fields(header) {
		if strings.HasPrefix(field, "to=") {
			toolName = strings.SplitN(strings.TrimPrefix(field, "to="), "<|", 2)[0]
			break
		}
	}
	payload = strings.TrimSpace(payload)
	for _, suffix := range []string{"<|im_end|>", "<|eot_id|>"} {
		payload = strings.TrimSpace(strings.TrimSuffix(payload, suffix))
	}
	if toolName == "" && (strings.Contains(header, "<|channel|>final") || strings.Contains(header, "<|meta_sep|>final")) {
		if resp, ok := recoverPlainTextResponse(payload); ok {
			return resp, true
		}
		return localControlRetry(), true
	}
	if !json.Valid([]byte(payload)) || len(payload) > 64*1024 {
		return localControlRetry(), true
	}
	switch toolName {
	case "execute":
		var args struct {
			Cmd     string `json:"cmd"`
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(payload), &args) != nil {
			return localControlRetry(), true
		}
		command := strings.TrimSpace(args.Command)
		if command == "" {
			command = strings.TrimSpace(args.Cmd)
		}
		if command == "" {
			return localControlRetry(), true
		}
		return AgentResponse{Action: "execute", Command: command, Thought: "本地模型请求执行命令。"}, true
	case "agent_action":
		resp, err := ParseToolCallResponse(payload)
		if err == nil {
			return resp, true
		}
	}
	return localControlRetry(), true
}

func localControlRetry() AgentResponse {
	return AgentResponse{Thought: "本地模型输出了无法识别的工具控制格式，正在请求标准 JSON 动作。", RiskLevel: "low"}
}

// extractJSONPayload 从混合文本/Markdown 中提取 JSON 对象，并返回前置说明文字
func extractJSONPayload(s string) (jsonPart, prose string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}

	// 优先：Markdown ```json ... ``` 或 ``` ... ```
	if block, before := extractMarkdownJSONBlock(s); block != "" {
		if obj := extractBalancedJSONObject(block); obj != "" {
			return obj, strings.TrimSpace(before)
		}
	}

	// 其次：文本中首个平衡 JSON 对象
	if obj := extractBalancedJSONObject(s); obj != "" {
		before := strings.TrimSpace(s[:strings.Index(s, obj)])
		return obj, before
	}

	return s, ""
}

func extractMarkdownJSONBlock(s string) (block, before string) {
	bestIdx := -1
	bestOpener := ""
	for _, opener := range []string{"```json", "```JSON", "```"} {
		if idx := strings.Index(s, opener); idx != -1 && (bestIdx == -1 || idx < bestIdx) {
			bestIdx = idx
			bestOpener = opener
		}
	}
	if bestIdx == -1 {
		return "", ""
	}
	before = s[:bestIdx]
	rest := s[bestIdx+len(bestOpener):]
	end := strings.Index(rest, "```")
	if end == -1 {
		return "", before
	}
	return strings.TrimSpace(rest[:end]), before
}

// extractBalancedJSONObject 提取第一个平衡的 {...} 片段
func extractBalancedJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func normalizeProseThought(prose string) string {
	prose = strings.TrimSpace(prose)
	if prose == "" {
		return ""
	}
	// 去掉常见前缀符号
	prose = strings.TrimPrefix(prose, "💭")
	prose = strings.TrimSpace(prose)
	if runes := []rune(prose); len(runes) > 500 {
		prose = string(runes[:500]) + "..."
	}
	return prose
}

// recoverPlainTextResponse handles providers that occasionally ignore the
// requested JSON envelope and answer with ordinary Markdown.  A readable
// answer must never be turned into a fatal "JSON parse failed" report.
func recoverPlainTextResponse(raw string) (AgentResponse, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return AgentResponse{}, false
	}
	if strings.HasPrefix(text, "<|channel|>") || strings.HasPrefix(text, "<|im_start|>assistant to=") {
		return localControlRetry(), true
	}

	// A response that is clearly trying to be JSON is more likely truncated
	// than conversational. Let the harness request a corrected action instead
	// of exposing parser internals to the user.
	if looksLikeJSONEnvelope(text) {
		return AgentResponse{
			Thought:   "模型响应 JSON 不完整，正在自动请求重新输出。",
			RiskLevel: "low",
		}, true
	}

	if question := extractClarificationQuestion(text); question != "" {
		return AgentResponse{
			Thought:   "需要用户补充信息后继续任务",
			Action:    "ask_user",
			Question:  question,
			RiskLevel: "low",
		}, true
	}

	// A complete shell fence is actionable even when its prose introduction is
	// informal. Detect it directly so broad words such as “让我/下一步” are not
	// needed as execution signals (those words also appear frequently in final
	// summaries and reflective answers).
	if command := extractMarkdownShellCommand(text); command != "" {
		thought := normalizeProseThought(textBeforeFirstFence(text))
		if thought == "" {
			thought = "模型返回了自然语言步骤，已从代码块恢复待执行命令。"
		}
		return finalizeResponse(AgentResponse{
			Thought:   thought,
			Action:    "execute",
			Command:   command,
			RiskLevel: "low",
		}), true
	}

	if looksLikeExecutionIntent(text) {
		// The model announced another step but omitted the machine-readable
		// action. Ask it to continue rather than presenting an unfinished plan
		// as a final report.
		return AgentResponse{
			Thought:   "模型已说明下一步但未给出可执行指令，正在自动请求补全。",
			RiskLevel: "low",
		}, true
	}

	// Plain prose without a pending action is already a valid user-facing
	// answer. Preserve its Markdown and finish cleanly.
	return AgentResponse{
		Thought:     "模型返回了自然语言结论，已直接呈现。",
		Action:      "finish",
		FinalReport: text,
		IsFinished:  true,
		RiskLevel:   "low",
	}, true
}

func looksLikeJSONEnvelope(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "{") ||
		strings.HasPrefix(strings.ToLower(trimmed), "```json") ||
		strings.Contains(trimmed, `"action"`)
}

func looksLikeExecutionIntent(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"接下来执行", "下一步执行", "我将执行", "我会执行", "先执行", "先读取", "先检查", "开始排查",
		"开始检查", "准备执行", "执行下面", "执行以下", "运行下面", "运行以下", "用下面命令",
		"i'll run", "i will run", "i will execute", "next command", "next step is to run", "starting the check",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func textBeforeFirstFence(text string) string {
	if idx := strings.Index(text, "```"); idx >= 0 {
		return strings.TrimSpace(text[:idx])
	}
	return text
}

func extractMarkdownShellCommand(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	inFence := false
	shellFence := false
	var command []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				lang := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
				shellFence = lang == "" || lang == "sh" || lang == "bash" || lang == "shell" || lang == "zsh" || lang == "console"
				inFence = true
				command = command[:0]
				continue
			}
			if shellFence {
				candidate := strings.TrimSpace(strings.Join(command, "\n"))
				if candidate != "" {
					return stripShellPrompt(candidate)
				}
			}
			inFence = false
			shellFence = false
			command = command[:0]
			continue
		}
		if inFence && shellFence {
			command = append(command, line)
		}
	}
	return ""
}

func stripShellPrompt(command string) string {
	lines := strings.Split(command, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		if strings.HasPrefix(trimmed, "$ ") || strings.HasPrefix(trimmed, "> ") {
			indent := line[:len(line)-len(trimmed)]
			lines[i] = indent + trimmed[2:]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
