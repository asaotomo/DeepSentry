package scheduler

import (
	"ai-edr/internal/config"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInterruptedOneShotCannotResume(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "tasks.json"))
	now := time.Now()
	if err := s.Add(Task{ID: "once", Prompt: "side effect", Kind: KindAgent, Repeat: RepeatOnce, RunAt: now, Status: StatusEnabled}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	r := NewRunner(config.Config{})
	r.Store = s
	r.execute = func(context.Context, Task, time.Time) (string, string, error) {
		close(entered)
		<-release
		return "", "done", nil
	}
	go func() { defer close(done); _, _ = r.RunDue(now) }()
	<-entered
	// Capture the exact on-disk state during the side effect, as a crash would.
	data, err := os.ReadFile(s.Path)
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	crashedPath := filepath.Join(t.TempDir(), "tasks.json")
	if err = os.WriteFile(crashedPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(crashedPath)
	tasks, err := restarted.Load()
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].RunCount != 1 || tasks[0].LastRunAt == nil {
		t.Fatalf("attempt not durable: %+v", tasks[0])
	}
	if err = restarted.SetStatus("once", StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err = restarted.SetStatus("once", StatusEnabled); err == nil {
		t.Fatal("interrupted one-shot can replay")
	}
	completed, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if completed[0].RunCount != 1 {
		t.Fatal("attempt counted twice")
	}
}

func TestReminderDedupIncludesRecipientAndText(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "tasks.json"))
	now := time.Now()
	input := PlanInput{Prompt: "每天九点提醒喝水", RunAt: "09:00", Repeat: RepeatDaily, ReplySession: "session_chat_alice_g0", ReplyText: "喝水"}
	a, err := PlanTask(input, now)
	if err != nil {
		t.Fatal(err)
	}
	input.ReplySession = "session_chat_bob_g0"
	b, err := PlanTask(input, now)
	if err != nil {
		t.Fatal(err)
	}
	input.ReplyText = "休息"
	c, err := PlanTask(input, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []Task{a.Task, b.Task, c.Task} {
		if _, created, err := s.AddUnique(task); err != nil || !created {
			t.Fatalf("independent reminder merged: %s %v", task.ReplySession, err)
		}
	}
	duplicate := b.Task
	duplicate.ID = "retry"
	if got, created, err := s.AddUnique(duplicate); err != nil || created || got.ID != b.Task.ID {
		t.Fatalf("same-recipient retry not deduplicated: %+v %v", got, err)
	}
	collision := b.Task
	collision.ID = a.Task.ID
	if _, _, err := s.AddUnique(collision); err == nil {
		t.Fatal("ID collision silently switched recipient")
	}
}

func TestChatPendingSurvivesInboxRemoval(t *testing.T) {
	store := filepath.Join(t.TempDir(), "tasks.json")
	line := ChatInboxLine{TaskID: "task", Session: "session_chat_alice_g0", Kind: "result", Text: "hello", At: time.Now()}
	if err := PostChatInbox(store, line); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(chatInboxPath(store)); err != nil {
		t.Fatal(err)
	}
	pending, err := PendingChatReplies(store)
	if err != nil || len(pending) != 1 {
		t.Fatalf("lost durable result: %v %v", pending, err)
	}
	if pending[0].Line.Text != "hello" {
		t.Fatal("wrong result")
	}
	if err = AckChatReply(store, "../outside"); err == nil {
		t.Fatal("unsafe ack accepted")
	}
	if err = AckChatReply(store, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	pending, err = PendingChatReplies(store)
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
}
