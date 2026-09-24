package chat

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/scheduler"
)

func routedReplyItem(s *Service, needle string) *OutboxItem {
	s.box.mu.Lock()
	defer s.box.mu.Unlock()
	for _, it := range s.box.items {
		if len(it.Chunks) > 0 && strings.Contains(it.Chunks[0], needle) {
			copy := *it
			return &copy
		}
	}
	return nil
}

func TestScheduledReplyRoutesBackToOriginChat(t *testing.T) {
	s := testService(t, nilRunner)
	s.inboxStore = filepath.Join(t.TempDir(), "tasks.json")
	c := config.ChatChannel{Name: "wechat-quick", Platform: "weixin", AppID: "bot", AllowedUsers: []string{"alice"}}
	m := Message{User: "alice", Chat: "alice", ID: "m1", ContextToken: "ctx"}
	s.mu.Lock()
	s.channels[c.Name] = c
	own := owner(c, m)
	s.rememberReplyRouteLocked(c, own, m)
	gen := s.sessions[own]
	s.mu.Unlock()

	line := scheduler.ChatInboxLine{TaskID: "sched_x", Session: sessionIDFor(own, gen), Kind: "result", Text: "定时提醒：该喝水了", At: time.Now()}
	if err := scheduler.PostChatInbox(s.inboxStore, line); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := scheduler.ReadChatInbox(s.inboxStore, 0, "", true)
	for _, ln := range lines {
		s.routeScheduledReply(ln)
	}
	item := routedReplyItem(s, "该喝水了")
	if item == nil {
		t.Fatal("scheduled reply was not delivered back to the origin chat")
	}
	if item.Channel.Name != c.Name || item.Message.User != "alice" {
		t.Fatalf("wrong destination: %+v %+v", item.Channel, item.Message)
	}
}

func TestScheduledReplyIgnoresUnknownSessionAndClock(t *testing.T) {
	s := testService(t, nilRunner)
	s.inboxStore = filepath.Join(t.TempDir(), "tasks.json")
	c := config.ChatChannel{Name: "wechat-quick", Platform: "weixin", AppID: "bot", AllowedUsers: []string{"alice"}}
	m := Message{User: "alice", Chat: "alice"}
	s.mu.Lock()
	s.channels[c.Name] = c
	own := owner(c, m)
	s.rememberReplyRouteLocked(c, own, m)
	gen := s.sessions[own]
	s.mu.Unlock()

	// Clock notices are noise for chat and must not be forwarded.
	s.routeScheduledReply(scheduler.ChatInboxLine{TaskID: "sched_x", Session: sessionIDFor(own, gen), Kind: "info", Text: "完成于 2026-09-24 09:00:00", At: time.Now()})
	// A TUI-only session has no route and must fall through to the inbox.
	s.routeScheduledReply(scheduler.ChatInboxLine{TaskID: "sched_y", Session: "session_local_123", Kind: "result", Text: "本地任务结果", At: time.Now()})

	if routedReplyItem(s, "完成于") != nil {
		t.Fatal("clock notice should not reach chat")
	}
	if routedReplyItem(s, "本地任务结果") != nil {
		t.Fatal("unknown session result should stay in the TUI inbox")
	}
}

func TestScheduledReplyDedupsRepeatedReads(t *testing.T) {
	s := testService(t, nilRunner)
	s.inboxStore = filepath.Join(t.TempDir(), "tasks.json")
	c := config.ChatChannel{Name: "wechat-quick", Platform: "weixin", AppID: "bot", AllowedUsers: []string{"alice"}}
	m := Message{User: "alice", Chat: "alice"}
	s.mu.Lock()
	s.channels[c.Name] = c
	own := owner(c, m)
	s.rememberReplyRouteLocked(c, own, m)
	gen := s.sessions[own]
	s.mu.Unlock()

	line := scheduler.ChatInboxLine{TaskID: "sched_x", Session: sessionIDFor(own, gen), Kind: "result", Text: "只发一次", At: time.Unix(1727000000, 0)}
	s.routeScheduledReply(line)
	s.routeScheduledReply(line)
	count := 0
	s.box.mu.Lock()
	for _, it := range s.box.items {
		if len(it.Chunks) > 0 && strings.Contains(it.Chunks[0], "只发一次") {
			count++
		}
	}
	s.box.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected one queued reply, got %d", count)
	}
}
