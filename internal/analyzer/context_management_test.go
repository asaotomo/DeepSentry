package analyzer

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func longHistoryForTest() []Message {
	history := []Message{{Role: "user", Content: "排查生产 Web 入侵并保留完整证据链"}}
	for i := 0; i < 32; i++ {
		history = append(history,
			Message{Role: "assistant", Content: fmt.Sprintf(`{"action":"execute","command":"check-%d"}`, i)},
			Message{Role: "user", Content: fmt.Sprintf("Output:\nstep=%d %s", i, strings.Repeat("evidence ", 300))},
		)
	}
	return history
}

func TestManageHistoryContextPreservesGoalPinnedCluesAndRecentTail(t *testing.T) {
	history := longHistoryForTest()
	history = append(history[:4], append([]Message{{Role: "user", Content: "补充：只读排查，不要修改目标文件"}}, history[4:]...)...)
	last := history[len(history)-1].Content
	called := false
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		PinnedContext: "[IP] 10.20.30.40 — 已验证 C2\n[PATH] /var/www/html/a.php",
		Summarize: func(_ context.Context, messages []Message) (string, error) {
			called = true
			total := estimateHistoryChars(messages)
			if total > 60000 {
				t.Fatalf("summary request exceeded bounded input: %d", total)
			}
			joined := ""
			for _, message := range messages {
				joined += message.Content
			}
			for _, want := range []string{"排查生产 Web 入侵", "只读排查", "10.20.30.40", "/var/www/html/a.php"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("summary request lost pinned context %q", want)
				}
			}
			return "已完成入口排查；待关联网络证据。", nil
		},
	})
	if err != nil || !compacted || !called {
		t.Fatalf("compaction failed: compacted=%v called=%v err=%v", compacted, called, err)
	}
	if len(history) != contextCompactKeepRecent+1 {
		t.Fatalf("history len=%d want %d", len(history), contextCompactKeepRecent+1)
	}
	for _, want := range []string{"【原始用户目标】", "排查生产 Web 入侵", "最新用户补充", "只读排查", "10.20.30.40", "待关联网络证据"} {
		if !strings.Contains(history[0].Content, want) {
			t.Fatalf("summary envelope lost %q:\n%s", want, history[0].Content)
		}
	}
	if history[len(history)-1].Content != last {
		t.Fatal("recent history tail changed during compaction")
	}
}

func TestManageHistoryContextCancellationDoesNotMutateHistory(t *testing.T) {
	history := longHistoryForTest()
	original := append([]Message(nil), history...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		Context: ctx,
		Summarize: func(ctx context.Context, _ []Message) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if compacted || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation result compacted=%v err=%v", compacted, err)
	}
	if len(history) != len(original) || !reflect.DeepEqual(history[0], original[0]) || !reflect.DeepEqual(history[len(history)-1], original[len(original)-1]) {
		t.Fatal("failed/cancelled compaction mutated live history")
	}
}

func TestManageHistoryContextAvoidsSummarizingManyTinyTurns(t *testing.T) {
	var history []Message
	for i := 0; i < 70; i++ {
		history = append(history, Message{Role: "user", Content: fmt.Sprintf("短消息-%d", i)})
	}
	called := false
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		Summarize: func(context.Context, []Message) (string, error) {
			called = true
			return "unexpected", nil
		},
	})
	if err != nil || compacted || called {
		t.Fatalf("tiny turns should not spend a summary call: compacted=%v called=%v err=%v", compacted, called, err)
	}
}

func TestTruncateHistoryFallbackPreservesGoalAndPinnedClues(t *testing.T) {
	history := longHistoryForTest()
	history = append(history[:1], append([]Message{{Role: "system", Content: "【前情提要】已确认旧证据 EVIDENCE-OLD"}}, history[1:]...)...)
	history = append(history[:5], append([]Message{{Role: "user", Content: "补充：只读，不做修复"}}, history[5:]...)...)
	last := history[len(history)-1]
	TruncateHistoryFallbackWithHints(&history, 8, "[CVE] CVE-2026-12345")
	if len(history) != 9 {
		t.Fatalf("fallback len=%d want 9", len(history))
	}
	for _, want := range []string{"排查生产 Web 入侵", "CVE-2026-12345", "最近 8 条", "EVIDENCE-OLD", "只读，不做修复"} {
		if !strings.Contains(history[0].Content, want) {
			t.Fatalf("fallback lost %q:\n%s", want, history[0].Content)
		}
	}
	if !reflect.DeepEqual(history[len(history)-1], last) {
		t.Fatal("fallback lost latest message")
	}
}

func TestManageHistoryContextLargeWindowDoesNotCompactEarly(t *testing.T) {
	history := longHistoryForTest()
	called := false
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		HistoryBudgetTokens: 800_000,
		Summarize: func(context.Context, []Message) (string, error) {
			called = true
			return "unexpected", nil
		},
	})
	if err != nil || compacted || called {
		t.Fatalf("large context was prematurely compacted: compacted=%v called=%v err=%v", compacted, called, err)
	}
}

func TestManageHistoryContextSplitsGiantMessageWithoutDroppingMiddle(t *testing.T) {
	middle := "MIDDLE-EVIDENCE-SENTINEL"
	giant := strings.Repeat("A", 18_000) + middle + strings.Repeat("Z", 18_000)
	history := []Message{
		{Role: "user", Content: "分析这份巨大日志"},
		{Role: "user", Content: giant},
		{Role: "assistant", Content: `{"action":"execute","command":"next"}`},
	}
	seenMiddle := false
	calls := 0
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		HistoryBudgetTokens: 2_000,
		KeepRecent:          8,
		SummaryChunkTokens:  1_000,
		Summarize: func(_ context.Context, messages []Message) (string, error) {
			calls++
			for _, message := range messages {
				if strings.Contains(message.Content, middle) {
					seenMiddle = true
				}
			}
			return fmt.Sprintf("分段摘要-%d", calls), nil
		},
	})
	if err != nil || !compacted || calls < 2 || !seenMiddle {
		t.Fatalf("giant message compaction failed: compacted=%v calls=%d middle=%v err=%v", compacted, calls, seenMiddle, err)
	}
}

func TestEstimateTextTokensTreatsCJKAndASCIIConservatively(t *testing.T) {
	if got := EstimateTextTokens("中文测试"); got != 4 {
		t.Fatalf("CJK estimate=%d want 4", got)
	}
	if got := EstimateTextTokens("abcdefghijkl"); got != 4 {
		t.Fatalf("ASCII estimate=%d want 4", got)
	}
}

func TestEstimateMessagesTokensCountsImageAttachments(t *testing.T) {
	text := Message{Role: "user", Content: "看这张图"}
	withFloor := text
	withFloor.Attachments = []ImageAttachment{{Path: "/tmp/shot.png", Size: 100}}
	large := text
	large.Attachments = []ImageAttachment{{Path: "/tmp/shot.png", Size: 2 << 20}}
	if EstimateMessagesTokens([]Message{withFloor}) <= EstimateMessagesTokens([]Message{text}) {
		t.Fatal("image attachment should estimate higher than text alone")
	}
	if EstimateMessagesTokens([]Message{large}) <= EstimateMessagesTokens([]Message{withFloor}) {
		t.Fatal("larger image should estimate higher than the per-image floor")
	}
}

func TestPruneOldToolOutputsSkipsSummaryWhenItFits(t *testing.T) {
	history := historyWithPrunableOldToolOutputs(t)
	before := EstimateMessagesTokens(history)
	const budget = 18000
	if before <= budget {
		t.Fatalf("fixture tokens=%d, want > %d", before, budget)
	}
	called := false
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		HistoryBudgetTokens: budget,
		Summarize: func(context.Context, []Message) (string, error) {
			called = true
			return "should-not-run", nil
		},
	})
	if err != nil || !compacted || called {
		t.Fatalf("prune should fit without a summary: compacted=%v called=%v err=%v tokens=%d", compacted, called, err, EstimateMessagesTokens(history))
	}
	joined := joinHistoryContent(history)
	for _, want := range []string{"GOAL-KEEP", "RECENT-KEEP", "第三轮", "PLAYBOOK-KEEP", "DESKTOP-KEEP", "已省略较早的工具输出", "原步骤 1", "/tmp/sessions/output_step1.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("pruned history lost %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "OLD-DROP") {
		t.Fatal("old tool output survived pruning")
	}
}

func TestManageHistoryContextSummarizesWhenPruneIsNotEnough(t *testing.T) {
	history := historyThatStillExceedsAfterPrune()
	called := false
	var summarized strings.Builder
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		Summarize: func(_ context.Context, messages []Message) (string, error) {
			called = true
			summarized.WriteString(joinHistoryContent(messages))
			return "已摘要剩余上下文", nil
		},
	})
	if err != nil || !compacted || !called {
		t.Fatalf("expected summary after prune: compacted=%v called=%v err=%v", compacted, called, err)
	}
	joined := summarized.String()
	if strings.Contains(joined, "OLD-DROP") {
		t.Fatal("summary request still contained pruned tool output")
	}
	if !strings.Contains(joined, "/tmp/sessions/output_step1.txt") || !strings.Contains(joined, "GOAL-STAY") {
		t.Fatal("summary request lost stub or goal")
	}
	if !strings.Contains(history[0].Content, "【分层上下文摘要】") || !strings.Contains(history[0].Content, "GOAL-STAY") {
		t.Fatalf("summary envelope missing goal:\n%s", history[0].Content)
	}
}

func TestCancelledSummaryDoesNotKeepPrunedHistory(t *testing.T) {
	history := historyThatStillExceedsAfterPrune()
	original := append([]Message(nil), history...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	compacted, err := ManageHistoryContextWithOptions(&history, ContextManageOptions{
		Context: ctx,
		Summarize: func(ctx context.Context, _ []Message) (string, error) {
			return "", ctx.Err()
		},
	})
	if compacted || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation result compacted=%v err=%v", compacted, err)
	}
	if !reflect.DeepEqual(history, original) {
		t.Fatal("cancelled compaction left pruned history in place")
	}
}

func historyWithPrunableOldToolOutputs(t *testing.T) []Message {
	t.Helper()
	blob := strings.Repeat("x", 11998)
	old := func(step int) Message {
		return Message{Role: "user", Content: fmt.Sprintf("Output:\nstep=%d OLD-DROP %s\n...(完整输出已保存为 artifact /tmp/sessions/output_step%d.txt，sha256=abc)...", step, blob, step)}
	}
	return []Message{
		{Role: "user", Content: "GOAL-KEEP 排查目标"},
		{Role: "user", Content: "Output:\nDESKTOP-KEEP", Attachments: []ImageAttachment{{Path: "/tmp/reports/computer-use/screen-1.png", Size: 100, Detail: "original"}}},
		old(1),
		{Role: "tool", Name: "load_skill", Content: "已加载 Skill [fofamap]\nPLAYBOOK-KEEP\n" + strings.Repeat("play ", 400)},
		old(2),
		old(3),
		{Role: "user", Content: "Output:\nstep=4 " + blob},
		{Role: "user", Content: "Output:\nstep=5 " + blob},
		{Role: "user", Content: "第二轮：继续只读"},
		{Role: "user", Content: "第三轮：看最新结果"},
		{Role: "assistant", Content: `{"action":"execute","command":"recent"}`},
		{Role: "user", Content: "Output:\nRECENT-KEEP"},
	}
}

func historyThatStillExceedsAfterPrune() []Message {
	blob := strings.Repeat("q", 11998)
	old := func(step int) Message {
		return Message{Role: "user", Content: fmt.Sprintf("Output:\nstep=%d OLD-DROP %s\n...(完整输出已保存为 artifact /tmp/sessions/output_step%d.txt，sha256=abc)...", step, blob, step)}
	}
	return []Message{
		{Role: "user", Content: "GOAL-STAY 保留目标"},
		old(1),
		old(2),
		{Role: "user", Content: "第二轮补充"},
		{Role: "user", Content: "第三轮继续"},
		{Role: "user", Content: "Output:\nRECENT-HUGE " + strings.Repeat("z", 60000)},
	}
}

func joinHistoryContent(messages []Message) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(message.Content)
		b.WriteByte('\n')
	}
	return b.String()
}
