package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCancelsApprovalAndQueueBeforeFreshTurn(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		if prompt == "old" {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return "stale result", ctx.Err()
		}
		return "fresh", nil
	})
	// Unblock the runner before the service cleanup, even after a failed assertion.
	defer close(release)
	c := testChannel()
	s.channels[c.Name] = c
	if _, err := s.command(c, testMessage("old", "old")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	old := waitJob(t, s, "old", "running")
	if _, err := s.command(c, testMessage("queued", "obsolete queued")); err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, "obsolete queued", "queued")
	s.mu.Lock()
	own := owner(c, testMessage("", ""))
	confirmDir := s.confirmDirForOwnerLocked(own)
	if confirmDir == "" {
		t.Error("missing active confirmation directory")
	}
	s.mu.Unlock()
	if err := WriteConfirmRequest(confirmDir, ConfirmRequest{Seq: 7}); err != nil {
		t.Fatal(err)
	}
	approval := &OutboxItem{Channel: c, Message: testMessage("old", "old"), Ephemeral: true, JobID: old.ID, ConfirmSeq: 7, Created: time.Now()}
	if _, ok := s.deliveryChannel(approval); !ok {
		t.Fatal("fixture approval was not deliverable before reset")
	}
	out, err := s.command(c, testMessage("reset", "/new"))
	if err != nil || !strings.Contains(out, "已开启新会话") {
		t.Fatalf("%s %v", out, err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("old task not cancelled")
	}
	waitJob(t, s, "obsolete queued", "cancelled")
	s.mu.Lock()
	dir := s.confirmDirForOwnerLocked(own)
	s.mu.Unlock()
	if dir != "" {
		t.Fatal("old approval still actionable")
	}
	if _, ok := s.deliveryChannel(approval); ok {
		t.Fatal("old approval still deliverable after reset")
	}
	if _, ok := s.deliveryChannel(&OutboxItem{Ephemeral: true, JobID: old.ID, Created: time.Now()}); ok {
		t.Fatal("old progress still deliverable")
	}
	if _, err := s.command(c, testMessage("fresh", "fresh task")); err != nil {
		t.Fatal(err)
	}
	// No new process overlaps cancellation cleanup. Release it explicitly.
	release <- struct{}{}
	fresh := waitJob(t, s, "fresh task", "completed")
	if fresh.SessionID == old.SessionID {
		t.Fatal("old context reused")
	}
	oldFinal := waitJob(t, s, "old", "cancelled")
	if oldFinal.Notification != nil {
		t.Fatal("old final leaked into new session")
	}
}

func TestFailedNewDoesNotCancelCurrentTask(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	own := owner(c, testMessage("", ""))
	cancelled := false
	s.jobs["running"] = &Job{ID: "running", Owner: own, Status: "running", SessionID: sessionIDFor(own, 0)}
	s.cancel["running"] = func() { cancelled = true }
	// A directory at the destination makes the atomic rename fail.
	if err := os.MkdirAll(filepath.Join(filepath.Dir(s.cfg.Store), "sessions.json"), 0700); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	out := s.restartSessionLocked(own)
	s.mu.Unlock()
	if !strings.Contains(out, "失败") || cancelled || s.sessions[own] != 0 {
		t.Fatalf("out=%s cancelled=%v", out, cancelled)
	}
}

func TestSessionChangeOnlyCancelsOwningConversation(t *testing.T) {
	for _, action := range []string{"new", "switch"} {
		t.Run(action, func(t *testing.T) {
			s := testService(t, nil)
			c := testChannel()
			own := owner(c, testMessage("", ""))
			otherMessage := testMessage("", "")
			otherMessage.User = "bob"
			other := owner(c, otherMessage)
			stopped := false
			unrelated := false
			s.jobs["old"] = &Job{ID: "old", Owner: own, SessionID: sessionIDFor(own, 0), Status: "running"}
			s.jobs["other"] = &Job{ID: "other", Owner: other, SessionID: sessionIDFor(other, 0), Status: "running"}
			s.cancel["old"] = func() { stopped = true }
			s.cancel["other"] = func() { unrelated = true }
			s.mu.Lock()
			if action == "new" {
				s.restartSessionLocked(own)
			} else {
				s.picks[own] = sessionPick{items: []chatSessionInfo{{Gen: 1, ID: sessionIDFor(own, 1)}}}
				s.switchListedLocked(own, 1)
			}
			s.mu.Unlock()
			if !stopped || unrelated || s.jobs["other"].Status != "running" {
				t.Fatal("session change did not isolate cancellation")
			}
		})
	}
}
