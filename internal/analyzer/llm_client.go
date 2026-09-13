package analyzer

import (
	"ai-edr/internal/config"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// LLMResult LLM 调用结果
type LLMResult struct {
	Content string
	// ReasoningContent must be preserved on assistant tool-call messages for
	// thinking-mode OpenAI-compatible providers that validate the next turn.
	ReasoningContent string
	ToolCallName     string
	ToolCallArgs     string
	ToolCallID       string
	ToolCalls        []LLMToolCall
	Usage            TokenUsage
	ModelID          string
	Attempts         int
	Failovers        int
}

type LLMToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type PartialStreamError struct {
	Result LLMResult
	Err    error
}

func (e *PartialStreamError) Error() string { return "partial stream response: " + e.Err.Error() }
func (e *PartialStreamError) Unwrap() error { return e.Err }

type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

func (u TokenUsage) HasAny() bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// CallLLMWithRetry 带重试与降级的统一 LLM 调用；onStream 非 nil 时在 OpenAI 兼容 JSON 模式下启用 SSE 流式
func CallLLMWithRetry(messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	return CallLLMWithRetryContext(context.Background(), messages, useNativeTools, onStream)
}

func CallLLMWithRetryContext(ctx context.Context, messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	baseCfg := config.GlobalConfig
	models := baseCfg.EffectiveModels()
	hasImages := MessagesHaveImages(messages)
	var lastErr error
	totalAttempts := 0
	compatibleModels := 0
	for modelIndex, model := range models {
		cfg := baseCfg.ConfigForModel(model)
		if hasImages && !cfg.EffectiveModelCapabilities().SupportsVision {
			continue
		}
		compatibleModels++
		retries := model.MaxRetries
		for attempt := 0; attempt <= retries; attempt++ {
			totalAttempts++
			if attempt > 0 {
				timer := time.NewTimer(llmRetryDelay(attempt))
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return LLMResult{}, ctx.Err()
				}
			}

			result, err := callLLMOnce(ctx, cfg, messages, useNativeTools, onStream)
			if err == nil {
				result.ModelID = model.ID
				result.Attempts = totalAttempts
				result.Failovers = modelIndex
				return result, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return LLMResult{}, ctx.Err()
			}
			if !isRetryable(err) {
				break
			}
		}
		if modelIndex+1 >= len(models) || !shouldFailover(baseCfg, lastErr) {
			break
		}
	}
	if hasImages && compatibleModels == 0 {
		return LLMResult{}, errors.New("当前模型链没有启用视觉能力；请将视觉模型的 vision_mode 设为 enabled，或使用名称包含 vision/VL 的模型")
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的模型")
	}
	return LLMResult{}, fmt.Errorf("LLM 调用失败(总尝试 %d 次): %w", totalAttempts, lastErr)
}

func callLLMOnce(ctx context.Context, cfg config.Config, messages []Message, useNativeTools bool, onStream func(string)) (LLMResult, error) {
	native := useNativeTools && cfg.IsOpenAICompatible() && !isAstraModel(cfg.ModelName)
	if cfg.IsAnthropic() {
		return callAnthropic(ctx, cfg, messages)
	}
	if cfg.IsOpenAIResponses() {
		return callOpenAIResponses(ctx, cfg, messages)
	}
	if native {
		result, err := callOpenAICompatible(ctx, cfg, messages, true, onStream)
		if err != nil && isStreamUnsupported(err) && onStream != nil {
			result, err = callOpenAICompatible(ctx, cfg, messages, true, nil)
		}
		if err != nil && isToolsUnsupported(err) {
			return callOpenAICompatible(ctx, cfg, messages, false, onStream)
		}
		return result, err
	}
	if onStream != nil && cfg.IsOpenAICompatible() {
		result, err := callOpenAICompatible(ctx, cfg, messages, false, onStream)
		if err != nil && isStreamUnsupported(err) {
			return callOpenAICompatible(ctx, cfg, messages, false, nil)
		}
		return result, err
	}
	return callOpenAICompatible(ctx, cfg, messages, false, nil)
}

func shouldFailover(cfg config.Config, err error) bool {
	kind := classifyLLMError(err)
	if len(cfg.ModelRouting.FailoverOn) == 0 {
		return kind == "rate_limit" || kind == "timeout" || kind == "server_error" || kind == "connection" || kind == "invalid_output"
	}
	for _, allowed := range cfg.ModelRouting.FailoverOn {
		if strings.EqualFold(strings.TrimSpace(allowed), kind) {
			return true
		}
	}
	return false
}

func classifyLLMError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "429"), strings.Contains(s, "rate limit"):
		return "rate_limit"
	case strings.Contains(s, "500"), strings.Contains(s, "502"), strings.Contains(s, "503"), strings.Contains(s, "504"):
		return "server_error"
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return "timeout"
	case strings.Contains(s, "connection reset"), strings.Contains(s, "eof"), strings.Contains(s, "broken pipe"):
		return "connection"
	case strings.Contains(s, "parse"), strings.Contains(s, "empty response"):
		return "invalid_output"
	default:
		return "unknown"
	}
}

// llmRetryDelay 使并发 Agent 的重试错开，避免限流期间在整秒上同时再次冲击供应商。
func llmRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	base := time.Duration(attempt*attempt) * time.Second
	jitter, err := rand.Int(rand.Reader, big.NewInt(501))
	if err != nil {
		return base
	}
	return base + time.Duration(jitter.Int64())*time.Millisecond
}

type responsesRequest struct {
	Model           string      `json:"model"`
	Input           interface{} `json:"input"`
	Temperature     float64     `json:"temperature,omitempty"`
	MaxOutputTokens int         `json:"max_output_tokens,omitempty"`
}

type responsesInputMessage struct {
	Role    string                  `json:"role"`
	Content []responsesContentBlock `json:"content"`
}

type responsesContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type responsesResponse struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func callOpenAIResponses(ctx context.Context, cfg config.Config, messages []Message) (LLMResult, error) {
	url := config.NormalizeAPIURL(cfg.ApiURL, config.ProtocolOpenAIResponses)
	input, err := buildOpenAIResponsesInput(messages)
	if err != nil {
		return LLMResult{}, err
	}
	reqBody := responsesRequest{
		Model:           cfg.ModelName,
		Input:           input,
		Temperature:     effectiveTemperature(cfg),
		MaxOutputTokens: cfg.EffectiveModelCapabilities().ReservedOutputTokens,
	}
	if isAstraModel(cfg.ModelName) {
		reqBody.Temperature = 0
	}
	body, status, err := doHTTPPost(ctx, url, cfg, reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	if status != 200 {
		return LLMResult{}, fmt.Errorf("调用 Responses API 失败 %d: %s", status, truncateStr(string(body), 500))
	}
	var rr responsesResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return LLMResult{}, err
	}
	if rr.Error != nil {
		return LLMResult{}, errors.New(rr.Error.Message)
	}
	usage := TokenUsage{
		PromptTokens:     rr.Usage.InputTokens,
		CompletionTokens: rr.Usage.OutputTokens,
		TotalTokens:      rr.Usage.TotalTokens,
	}
	if strings.TrimSpace(rr.OutputText) != "" {
		return LLMResult{Content: rr.OutputText, Usage: usage}, nil
	}
	var b strings.Builder
	for _, out := range rr.Output {
		for _, c := range out.Content {
			if c.Text != "" {
				b.WriteString(c.Text)
			}
		}
	}
	if b.Len() == 0 {
		return LLMResult{}, errors.New("empty responses output")
	}
	return LLMResult{Content: b.String(), Usage: usage}, nil
}

func messagesToTranscript(messages []Message) string {
	var b strings.Builder
	for _, m := range messages {
		role := strings.ToUpper(strings.TrimSpace(m.Role))
		if role == "" {
			role = "USER"
		}
		b.WriteString(role)
		b.WriteString(":\n")
		b.WriteString(m.Content)
		if summary := attachmentSummary(m.Attachments); summary != "" {
			if strings.TrimSpace(m.Content) != "" {
				b.WriteString("\n")
			}
			b.WriteString(summary)
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func buildOpenAIResponsesInput(messages []Message) (interface{}, error) {
	if !MessagesHaveImages(messages) {
		return messagesToTranscript(messages), nil
	}
	blocks := []responsesContentBlock{{Type: "input_text", Text: messagesToTranscript(messages)}}
	for _, message := range messages {
		if len(message.Attachments) == 0 {
			continue
		}
		images, err := materializeImages(message.Attachments)
		if err != nil {
			return nil, err
		}
		for _, image := range images {
			detail := image.Attachment.Detail
			if detail == "" {
				detail = "auto"
			}
			blocks = append(blocks, responsesContentBlock{
				Type:     "input_image",
				ImageURL: "data:" + image.Attachment.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
				Detail:   detail,
			})
		}
	}
	return []responsesInputMessage{{Role: "user", Content: blocks}}, nil
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, code := range []string{"429", "500", "502", "503", "504", "timeout", "connection reset", "eof"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}

func isToolsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "tool") || strings.Contains(s, "400") || strings.Contains(s, "404")
}

func isStreamUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stream") || strings.Contains(s, "400") ||
		strings.Contains(s, "404") || strings.Contains(s, "not support")
}

func isStreamOptionsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stream_options") ||
		strings.Contains(s, "include_usage") ||
		strings.Contains(s, "unknown parameter") ||
		strings.Contains(s, "unrecognized") ||
		strings.Contains(s, "unsupported parameter")
}

func isMaxTokensUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "max_tokens") && !strings.Contains(s, "max tokens") {
		return false
	}
	return strings.Contains(s, "unknown") || strings.Contains(s, "unrecognized") ||
		strings.Contains(s, "unsupported") || strings.Contains(s, "not support") ||
		strings.Contains(s, "extra inputs") || strings.Contains(s, "400")
}

func callOpenAICompatible(ctx context.Context, cfg config.Config, messages []Message, withTools bool, onStream func(string)) (LLMResult, error) {
	url := config.NormalizeChatURL(cfg.ApiURL)
	useStream := onStream != nil
	chatMessages, err := buildOpenAIChatMessages(messages)
	if err != nil {
		return LLMResult{}, err
	}
	reqBody := ChatRequest{
		Model:       cfg.ModelName,
		Messages:    chatMessages,
		Stream:      useStream,
		Temperature: effectiveTemperature(cfg),
		MaxTokens:   cfg.EffectiveModelCapabilities().ReservedOutputTokens,
	}
	if withTools {
		reqBody.Tools = nativeToolDefinitionsForRequest(cfg, messages)
		// auto lets the model select a strongly typed built-in function, while
		// agent_action remains available for shell/file/task/finish actions.
		reqBody.ToolChoice = "auto"
	}

	if useStream {
		reqBody.StreamOptions = &StreamOptions{IncludeUsage: true}
		result, err := callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		if err != nil && isStreamOptionsUnsupported(err) {
			reqBody.StreamOptions = nil
			result, err = callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		}
		if err != nil && reqBody.MaxTokens > 0 && isMaxTokensUnsupported(err) {
			reqBody.MaxTokens = 0
			return callOpenAICompatibleStream(ctx, url, cfg, reqBody, onStream)
		}
		return result, err
	}

	body, status, err := doHTTPPost(ctx, url, cfg, reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	if status != 200 && reqBody.MaxTokens > 0 {
		apiErr := fmt.Errorf("API Error %d: %s", status, truncateStr(string(body), 500))
		if isMaxTokensUnsupported(apiErr) {
			reqBody.MaxTokens = 0
			body, status, err = doHTTPPost(ctx, url, cfg, reqBody)
			if err != nil {
				return LLMResult{}, err
			}
		}
	}
	if status != 200 {
		return LLMResult{}, fmt.Errorf("API Error %d: %s", status, truncateStr(string(body), 500))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return LLMResult{}, fmt.Errorf("parse error: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return LLMResult{}, errors.New("empty response")
	}
	msg := chatResp.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		calls := make([]LLMToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			calls = append(calls, LLMToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
		return LLMResult{Content: msg.Content, ReasoningContent: msg.ReasoningContent, ToolCallID: calls[0].ID, ToolCallName: calls[0].Name, ToolCallArgs: calls[0].Arguments, ToolCalls: calls, Usage: chatResp.Usage}, nil
	}
	return LLMResult{Content: msg.Content, ReasoningContent: msg.ReasoningContent, Usage: chatResp.Usage}, nil
}

const runtimeV3DeferredToolLimit = 16

func nativeToolDefinitionsForRequest(cfg config.Config, messages []Message) []ToolDefinition {
	capabilities := cfg.EffectiveModelCapabilities()
	contextText := recentToolSelectionContext(messages, 12000)
	if cfg.EffectiveAgentRuntime() != "v3" {
		return AgentToolDefinitionsForContext(capabilities.NativeToolLimit, contextText)
	}
	limit := capabilities.NativeToolLimit
	if limit <= 0 {
		// A large context window is not a reason to resend every tool schema.
		// Keep a small goal-ranked candidate set so models without native
		// deferred-tool APIs can still complete common tasks in one turn.
		limit = runtimeV3DeferredToolLimit
	}
	return AgentToolDefinitionsForContextWithPinned(limit, contextText, pinnedNativeToolNames(messages))
}

func pinnedNativeToolNames(messages []Message) []string {
	known := make(map[string]bool)
	allNames := AgentToolDefinitionsForContext(0, "")
	for _, definition := range allNames {
		known[definition.Function.Name] = true
	}
	selected := make(map[string]bool)
	for _, message := range messages {
		if message.Role == "tool" && known[message.Name] && !alwaysVisibleNativeTool(message.Name) {
			selected[message.Name] = true
		}
		if message.Role != "system" {
			continue
		}
		const marker = "【本任务已验证工具】"
		for _, line := range strings.Split(message.Content, "\n") {
			index := strings.Index(line, marker)
			if index < 0 {
				continue
			}
			list := line[index+len(marker):]
			if colon := strings.IndexAny(list, ":："); colon >= 0 {
				list = list[colon+1:]
			}
			for _, name := range strings.Split(list, ",") {
				name = strings.TrimSpace(name)
				if known[name] && !alwaysVisibleNativeTool(name) {
					selected[name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(selected))
	for name := range selected {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content,omitempty"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage TokenUsage `json:"usage"`
}

func callOpenAICompatibleStream(ctx context.Context, url string, cfg config.Config, reqBody ChatRequest, onStream func(string)) (LLMResult, error) {
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return LLMResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return LLMResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if cfg.ApiKey != "" && cfg.ApiKey != "none" {
		req.Header.Set("Authorization", "Bearer "+cfg.ApiKey)
	}
	client := config.HTTPClient(time.Duration(cfg.EffectiveLLMTimeout()) * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return LLMResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := readLimitedResponseBody(resp.Body, maxLLMResponseBytes(cfg))
		return LLMResult{}, fmt.Errorf("API Error %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var content strings.Builder
	var reasoningContent strings.Builder
	type toolCallBuilder struct {
		id   strings.Builder
		name strings.Builder
		args strings.Builder
	}
	toolBuilders := make(map[int]*toolCallBuilder)
	var usage TokenUsage
	var partialErr error
	maxResponseBytes := maxLLMResponseBytes(cfg)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			partialErr = fmt.Errorf("invalid SSE JSON: %w", err)
			continue
		}
		if chunk.Usage.HasAny() {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.ReasoningContent != "" {
			reasoningContent.WriteString(choice.Delta.ReasoningContent)
		}
		for _, call := range choice.Delta.ToolCalls {
			builder := toolBuilders[call.Index]
			if builder == nil {
				builder = &toolCallBuilder{}
				toolBuilders[call.Index] = builder
			}
			builder.id.WriteString(call.ID)
			builder.name.WriteString(call.Function.Name)
			builder.args.WriteString(call.Function.Arguments)
		}
		delta := choice.Delta.Content
		toolBytes := 0
		for _, builder := range toolBuilders {
			toolBytes += builder.id.Len() + builder.name.Len() + builder.args.Len()
		}
		if int64(content.Len()+reasoningContent.Len()+len(delta)+toolBytes) > maxResponseBytes {
			return LLMResult{}, fmt.Errorf("LLM 流式响应超过上限 %d 字节", maxResponseBytes)
		}
		if delta != "" {
			content.WriteString(delta)
			onStream(delta)
		}
	}
	if err := scanner.Err(); err != nil {
		partialErr = fmt.Errorf("stream read error: %w", err)
	}
	if content.Len() == 0 && len(toolBuilders) == 0 {
		return LLMResult{}, errors.New("empty stream response")
	}
	indexes := make([]int, 0, len(toolBuilders))
	for index := range toolBuilders {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]LLMToolCall, 0, len(indexes))
	for _, index := range indexes {
		builder := toolBuilders[index]
		call := LLMToolCall{ID: builder.id.String(), Name: builder.name.String(), Arguments: builder.args.String()}
		if strings.TrimSpace(call.Arguments) != "" && !json.Valid([]byte(call.Arguments)) {
			partialErr = fmt.Errorf("invalid JSON tool arguments for %s", call.Name)
		}
		calls = append(calls, call)
	}
	result := LLMResult{Content: content.String(), ReasoningContent: reasoningContent.String(), ToolCalls: calls, Usage: usage}
	if len(calls) > 0 {
		result.ToolCallID = calls[0].ID
		result.ToolCallName = calls[0].Name
		result.ToolCallArgs = calls[0].Arguments
	}
	if partialErr != nil {
		return result, &PartialStreamError{Result: result, Err: partialErr}
	}
	return result, nil
}

type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type anthropicContentBlock struct {
	Type   string                `json:"type"`
	Text   string                `json:"text,omitempty"`
	Source *anthropicImageSource `json:"source,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicResponse struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking,omitempty"`
	} `json:"content"`
	StopReason string `json:"stop_reason,omitempty"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func callAnthropic(ctx context.Context, cfg config.Config, messages []Message) (LLMResult, error) {
	url := config.NormalizeAPIURL(cfg.ApiURL, config.ProtocolAnthropicMessages)
	var system strings.Builder
	var msgs []anthropicMessage
	for _, m := range messages {
		switch m.Role {
		case "system":
			system.WriteString(m.Content)
			system.WriteString("\n")
		case "user", "assistant":
			content := m.Content
			if len(m.ToolCalls) > 0 {
				var calls []string
				for _, call := range m.ToolCalls {
					calls = append(calls, fmt.Sprintf("%s(%s)", call.Function.Name, call.Function.Arguments))
				}
				content = "Tool calls requested: " + strings.Join(calls, ", ")
			}
			if len(m.Attachments) > 0 {
				if m.Role != "user" {
					return LLMResult{}, errors.New("Anthropic 图片附件只能出现在 user 消息中")
				}
				images, err := materializeImages(m.Attachments)
				if err != nil {
					return LLMResult{}, err
				}
				if strings.TrimSpace(content) == "" {
					content = "请分析所附图片，并结合当前任务继续。"
				}
				blocks := []anthropicContentBlock{{Type: "text", Text: content}}
				for _, image := range images {
					blocks = append(blocks, anthropicContentBlock{Type: "image", Source: &anthropicImageSource{
						Type: "base64", MediaType: image.Attachment.MediaType,
						Data: base64.StdEncoding.EncodeToString(image.Data),
					}})
				}
				msgs = append(msgs, anthropicMessage{Role: m.Role, Content: blocks})
			} else if strings.TrimSpace(content) != "" {
				msgs = append(msgs, anthropicMessage{Role: m.Role, Content: content})
			}
		case "tool":
			msgs = append(msgs, anthropicMessage{Role: "user", Content: fmt.Sprintf("Tool result [%s, call_id=%s]:\n%s", m.Name, m.ToolCallID, m.Content)})
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, anthropicMessage{Role: "user", Content: "continue"})
	}

	reqBody := anthropicRequest{
		Model:     cfg.ModelName,
		MaxTokens: anthropicMaxTokens(cfg),
		System:    strings.TrimSpace(system.String()),
		Messages:  msgs,
	}
	if effort := anthropicEffort(cfg.ModelName); effort != "" {
		reqBody.OutputConfig = &anthropicOutputConfig{Effort: effort}
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return LLMResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return LLMResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.ApiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := config.HTTPClient(time.Duration(cfg.EffectiveLLMTimeout()) * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return LLMResult{}, err
	}
	defer resp.Body.Close()
	body, err := readLimitedResponseBody(resp.Body, maxLLMResponseBytes(cfg))
	if err != nil {
		return LLMResult{}, err
	}
	if resp.StatusCode != 200 {
		return LLMResult{}, fmt.Errorf("调用 Anthropic API 失败 %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return LLMResult{}, err
	}
	if ar.Error != nil {
		return LLMResult{}, errors.New(ar.Error.Message)
	}
	var text, thinking strings.Builder
	for _, c := range ar.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "thinking":
			thinking.WriteString(c.Thinking)
		}
	}
	if strings.EqualFold(ar.StopReason, "refusal") {
		msg := strings.TrimSpace(text.String())
		if msg == "" {
			msg = "模型拒绝回答"
		}
		return LLMResult{}, fmt.Errorf("Anthropic 拒绝回答: %s", msg)
	}
	usage := TokenUsage{
		PromptTokens:     ar.Usage.InputTokens,
		CompletionTokens: ar.Usage.OutputTokens,
		TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
	}
	return LLMResult{Content: text.String(), ReasoningContent: thinking.String(), Usage: usage}, nil
}

// anthropicMaxTokens reserves room for Claude 5 adaptive thinking, which shares
// the max_tokens budget. Operators can still pin ReservedOutputTokens.
func anthropicMaxTokens(cfg config.Config) int {
	if cfg.ReservedOutputTokens > 0 {
		return cfg.ReservedOutputTokens
	}
	if anthropicUsesAdaptiveThinking(cfg.ModelName) {
		return 65536
	}
	return cfg.EffectiveModelCapabilities().ReservedOutputTokens
}

func anthropicEffort(model string) string {
	if anthropicUsesAdaptiveThinking(model) {
		return "high"
	}
	return ""
}

func anthropicUsesAdaptiveThinking(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" || strings.Contains(m, "haiku") {
		return false
	}
	return strings.Contains(m, "claude-opus-5") ||
		strings.Contains(m, "claude-sonnet-5") ||
		strings.Contains(m, "claude-fable-5") ||
		strings.Contains(m, "claude-opus-4-8") ||
		strings.Contains(m, "claude-opus-4-7")
}

func recentToolSelectionContext(messages []Message, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 12000
	}
	var parts []string
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "system" {
			continue
		}
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		if len(content)+used > maxBytes {
			content = truncateStr(content, maxBytes-used)
		}
		parts = append(parts, content)
		used += len(content)
		if used >= maxBytes {
			break
		}
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "\n")
}

func doHTTPPost(ctx context.Context, url string, cfg config.Config, payload interface{}) ([]byte, int, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.ApiKey != "" && cfg.ApiKey != "none" {
		req.Header.Set("Authorization", "Bearer "+cfg.ApiKey)
	}
	client := config.HTTPClient(time.Duration(cfg.EffectiveLLMTimeout()) * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := readLimitedResponseBody(resp.Body, maxLLMResponseBytes(cfg))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func maxLLMResponseBytes(cfg config.Config) int64 {
	limit := int64(cfg.EffectiveModelCapabilities().ReservedOutputTokens) * 8
	if limit < 1<<20 {
		limit = 1 << 20
	}
	if limit > 32<<20 {
		limit = 32 << 20
	}
	return limit
}

func readLimitedResponseBody(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("LLM HTTP 响应超过上限 %d 字节", limit)
	}
	return body, nil
}

func effectiveTemperature(cfg config.Config) float64 {
	// Zero is a valid and intentional value for operational/benchmark
	// determinism. The old fallback silently turned temperature: 0.0 into 0.1,
	// causing identical Runtime A/B prompts to select different tools.
	return cfg.Temperature
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end] + "..."
}

// TruncateHistoryFallback 摘要失败时的机械截断
func TruncateHistoryFallback(history *[]Message, keepRecent int) {
	TruncateHistoryFallbackWithHints(history, keepRecent, "")
}

// TruncateHistoryFallbackWithHints 在摘要服务不可用时仍保留原始目标、已有摘要与核心线索。
func TruncateHistoryFallbackWithHints(history *[]Message, keepRecent int, pinnedContext string) {
	truncateHistoryFallbackToBudget(history, keepRecent, pinnedContext, 0)
}

func truncateHistoryFallbackToBudget(history *[]Message, keepRecent int, pinnedContext string, tokenBudget int) {
	if history == nil || keepRecent <= 0 {
		return
	}
	h := *history
	if len(h) <= keepRecent+2 && (tokenBudget <= 0 || EstimateMessagesTokens(h) <= tokenBudget) {
		return
	}
	if keepRecent > len(h) {
		keepRecent = len(h)
	}
	trimmed := append([]Message(nil), h[len(h)-keepRecent:]...)
	goal := firstHistoryUserGoal(h)
	latestDirective := latestHistoryUserDirective(h)
	priorSummary := latestHistorySummary(h)
	var context strings.Builder
	context.WriteString(fmt.Sprintf("【前情提要】(摘要服务不可用，机械保留最近 %d 条)\n", keepRecent))
	if goal != "" {
		context.WriteString("【原始用户目标】\n")
		context.WriteString(trimContextText(goal, 3000))
		context.WriteString("\n")
	}
	if latestDirective != "" && latestDirective != goal {
		context.WriteString("【最新用户补充/修正】\n")
		context.WriteString(trimContextText(latestDirective, 3000))
		context.WriteString("\n")
	}
	if strings.TrimSpace(pinnedContext) != "" {
		context.WriteString(trimContextText(pinnedContext, 8000))
		context.WriteString("\n")
	}
	if priorSummary != "" {
		context.WriteString("【上一次有效摘要】\n")
		context.WriteString(trimContextText(priorSummary, 8000))
		context.WriteString("\n")
	}
	context.WriteString("较早原始对话已省略；以上固定信息与最近步骤继续有效。")
	contextMessage := Message{
		Role:    "system",
		Content: context.String(),
	}
	if tokenBudget > 0 {
		remaining := tokenBudget - EstimateMessagesTokens([]Message{contextMessage})
		if remaining < 256 {
			remaining = 256
		}
		selected := make([]Message, 0, len(trimmed))
		for i := len(trimmed) - 1; i >= 0 && remaining > 0; i-- {
			message := trimmed[i]
			cost := 4 + EstimateTextTokens(message.Role) + EstimateTextTokens(message.Content)
			if cost > remaining {
				message.Content = trimContextTextToTokens(message.Content, maxAnalyzerInt(64, remaining-8))
				cost = 4 + EstimateTextTokens(message.Role) + EstimateTextTokens(message.Content)
			}
			selected = append(selected, message)
			remaining -= cost
		}
		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
		trimmed = selected
	}
	*history = append([]Message{contextMessage}, trimmed...)
}

func isAstraModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return m == "gpt-6-astra" || strings.HasPrefix(m, "gpt-6-astra-20")
}

// Astra accepts Chat Completions for text, but rejects sampling parameters.
// Keep other models' explicit temperature=0 behavior unchanged.
func (r ChatRequest) MarshalJSON() ([]byte, error) {
	type plain ChatRequest
	if !isAstraModel(r.Model) {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Temperature         *float64 `json:"temperature,omitempty"`
		MaxTokens           *int     `json:"max_tokens,omitempty"`
		MaxCompletionTokens int      `json:"max_completion_tokens,omitempty"`
	}{plain: plain(r), MaxCompletionTokens: r.MaxTokens})
}
