package analyzer

import (
	"ai-edr/internal/config"
	"ai-edr/internal/harness/subagent"
	"strings"
	"testing"
)

func TestTaskSchemaListsRegisteredSubAgents(t *testing.T) {
	definitions := AgentToolDefinitions()
	for _, definition := range definitions {
		if definition.Function.Name != "agent_action" {
			continue
		}
		properties := definition.Function.Parameters["properties"].(map[string]interface{})
		task := properties["task_name"].(map[string]string)
		for _, name := range subagent.Names() {
			if !strings.Contains(task["description"], name) {
				t.Fatalf("native task schema does not expose %s", name)
			}
		}
		return
	}
	t.Fatal("agent_action schema missing")
}

func TestAgentToolDefinitionsForCompactContextLimitsAndRanks(t *testing.T) {
	definitions := AgentToolDefinitionsForContext(8, "请离线 analyze pcap 并检查 DNS 会话")
	if len(definitions) < 4 || len(definitions) > 11 { // always-visible natives + only positive candidates
		t.Fatalf("compact definitions=%d want 4..11", len(definitions))
	}
	want := map[string]bool{"agent_action": false, "tool_catalog": false, "skill": false, "pcap_analyze": false}
	for _, definition := range definitions {
		if _, ok := want[definition.Function.Name]; ok {
			want[definition.Function.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("compact native schema lost %s", name)
		}
	}
}

func TestAgentToolDefinitionsFullContextKeepsEveryTool(t *testing.T) {
	if got, want := len(AgentToolDefinitionsForContext(0, "")), len(AgentToolDefinitions()); got != want {
		t.Fatalf("full profile definitions=%d want %d", got, want)
	}
}

func TestRuntimeV3DeferredToolsBoundFullProfileAndKeepPinned(t *testing.T) {
	cfg := config.Config{AgentRuntime: "v3", Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: "https://example.com/v1", ModelName: "large", ContextWindowTokens: 1_048_576}
	messages := []Message{
		{Role: "system", Content: "【本任务已验证工具】后续轮次优先保留这些工具的完整 schema: pcap_analyze, webshell_hunt"},
		{Role: "user", Content: "继续分析证据"},
	}
	definitions := nativeToolDefinitionsForRequest(cfg, messages)
	if got, max := len(definitions), runtimeV3DeferredToolLimit+alwaysVisibleNativeSchemaCount; got < 5 || got > max {
		t.Fatalf("v3 definitions=%d want 5..%d", got, max)
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		seen[definition.Function.Name] = true
	}
	for _, name := range []string{"agent_action", "tool_catalog", "skill", "pcap_analyze", "webshell_hunt"} {
		if !seen[name] {
			t.Fatalf("v3 deferred schema lost %s", name)
		}
	}
}

func TestDeferredToolRetrievalDoesNotAlphabeticallyFillZeroScores(t *testing.T) {
	definitions := AgentToolDefinitionsForContextWithPinned(runtimeV3DeferredToolLimit, "跟进分页安全公告中的相关链接", nil)
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		seen[definition.Function.Name] = true
	}
	if !seen["browser_browse"] {
		t.Fatal("browser_browse was not retrieved")
	}
	if seen["config_manage"] {
		t.Fatal("zero-relevance config_manage leaked into deferred candidates")
	}
	if len(definitions) >= runtimeV3DeferredToolLimit+alwaysVisibleNativeSchemaCount {
		t.Fatalf("deferred candidates were padded to the hard limit: %d", len(definitions))
	}
}

func TestDeferredConfigManageRequiresExplicitProductConfigIntent(t *testing.T) {
	for _, query := range []string{"检查 AWD 服务可用性 target status", "审计 Redis 危险配置 config_path"} {
		definitions := AgentToolDefinitionsForContextWithPinned(runtimeV3DeferredToolLimit, query, nil)
		for _, definition := range definitions {
			if definition.Function.Name == "config_manage" {
				t.Fatalf("query %q incorrectly exposed config_manage", query)
			}
		}
	}
	definitions := AgentToolDefinitionsForContextWithPinned(runtimeV3DeferredToolLimit, "修改 DeepSentry config.yaml 添加目标", nil)
	found := false
	for _, definition := range definitions {
		found = found || definition.Function.Name == "config_manage"
	}
	if !found {
		t.Fatal("explicit DeepSentry config intent did not expose config_manage")
	}
}

func TestRuntimeV3PinsStructuredToolResultsAndLegacyRemainsFull(t *testing.T) {
	base := config.Config{Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: "https://example.com/v1", ModelName: "large", ContextWindowTokens: 1_048_576}
	messages := []Message{{Role: "tool", Name: "network_device_diagnose", ToolCallID: "call_1", Content: "ok"}}
	base.AgentRuntime = "v3"
	v3 := nativeToolDefinitionsForRequest(base, messages)
	found := false
	for _, definition := range v3 {
		found = found || definition.Function.Name == "network_device_diagnose"
	}
	if !found {
		t.Fatal("v3 did not pin the completed structured tool")
	}
	base.AgentRuntime = "legacy"
	legacy := nativeToolDefinitionsForRequest(base, messages)
	if len(legacy) <= len(v3) {
		t.Fatalf("legacy definitions=%d should remain larger than bounded v3=%d", len(legacy), len(v3))
	}
}

func TestDeferredToolRetrievalCoversOperationalSynonyms(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"识别伪装二进制，fixture forensic/magic，并记录完整性", []string{"file_ident", "file_hash"}},
		{"提取网页表单与页面脚本", []string{"web_snapshot"}},
		{"跟进分页安全公告中的相关链接", []string{"browser_browse"}},
		{"检查 AWD 服务可用性并批量探活", []string{"awd_service_check"}},
		{"Fleet 只读批量核查监听端口", []string{"fleet_exec"}},
		{"SSH 中断后恢复多目标汇总", []string{"fleet_exec"}},
	}
	for _, test := range tests {
		definitions := AgentToolDefinitionsForContextWithPinned(runtimeV3DeferredToolLimit, test.query, nil)
		seen := make(map[string]bool, len(definitions))
		for _, definition := range definitions {
			seen[definition.Function.Name] = true
		}
		for _, name := range test.want {
			if !seen[name] {
				t.Errorf("query %q did not retrieve %s", test.query, name)
			}
		}
	}
}

func TestDesktopFallbackDiscoveredWithoutDisplacingCLIWork(t *testing.T) {
	for _, query := range []string{"帮我打开微信给DingDong发一个你好", "原生应用 UI自动化 辅助功能", "屏幕截图 操作电脑"} {
		defs := AgentToolDefinitionsForContextWithPinned(1, query, nil)
		found := false
		for _, d := range defs {
			if d.Function.Name == "computer_use" {
				found = true
			}
		}
		if !found {
			t.Fatalf("desktop fallback missing for %q", query)
		}
	}
	for _, query := range []string{"统计日志错误数量", "检查磁盘空间", "通过微信API发送通知"} {
		defs := AgentToolDefinitionsForContextWithPinned(1, query, nil)
		for _, d := range defs {
			if d.Function.Name == "computer_use" {
				t.Fatalf("CLI task routed to desktop: %q", query)
			}
		}
	}
}
