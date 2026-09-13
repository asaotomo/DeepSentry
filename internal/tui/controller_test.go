package tui

import (
	"strings"
	"testing"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/harness"
)

func TestPrepareSessionConfigRejectsNilAgentAndInitializesDefaults(t *testing.T) {
	if err := prepareSessionConfig(&SessionConfig{}); err == nil {
		t.Fatal("nil Agent should return a startup error instead of panicking")
	}
	cfg := SessionConfig{Agent: &harness.DeepAgent{}}
	if err := prepareSessionConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.History == nil || cfg.MaxSteps != 30 {
		t.Fatalf("defaults not initialized: history=%v maxSteps=%d", cfg.History, cfg.MaxSteps)
	}
}

func TestInterruptQueuesHistoryMutationUntilRunStops(t *testing.T) {
	history := []analyzer.Message{{Role: "user", Content: "original"}}
	c := &SessionController{
		cfg:     SessionConfig{History: &history},
		running: true,
		stopCh:  make(chan struct{}),
	}
	if !c.InterruptWithInput("改成只读排查") {
		t.Fatal("interrupt should be accepted while running")
	}
	if len(history) != 1 {
		t.Fatalf("active run history was mutated concurrently: %#v", history)
	}
	if c.pendingInterruptText != "改成只读排查" {
		t.Fatalf("queued interrupt=%q", c.pendingInterruptText)
	}
	select {
	case <-c.stopCh:
	default:
		t.Fatal("interrupt should request current run stop")
	}
}

func TestAllowAllSessionGrantClearedOnNewSession(t *testing.T) {
	c := newSessionController(SessionConfig{Agent: &harness.DeepAgent{}})
	c.allowAllSession = true
	c.sessionApprovals["scope"] = "evaluate"
	c.clearSessionApprovals()
	if c.allowAllSession || len(c.sessionApprovals) != 0 {
		t.Fatal("session-wide approval survived reset")
	}
}

func TestControllerPreservesImageAttachmentsForInitialAndInterruptedTurns(t *testing.T) {
	history := []analyzer.Message{}
	attachment := analyzer.ImageAttachment{Path: "/tmp/evidence.png", Name: "evidence.png", MediaType: "image/png", SHA256: "abc"}
	c := &SessionController{cfg: SessionConfig{History: &history}}
	c.SetInitialGoalWithAttachments("分析截图", []analyzer.ImageAttachment{attachment})
	if len(history) != 1 || len(history[0].Attachments) != 1 || history[0].Attachments[0].Path != attachment.Path {
		t.Fatalf("initial image attachment lost: %#v", history)
	}

	c.running = true
	c.stopCh = make(chan struct{})
	if !c.InterruptWithAttachments("", []analyzer.ImageAttachment{attachment}) {
		t.Fatal("image-only interrupt should be accepted")
	}
	if c.pendingInterruptText == "" || len(c.pendingInterruptImgs) != 1 {
		t.Fatalf("interrupted image attachment not queued: text=%q images=%#v", c.pendingInterruptText, c.pendingInterruptImgs)
	}
}

func TestStatsUsesSafeSnapshotWhileRunning(t *testing.T) {
	history := []analyzer.Message{{Role: "user", Content: "first"}}
	c := &SessionController{cfg: SessionConfig{History: &history}}
	c.mu.Lock()
	c.refreshStatsLocked()
	c.running = true
	c.mu.Unlock()

	// Simulate history being owned by RunLoop. Stats must not inspect it until
	// running becomes false, so the cached message count remains stable.
	history = append(history, analyzer.Message{Role: "assistant", Content: strings.Repeat("x", 200)})
	if got := c.Stats(); got.Messages != 1 || !got.Running {
		t.Fatalf("running stats should use safe snapshot: %#v", got)
	}

	c.mu.Lock()
	c.running = false
	c.mu.Unlock()
	if got := c.Stats(); got.Messages != 2 || got.Running {
		t.Fatalf("idle stats should refresh from history: %#v", got)
	}
}
