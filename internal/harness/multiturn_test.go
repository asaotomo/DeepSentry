package harness

import (
	"strings"
	"testing"

	"ai-edr/internal/analyzer"
)

func TestCountUserTurns(t *testing.T) {
	h := []analyzer.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "Output:\ncommand result"},
		{Role: "user", Content: "系统警告: retry"},
		{Role: "user", Content: "【系统】本轮已结束。"},
		{Role: "user", Content: "c"},
	}
	if n := CountUserTurns(h); n != 2 {
		t.Fatalf("expected 2 user turns, got %d", n)
	}
}

func TestSyntheticFeedbackDoesNotBecomeUserFollowUp(t *testing.T) {
	history := []analyzer.Message{
		{Role: "user", Content: "需求：排查这台主机"},
		{Role: "user", Content: "Output:\nresult", Synthetic: true},
		{Role: "user", Content: "子 Agent [log-analyst] 从 checkpoint 恢复后的结果：\n已完成", Synthetic: true},
		{Role: "user", Content: "子 Agent [network-analyst] 从 checkpoint 恢复后的结果：\n旧版快照"},
		{Role: "user", Content: "MCP 工具返回了图片证据，请分析", Synthetic: true},
		{Role: "user", Content: "循环守卫：请换方案", Synthetic: true},
	}
	if got := CountUserTurns(history); got != 1 {
		t.Fatalf("synthetic messages inflated user turns: %d", got)
	}
	if got := ChatReplyExtraPrompt(true, &history); strings.Contains(got, "【续聊】") {
		t.Fatalf("synthetic feedback triggered follow-up prompt: %q", got)
	}
	if got := latestUserTask(history); got != "需求：排查这台主机" {
		t.Fatalf("skill matching used synthetic feedback: %q", got)
	}
	goal, latest := collaborationUserDirectives(history)
	if goal != "需求：排查这台主机" || latest != goal {
		t.Fatalf("sub-agent brief used synthetic feedback: goal=%q latest=%q", goal, latest)
	}
	if !canContinueSavedSubAgents(append(history, analyzer.Message{Role: "user", Content: "需求：继续"}), 1) {
		t.Fatal("synthetic feedback blocked an explicit continue")
	}
	if canContinueSavedSubAgents(append(history, analyzer.Message{Role: "user", Content: "需求：查另一台"}), 1) {
		t.Fatal("new user task was mistaken for continue")
	}
}

func TestCommitFinishDoesNotCreateSyntheticUserTurn(t *testing.T) {
	h := []analyzer.Message{{Role: "user", Content: "真实需求"}}
	CommitFinishToHistory(&h, AgentAction{Type: ActionFinish, FinalReport: "完成"}, "完成")
	if got := CountUserTurns(h); got != 1 {
		t.Fatalf("finish summary must not inflate user turns: got %d history=%#v", got, h)
	}
	if got := h[len(h)-1].Role; got != "system" {
		t.Fatalf("finish summary role=%q, want system", got)
	}
}

func TestMultiTurnExtraPrompt(t *testing.T) {
	h := []analyzer.Message{{Role: "user", Content: "only one"}}
	if p := MultiTurnExtraPrompt(true, &h); p != "" {
		t.Fatal("single turn should not inject follow-up prompt")
	}
	h = append(h, analyzer.Message{Role: "user", Content: "follow up"})
	if p := MultiTurnExtraPrompt(true, &h); p == "" {
		t.Fatal("second user turn should inject follow-up prompt")
	}
	if !strings.Contains(MultiTurnExtraPrompt(true, &h), "不要把本次追问当成新会话") {
		t.Fatal("follow-up prompt should discourage re-introduction")
	}
	first := []analyzer.Message{{Role: "user", Content: "你好"}}
	if p := ChatReplyExtraPrompt(true, &first); p == "" || !strings.Contains(p, "手机 IM") {
		t.Fatalf("first-turn chat prompt missing: %q", p)
	}
	if p := ChatReplyExtraPrompt(true, &h); p == "" || !strings.Contains(p, "续聊") {
		t.Fatalf("follow-up chat prompt missing: %q", p)
	}
	if p := ChatReplyExtraPrompt(false, &h); p != "" {
		t.Fatal("chat reply prompt should be opt-in")
	}
}
