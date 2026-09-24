package analyzer

import (
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/security"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Message struct {
	ID               string            `json:"id,omitempty"`
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	Synthetic        bool              `json:"synthetic,omitempty"` // model-facing feedback, not a user turn
	Attachments      []ImageAttachment `json:"attachments,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	Name             string            `json:"name,omitempty"`
}

// ImageAttachment is a durable, path-backed image reference. DeepSentry keeps
// binary data out of checkpoints and traces; provider adapters materialize a
// validated data URL/base64 block only for the outbound model request.
type ImageAttachment struct {
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ChatRequest struct {
	Model         string              `json:"model"`
	Messages      []openAIChatMessage `json:"messages"`
	Stream        bool                `json:"stream"`
	Temperature   float64             `json:"temperature"`
	MaxTokens     int                 `json:"max_tokens,omitempty"`
	Tools         []ToolDefinition    `json:"tools,omitempty"`
	ToolChoice    interface{}         `json:"tool_choice,omitempty"`
	StreamOptions *StreamOptions      `json:"stream_options,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content,omitempty"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage TokenUsage `json:"usage"`
}

type AgentResponse struct {
	Thought     string   `json:"thought"`
	Command     string   `json:"command"`
	RiskLevel   string   `json:"risk_level"`
	Reason      string   `json:"reason"`
	IsFinished  bool     `json:"is_finished"`
	FinalReport string   `json:"final_report"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`

	// Deep Agent Harness 扩展字段（对标 deepagents 多工具协议）
	Action         string     `json:"action"`
	TaskName       string     `json:"task_name"`
	TaskPrompt     string     `json:"task_prompt"`
	TaskMaxSteps   int        `json:"task_max_steps"`
	ParallelTasks  []TaskSpec `json:"parallel_tasks"`
	TargetSelector string     `json:"target_selector"`
	TargetName     string     `json:"target_name"`
	TargetProtocol string     `json:"target_protocol"`
	TargetHost     string     `json:"target_host"`
	SkillName      string     `json:"skill_name"`
	Path           string     `json:"path"`
	Content        string     `json:"content"`
	Pattern        string     `json:"pattern"`
	Todos          []TodoItem `json:"todos"`

	// memory
	MemoryKey   string `json:"memory_key"`
	MemoryValue string `json:"memory_value"`
	MemoryScope string `json:"memory_scope"`

	// tool
	ToolName string            `json:"tool_name"`
	ToolArgs map[string]string `json:"tool_args"`

	// edit_file / glob
	OldString   string `json:"old_string"`
	NewString   string `json:"new_string"`
	ReplaceAll  bool   `json:"replace_all"`
	GlobPattern string `json:"glob_pattern"`
	Offset      int    `json:"offset,omitempty"`
	Limit       int    `json:"limit,omitempty"`

	// NativeToolCalls preserves provider call IDs and every call in a single
	// response. Legacy fields remain populated for single-call compatibility.
	ToolCallID       string           `json:"-"`
	ToolCallName     string           `json:"-"`
	ReasoningContent string           `json:"-"`
	NativeToolCalls  []NativeToolCall `json:"-"`
}

type NativeToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// TodoItem 任务清单项
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

type TaskSpec struct {
	TaskName       string `json:"task_name"`
	TaskPrompt     string `json:"task_prompt"`
	TargetSelector string `json:"target_selector"`
	TaskMaxSteps   int    `json:"task_max_steps"`
}

func (t *TodoItem) UnmarshalJSON(data []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.ID = stringifyTodoField(raw["id"])
	t.Content = firstNonEmptyString(
		stringifyTodoField(raw["content"]),
		stringifyTodoField(raw["title"]),
		stringifyTodoField(raw["detail"]),
		stringifyTodoField(raw["description"]),
	)
	if detail := stringifyTodoField(raw["detail"]); detail != "" && t.Content != "" && detail != t.Content {
		t.Content = t.Content + " - " + detail
	}
	t.Status = stringifyTodoField(raw["status"])
	return nil
}

func stringifyTodoField(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	case bool:
		return fmt.Sprintf("%v", x)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", x))
	}
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// 兼容性结构体：用于解析 AI 可能返回的多种格式
type CompatibilityResponse struct {
	Thought        string                 `json:"thought"`
	Command        string                 `json:"command"`
	RiskLevel      string                 `json:"risk_level"`
	IsFinished     bool                   `json:"is_finished"`
	FinalReport    interface{}            `json:"final_report"`
	Question       string                 `json:"question"`
	Options        []string               `json:"options"`
	CmdArray       []string               `json:"cmd"`
	Explanation    string                 `json:"explanation"`
	Action         string                 `json:"action"`
	TaskName       string                 `json:"task_name"`
	TaskPrompt     string                 `json:"task_prompt"`
	TaskMaxSteps   int                    `json:"task_max_steps"`
	ParallelTasks  []TaskSpec             `json:"parallel_tasks"`
	TargetSelector string                 `json:"target_selector"`
	TargetName     string                 `json:"target_name"`
	TargetProtocol string                 `json:"target_protocol"`
	TargetHost     string                 `json:"target_host"`
	SkillName      string                 `json:"skill_name"`
	Path           string                 `json:"path"`
	Content        string                 `json:"content"`
	Pattern        string                 `json:"pattern"`
	Todos          []TodoItem             `json:"todos"`
	MemoryKey      string                 `json:"memory_key"`
	MemoryValue    string                 `json:"memory_value"`
	MemoryScope    string                 `json:"memory_scope"`
	ToolName       string                 `json:"tool_name"`
	ToolArgs       map[string]interface{} `json:"tool_args"`
	OldString      string                 `json:"old_string"`
	NewString      string                 `json:"new_string"`
	ReplaceAll     bool                   `json:"replace_all"`
	GlobPattern    string                 `json:"glob_pattern"`
	Offset         int                    `json:"offset,omitempty"`
	Limit          int                    `json:"limit,omitempty"`
}

// StepOptions Agent 单步选项
type StepOptions struct {
	Context        context.Context
	SysCtx         collector.SystemContext
	History        *[]Message
	ExtraPrompt    string
	PinnedContext  string
	UseNativeTools bool
	OnStream       func(delta string) // 非 nil 且模型支持时启用 SSE 流式输出
	OnUsage        func(TokenUsage)   // 模型返回真实 usage 时回调
	OnModelRoute   func(ModelRouteInfo)
	OnContextEvent func(compacted bool, fallback bool, beforeTokens int, afterTokens int)
}

type ModelRouteInfo struct {
	ModelID   string
	Attempts  int
	Failovers int
}

// RunAgentStep 执行 Agent 的单步思考
func RunAgentStep(sysCtx collector.SystemContext, history *[]Message) (AgentResponse, error) {
	return RunAgentStepWithPrompt(sysCtx, history, "")
}

// RunAgentStepWithPrompt 支持额外 system prompt 注入（供 Deep Agent Harness 使用）
func RunAgentStepWithPrompt(sysCtx collector.SystemContext, history *[]Message, extraPrompt string) (AgentResponse, error) {
	return RunAgentStepWithOptions(StepOptions{
		SysCtx:         sysCtx,
		History:        history,
		ExtraPrompt:    extraPrompt,
		UseNativeTools: config.GlobalConfig.UseNativeTools,
	})
}

// RunAgentStepWithOptions 完整单步选项
func RunAgentStepWithOptions(opts StepOptions) (AgentResponse, error) {
	sysCtx := opts.SysCtx
	history := opts.History
	extraPrompt := opts.ExtraPrompt

	// 1. 获取基础 System Prompt (来自 collector)
	basePrompt := sysCtx.GenerateSystemPrompt()
	if extraPrompt != "" {
		basePrompt = basePrompt + extraPrompt
	}

	// 增强 Windows 路径操作指南；输出约束与实际工具传输模式一致。
	selfProtectionPrompt := `
【⛔ 核心自我保护守则】
1. 绝对禁止删除/移动 config.yaml, deepsentry.exe, reports/ 目录。

【🪟 Windows 文件操作专家模式】
1. **中文路径与乱码**：如果 'dir' 显示乱码，请使用通配符 (*.pdf) 操作，不要直接复制乱码文件名。
2. **路径变量**：使用 PowerShell 时可直接用 $HOME。
`
	if opts.UseNativeTools && config.GlobalConfig.IsOpenAICompatible() {
		selfProtectionPrompt += `
【⚠️ 原生工具调用】
需要操作时通过 API 的 tool_calls 调用已提供的工具；参数必须是合法 JSON 对象。不要把 <|channel|>、to=execute、<|message|> 等控制标记写进普通文本。最终答复可通过 agent_action 的 finish 动作返回。
`
	} else {
		selfProtectionPrompt += `
【⚠️ JSON 严格语法】
1. 在 JSON 字符串值中，**双引号 (") 必须转义为 (\\")**。
2. **反斜杠 (\\) 必须转义为 (\\\\)**。
3. **严禁** Markdown 代码块或与 JSON 混排；说明只能放 thought 字段。
4. 响应必须是纯 JSON 对象，以 { 开头、以 } 结尾。
5. 若需要执行，输出 {"action":"execute","command":"实际命令"}；不要输出 to=execute 或其他控制标记。
`
	}
	systemPrompt := basePrompt + selfProtectionPrompt

	capabilities := config.GlobalConfig.EffectiveModelCapabilities()
	systemPrompt = fitSystemPrompt(systemPrompt, capabilities.SystemPromptBudgetTokens())
	requestOverheadTokens := EstimateTextTokens(systemPrompt)
	if opts.UseNativeTools && config.GlobalConfig.IsOpenAICompatible() {
		toolMessages := append([]Message{{Role: "system", Content: systemPrompt}}, (*history)...)
		if encodedTools, encodeErr := json.Marshal(nativeToolDefinitionsForRequest(config.GlobalConfig, toolMessages)); encodeErr == nil {
			requestOverheadTokens += EstimateTextTokens(string(encodedTools))
		}
	}
	historyBudgetTokens := capabilities.HistoryBudgetTokens(requestOverheadTokens)
	beforeTokens := EstimateMessagesTokens(*history)
	compacted, compactErr := ManageHistoryContextWithOptions(history, ContextManageOptions{
		Context:             opts.Context,
		PinnedContext:       opts.PinnedContext,
		HistoryBudgetTokens: historyBudgetTokens,
		KeepRecent:          capabilities.KeepRecentMessages,
		SummaryChunkTokens:  capabilities.SummaryChunkTokens,
	})
	fallback := false
	if compactErr != nil {
		fallback = true
		truncateHistoryFallbackToBudget(history, maxAnalyzerInt(4, capabilities.KeepRecentMessages/2), opts.PinnedContext, historyBudgetTokens)
	}
	if opts.OnContextEvent != nil && (compacted || fallback) {
		opts.OnContextEvent(compacted, fallback, beforeTokens, EstimateMessagesTokens(*history))
	}

	messages := []Message{
		{Role: "system", Content: systemPrompt},
	}
	messages = append(messages, *history...)
	messages = PatchDanglingToolCalls(messages)

	llmResult, err := CallLLMWithRetryContext(opts.Context, messages, opts.UseNativeTools, opts.OnStream)
	if err != nil && isContextLimitError(err) && history != nil {
		// Runtime context settings on local servers can be lower than the
		// model card. Recover once mechanically, preserve goal/clues/tail, and
		// retry instead of repeating the same oversized request.
		before := EstimateMessagesTokens(*history)
		truncateHistoryFallbackToBudget(history, maxAnalyzerInt(4, capabilities.KeepRecentMessages/2), opts.PinnedContext, historyBudgetTokens/2)
		messages = []Message{{Role: "system", Content: systemPrompt}}
		messages = append(messages, *history...)
		messages = PatchDanglingToolCalls(messages)
		if opts.OnContextEvent != nil {
			opts.OnContextEvent(false, true, before, EstimateMessagesTokens(*history))
		}
		llmResult, err = CallLLMWithRetryContext(opts.Context, messages, opts.UseNativeTools, opts.OnStream)
	}
	if err != nil {
		return AgentResponse{}, err
	}
	if opts.OnUsage != nil && llmResult.Usage.HasAny() {
		opts.OnUsage(llmResult.Usage)
	}
	if opts.OnModelRoute != nil {
		opts.OnModelRoute(ModelRouteInfo{ModelID: llmResult.ModelID, Attempts: llmResult.Attempts, Failovers: llmResult.Failovers})
	}
	rawResp := llmResult.Content
	toolCallArgs := llmResult.ToolCallArgs

	if len(llmResult.ToolCalls) > 1 {
		allDirectTools := true
		calls := make([]NativeToolCall, 0, len(llmResult.ToolCalls))
		for _, call := range llmResult.ToolCalls {
			if call.Name == "agent_action" {
				allDirectTools = false
				break
			}
			calls = append(calls, NativeToolCall(call))
		}
		if allDirectTools {
			return AgentResponse{Action: "tool_batch", ReasoningContent: llmResult.ReasoningContent, NativeToolCalls: calls}, nil
		}
	}

	if toolCallArgs != "" {
		resp, perr := ParseNamedToolCall(llmResult.ToolCallName, toolCallArgs)
		if perr == nil {
			resp.ToolCallID = llmResult.ToolCallID
			resp.ToolCallName = llmResult.ToolCallName
			resp.ReasoningContent = llmResult.ReasoningContent
			return finalizeResponse(resp), nil
		}
		// fallback to JSON content parse
	}
	if recovered, ok := recoverLocalControlMessage(rawResp); ok {
		return finalizeResponse(recovered), nil
	}

	// 2. 清洗 JSON（支持 Markdown 代码块 + 前置说明文字）
	cleanResp, prose := cleanJSON(rawResp)
	var compat CompatibilityResponse

	// 3. 尝试标准解析
	err = json.Unmarshal([]byte(cleanResp), &compat)

	// 🟢 JSON 解析失败时的智能兜底
	if err != nil {
		fixTry := cleanResp
		if !strings.HasSuffix(strings.TrimSpace(fixTry), "}") {
			fixTry += "}"
		}

		if err2 := json.Unmarshal([]byte(fixTry), &compat); err2 != nil {
			// 再次从原始响应提取 JSON
			if retry, _ := extractJSONPayload(rawResp); retry != "" && retry != cleanResp {
				if err3 := json.Unmarshal([]byte(retry), &compat); err3 == nil {
					err = nil
					cleanResp = retry
				}
			}
		} else {
			err = nil
		}
	}

	if err != nil {
		extractedCmd, found := extractCommandString(cleanResp)
		if !found {
			extractedCmd, found = extractCommandString(rawResp)
		}

		if found && extractedCmd != "" {
			compat.Command = extractedCmd
			compat.Thought = "JSON 格式异常(转义错误)，已启用【字符级扫描】精确提取命令。"
			compat.RiskLevel = "high"
			err = nil
		} else {
			if recovered, ok := recoverPlainTextResponse(rawResp); ok {
				return recovered, nil
			}
			return AgentResponse{Thought: "模型响应为空，正在自动请求重新输出。", RiskLevel: "low"}, nil
		}
	}

	if compat.Thought == "" && prose != "" {
		compat.Thought = normalizeProseThought(prose)
	} else if compat.Thought == "" {
		if p := normalizeProseThought(prose); p != "" {
			compat.Thought = p
		}
	}

	resp := AgentResponse{
		RiskLevel:      compat.RiskLevel,
		IsFinished:     compat.IsFinished,
		Question:       compat.Question,
		Options:        compat.Options,
		Action:         compat.Action,
		TaskName:       compat.TaskName,
		TaskPrompt:     compat.TaskPrompt,
		TargetSelector: compat.TargetSelector,
		TargetName:     compat.TargetName,
		TargetProtocol: compat.TargetProtocol,
		TargetHost:     compat.TargetHost,
		SkillName:      compat.SkillName,
		Path:           compat.Path,
		Content:        compat.Content,
		Pattern:        compat.Pattern,
		Todos:          compat.Todos,
		MemoryKey:      compat.MemoryKey,
		MemoryValue:    compat.MemoryValue,
		MemoryScope:    compat.MemoryScope,
		ToolName:       compat.ToolName,
		ToolArgs:       parseToolArgs(compat.ToolArgs),
		OldString:      compat.OldString,
		NewString:      compat.NewString,
		ReplaceAll:     compat.ReplaceAll,
		GlobPattern:    compat.GlobPattern,
		Offset:         compat.Offset,
		Limit:          compat.Limit,
	}

	// 适配 Command (兼容 string 或 []string)
	if compat.Command != "" {
		resp.Command = compat.Command
	} else if len(compat.CmdArray) > 0 {
		resp.Command = compat.CmdArray[len(compat.CmdArray)-1]
	}

	// 适配 Thought
	if compat.Thought != "" {
		resp.Thought = compat.Thought
	} else if compat.Explanation != "" {
		resp.Thought = compat.Explanation
	} else {
		resp.Thought = inferThoughtFromCommand(resp.Command)
	}

	// 适配 Report
	switch v := compat.FinalReport.(type) {
	case string:
		resp.FinalReport = v
	case map[string]interface{}, []interface{}:
		prettyBytes, _ := json.MarshalIndent(v, "", "  ")
		resp.FinalReport = string(prettyBytes)
	default:
		if v != nil {
			resp.FinalReport = fmt.Sprintf("%v", v)
		}
	}

	return finalizeResponse(resp), nil
}

// PatchDanglingToolCalls inserts explicit failed tool results for calls whose
// execution was canceled or lost during restore. Strict provider APIs reject
// histories containing unmatched calls.
func PatchDanglingToolCalls(messages []Message) []Message {
	results := make(map[string]bool)
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID != "" {
			results[message.ToolCallID] = true
		}
	}
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, message)
		for _, call := range message.ToolCalls {
			if call.ID == "" || results[call.ID] {
				continue
			}
			out = append(out, Message{
				Role:       "tool",
				Name:       call.Function.Name,
				ToolCallID: call.ID,
				Content:    "Tool execution was interrupted before a result was recorded.",
			})
		}
	}
	return out
}

func extractClarificationQuestion(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	// Plain-text recovery must be deliberately conservative. A final answer
	// often contains rhetorical questions, words such as “需要/选择/地址”, or
	// URLs; those signals alone do not mean the user must provide anything.
	// Prefer explicit missing-information requests, plus short direct questions.
	strongRequests := []string{
		"请提供", "请补充", "请告诉", "请告知", "请确认", "请填写", "请给出", "请选择", "请回答",
		"麻烦提供", "需要您提供", "需要你提供", "还缺少", "缺少必要", "缺少以下", "无法继续", "才能继续",
		"please provide", "please tell", "please confirm", "please choose", "please answer",
		"need you to provide", "missing required", "cannot continue", "before i can continue",
	}
	for _, marker := range strongRequests {
		if strings.Contains(text, marker) || strings.Contains(lower, marker) {
			return truncateClarificationQuestion(text)
		}
	}
	if looksLikeInteractiveChoicePrompt(text) {
		return truncateClarificationQuestion(text)
	}

	runes := []rune(text)
	if len(runes) <= 240 && strings.Count(text, "\n") <= 3 && endsAsQuestion(text) {
		questionWords := []string{
			"什么", "哪个", "哪一个", "哪种", "是否", "还是", "多少", "哪里", "何时", "什么时候", "怎么", "如何", "能否", "要不要", "可以吗",
			"what", "which", "where", "when", "how", "would you", "do you", "can you", "should i",
		}
		for _, marker := range questionWords {
			if strings.Contains(text, marker) || strings.Contains(lower, marker) {
				return text
			}
		}
	}
	return ""
}

func looksLikeInteractiveChoicePrompt(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	// An ask_user action is already a strong protocol signal. These phrases
	// distinguish an actionable choice/request from a rhetorical question in a
	// long conclusion, even when playful prose follows the final question mark.
	for _, marker := range []string{
		"请选", "选一个", "选一项", "选哪", "想选", "想要哪", "你选", "您选", "翻牌",
		"回复序号", "回复编号", "输入序号", "输入编号", "请回复", "告诉我你的选择", "告诉我您的选择",
		"choose one", "pick one", "select one", "which option", "reply with", "enter the number",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	// Menu-shaped asks often carry the question near the top and end with a
	// friendly sentence. Require both a question and explicit menu/option
	// language so ordinary reports containing lists do not become prompts.
	hasQuestion := strings.Contains(text, "？") || strings.Contains(text, "?")
	hasMenu := false
	for _, marker := range []string{"菜单", "选项", "可选", "请选择以下", "options", "choices", "menu"} {
		if strings.Contains(lower, marker) {
			hasMenu = true
			break
		}
	}
	return hasQuestion && hasMenu && choiceLineCount(text) >= 2
}

func choiceLineCount(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*") || strings.HasPrefix(line, "•") {
			count++
			continue
		}
		runes := []rune(line)
		if len(runes) >= 2 && runes[0] >= '0' && runes[0] <= '9' && (runes[1] == '.' || runes[1] == ')' || runes[1] == '、') {
			count++
			continue
		}
		// Emoji-led menu rows in conversational Skills commonly use " — " or
		// " - " instead of Markdown bullets.
		if strings.Contains(line, " — ") || strings.Contains(line, " - ") {
			count++
		}
	}
	return count
}

func endsAsQuestion(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasSuffix(text, "？") || strings.HasSuffix(text, "?")
}

func truncateClarificationQuestion(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 1200 {
		return string(runes[:1200]) + "\n...(问题背景过长已截断)..."
	}
	return string(runes)
}

func finalizeResponse(resp AgentResponse) AgentResponse {
	resp = normalizeResponseSemantics(resp)
	if resp.Command != "" {
		realRisk, realReason := security.CheckRisk(resp.Command)
		resp.RiskLevel = realRisk
		resp.Reason = realReason
	}
	if resp.IsFinished {
		if strings.TrimSpace(resp.FinalReport) == "" || resp.FinalReport == "任务完成" {
			if resp.Thought != "" {
				resp.FinalReport = fmt.Sprintf("📋 任务总结: %s", resp.Thought)
			} else {
				resp.FinalReport = "任务已结束 (详细结果请向上翻阅执行日志)"
			}
		}
	}
	return resp
}

// UnescapeModelLineBreaks turns model text that still contains the two
// characters "\n" into real paragraphs while preserving path and code escapes.
func UnescapeModelLineBreaks(s string) string {
	count := strings.Count(s, `\n`)
	if count == 0 {
		return s
	}
	if count == 1 && !markdownBoundaryAfter(s, strings.Index(s, `\n`)+2) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inlineTicks := 0
	for i := 0; i < len(s); {
		if s[i] == '`' {
			j := i + 1
			for j < len(s) && s[j] == '`' {
				j++
			}
			run := j - i
			if run < 3 {
				if inlineTicks == 0 {
					inlineTicks = run
				} else if inlineTicks == run {
					inlineTicks = 0
				}
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		if inlineTicks != 0 || s[i+1] != 'n' || (i > 0 && s[i-1] == '\\') ||
			asciiLetterAt(s, i+2) || (asciiDigitAt(s, i+2) && !numberedListAfter(s, i+2)) ||
			(i+2 < len(s) && (s[i+2] == '\'' || s[i+2] == '"')) ||
			(inWindowsPath(s, i) && (i > 0 && s[i-1] == ':' || !markdownBoundaryAfter(s, i+2))) {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteByte('\n')
		i += 2
	}
	return b.String()
}

func asciiLetterAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func asciiDigitAt(s string, i int) bool {
	return i >= 0 && i < len(s) && s[i] >= '0' && s[i] <= '9'
}

func numberedListAfter(s string, i int) bool {
	for asciiDigitAt(s, i) {
		i++
	}
	return i+1 < len(s) && (s[i] == '.' || s[i] == ')') && s[i+1] == ' '
}

func markdownBoundaryAfter(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	if strings.HasPrefix(s[i:], `\n`) || s[i] == '#' || s[i] == '`' {
		return true
	}
	return i+1 < len(s) && (s[i] == '-' || s[i] == '*' || s[i] == '+') && s[i+1] == ' ' ||
		numberedListAfter(s, i)
}

func inWindowsPath(s string, slash int) bool {
	start := slash
	for start > 0 && !strings.ContainsRune(" \t\r\n\"'`<>()[]{}", rune(s[start-1])) {
		start--
	}
	token := s[start : slash+1]
	return len(token) >= 3 && asciiLetterAt(token, 0) && token[1] == ':' && token[2] == '\\' ||
		strings.HasPrefix(token, `\\`)
}

func normalizeResponseSemantics(resp AgentResponse) AgentResponse {
	resp.Thought = UnescapeModelLineBreaks(resp.Thought)
	resp.Question = UnescapeModelLineBreaks(resp.Question)
	resp.FinalReport = UnescapeModelLineBreaks(resp.FinalReport)
	action := strings.ToLower(strings.TrimSpace(resp.Action))
	if action == "ask_user" {
		question := strings.TrimSpace(resp.Question)
		if question == "" {
			question = strings.TrimSpace(resp.Thought)
		}
		// In an explicit ask_user envelope, a trailing question mark remains
		// actionable regardless of length. The stricter extractor is shared with
		// untyped plain-text recovery, where rhetorical questions need more care.
		validAsk := len(nonEmptyStrings(resp.Options)) > 0 || extractClarificationQuestion(question) != "" || endsAsQuestion(question)
		if validAsk {
			resp.Action = "ask_user"
			resp.Question = question
			resp.IsFinished = false
			resp.FinalReport = ""
			return resp
		}
		// A substantial non-question payload is a user-facing answer that the
		// provider put in the wrong field. Present it instead of opening an
		// input panel that has nothing actionable to answer.
		payload := firstNonEmptyString(resp.FinalReport, resp.Question)
		if len([]rune(payload)) >= 160 || strings.Count(payload, "\n") >= 3 {
			resp.Action = "finish"
			resp.IsFinished = true
			resp.FinalReport = payload
			resp.Question = ""
			resp.Options = nil
			resp.Thought = "模型把总结误标为 ask_user，已自动按最终报告呈现。"
			return resp
		}
		// Short malformed asks should be retried, not shown as a fake prompt.
		resp.Action = ""
		resp.Question = ""
		resp.Options = nil
		resp.Thought = "ask_user 未包含明确可回答的问题，正在自动请求模型改正。"
		return resp
	}

	if action == "think" || action == "thinking" || action == "analysis" || action == "reflect" {
		// These are provider-side narration aliases, never executable actions.
		// A long answer-like payload can finish directly; a short thought asks
		// the next model turn for a real action without rendering a fake tool.
		payload := firstNonEmptyString(resp.FinalReport, resp.Question, resp.Thought)
		if strings.TrimSpace(resp.FinalReport) != "" || len([]rune(payload)) >= 240 || strings.Count(payload, "\n") >= 4 {
			resp.Action = "finish"
			resp.IsFinished = true
			resp.FinalReport = payload
			resp.Question = ""
			resp.Options = nil
			resp.Thought = "模型使用了非标准总结动作，已自动按最终报告呈现。"
			return resp
		}
		resp.Action = ""
		resp.Thought = "模型只返回了思考过程，正在自动请求可执行动作或最终报告。"
	}
	return resp
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

const (
	contextCompactKeepRecent     = 12
	defaultHistoryTokenBudget    = 18000
	defaultSummaryChunkTokens    = 12000
	toolOutputPruneProtectTokens = 8000
	toolOutputPruneMinSavings    = 2000
	imageAttachmentTokenFloor    = 1500
)

// ManageHistoryContext 自动压缩历史上下文，提供接近“无限上下文”的滚动体验。
func ManageHistoryContext(history *[]Message) (bool, error) {
	return ManageHistoryContextWithOptions(history, ContextManageOptions{})
}

// ContextManageOptions 为长上下文压缩提供可取消调用和不可丢失的会话线索。
type ContextManageOptions struct {
	Context             context.Context
	PinnedContext       string
	HistoryBudgetTokens int
	KeepRecent          int
	SummaryChunkTokens  int
	Summarize           func(context.Context, []Message) (string, error)
}

func ManageHistoryContextWithOptions(history *[]Message, opts ContextManageOptions) (bool, error) {
	if history == nil || len(*history) == 0 {
		return false, nil
	}
	budget := opts.HistoryBudgetTokens
	if budget <= 0 {
		budget = defaultHistoryTokenBudget
	}
	if EstimateMessagesTokens(*history) <= budget {
		return false, nil
	}
	// Cheap local prune first. A failed or cancelled summary must leave the
	// live history untouched, so the pruned copy is committed only when it
	// fits or the later summary succeeds.
	original := append([]Message(nil), (*history)...)
	pruned := pruneOldToolOutputs(history)
	if EstimateMessagesTokens(*history) <= budget {
		return pruned, nil
	}
	if err := compressHistoryWithOptions(history, opts); err != nil {
		*history = original
		return false, err
	}
	return true, nil
}

func estimateHistoryChars(history []Message) int {
	n := 0
	for _, m := range history {
		n += len(m.Role) + len(m.Content) + 8
	}
	return n
}

// EstimateTextTokens is a conservative tokenizer-independent estimate. CJK
// runes are close to one token each; ASCII prose/code averages roughly three
// bytes per token. Overestimation is intentional to protect local runtimes.
func EstimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii, nonASCII := 0, 0
	for _, r := range text {
		if r <= 0x7f {
			ascii++
		} else {
			nonASCII++
		}
	}
	return (ascii+2)/3 + nonASCII
}

func EstimateMessagesTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += 4 + EstimateTextTokens(message.Role) + EstimateTextTokens(message.Content)
		for _, attachment := range message.Attachments {
			total += estimateImageAttachmentTokens(attachment)
		}
	}
	return total
}

// estimateImageAttachmentTokens is a conservative upper bound for a path-backed
// image. Provider vision billing is much higher than the caption text, so each
// image has a floor and large files rise with their byte size.
func estimateImageAttachmentTokens(attachment ImageAttachment) int {
	tokens := imageAttachmentTokenFloor
	if attachment.Size > 0 {
		bySize := int((attachment.Size + 255) / 256)
		if bySize > tokens {
			tokens = bySize
		}
	}
	return tokens
}

// pruneOldToolOutputs replaces tool logs outside the recent protection window
// with a one-line stub. Real user goals, skill bodies, and the latest desktop
// screenshot stay intact. The history is left unchanged when the savings are
// too small to be worth a rewrite.
func pruneOldToolOutputs(history *[]Message) bool {
	if history == nil || len(*history) < 2 {
		return false
	}
	current := *history
	latestShot := latestDesktopShotIndex(current)
	turnBoundary := recentUserTurnBoundary(current, 2)
	ordinal := make(map[int]int, len(current))
	toolN := 0
	for i := range current {
		if isPrunableToolOutput(current[i]) {
			toolN++
			ordinal[i] = toolN
		}
	}
	protected := 0
	stubAt := make(map[int]struct{})
	for i := len(current) - 1; i >= 0; i-- {
		prunable := isPrunableToolOutput(current[i])
		if i == latestShot {
			if prunable {
				protected += EstimateMessagesTokens(current[i : i+1])
			}
			continue
		}
		if !prunable {
			continue
		}
		// The last two real user turns stay intact. Older tool logs are kept
		// only until the recent window is about 8000 tokens, then stubbed.
		if i >= turnBoundary || protected < toolOutputPruneProtectTokens {
			protected += EstimateMessagesTokens(current[i : i+1])
			continue
		}
		stubAt[i] = struct{}{}
	}
	if len(stubAt) == 0 {
		return false
	}
	next := append([]Message(nil), current...)
	for i := range next {
		if _, ok := stubAt[i]; !ok {
			continue
		}
		next[i].Content = toolOutputStub(current[i], ordinal[i])
		next[i].Attachments = nil
	}
	if EstimateMessagesTokens(current)-EstimateMessagesTokens(next) <= toolOutputPruneMinSavings {
		return false
	}
	*history = next
	return true
}

// recentUserTurnBoundary is the index of the Nth most recent real user
// message. Messages from there to the end are the protected turns. Fewer
// than N real user messages protects the whole history.
func recentUserTurnBoundary(history []Message, turns int) int {
	if turns <= 0 {
		return len(history)
	}
	seen := 0
	for i := len(history) - 1; i >= 0; i-- {
		if !isRealHistoryUserTurn(history[i]) {
			continue
		}
		seen++
		if seen >= turns {
			return i
		}
	}
	return 0
}

func isRealHistoryUserTurn(message Message) bool {
	return IsRealUserTurn(message)
}

// IsRealUserTurn keeps model-facing tool and control feedback out of
// conversation-turn accounting. Prefixes cover checkpoints written before
// Synthetic was introduced; new feedback should set Synthetic explicitly.
func IsRealUserTurn(message Message) bool {
	if message.Role != "user" {
		return false
	}
	if message.Synthetic {
		return false
	}
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return false
	}
	for _, prefix := range []string{
		"Output:",
		"系统警告:",
		"【系统】",
		"上一步执行失败:",
		"用户拒绝执行",
		"已省略较早的工具输出",
		"MCP 工具返回了图片证据",
		"恢复提示：",
		"循环守卫：",
	} {
		if strings.HasPrefix(content, prefix) {
			return false
		}
	}
	if strings.HasPrefix(content, "子 Agent [") && strings.Contains(content, "] 从 checkpoint 恢复后的结果：") {
		return false
	}
	if content == "子 Agent 不能委派 task，请直接执行。" || content == "请执行具体 action 或 finish 返回结论。" {
		return false
	}
	return true
}

func isPrunableToolOutput(message Message) bool {
	if isSkillHistoryMessage(message) {
		return false
	}
	content := strings.TrimSpace(message.Content)
	if content == "" || strings.HasPrefix(content, "已省略较早的工具输出") {
		return false
	}
	if message.Role == "tool" {
		return true
	}
	if message.Role != "user" {
		return false
	}
	for _, prefix := range []string{"Output:", "上一步执行失败:", "用户拒绝执行"} {
		if strings.HasPrefix(content, prefix) {
			return true
		}
	}
	return strings.Contains(content, "已保存为 artifact") || strings.Contains(content, "输出过长已截断")
}

func isSkillHistoryMessage(message Message) bool {
	switch strings.ToLower(strings.TrimSpace(message.Name)) {
	case "skill", "load_skill":
		return true
	}
	return strings.Contains(message.Content, "已加载 Skill") || strings.Contains(message.Content, "【已加载 Skills】")
}

func latestDesktopShotIndex(history []Message) int {
	latest := -1
	for i := range history {
		for _, attachment := range history[i].Attachments {
			if isHistoryDesktopShot(attachment.Path) {
				latest = i
			}
		}
	}
	return latest
}

func isHistoryDesktopShot(path string) bool {
	slash := strings.ReplaceAll(path, "\\", "/")
	base := slash
	if i := strings.LastIndex(slash, "/"); i >= 0 {
		base = slash[i+1:]
	}
	lower := strings.ToLower(base)
	return strings.HasPrefix(base, "screen-") && strings.HasSuffix(lower, ".png") && strings.Contains(slash, "computer-use/")
}

func toolOutputStub(message Message, step int) string {
	label := strconv.Itoa(step)
	if parsed := explicitToolStep(message.Content); parsed != "" {
		label = parsed
	} else if name := strings.TrimSpace(message.Name); name != "" {
		label = name
	}
	if path := toolOutputArtifactPath(message.Content); path != "" {
		return fmt.Sprintf("已省略较早的工具输出（原步骤 %s）。完整内容：%s", label, path)
	}
	return fmt.Sprintf("已省略较早的工具输出（原步骤 %s）。", label)
}

func explicitToolStep(content string) string {
	const marker = "step="
	idx := strings.Index(content, marker)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	return rest[:end]
}

func toolOutputArtifactPath(content string) string {
	const marker = "artifact "
	idx := strings.LastIndex(content, marker)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(marker):]
	end := len(rest)
	for i, r := range rest {
		switch r {
		case ' ', '\n', '\r', ',', '，', '。', ')', '）':
			end = i
			goto done
		}
	}
done:
	return strings.TrimSpace(rest[:end])
}

func compressHistoryWithOptions(history *[]Message, opts ContextManageOptions) error {
	keepRecent := opts.KeepRecent
	if keepRecent <= 0 {
		keepRecent = contextCompactKeepRecent
	}
	if keepRecent >= len(*history) {
		keepRecent = len(*history) - 1
	}
	if keepRecent < 0 {
		keepRecent = 0
	}
	budget := opts.HistoryBudgetTokens
	if budget <= 0 {
		budget = defaultHistoryTokenBudget
	}
	// Do not let a handful of recent giant messages prevent compaction. Keep a
	// token-bounded tail; if even the newest message dominates the budget, fold
	// it into the hierarchical summary too.
	for keepRecent > 0 {
		tail := (*history)[len(*history)-keepRecent:]
		if EstimateMessagesTokens(tail) <= maxAnalyzerInt(512, budget/2) {
			break
		}
		keepRecent--
	}
	cutIndex := len(*history) - keepRecent
	toSummarize := append([]Message(nil), (*history)[:cutIndex]...)
	remaining := append([]Message(nil), (*history)[cutIndex:]...)
	goal := firstHistoryUserGoal(*history)
	latestDirective := latestHistoryUserDirective(*history)
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	summarize := opts.Summarize
	if summarize == nil {
		summarize = compressCallLLMContext
	}
	chunkTokens := opts.SummaryChunkTokens
	if chunkTokens <= 0 {
		chunkTokens = defaultSummaryChunkTokens
	}
	chunks := splitHistoryByTokenBudget(toSummarize, chunkTokens)
	if len(chunks) == 0 {
		return nil
	}
	summaries := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		summaryPrompt := buildSummaryPrompt(chunk, goal, latestDirective, opts.PinnedContext, index+1, len(chunks))
		summaryText, err := summarize(ctx, summaryPrompt)
		if err != nil {
			return err
		}
		summaryText = strings.TrimSpace(summaryText)
		if summaryText == "" {
			return fmt.Errorf("上下文摘要为空")
		}
		summaries = append(summaries, summaryText)
	}
	summaryText := strings.Join(summaries, "\n\n")
	var err error
	if len(summaries) > 1 && EstimateTextTokens(summaryText) > maxAnalyzerInt(2_000, chunkTokens/3) {
		reducePrompt := []Message{
			{Role: "system", Content: "合并以下分段摘要为一份可续跑的 DeepSentry 前情提要。保留每段的目标、约束、证据、命令结果、失败原因、未完成事项和下一步；不得遗漏冲突或编造。"},
			{Role: "user", Content: summaryText},
		}
		summaryText, err = summarize(ctx, reducePrompt)
	}
	if err != nil {
		return err
	}
	summaryText = strings.TrimSpace(summaryText)
	if summaryText == "" {
		return fmt.Errorf("上下文摘要为空")
	}
	var envelope strings.Builder
	envelope.WriteString("【分层上下文摘要】\n")
	if goal != "" {
		envelope.WriteString("\n【原始用户目标】\n")
		envelope.WriteString(trimContextText(goal, 3000))
		envelope.WriteString("\n")
	}
	if latestDirective != "" && latestDirective != goal {
		envelope.WriteString("\n【最新用户补充/修正】\n")
		envelope.WriteString(trimContextText(latestDirective, 3000))
		envelope.WriteString("\n")
	}
	if strings.TrimSpace(opts.PinnedContext) != "" {
		envelope.WriteString("\n")
		envelope.WriteString(trimContextText(opts.PinnedContext, 8000))
		envelope.WriteString("\n")
	}
	envelope.WriteString("\n【前情提要】\n")
	envelope.WriteString(strings.TrimSpace(summaryText))
	newHistory := []Message{
		{Role: "system", Content: envelope.String()},
	}
	newHistory = append(newHistory, remaining...)
	*history = newHistory
	return nil
}

func buildSummaryPrompt(chunk []Message, goal, latestDirective, pinned string, index, total int) []Message {
	prompt := []Message{{Role: "system", Content: fmt.Sprintf("你是 DeepSentry 上下文压缩器，正在处理第 %d/%d 段。必须保留：用户目标、约束和批准边界、已执行命令、关键输出结论及证据、IP/URL/CVE/哈希/文件路径、已修改文件、TODO、失败原因、冲突、不确定项和下一步。合并重复信息，不要编造。", index, total)}}
	if strings.TrimSpace(goal) != "" {
		prompt = append(prompt, Message{Role: "system", Content: "【不可丢失的原始目标】\n" + trimContextText(goal, 3000)})
	}
	if latestDirective != "" && latestDirective != goal {
		prompt = append(prompt, Message{Role: "system", Content: "【不可丢失的最新用户补充/修正】\n" + trimContextText(latestDirective, 3000)})
	}
	if strings.TrimSpace(pinned) != "" {
		prompt = append(prompt, Message{Role: "system", Content: "【不可丢失的结构化线索】\n" + trimContextText(pinned, 10000)})
	}
	prompt = append(prompt, chunk...)
	prompt = append(prompt, Message{Role: "user", Content: "请生成紧凑但可续跑的本段前情提要。"})
	return prompt
}

func splitHistoryByTokenBudget(history []Message, budget int) [][]Message {
	if budget <= 0 {
		budget = defaultSummaryChunkTokens
	}
	var chunks [][]Message
	current := make([]Message, 0)
	used := 0
	for _, message := range history {
		pieces := []string{message.Content}
		if 4+EstimateTextTokens(message.Role)+EstimateTextTokens(message.Content) > budget {
			pieces = splitTextByTokenBudget(message.Content, maxAnalyzerInt(256, budget-16))
		}
		for index, piece := range pieces {
			part := message
			if len(pieces) > 1 {
				part.Content = fmt.Sprintf("【原消息分片 %d/%d】\n%s", index+1, len(pieces), piece)
			} else {
				part.Content = piece
			}
			cost := 4 + EstimateTextTokens(part.Role) + EstimateTextTokens(part.Content)
			if len(current) > 0 && used+cost > budget {
				chunks = append(chunks, current)
				current = make([]Message, 0)
				used = 0
			}
			current = append(current, part)
			used += cost
		}
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func splitTextByTokenBudget(text string, maxTokens int) []string {
	if maxTokens <= 0 || EstimateTextTokens(text) <= maxTokens {
		return []string{text}
	}
	var parts []string
	remaining := text
	for remaining != "" {
		if EstimateTextTokens(remaining) <= maxTokens {
			parts = append(parts, remaining)
			break
		}
		low, high := 1, minAnalyzerInt(len(remaining), maxTokens*3)
		best := 0
		for low <= high {
			mid := (low + high) / 2
			for mid > 0 && !utf8.ValidString(remaining[:mid]) {
				mid--
			}
			if mid == 0 {
				low++
				continue
			}
			if EstimateTextTokens(remaining[:mid]) <= maxTokens {
				best = mid
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if best <= 0 {
			_, size := utf8.DecodeRuneInString(remaining)
			best = size
		}
		parts = append(parts, remaining[:best])
		remaining = remaining[best:]
	}
	return parts
}

func trimContextTextToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 || EstimateTextTokens(text) <= maxTokens {
		return text
	}
	// Binary search a UTF-8-safe byte budget, then preserve both evidence
	// prefixes and error/result tails via trimContextText.
	low, high := 1, len(text)
	for low < high {
		mid := (low + high + 1) / 2
		candidate := trimContextText(text, mid)
		if EstimateTextTokens(candidate) <= maxTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return trimContextText(text, low)
}

func fitSystemPrompt(prompt string, maxTokens int) string {
	if maxTokens <= 0 || EstimateTextTokens(prompt) <= maxTokens {
		return prompt
	}
	trimmed := trimContextTextToTokens(prompt, maxTokens)
	return trimmed + "\n【系统提示已按模型上下文能力压缩；安全边界与末尾规则仍有效】"
}

func isContextLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{"context length", "context_length", "maximum context", "max context", "prompt too long", "too many tokens", "token limit", "num_ctx", "max_model_len"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func maxAnalyzerInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minAnalyzerInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstHistoryUserGoal(history []Message) string {
	for _, message := range history {
		if !IsRealUserTurn(message) {
			continue
		}
		content := strings.TrimSpace(message.Content)
		return content
	}
	return ""
}

func latestHistoryUserDirective(history []Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		message := history[i]
		if !IsRealUserTurn(message) {
			continue
		}
		content := strings.TrimSpace(message.Content)
		return content
	}
	return ""
}

func latestHistorySummary(history []Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "system" {
			continue
		}
		content := strings.TrimSpace(history[i].Content)
		if strings.Contains(content, "前情提要") || strings.Contains(content, "分层上下文摘要") {
			return content
		}
	}
	return ""
}

func trimContextText(text string, maxBytes int) string {
	text = strings.TrimSpace(text)
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	head := maxBytes * 2 / 3
	tail := maxBytes - head
	for head > 0 && !utf8.ValidString(text[:head]) {
		head--
	}
	start := len(text) - tail
	for start < len(text) && !utf8.ValidString(text[start:]) {
		start++
	}
	return text[:head] + "\n...(中间大段输出已压缩；保留首尾证据)...\n" + text[start:]
}

func inferThoughtFromCommand(cmd string) string {
	if strings.HasPrefix(cmd, "upload") {
		return "正在上传文件到目标主机..."
	}
	if strings.HasPrefix(cmd, "download") {
		return "正在下载文件到本地分析..."
	}
	if cmd == "" {
		return "分析中..."
	}
	return fmt.Sprintf("执行: %s", cmd)
}

// cleanJSON 从 LLM 响应中提取并清洗 JSON，返回 (json, 前置说明文字)
func cleanJSON(s string) (string, string) {
	jsonPart, prose := extractJSONPayload(s)
	s = strings.TrimSpace(jsonPart)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// Providers sometimes emit raw shell/Windows path backslashes. A global
	// replacement damages already-valid JSON, so repair string values only.
	s = repairModelJSONEscapes(s)
	return s, prose
}

func repairModelJSONEscapes(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	stringStart := -1
	inWindowsPath := false
	pathValue := false
	pathSingleQuoted := false
	pathDoubleQuoted := false
	for i := 0; i < len(s); {
		if s[i] == '"' {
			inString = !inString
			if inString {
				stringStart = i + 1
			}
			inWindowsPath = false
			b.WriteByte(s[i])
			i++
			continue
		}
		if inString && i+2 < len(s) && asciiLetterAt(s, i) && s[i+1] == ':' && s[i+2] == '\\' {
			inWindowsPath = true
			pathValue = i == stringStart
			pathSingleQuoted = i > 0 && s[i-1] == '\''
			pathDoubleQuoted = i > 0 && s[i-1] == '"' && !pathValue
		}
		if inWindowsPath && ((pathSingleQuoted && s[i] == '\'') ||
			(!pathValue && !pathSingleQuoted && !pathDoubleQuoted && strings.ContainsRune(" \t\r\n;|&><", rune(s[i])))) {
			inWindowsPath = false
		}
		if !inString || s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '\\' {
			j++
		}
		run := j - i
		b.WriteString(s[i:j])
		closingDoublePath := run%2 == 1 && pathDoubleQuoted && inWindowsPath && j < len(s) && s[j] == '"'
		if run%2 == 1 && !closingDoublePath && (inWindowsPath || j < len(s) && !validJSONEscapeAt(s, j)) {
			b.WriteByte('\\')
		}
		if run%2 == 1 && (!inWindowsPath || closingDoublePath) && j < len(s) && s[j] == '"' {
			b.WriteByte('"')
			j++
			if closingDoublePath {
				inWindowsPath = false
			}
		}
		i = j
	}
	return b.String()
}

func validJSONEscapeAt(s string, i int) bool {
	switch s[i] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	case 'u':
		if i+4 >= len(s) {
			return false
		}
		for _, c := range s[i+1 : i+5] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// 🟢 [核心新增] extractCommandString 手动扫描字符串，提取 "command": "..." 中的值
// 能够完美处理转义引号 (\") 和转义反斜杠 (\\)，不依赖正则
func extractCommandString(jsonStr string) (string, bool) {
	// 1. 定位 key
	key := `"command"`
	idx := strings.Index(jsonStr, key)
	if idx == -1 {
		return "", false
	}

	// 2. 从 key 后面开始找第一个冒号
	cursor := idx + len(key)
	// 跳过冒号前的空白
	for cursor < len(jsonStr) && (jsonStr[cursor] == ' ' || jsonStr[cursor] == ':' || jsonStr[cursor] == '\n' || jsonStr[cursor] == '\r') {
		cursor++
	}

	// 3. 找值的起始引号
	startQuote := -1
	for i := cursor; i < len(jsonStr); i++ {
		if jsonStr[i] == '"' {
			startQuote = i
			break
		}
	}
	if startQuote == -1 {
		return "", false
	}

	// 4. 逐字符扫描，寻找结束引号（注意跳过转义字符）
	var resultBuilder strings.Builder
	inEscape := false // 是否处于转义状态

	for i := startQuote + 1; i < len(jsonStr); i++ {
		char := jsonStr[i]

		if inEscape {
			// 上一个字符是反斜杠，当前字符是转义后的字符
			// JSON 规范中，\" 代表 "，\\ 代表 \

			// 我们需要还原出“原始的Shell命令字符串”
			// 如果 JSON 里写的是 \" (即Shell里的 ")，我们需要写入 "
			// 如果 JSON 里写的是 \\ (即Shell里的 \)，我们需要写入 \

			switch char {
			case '"', '\\', '/':
				resultBuilder.WriteByte(char)
			case 'n':
				resultBuilder.WriteByte('\n')
			case 'r':
				resultBuilder.WriteByte('\r')
			case 't':
				resultBuilder.WriteByte('\t')
			default:
				// 其他情况，保留反斜杠和字符 (比如正则里的 \d，AI可能写成了 \\d)
				// 既然是手动提取，我们尽量保留原意
				resultBuilder.WriteByte('\\')
				resultBuilder.WriteByte(char)
			}
			inEscape = false
		} else {
			if char == '\\' {
				inEscape = true
			} else if char == '"' {
				// The surrounding envelope may be malformed even when the command
				// string is valid JSON. Decode that string once, preserving literal
				// backslashes while handling Unicode and surrogate pairs correctly.
				var decoded string
				if err := json.Unmarshal([]byte(jsonStr[startQuote:i+1]), &decoded); err == nil {
					return decoded, true
				}
				return resultBuilder.String(), true
			} else {
				resultBuilder.WriteByte(char)
			}
		}
	}

	return "", false
}

func compressCallLLMContext(ctx context.Context, messages []Message) (string, error) {
	res, err := CallLLMWithRetryContext(ctx, messages, false, nil)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

func parseToolArgs(raw map[string]interface{}) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch val := v.(type) {
		case string:
			out[k] = val
		case float64:
			out[k] = fmt.Sprintf("%.0f", val)
		case bool:
			out[k] = fmt.Sprintf("%v", val)
		default:
			if encoded, err := json.Marshal(v); err == nil {
				out[k] = string(encoded)
			} else {
				out[k] = fmt.Sprintf("%v", v)
			}
		}
	}
	return out
}
