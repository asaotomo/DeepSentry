package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseConfirmText(t *testing.T) {
	cases := map[string]string{
		"允许":           "allow_once",
		"允许本次":         "allow_once",
		"/ds 拒绝":       "deny",
		"本会话同类":        "allow_session",
		"本次会话允许所有高危操作": "allow_all_session",
		"本会话全部":        "allow_all_session",
		"yes":          "allow_once",
		"随便聊聊":         "",
	}
	for in, want := range cases {
		got, ok := ParseConfirmText(in)
		if want == "" {
			if ok {
				t.Fatalf("%q should not parse", in)
			}
			continue
		}
		if !ok || got != want {
			t.Fatalf("%q => %q ok=%v want %q", in, got, ok, want)
		}
	}
}

func TestWaitConfirmReplyReadsDecision(t *testing.T) {
	dir := t.TempDir()
	if err := WriteConfirmRequest(dir, ConfirmRequest{Summary: "tool evaluate", Reason: "任意 JS"}); err != nil {
		t.Fatal(err)
	}
	req, err := ReadConfirmRequest(dir)
	if err != nil || req.Summary == "" || req.Seq == 0 {
		t.Fatalf("request=%#v err=%v", req, err)
	}
	done := make(chan ConfirmReply, 1)
	go func() {
		reply, ok := WaitConfirmReply(dir, nil, time.Second)
		if !ok {
			t.Error("expected reply")
			return
		}
		done <- reply
	}()
	time.Sleep(20 * time.Millisecond)
	if err := WriteConfirmReply(dir, "allow_once"); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-done:
		if reply.Decision != "allow_once" {
			t.Fatalf("%#v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestCommandAcceptsConfirmWhileJobRunning(t *testing.T) {
	started := make(chan struct{})
	s := testService(t, func(ctx context.Context, prompt, sessionID string) (string, error) {
		close(started)
		<-ctx.Done()
		return "done", ctx.Err()
	})
	c := testChannel()
	if _, err := s.command(c, testMessage("t1", "查一下状态")); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, "running")
	<-started
	j := onlyJob(t, s)
	dir := filepath.Join(filepath.Dir(s.cfg.Store), "confirm", j.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfirmRequest(dir, ConfirmRequest{Seq: 1, Summary: "test operation"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.confirms[j.ID] = dir
	s.mu.Unlock()
	reply, err := s.command(c, testMessage("t2", "允许本次"))
	if err != nil || reply != "已收到确认，任务继续。" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	got, ok := readConfirmReply(dir)
	if !ok || got.Decision != "allow_once" {
		t.Fatalf("reply file %#v ok=%v", got, ok)
	}
}

func TestConfirmationRequiresPendingRequestAndMatchingSequence(t *testing.T) {
	dir := t.TempDir()
	if err := WriteConfirmReply(dir, "allow_once"); err == nil {
		t.Fatal("accepted reply without pending operation")
	}
	if err := WriteConfirmRequest(dir, ConfirmRequest{Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfirmReply(dir, "allow_once"); err != nil {
		t.Fatal(err)
	}
	if err := writeConfirmJSON(filepath.Join(dir, "request.json"), ConfirmRequest{Seq: 2}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readConfirmReply(dir); ok {
		t.Fatal("stale reply approved another operation")
	}
}

func TestConfirmRedeliveryCannotApproveNextRequest(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	m := testMessage("confirm", "允许本次")
	dir := t.TempDir()
	s.jobs["job"] = &Job{ID: "job", Owner: owner(c, m), Status: "running"}
	s.confirms["job"] = dir
	if err := WriteConfirmRequest(dir, ConfirmRequest{Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if out, err := s.command(c, m); err != nil || out == "" {
		t.Fatalf("%q %v", out, err)
	}
	if err := WriteConfirmRequest(dir, ConfirmRequest{Seq: 2}); err != nil {
		t.Fatal(err)
	}
	if out, err := s.command(c, m); err != nil || out != "" {
		t.Fatalf("replayed: %q %v", out, err)
	}
	if _, ok := readConfirmReply(dir); ok {
		t.Fatal("duplicate approval reached next request")
	}
	m.ID = ""
	if _, err := s.command(c, m); err == nil {
		t.Fatal("confirmation without identity accepted")
	}
}
