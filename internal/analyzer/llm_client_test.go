package analyzer

import (
	"ai-edr/internal/collector"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestOpenAICompatibleParsesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"ok\"}"}}],
			"usage":{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168}
		}`))
	}))
	defer server.Close()

	result, err := callOpenAICompatible(context.Background(), config.Config{
		ApiURL:    server.URL + "/v1/chat/completions",
		ModelName: "test-model",
		ApiKey:    "none",
	}, []Message{{Role: "user", Content: "hi"}}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.PromptTokens != 123 || result.Usage.CompletionTokens != 45 || result.Usage.TotalTokens != 168 {
		t.Fatalf("usage not parsed: %#v", result.Usage)
	}
}

func TestOpenAICompatibleSendsNativeBuiltinSchemasAndParsesName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ToolChoice != "auto" {
			t.Fatalf("tool_choice=%#v, want auto", request.ToolChoice)
		}
		if request.MaxTokens != 4096 {
			t.Fatalf("max_tokens=%d want local adaptive 4096", request.MaxTokens)
		}
		if len(request.Tools) > 11 {
			t.Fatalf("local compact model received too many native schemas: %d", len(request.Tools))
		}
		found := false
		for _, def := range request.Tools {
			if def.Function.Name == "config_manage" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("config_manage native definition missing from %d tools", len(request.Tools))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"config_manage","arguments":"{\"action\":\"status\"}"}}]}}]}`))
	}))
	defer server.Close()

	result, err := callOpenAICompatible(context.Background(), config.Config{
		ApiURL: server.URL, ModelName: "test-model", ApiKey: "none",
	}, []Message{{Role: "user", Content: "show config status"}}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolCallName != "config_manage" || result.ToolCallArgs != `{"action":"status"}` {
		t.Fatalf("unexpected tool result: %#v", result)
	}
}

func TestOpenAICompatiblePreservesReasoningContentAcrossToolTurn(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"signed-thought","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_log","arguments":"{\"path\":\"fixture.log\"}"}}]}}]}`))
			return
		}
		if len(request.Messages) < 2 || request.Messages[1].ReasoningContent != "signed-thought" {
			t.Fatalf("reasoning content was not passed back: %#v", request.Messages)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	defer server.Close()

	cfg := config.Config{ApiURL: server.URL, ModelName: "thinking-model", ApiKey: "none"}
	first, err := callOpenAICompatible(context.Background(), cfg, []Message{{Role: "user", Content: "inspect"}}, true, nil)
	if err != nil || first.ReasoningContent != "signed-thought" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	call := ToolCall{ID: first.ToolCallID, Type: "function"}
	call.Function.Name = first.ToolCallName
	call.Function.Arguments = first.ToolCallArgs
	messages := []Message{
		{Role: "user", Content: "inspect"},
		{Role: "assistant", ReasoningContent: first.ReasoningContent, ToolCalls: []ToolCall{call}},
		{Role: "tool", ToolCallID: first.ToolCallID, Name: first.ToolCallName, Content: "ok"},
	}
	if _, err := callOpenAICompatible(context.Background(), cfg, messages, true, nil); err != nil || requests != 2 {
		t.Fatalf("round trip requests=%d err=%v", requests, err)
	}
}

func TestOpenAIStreamingPreservesMultipleToolCallsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"reasoning_content":"reason-","tool_calls":[{"index":0,"id":"call_a","function":{"name":"process_list","arguments":"{\"lim"}},{"index":1,"id":"call_b","function":{"name":"port_listen","arguments":"{}"}}]}}]}`)
		_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"reasoning_content":"content","tool_calls":[{"index":0,"function":{"arguments":"it\":20}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}`)
		_, _ = fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	result, err := callOpenAICompatible(context.Background(), config.Config{ApiURL: server.URL, ModelName: "test", ApiKey: "none"}, []Message{{Role: "user", Content: "inspect"}}, true, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 2 || result.ToolCalls[0].ID != "call_a" || result.ToolCalls[0].Arguments != `{"limit":20}` || result.ToolCalls[1].ID != "call_b" {
		t.Fatalf("tool calls not reconstructed: %#v", result.ToolCalls)
	}
	if result.Usage.TotalTokens != 16 {
		t.Fatalf("usage=%#v", result.Usage)
	}
	if result.ReasoningContent != "reason-content" {
		t.Fatalf("reasoning content=%q", result.ReasoningContent)
	}
}

func TestOpenAIStreamingReturnsStructuredPartialError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_partial","function":{"name":"process_list","arguments":"{\"limit\":"}}]}}]}`)
	}))
	defer server.Close()
	result, err := callOpenAICompatible(context.Background(), config.Config{ApiURL: server.URL, ModelName: "test", ApiKey: "none"}, []Message{{Role: "user", Content: "inspect"}}, true, func(string) {})
	var partial *PartialStreamError
	if !errors.As(err, &partial) || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call_partial" || partial.Result.ToolCalls[0].ID != "call_partial" {
		t.Fatalf("result=%#v err=%T %v", result, err, err)
	}
}

func TestLLMFailoverUsesConfiguredFallback(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"fallback-ok\"}"}}]}`))
	}))
	defer fallback.Close()
	config.GlobalConfig = config.Config{
		Provider: "custom", APIProtocol: "openai_chat", ApiURL: primary.URL, ApiKey: "none", ModelName: "primary",
		Models: []config.ModelConfig{
			{ID: "primary", Role: "primary", Provider: "custom", APIProtocol: "openai_chat", APIURL: primary.URL, ModelName: "primary", MaxRetries: 0},
			{ID: "fallback", Role: "fallback", Provider: "custom", APIProtocol: "openai_chat", APIURL: fallback.URL, ModelName: "fallback", MaxRetries: 0},
		},
		ModelRouting: config.ModelRoutingConfig{FailoverOn: []string{"invalid_output"}},
	}
	result, err := CallLLMWithRetryContext(context.Background(), []Message{{Role: "user", Content: "hi"}}, false, nil)
	if err != nil || result.ModelID != "fallback" || !strings.Contains(result.Content, "fallback-ok") || result.Failovers != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestLLMEmptyMessageFailsInsteadOfProducingBlankAgentTurn(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
	}))
	defer server.Close()
	config.GlobalConfig = config.Config{Provider: "custom", APIProtocol: "openai_chat", ApiURL: server.URL, ApiKey: "none", ModelName: "blank"}
	_, err := CallLLMWithRetryContext(context.Background(), []Message{{Role: "user", Content: "inspect"}}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "empty response") || requests != 1 {
		t.Fatalf("blank provider response was treated as success or retried indefinitely: err=%v requests=%d", err, requests)
	}
}

func TestLLMEmptyMessageCanFailOverToUsableModel(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  "}}]}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"recovered\"}"}}]}`))
	}))
	defer fallback.Close()
	config.GlobalConfig = config.Config{
		Provider: "custom", APIProtocol: "openai_chat", ApiURL: primary.URL, ApiKey: "none", ModelName: "primary",
		Models: []config.ModelConfig{
			{ID: "primary", Role: "primary", Provider: "custom", APIProtocol: "openai_chat", APIURL: primary.URL, ModelName: "primary"},
			{ID: "fallback", Role: "fallback", Provider: "custom", APIProtocol: "openai_chat", APIURL: fallback.URL, ModelName: "fallback"},
		},
		ModelRouting: config.ModelRoutingConfig{FailoverOn: []string{"invalid_output"}},
	}
	result, err := CallLLMWithRetryContext(context.Background(), []Message{{Role: "user", Content: "inspect"}}, false, nil)
	if err != nil || result.ModelID != "fallback" || !strings.Contains(result.Content, "recovered") {
		t.Fatalf("empty primary response did not fail over: result=%#v err=%v", result, err)
	}
}

func TestLLMEmptyStreamAndBlankToolCallFailOver(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	for _, tc := range []struct {
		name   string
		stream bool
		body   string
	}{
		{name: "whitespace stream", stream: true, body: "data: {\"choices\":[{\"delta\":{\"content\":\"  \"}}]}\n\ndata: [DONE]\n"},
		{name: "blank tool call", body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_blank","function":{"name":"","arguments":"{}"}}]}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"action\\\":\\\"finish\\\",\\\"final_report\\\":\\\"recovered\\\"}\"}}]}\n\ndata: [DONE]\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"recovered\"}"}}]}`))
			}))
			defer fallback.Close()
			config.GlobalConfig = config.Config{
				Provider: "custom", APIProtocol: "openai_chat", ApiURL: primary.URL, ApiKey: "none", ModelName: "primary",
				Models: []config.ModelConfig{
					{ID: "primary", Role: "primary", Provider: "custom", APIProtocol: "openai_chat", APIURL: primary.URL, ModelName: "primary", MaxRetries: 0},
					{ID: "fallback", Role: "fallback", Provider: "custom", APIProtocol: "openai_chat", APIURL: fallback.URL, ModelName: "fallback", MaxRetries: 0},
				},
				ModelRouting: config.ModelRoutingConfig{FailoverOn: []string{"invalid_output"}},
			}
			var onStream func(string)
			if tc.stream {
				onStream = func(string) {}
			}
			result, err := CallLLMWithRetryContext(context.Background(), []Message{{Role: "user", Content: "inspect"}}, false, onStream)
			if err != nil || result.ModelID != "fallback" || !strings.Contains(result.Content, "recovered") {
				t.Fatalf("blank output did not fail over: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestOpenAICompatibleRetriesWithoutUnsupportedMaxTokens(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if request.MaxTokens == 0 {
				t.Fatal("first request should use adaptive max_tokens")
			}
			http.Error(w, `{"error":"unknown parameter max_tokens"}`, http.StatusBadRequest)
			return
		}
		if request.MaxTokens != 0 {
			t.Fatalf("compatibility retry still sent max_tokens=%d", request.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	cfg := config.Config{
		ApiURL: server.URL, ModelName: "local-model", ApiKey: "none",
	}
	_, err := callOpenAICompatible(context.Background(), cfg, []Message{{Role: "user", Content: "hi"}}, false, nil)
	if err == nil {
		_, err = callOpenAICompatible(context.Background(), cfg, []Message{{Role: "user", Content: "again"}}, false, nil)
	}
	if err != nil || requests != 3 {
		t.Fatalf("max_tokens compatibility fallback failed: requests=%d err=%v", requests, err)
	}
}

func TestExplicitUnsupportedToolsAreSkippedOnNextTurn(t *testing.T) {
	requests, withTools := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) > 0 {
			withTools++
			http.Error(w, `{"error":"unknown parameter tools"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	cfg := config.Config{Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: server.URL, ModelName: "local-model", ApiKey: "none"}
	for i := 0; i < 2; i++ {
		if _, err := callLLMOnce(context.Background(), cfg, []Message{{Role: "user", Content: "inspect"}}, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 3 || withTools != 1 {
		t.Fatalf("unsupported tools retried unnecessarily: requests=%d with_tools=%d", requests, withTools)
	}
}

func TestGenericBadRequestDoesNotDisableNativeTools(t *testing.T) {
	requests, withTools := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) > 0 {
			withTools++
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	cfg := config.Config{Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: server.URL, ModelName: "local-model", ApiKey: "none"}
	for i := 0; i < 2; i++ {
		if _, err := callLLMOnce(context.Background(), cfg, []Message{{Role: "user", Content: "inspect"}}, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 4 || withTools != 2 {
		t.Fatalf("generic 400 disabled native tools: requests=%d with_tools=%d", requests, withTools)
	}
}

func TestExplicitUnsupportedStreamOptionsAreSkippedOnNextTurn(t *testing.T) {
	requests, withOptions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.StreamOptions != nil {
			withOptions++
			http.Error(w, `{"error":"unknown parameter stream_options"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
		_, _ = fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()
	cfg := config.Config{Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: server.URL, ModelName: "local-model", ApiKey: "none"}
	for i := 0; i < 2; i++ {
		result, err := callOpenAICompatible(context.Background(), cfg, []Message{{Role: "user", Content: "inspect"}}, false, func(string) {})
		if err != nil || result.Content != "ok" {
			t.Fatalf("stream compatibility fallback failed: result=%#v err=%v", result, err)
		}
	}
	if requests != 3 || withOptions != 1 {
		t.Fatalf("unsupported stream_options retried unnecessarily: requests=%d with_options=%d", requests, withOptions)
	}
}

func TestRunAgentStepRetriesWithSmallerHistoryAfterContextLimit(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })

	requestSizes := make([]int, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requestSizes = append(requestSizes, len(request.Messages))
		if len(requestSizes) == 1 {
			http.Error(w, `{"error":{"message":"maximum context length exceeded"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"recovered\"}"}}]}`))
	}))
	defer server.Close()

	config.GlobalConfig = config.Config{
		Provider:            "custom",
		APIProtocol:         "openai_chat",
		ApiURL:              server.URL,
		ApiKey:              "none",
		ModelName:           "local-14b",
		ModelParameterB:     14,
		ContextWindowTokens: 32_768,
	}
	history := []Message{{Role: "user", Content: "排查异常登录"}}
	for i := 0; i < 12; i++ {
		history = append(history, Message{Role: "user", Content: fmt.Sprintf("步骤 %d: %s", i, strings.Repeat("evidence ", 30))})
	}
	response, err := RunAgentStepWithOptions(StepOptions{
		Context:        context.Background(),
		SysCtx:         collector.SystemContext{},
		History:        &history,
		UseNativeTools: false,
	})
	if err != nil || response.Action != "finish" || response.FinalReport != "recovered" {
		t.Fatalf("context-limit recovery failed: response=%#v err=%v", response, err)
	}
	if len(requestSizes) != 2 || requestSizes[1] >= requestSizes[0] {
		t.Fatalf("retry did not shrink history: %v", requestSizes)
	}
}

func TestOpenAICompatibleRequestCanBeCancelled(t *testing.T) {
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := callOpenAICompatible(ctx, config.Config{
		ApiURL:    server.URL + "/v1/chat/completions",
		ModelName: "test-model",
		ApiKey:    "none",
	}, []Message{{Role: "user", Content: "hi"}}, false, nil)
	close(releaseHandler)
	server.Close()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancelled request err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestReadLimitedResponseBodyRejectsOversize(t *testing.T) {
	body, err := readLimitedResponseBody(strings.NewReader("1234"), 4)
	if err != nil || string(body) != "1234" {
		t.Fatalf("exact limit rejected: body=%q err=%v", body, err)
	}
	if _, err := readLimitedResponseBody(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized LLM response should be rejected")
	}
}

func TestLLMRetryDelayUsesBoundedJitter(t *testing.T) {
	if got := llmRetryDelay(0); got != 0 {
		t.Fatalf("attempt 0 delay=%s, want 0", got)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		base := time.Duration(attempt*attempt) * time.Second
		got := llmRetryDelay(attempt)
		if got < base || got > base+500*time.Millisecond {
			t.Fatalf("attempt %d delay=%s, want %s..%s", attempt, got, base, base+500*time.Millisecond)
		}
	}
}

func TestEffectiveTemperaturePreservesConfiguredZero(t *testing.T) {
	if got := effectiveTemperature(config.Config{Temperature: 0}); got != 0 {
		t.Fatalf("temperature 0 became %v", got)
	}
	if got := effectiveTemperature(config.Config{Temperature: 0.35}); got != 0.35 {
		t.Fatalf("temperature 0.35 became %v", got)
	}
}

func TestClaude5MessagesUsesThinkingBudgetAndParsesThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("x-api-key") != "test" {
			t.Fatalf("unexpected Anthropic headers: %#v", r.Header)
		}
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if raw["model"] != "claude-opus-5" {
			t.Fatalf("model=%v", raw["model"])
		}
		if raw["max_tokens"].(float64) != 65536 {
			t.Fatalf("max_tokens=%v want 65536 for Claude 5 thinking", raw["max_tokens"])
		}
		cfg, _ := raw["output_config"].(map[string]interface{})
		if cfg["effort"] != "high" {
			t.Fatalf("output_config=%#v", raw["output_config"])
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"plan first"},{"type":"text","text":"done"}],"usage":{"input_tokens":11,"output_tokens":22}}`))
	}))
	defer server.Close()

	result, err := callAnthropic(context.Background(), config.Config{
		ApiURL: server.URL + "/v1/messages", APIProtocol: config.ProtocolAnthropicMessages,
		ModelName: "claude-opus-5", ApiKey: "test",
	}, []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || result.ReasoningContent != "plan first" {
		t.Fatalf("unexpected Claude 5 result: %#v", result)
	}
	if result.Usage.PromptTokens != 11 || result.Usage.CompletionTokens != 22 {
		t.Fatalf("usage not parsed: %#v", result.Usage)
	}
}

func TestClaudeOpus55UsesDefaultMediumEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if raw["model"] != "claude-opus-5-5" || raw["max_tokens"] != float64(65536) {
			t.Fatalf("unexpected Opus 5.5 request: %#v", raw)
		}
		if _, ok := raw["thinking"]; ok {
			t.Fatalf("Opus 5.5 should use adaptive thinking: %#v", raw["thinking"])
		}
		if output, ok := raw["output_config"].(map[string]interface{}); !ok || output["effort"] != "medium" {
			t.Fatalf("unexpected Opus 5.5 effort: %#v", raw["output_config"])
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer server.Close()

	if _, err := callAnthropic(context.Background(), config.Config{
		ApiURL: server.URL + "/v1/messages", APIProtocol: config.ProtocolAnthropicMessages,
		ModelName: "claude-opus-5-5", ApiKey: "test",
	}, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeHaikuSkipsEffortAndHonorsReservedOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if raw["max_tokens"].(float64) != 4096 {
			t.Fatalf("max_tokens=%v want explicit reserved 4096", raw["max_tokens"])
		}
		if _, ok := raw["output_config"]; ok {
			t.Fatalf("Haiku should not send effort: %#v", raw["output_config"])
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer server.Close()

	if _, err := callAnthropic(context.Background(), config.Config{
		ApiURL: server.URL + "/v1/messages", APIProtocol: config.ProtocolAnthropicMessages,
		ModelName: "claude-haiku-4-5", ApiKey: "test", ReservedOutputTokens: 4096,
	}, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeRefusalStopReasonIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stop_reason":"refusal","content":[{"type":"text","text":"cannot help"}]}`))
	}))
	defer server.Close()

	_, err := callAnthropic(context.Background(), config.Config{
		ApiURL: server.URL + "/v1/messages", APIProtocol: config.ProtocolAnthropicMessages,
		ModelName: "claude-sonnet-5", ApiKey: "test",
	}, []Message{{Role: "user", Content: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "拒绝回答") {
		t.Fatalf("expected refusal error, got %v", err)
	}
}

func TestResponsesProtocolUsesSingleEndpointAndGPT6Parameters(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/gateway/v1/responses" {
					t.Errorf("wrong path: %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, ok := body["temperature"]; ok {
					t.Error("GPT-6 reasoning rejects temperature")
				}
				if body["model"] != model {
					t.Error(body["model"])
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer server.Close()
			c := config.Config{Provider: "openai", ApiURL: server.URL + "/gateway/v1/chat/completions", APIProtocol: config.ProtocolOpenAIResponses, ModelName: model, Temperature: 0.5}
			config.ApplyProviderDefaults(&c)
			result, err := callLLMOnce(context.Background(), c, []Message{{Role: "user", Content: "test"}}, false, nil)
			if err != nil || result.Content != "ok" || result.Usage.TotalTokens != 3 {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

func TestGPT6ChatMarshalingOmitsUnsupportedFields(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			b, err := json.Marshal(ChatRequest{Model: model, Temperature: 0.5, MaxTokens: 8192})
			if err != nil {
				t.Fatal(err)
			}
			var req map[string]any
			json.Unmarshal(b, &req)
			if _, ok := req["temperature"]; ok {
				t.Fatal(string(b))
			}
			if _, ok := req["max_tokens"]; ok {
				t.Fatal(string(b))
			}
			if req["max_completion_tokens"] != float64(8192) {
				t.Fatal(string(b))
			}
		})
	}
}

func TestGPT6ChatDoesNotSendNativeToolsAtDefaultReasoningEffort(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if _, ok := request["tools"]; ok {
					t.Fatalf("native tools require reasoning_effort none in Chat Completions: %#v", request["tools"])
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer server.Close()
			_, err := callLLMOnce(context.Background(), config.Config{
				Provider: "openai", APIProtocol: config.ProtocolOpenAIChat,
				ApiURL: server.URL + "/v1/chat/completions", ModelName: model, ApiKey: "test",
			}, []Message{{Role: "user", Content: "hi"}}, true, nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
