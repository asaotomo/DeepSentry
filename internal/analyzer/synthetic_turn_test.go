package analyzer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSyntheticFeedbackDoesNotReplaceUserDirective(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "检查生产主机"},
		{Role: "user", Content: "工具自定义反馈", Synthetic: true},
		{Role: "user", Content: "子 Agent [log-analyst] 从 checkpoint 恢复后的结果：\n已完成"}, // legacy checkpoint
	}
	if goal := firstHistoryUserGoal(history); goal != "检查生产主机" {
		t.Fatalf("goal=%q", goal)
	}
	if latest := latestHistoryUserDirective(history); latest != "检查生产主机" {
		t.Fatalf("synthetic feedback became latest directive: %q", latest)
	}
	if boundary := recentUserTurnBoundary(history, 1); boundary != 0 {
		t.Fatalf("recent user boundary=%d", boundary)
	}
	raw, err := json.Marshal(history[1])
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(raw, &restored); err != nil || !restored.Synthetic {
		t.Fatalf("synthetic origin lost in checkpoint JSON: message=%#v err=%v", restored, err)
	}
	providerMessages, err := buildOpenAIChatMessages([]Message{history[1]})
	if err != nil {
		t.Fatal(err)
	}
	providerJSON, err := json.Marshal(providerMessages)
	if err != nil || strings.Contains(string(providerJSON), "synthetic") {
		t.Fatalf("internal origin leaked into model API: %s err=%v", providerJSON, err)
	}
}
