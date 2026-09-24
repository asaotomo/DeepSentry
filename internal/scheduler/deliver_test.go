package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestDirectChatReplyExtractsUtterance(t *testing.T) {
	cases := []struct {
		prompt string
		want   string
		ok     bool
	}{
		{"每分钟在当前聊天框给我发一句hello", "hello", true},
		{"每分钟回一句 hello", "hello", true},
		{"在当前聊天框发一句「hello」", "hello", true},
		{"每天9点巡检", "", false},
		{"每分钟发钉钉通知", "", false},
	}
	for _, tc := range cases {
		got, ok := directChatReply(Task{Prompt: tc.prompt, Kind: KindAgent})
		if ok != tc.ok || got != tc.want {
			t.Fatalf("%q got %q ok=%v want %q ok=%v", tc.prompt, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := directChatReply(Task{Kind: KindInspection, ReplyText: "hello"}); ok {
		t.Fatal("inspection must not become a chat reply")
	}
}

func TestChatInboxSkipsHistoryAndFiltersSession(t *testing.T) {
	store := filepath.Join(t.TempDir(), "tasks.json")
	if err := postChatInbox(store, ChatInboxLine{Text: "old", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	lines, next, primed := ReadChatInbox(store, 0, "s1", false)
	if len(lines) != 0 || !primed || next == 0 {
		t.Fatalf("history leaked: lines=%d next=%d primed=%v", len(lines), next, primed)
	}
	if err := postChatInbox(store, ChatInboxLine{Text: "hello", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := postChatInbox(store, ChatInboxLine{Text: "other", Session: "s2"}); err != nil {
		t.Fatal(err)
	}
	lines, next, primed = ReadChatInbox(store, next, "s1", primed)
	if !primed || len(lines) != 1 || lines[0].Text != "hello" || next == 0 {
		t.Fatalf("filter failed: lines=%v next=%d primed=%v", lines, next, primed)
	}
}

func TestChatInboxRotationKeepsUnreadLines(t *testing.T) {
	store := filepath.Join(t.TempDir(), "tasks.json")
	if err := postChatInbox(store, ChatInboxLine{Text: "seed", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	_, next, primed := ReadChatInbox(store, 0, "s1", false)
	filler := strings.Repeat("x", maxInboxRunes-10)
	for i := 0; i < 70; i++ {
		if err := postChatInbox(store, ChatInboxLine{Text: filler, Session: "other"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := postChatInbox(store, ChatInboxLine{Text: "before-rotate", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := postChatInbox(store, ChatInboxLine{Text: "after-rotate", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(chatInboxPath(store) + ".1"); err != nil {
		t.Fatalf("inbox did not rotate: %v", err)
	}
	lines, _, _ := ReadChatInbox(store, next, "s1", primed)
	var got []string
	for _, line := range lines {
		got = append(got, line.Text)
	}
	if strings.Join(got, ",") != "before-rotate,after-rotate" {
		t.Fatalf("unread lines lost across rotation: %v", got)
	}
}

func TestRunNowPostsChatReplyWithoutAgent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "tasks.json"))
	now := time.Date(2026, 9, 22, 9, 40, 0, 0, time.Local)
	task := Task{
		ID: "sched_hello", Name: "hello", Prompt: "每分钟回一句 hello",
		Kind: KindAgent, Repeat: RepeatInterval, IntervalSec: 60,
		RunAt: now.Add(-time.Minute), Status: StatusEnabled, ReplySession: "session_a",
	}
	if err := store.Add(task); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(config.Config{SchedulerStore: store.Path})
	runner.Store = store
	called := false
	runner.execute = func(context.Context, Task, time.Time) (string, string, error) {
		called = true
		return "", "", nil
	}
	result, err := runner.RunNow("sched_hello", now)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("chat reply must not start an agent")
	}
	if !strings.Contains(result, "已发送到当前聊天: hello") {
		t.Fatalf("result=%q", result)
	}
	lines, _, primed := ReadChatInbox(store.Path, 0, "session_a", true)
	if !primed || len(lines) != 2 || lines[0].Text != "hello" || !strings.Contains(lines[1].Text, "完成于 ") || !strings.Contains(lines[1].Text, "下次 ") {
		t.Fatalf("inbox=%v", lines)
	}
}

func TestRunNoticePostsStartAndFinish(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "tasks.json"))
	now := time.Date(2026, 9, 22, 9, 40, 0, 0, time.Local)
	task := Task{
		ID: "sched_check", Name: "每天巡检", Prompt: "每天9点巡检服务器",
		Kind: KindAgent, Repeat: RepeatDaily, RunAt: now.Add(-time.Minute),
		Status: StatusEnabled, ReplySession: "session_a", AllowBatch: true,
	}
	if err := store.Add(task); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(config.Config{SchedulerStore: store.Path})
	runner.Store = store
	runner.execute = func(context.Context, Task, time.Time) (string, string, error) {
		return filepath.Join(dir, "report.md"), "巡检报告已生成", nil
	}
	if _, err := runner.RunNow("sched_check", now); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := ReadChatInbox(store.Path, 0, "session_a", true)
	if len(lines) != 2 || lines[0].Text != "巡检报告已生成" || lines[0].Kind != "result" {
		t.Fatalf("body=%v", lines)
	}
	if !strings.Contains(lines[1].Text, "完成于 ") || !strings.Contains(lines[1].Text, "下次 ") || lines[1].Kind != "info" {
		t.Fatalf("clock=%v", lines[1])
	}
}

func TestFormatRunClockOmitsNextForOneShot(t *testing.T) {
	finished := time.Date(2026, 9, 22, 11, 28, 58, 0, time.Local)
	once := formatRunClock(Task{Status: StatusCompleted, Repeat: RepeatOnce, RunAt: finished}, finished)
	if once != "完成于 2026-09-22 11:28:58" {
		t.Fatalf("once=%q", once)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 22, 11, 28, 58, 0, loc)
	again := formatRunClock(Task{Status: StatusEnabled, Repeat: RepeatInterval, IntervalSec: 60, RunAt: at.Add(time.Minute), Timezone: "Asia/Shanghai"}, at)
	if again != "完成于 2026-09-22 11:28:58，下次 2026-09-22 11:29:58" {
		t.Fatalf("interval=%q", again)
	}
}

func TestExtractDeliveredTextKeepsFinalReportBody(t *testing.T) {
	raw := "想法: 只输出笑话\n\n📝  最终报告:\n========================================\n公司新来了一只鹦鹉。\n老板说它负责复读。\n========================================\n\n📂  日志: reports/report.md\n"
	got := extractDeliveredText(raw)
	if got != "公司新来了一只鹦鹉。\n老板说它负责复读。" {
		t.Fatalf("got %q", got)
	}
	path := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if body := presentRunResult("Agent 任务完成，报告: "+path, path); body != got {
		t.Fatalf("present=%q", body)
	}
}
