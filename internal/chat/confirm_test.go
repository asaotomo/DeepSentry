package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatConfirmPromptShowsCommandAndReason(t *testing.T) {
	got := FormatConfirmPrompt(ConfirmRequest{
		Command:    "rm -rf /tmp/example",
		Reason:     "递归删除",
		ScopeLabel: "后续在同一目标执行完全相同的命令",
	})
	if strings.HasPrefix(got, "需要确认高危操作") {
		t.Fatalf("prompt still asks whether to intercept:\n%s", got)
	}
	for _, want := range []string{
		"已拦截，尚未执行。",
		"要执行：",
		"rm -rf /tmp/example",
		"拦截理由：",
		"递归删除",
		"请原样回复其中一项：允许本次 / 本会话同类 / 本次会话允许所有高危操作 / 拒绝",
		"10 分钟内未回复按拒绝处理",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

func TestFormatConfirmPromptFallsBackToSummary(t *testing.T) {
	got := FormatConfirmPrompt(ConfirmRequest{Summary: "tool evaluate", Reason: "任意 JS"})
	if !strings.Contains(got, "tool evaluate") || !strings.Contains(got, "任意 JS") {
		t.Fatalf("prompt=%s", got)
	}
}

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
		"好":            "",
		"是":            "",
		"ok":           "",
		"y":            "",
		"本会话允许":        "",
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
	if reply, err := s.command(c, testMessage("t-chat", "好")); err != nil || !strings.Contains(reply, "还有一项操作在等你确认") {
		t.Fatalf("casual reply while pending=%q err=%v", reply, err)
	}
	if _, ok := readConfirmReply(dir); ok {
		t.Fatal("casual reply approved a pending operation")
	}
	reply, err := s.command(c, testMessage("t2", "允许本次"))
	if err != nil || reply != "已允许本次，任务继续。" {
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

func TestExplicitConfirmWithoutPendingDoesNotStartTask(t *testing.T) {
	s := testService(t, nil)
	out, err := s.command(testChannel(), testMessage("stray", "允许本次"))
	if err != nil || !strings.Contains(out, "当前没有等你确认的操作") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) != 0 {
		t.Fatalf("stray approval started %d jobs", len(s.jobs))
	}
}

func TestPendingInputAcceptsTextWithAttachment(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	m := testMessage("code", "4821")
	m.Attachments = []Attachment{{Kind: "image", URL: "https://cdn.example/a"}}
	dir := t.TempDir()
	s.jobs["job"] = &Job{ID: "job", Owner: owner(c, m), Status: "running"}
	s.confirms["job"] = dir
	if err := WriteConfirmRequest(dir, ConfirmRequest{Kind: "input", Seq: 1, Summary: "验证码"}); err != nil {
		t.Fatal(err)
	}
	out, err := s.command(c, m)
	if err != nil || !strings.Contains(out, "已收到文字补充") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	reply, ok := readConfirmReply(dir)
	if !ok || reply.Text != "4821" {
		t.Fatalf("reply=%+v ok=%v", reply, ok)
	}
	if len(s.jobs) != 1 {
		t.Fatalf("input reply started a new job: %d", len(s.jobs))
	}
}
