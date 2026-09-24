package analyzer

import (
	"ai-edr/internal/mcp"
	"strings"
	"testing"
)

func TestLimitedNativeProfileSelectsTypedHawkEyeToolsByWorkflowIntent(t *testing.T) {
	tools := []mcp.ExternalTool{
		{Name: "hawkeye__browser_tabs", OriginalName: "browser_tabs", Server: "hawkeye"},
		{Name: "hawkeye__browser_snapshot", OriginalName: "browser_snapshot", Server: "hawkeye"},
		{Name: "hawkeye__hawkeye_capture_state", OriginalName: "hawkeye_capture_state", Server: "hawkeye"},
		{Name: "hawkeye__hawkeye_capture_history", OriginalName: "hawkeye_capture_history", Server: "hawkeye"},
		{Name: "hawkeye__hawkeye_request_get", OriginalName: "hawkeye_request_get", Server: "hawkeye"},
		{Name: "hawkeye__hawkeye_intercept", OriginalName: "hawkeye_intercept", Server: "hawkeye"},
		{Name: "hawkeye__hawkeye_evaluate", OriginalName: "hawkeye_evaluate", Server: "hawkeye"},
	}
	selected := selectMCPToolsForContext(tools, 12, "检查当前页面为什么抓包没有请求头和请求体", nil)
	if len(selected) == 0 || len(selected) > 6 {
		t.Fatalf("unexpected selected count: %d %#v", len(selected), selected)
	}
	names := make(map[string]bool)
	for _, tool := range selected {
		names[tool.OriginalName] = true
	}
	for _, want := range []string{"hawkeye_capture_state", "hawkeye_capture_history", "hawkeye_request_get"} {
		if !names[want] {
			t.Fatalf("capture workflow missing typed tool %s: %#v", want, names)
		}
	}
	if names["hawkeye_evaluate"] {
		t.Fatalf("raw evaluate displaced safer capture workflow tools: %#v", names)
	}
}

func TestPinnedHawkEyeToolSurvivesChangingContext(t *testing.T) {
	tools := []mcp.ExternalTool{
		{Name: "hawkeye__browser_tabs", OriginalName: "browser_tabs", Server: "hawkeye"},
		{Name: "hawkeye__hawkeye_intercept", OriginalName: "hawkeye_intercept", Server: "hawkeye"},
	}
	selected := selectMCPToolsForContext(tools, 4, "继续分析结果", []string{"hawkeye__hawkeye_intercept"})
	if len(selected) != 1 || selected[0].Name != "hawkeye__hawkeye_intercept" {
		t.Fatalf("pinned MCP tool was lost: %#v", selected)
	}
}

func TestHawkEyeIntentRoutesCompleteWorkflowSchemas(t *testing.T) {
	tools := make([]mcp.ExternalTool, 0, len(mcp.HawkEyeKnownToolNames()))
	for _, name := range mcp.HawkEyeKnownToolNames() {
		tools = append(tools, mcp.ExternalTool{Name: "browser__" + name, OriginalName: name, Server: "custom-browser"})
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"Firefox 用真实手势按 F 进入全屏", []string{"browser_tabs", "browser_snapshot", "browser_click", "browser_press_key"}},
		{"打开b站播放杰克奥特曼第2集并2倍速全屏播放", []string{"browser_navigate", "browser_snapshot", "hawkeye_evaluate", "browser_press_key"}},
		{"抓包后检查 POST 的请求头和请求体", []string{"hawkeye_capture_state", "hawkeye_capture_start", "hawkeye_capture_history", "hawkeye_capture_inspect", "hawkeye_request_get"}},
		{"拦截请求、改包后放行，最后关闭拦截", []string{"hawkeye_scope", "hawkeye_intercept"}},
		{"搜索资料并深入调研页面", []string{"browser_search", "browser_fetch", "browser_research", "browser_read_text"}},
		{"识别登录页验证码并填写", []string{"browser_captcha_assist", "browser_snapshot"}},
		{"检查当前站点证书和 TLS", []string{"browser_security"}},
	}
	for _, test := range tests {
		selected := selectMCPToolsForContext(tools, 15, test.query, nil)
		got := make(map[string]bool)
		for _, tool := range selected {
			got[tool.OriginalName] = true
		}
		for _, want := range test.want {
			if !got[want] {
				t.Errorf("query %q missing %s; selected=%v", test.query, want, got)
			}
		}
		if strings.Contains(test.query, "验证码") && got["hawkeye_evaluate"] {
			t.Errorf("captcha workflow should not prefer evaluate: %v", got)
		}
	}
}
