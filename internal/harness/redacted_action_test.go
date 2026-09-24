package harness

import (
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactedActionRemovesSecretsWithoutMutatingOriginal(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	config.GlobalConfig.ApiKey = "configured-api-secret"

	action := AgentAction{
		Type:        ActionTool,
		Command:     "curl --token literal-token https://example.test",
		TaskPrompt:  "authorization=Bearer configured-api-secret",
		MemoryValue: "password=memory-secret",
		ToolName:    "config_manage",
		ToolArgs: map[string]string{
			"password": "tool-secret",
			"api_key":  "another-secret",
			"host":     "example.test",
		},
		ParallelTasks: []SubAgentTaskAction{{TaskPrompt: "token=parallel-secret"}},
		Options:       []string{"password=option-secret"},
	}

	redacted := RedactedAction(action)
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, secret := range []string{
		"configured-api-secret", "literal-token", "memory-secret", "tool-secret",
		"another-secret", "parallel-secret", "option-secret",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted action still contains %q: %s", secret, got)
		}
	}
	if redacted.ToolArgs["host"] != "example.test" {
		t.Fatalf("non-sensitive argument changed: %#v", redacted.ToolArgs)
	}
	if action.ToolArgs["password"] != "tool-secret" || action.ParallelTasks[0].TaskPrompt != "token=parallel-secret" {
		t.Fatalf("original action was mutated: %#v", action)
	}
}

func TestAuthorizeSubAgentExecuteShowsRedactedCommandButApprovesOriginal(t *testing.T) {
	action := AgentAction{Type: ActionExecute, Command: "rm -rf /tmp/demo --password super-secret"}
	var shown *AgentAction
	allowed, _ := authorizeSubAgentExecute(&action, collector.SystemContext{}, false, nil, func(candidate *AgentAction) bool {
		copy := *candidate
		shown = &copy
		return true
	}, func(collector.SystemContext, string, string) (string, string, bool) {
		return "high", "仍需确认", true
	})
	if !allowed {
		t.Fatal("expected confirmation approval")
	}
	if shown == nil || strings.Contains(shown.Command, "super-secret") {
		t.Fatalf("confirmation received unredacted action: %#v", shown)
	}
	if !strings.Contains(action.Command, "super-secret") {
		t.Fatalf("executable action was unexpectedly mutated: %#v", action)
	}
}
