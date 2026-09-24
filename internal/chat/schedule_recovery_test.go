package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/scheduler"
)

func scheduledReplyFixture(t *testing.T) (*Service, config.ChatChannel, Message, scheduler.ChatInboxLine) {
	t.Helper()
	s := testService(t, nilRunner)
	s.inboxStore = filepath.Join(t.TempDir(), "tasks.json")
	c := config.ChatChannel{Name: "qq", Platform: "qqbot", AppID: "test-app", AppSecret: "test-secret-do-not-store", AllowedUsers: []string{"alice"}}
	s.cfg.Channels = []config.ChatChannel{c}
	s.channels[c.Name] = c
	m := Message{User: "alice", Chat: "alice", ID: "m1"}
	line := scheduler.ChatInboxLine{TaskID: "reminder", Session: sessionIDFor(owner(c, m), 0), Kind: "result", Text: "喝水", At: time.Now()}
	return s, c, m, line
}

func restartScheduledChat(t *testing.T, s *Service) *Service {
	t.Helper()
	next, err := New(context.Background(), s.cfg, nilRunner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.stop)
	next.inboxStore = s.inboxStore
	if err = next.restoreOutbox(); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestScheduledReplySurvivesServiceRestart(t *testing.T) {
	s, c, m, line := scheduledReplyFixture(t)
	if err := s.rememberReplyRouteLocked(c, owner(c, m), m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.cfg.Store + ".routes.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), c.AppSecret) {
		t.Fatal("application secret persisted in route")
	}
	// Scheduler runs while the chat daemon is offline.
	if err = scheduler.PostChatInbox(s.inboxStore, line); err != nil {
		t.Fatal(err)
	}
	next := restartScheduledChat(t, s)
	if err = next.drainScheduledReplies(); err != nil {
		t.Fatal(err)
	}
	item := routedReplyItem(next, "喝水")
	if item == nil || item.Message.User != "alice" {
		t.Fatal("result lost or misrouted after restart")
	}
	pending, err := scheduler.PendingChatReplies(s.inboxStore)
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
}

func TestScheduledReplyRetriesUnknownRouteAndOutboxFailure(t *testing.T) {
	s, c, m, line := scheduledReplyFixture(t)
	if err := scheduler.PostChatInbox(s.inboxStore, line); err != nil {
		t.Fatal(err)
	}
	if err := s.drainScheduledReplies(); err != nil {
		t.Fatal(err)
	}
	pending, err := scheduler.PendingChatReplies(s.inboxStore)
	if err != nil || len(pending) != 1 {
		t.Fatal("unknown recipient result was discarded", err)
	}
	if err = s.rememberReplyRouteLocked(c, owner(c, m), m); err != nil {
		t.Fatal(err)
	}
	// Prevent creating the outbox directory to simulate failed durable enqueue.
	if err = os.WriteFile(s.cfg.Store+".outbox", []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.drainScheduledReplies(); err != nil {
		t.Fatal(err)
	}
	pending, err = scheduler.PendingChatReplies(s.inboxStore)
	if err != nil || len(pending) != 1 {
		t.Fatal("failed enqueue acknowledged", err)
	}
	if err = os.Remove(s.cfg.Store + ".outbox"); err != nil {
		t.Fatal(err)
	}
	if err = s.drainScheduledReplies(); err != nil {
		t.Fatal(err)
	}
	if routedReplyItem(s, "喝水") == nil {
		t.Fatal("result not retried")
	}
}

func TestScheduledReplyCrashBetweenEnqueueAndAckDoesNotDuplicate(t *testing.T) {
	s, c, m, line := scheduledReplyFixture(t)
	if err := s.rememberReplyRouteLocked(c, owner(c, m), m); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.PostChatInbox(s.inboxStore, line); err != nil {
		t.Fatal(err)
	}
	if !s.routeScheduledReply(line) {
		t.Fatal("enqueue failed")
	}
	// Crash before acknowledging the pending file, after network delivery.
	item := routedReplyItem(s, "喝水")
	item.Status = "delivered"
	item.Next = len(item.Chunks)
	if err := s.saveOutbox(item); err != nil {
		t.Fatal(err)
	}
	next := restartScheduledChat(t, s)
	if err := next.drainScheduledReplies(); err != nil {
		t.Fatal(err)
	}
	if len(next.box.items) != 1 {
		t.Fatal("duplicate outbox item")
	}
	got := routedReplyItem(next, "喝水")
	if got == nil || got.Status != "delivered" {
		t.Fatal("delivered result rearmed")
	}
}

func TestRestoredReplyRouteRejectsReplacedAccount(t *testing.T) {
	s, c, m, line := scheduledReplyFixture(t)
	if err := s.rememberReplyRouteLocked(c, owner(c, m), m); err != nil {
		t.Fatal(err)
	}
	s.cfg.Channels[0].AppID = "another-bot"
	next := restartScheduledChat(t, s)
	if next.routeScheduledReply(line) {
		t.Fatal("old result sent through replaced account")
	}
}
