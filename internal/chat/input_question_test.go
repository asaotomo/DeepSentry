package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCaptchaReplyResumesSameTaskWithoutQueueing(t *testing.T) {
	ready := make(chan struct{})
	var calls atomic.Int32
	answer := make(chan string, 1)
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		calls.Add(1)
		dir := ConfirmDirFromContext(ctx)
		if err := WriteConfirmRequest(dir, ConfirmRequest{Kind: "input", Summary: "请输入当前设备验证码"}); err != nil {
			return "", err
		}
		close(ready)
		reply, ok := WaitConfirmReply(dir, ctx.Done(), 3*time.Second)
		if !ok {
			return "", errors.New("input timeout")
		}
		answer <- reply.Text
		return "继续同一浏览器巡检", nil
	})
	c := testChannel()
	s.mu.Lock()
	s.rememberApprovalLocked(owner(c, testMessage("", "")), "", "allow_all_session")
	s.mu.Unlock()
	_, err := s.command(c, testMessage("task", "巡检设备"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("question not created")
	}
	// A blanket risk approval must never auto-answer a captcha/question.
	select {
	case <-answer:
		t.Fatal("risk grant answered user question")
	case <-time.After(300 * time.Millisecond):
	}
	out, err := s.command(c, testMessage("code", "839201"))
	if err != nil || out == "" {
		t.Fatalf("%s %v", out, err)
	}
	select {
	case got := <-answer:
		if got != "839201" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("captcha not delivered to original task")
	}
	waitJob(t, s, "巡检设备", "completed")
	if calls.Load() != 1 {
		t.Fatal("captcha started another agent/browser")
	}
	if jobByPrompt(t, s, "839201") != nil {
		t.Fatal("captcha queued as a task")
	}
	// Redelivery cannot become the answer to another step.
	out, err = s.command(c, testMessage("code", "839201"))
	if err != nil || out != "" {
		t.Fatal("duplicate not suppressed")
	}
}
func TestInputQuestionCannotGrantRiskApproval(t *testing.T) {
	dir := t.TempDir()
	if err := WriteConfirmRequest(dir, ConfirmRequest{Kind: "input", Seq: 1, Summary: "code"}); err != nil {
		t.Fatal(err)
	}
	if WriteConfirmReply(dir, "allow_all_session") == nil {
		t.Fatal("question accepted risk approval")
	}
	if err := WriteInputReply(dir, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfirmRequest(dir, ConfirmRequest{Kind: "input", Seq: 2}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readConfirmReply(dir); ok {
		t.Fatal("old code reused for new question")
	}
}

func TestQuestionFlushesScreenshotBeforeTaskCompletes(t *testing.T) {
	ready := make(chan struct{})
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		dir := ConfirmDirFromContext(ctx)
		send := filepath.Join(dir, "send")
		if err := os.MkdirAll(send, 0700); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(send, "captcha.png"), pngFixture(t), 0600); err != nil {
			return "", err
		}
		if err := WriteConfirmRequest(dir, ConfirmRequest{Kind: "input", Summary: "看图输入验证码"}); err != nil {
			return "", err
		}
		close(ready)
		<-ctx.Done()
		return "", ctx.Err()
	})
	c := testChannel()
	_, err := s.command(c, testMessage("image-task", "巡检"))
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	deadline := time.After(2 * time.Second)
	for {
		s.box.mu.Lock()
		var fileAt, questionAt time.Time
		for _, item := range s.box.items {
			if item.File != nil {
				fileAt = item.Created
			}
			if item.ConfirmSeq != 0 {
				questionAt = item.Created
			}
		}
		s.box.mu.Unlock()
		if !fileAt.IsZero() && !questionAt.IsZero() {
			if fileAt.After(questionAt) {
				t.Fatal("question queued before screenshot")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("screenshot waiting for job completion")
		case <-time.After(10 * time.Millisecond):
		}
	}
	job := jobByPrompt(t, s, "巡检")
	if job == nil || job.Status != "running" {
		t.Fatal("task should still be waiting")
	}
}
