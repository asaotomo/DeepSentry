package analyzer

import (
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalNativeToolCallUsesToolSchema(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Tools) == 0 || len(request.Messages) == 0 ||
			strings.Contains(request.Messages[0].Content, "响应必须是纯 JSON 对象") {
			t.Errorf("local native tool request was not configured: tools=%d messages=%d", len(request.Tools), len(request.Messages))
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"agent_action","arguments":"{\"action\":\"execute\",\"command\":\"echo ok\"}"}}]}}]}`))
	}))
	defer server.Close()
	config.GlobalConfig = config.Config{
		Provider: "lmstudio", APIProtocol: "openai_chat", ApiURL: server.URL,
		ApiKey: "none", ModelName: "openai/gpt-oss-20b", ContextWindowTokens: 15687,
		UseNativeTools: true,
	}
	history := []Message{{Role: "user", Content: "执行 echo ok"}}
	resp, err := RunAgentStepWithOptions(StepOptions{
		Context: context.Background(), SysCtx: collector.SystemContext{},
		History: &history, UseNativeTools: true,
	})
	if err != nil || resp.Action != "execute" || resp.Command != "echo ok" {
		t.Fatalf("native tool result=%+v err=%v", resp, err)
	}
}

func TestLocalControlTokenExecutesThroughNormalActionPath(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"<|channel|>commentary to=execute <|constrain|>json<|message|>{\"cmd\":\"open -a WeChat\"}"}}]}`))
	}))
	defer server.Close()
	config.GlobalConfig = config.Config{
		Provider: "lmstudio", APIProtocol: "openai_chat", ApiURL: server.URL,
		ApiKey: "none", ModelName: "openai/gpt-oss-20b", ContextWindowTokens: 15687,
	}
	history := []Message{{Role: "user", Content: "帮我打开微信"}}
	resp, err := RunAgentStepWithOptions(StepOptions{
		Context: context.Background(), SysCtx: collector.SystemContext{},
		History: &history, UseNativeTools: false,
	})
	if err != nil || resp.Action != "execute" || resp.Command != "open -a WeChat" || resp.IsFinished {
		t.Fatalf("local control message result=%+v err=%v", resp, err)
	}
}

func TestLocalControlTokenRejectsMalformedOrUnknownTool(t *testing.T) {
	for _, raw := range []string{
		`<|channel|>commentary to=execute <|constrain|>json<|message|>{"cmd":`,
		`<|channel|>commentary to=delete_everything <|constrain|>json<|message|>{"cmd":"echo unsafe"}`,
		`<|im_start|>assistant to=execute<|meta_sep|>commentary<|im_sep|>{"cmd":""}<|im_end|>`,
	} {
		resp, ok := recoverLocalControlMessage(raw)
		if !ok || resp.Action != "" || resp.Command != "" || resp.IsFinished || strings.Contains(resp.FinalReport, "<|channel|>") {
			t.Fatalf("malformed control message must request correction: %+v, ok=%v", resp, ok)
		}
	}
}

func TestLocalFinalChannelKeepsOnlyUserFacingText(t *testing.T) {
	resp, ok := recoverLocalControlMessage("<|channel|>final<|message|>已完成检查。<|im_end|>")
	if !ok || !resp.IsFinished || resp.FinalReport != "已完成检查。" {
		t.Fatalf("local final channel=%+v ok=%v", resp, ok)
	}
}
