package chat

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestChatSessionApprovalSurvivesNewAgentProcess(t *testing.T) {
	for _, choice := range []string{"本会话同类", "本次会话允许所有高危操作"} {
		t.Run(choice, func(t *testing.T) {
			ready := make(chan string, 4)
			s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
				scope := "scope-a"
				if choice != "本会话同类" && prompt == "second" {
					scope = "scope-b"
				}
				dir := ConfirmDirFromContext(ctx)
				if err := WriteConfirmRequest(dir, ConfirmRequest{ScopeKey: scope, ScopeLabel: "test operation"}); err != nil {
					return "", err
				}
				ready <- prompt
				reply, ok := WaitConfirmReply(dir, ctx.Done(), 3*time.Second)
				if !ok || reply.Decision == "deny" {
					return "", errors.New("denied")
				}
				return "done", nil
			})
			c := testChannel()
			submit := func(id, text string) {
				t.Helper()
				if _, err := s.command(c, testMessage(id, text)); err != nil {
					t.Fatal(err)
				}
			}
			await := func() {
				t.Helper()
				select {
				case <-ready:
				case <-time.After(time.Second):
					t.Fatal("no confirmation request")
				}
			}
			submit("1", "first")
			await()
			submit("approve", choice)
			waitJob(t, s, "first", "completed")
			submit("2", "second")
			await()
			waitJob(t, s, "second", "completed") // separate invocation has no local approval map
			s.mu.Lock()
			own := owner(c, testMessage("", ""))
			if s.hasApprovalLocked(own+"other", "scope-a") {
				t.Error("approval leaked to another sender")
			}
			if choice == "本会话同类" && s.hasApprovalLocked(own, "scope-b") {
				t.Error("scope approval broadened")
			}
			s.mu.Unlock()
			submit("reset", "/new")
			s.mu.Lock()
			granted := s.hasApprovalLocked(own, "scope-a")
			s.mu.Unlock()
			if granted {
				t.Fatal("reset retained approval")
			}
			submit("3", "after reset")
			await()
			submit("deny", "拒绝")
			waitJob(t, s, "after reset", "failed")
		})
	}
}

func TestApprovalOnlyGrantedForPendingRequest(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	m := testMessage("1", "本次会话允许所有高危操作")
	_, _ = s.command(c, m)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasApprovalLocked(owner(c, m), "anything") {
		t.Fatal("grant without active confirmation")
	}
}

func TestSessionApprovalRevokedOnSwitchAndDisconnect(t *testing.T) {
	for _, action := range []string{"switch", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			s := testService(t, nil)
			c := testChannel()
			own := owner(c, testMessage("", ""))
			s.mu.Lock()
			s.rememberApprovalLocked(own, "scope", "allow_all_session")
			s.jobs["old"] = &Job{ID: "old", Owner: own, Status: "completed", SessionID: sessionIDFor(own, 0)}
			if action == "switch" {
				s.picks[own] = sessionPick{items: []chatSessionInfo{{Gen: 1, ID: sessionIDFor(own, 1)}}}
				s.switchListedLocked(own, 1)
			}
			s.mu.Unlock()
			if action == "disconnect" {
				s.mu.Lock()
				delete(s.jobs, "old")
				s.mu.Unlock() // job history may already have been pruned
				s.Disconnect(c.Name)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.hasApprovalLocked(own, "scope") {
				t.Fatal("grant survived", action)
			}
		})
	}
}
