package chat

import (
	"ai-edr/internal/config"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func jobByPrompt(t *testing.T, s *Service, prompt string) *Job {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.Prompt == prompt {
			copyJob := *j
			return &copyJob
		}
	}
	return nil
}

func waitJob(t *testing.T, s *Service, prompt, want string) Job {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if j := jobByPrompt(t, s, prompt); j != nil && j.Status == want {
			return *j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j := jobByPrompt(t, s, prompt)
	t.Fatalf("prompt=%s want=%s got=%v", prompt, want, j)
	return Job{}
}

func submitAs(t *testing.T, s *Service, c config.ChatChannel, user, chat, id, prompt string) {
	t.Helper()
	m := testMessage(id, "/ds run "+prompt)
	m.User = user
	m.Chat = chat
	out, err := s.command(c, m)
	if err != nil || out != "" {
		t.Fatalf("submit %s: %q %v", prompt, out, err)
	}
}

func TestConcurrentRobotsUseIsolatedSessions(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan string, 3)
	var mu sync.Mutex
	sessions := map[string]string{}
	s := testService(t, func(_ context.Context, prompt, sessionID string) (string, error) {
		mu.Lock()
		sessions[prompt] = sessionID
		mu.Unlock()
		started <- prompt
		<-gate
		return "ok", nil
	})
	c := testChannel()
	c.AllowedUsers = []string{"alice", "bob", "carol"}
	submitAs(t, s, c, "alice", "chat-a", "a1", "alice")
	submitAs(t, s, c, "bob", "chat-b", "b1", "bob")
	submitAs(t, s, c, "carol", "chat-c", "c1", "carol")

	got := map[string]bool{}
	for i := 0; i < 3; i++ {
		select {
		case p := <-started:
			got[p] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("expected 3 concurrent robots, got %v", got)
		}
	}
	close(gate)
	waitJob(t, s, "alice", "completed")
	waitJob(t, s, "bob", "completed")
	waitJob(t, s, "carol", "completed")

	mu.Lock()
	defer mu.Unlock()
	if sessions["alice"] == "" || sessions["alice"] == sessions["bob"] || sessions["alice"] == sessions["carol"] || sessions["bob"] == sessions["carol"] {
		t.Fatalf("sessions not isolated: %v", sessions)
	}
}

func TestSameSessionQueuesInsteadOfBusyReject(t *testing.T) {
	release := make(chan struct{})
	var secondStarted atomic.Bool
	var firstSession, secondSession string
	started := make(chan struct{})
	s := testService(t, func(_ context.Context, prompt, sessionID string) (string, error) {
		if prompt == "first" {
			firstSession = sessionID
			close(started)
			<-release
			return "one", nil
		}
		secondSession = sessionID
		secondStarted.Store(true)
		return "two", nil
	})
	c := testChannel()
	if _, err := s.command(c, testMessage("m1", "/ds run first")); err != nil {
		t.Fatal(err)
	}
	<-started
	if out, err := s.command(c, testMessage("m2", "/ds run second")); err != nil || out != "" {
		t.Fatalf("follow-up should queue silently, got %q %v", out, err)
	}
	if jobByPrompt(t, s, "second").Status != "queued" {
		t.Fatalf("expected queued, got %+v", jobByPrompt(t, s, "second"))
	}
	if secondStarted.Load() {
		t.Fatal("same-session follow-up started while first still running")
	}
	close(release)
	waitJob(t, s, "second", "completed")
	if firstSession == "" || firstSession != secondSession {
		t.Fatalf("same private chat should reuse session: first=%q second=%q", firstSession, secondSession)
	}
}

func TestMaxFiveConcurrentThenQueueSixth(t *testing.T) {
	release := make(chan struct{})
	var running atomic.Int32
	var maxSeen atomic.Int32
	started := make(chan string, 6)
	s := testService(t, func(_ context.Context, prompt, _ string) (string, error) {
		n := running.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		started <- prompt
		<-release
		running.Add(-1)
		return "ok", nil
	})
	c := testChannel()
	c.AllowedUsers = []string{"u0", "u1", "u2", "u3", "u4", "u5"}
	for i := 0; i < 6; i++ {
		submitAs(t, s, c, "u"+string(rune('0'+i)), "chat"+string(rune('0'+i)), "m"+string(rune('0'+i)), "t"+string(rune('0'+i)))
	}

	got := map[string]bool{}
	for i := 0; i < 5; i++ {
		select {
		case p := <-started:
			got[p] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("expected 5 running robots, got %v", got)
		}
	}
	select {
	case p := <-started:
		t.Fatalf("sixth job started too early: %s", p)
	case <-time.After(40 * time.Millisecond):
	}
	sixth := jobByPrompt(t, s, "t5")
	if sixth == nil || sixth.Status != "queued" {
		t.Fatalf("sixth should be queued, got %+v", sixth)
	}
	if maxSeen.Load() > 5 {
		t.Fatalf("max concurrent=%d want<=5", maxSeen.Load())
	}
	close(release)
	waitJob(t, s, "t5", "completed")
	if maxSeen.Load() > 5 {
		t.Fatalf("max concurrent after drain=%d", maxSeen.Load())
	}
}
