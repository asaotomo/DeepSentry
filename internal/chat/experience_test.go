package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestStopCancelsActiveAndQueuedWithoutAffectingOtherOwner(t *testing.T) {
	var calls atomic.Int32
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		calls.Add(1)
		<-ctx.Done()
		return "", ctx.Err()
	})
	c := testChannel()
	submitAs(t, s, c, "alice", "private", "1", "first")
	submitAs(t, s, c, "alice", "private", "2", "queued")
	submitAs(t, s, c, "bob", "other", "3", "other")
	waitJob(t, s, "first", "running")
	waitJob(t, s, "queued", "queued")
	out, err := s.command(c, testMessage("stop", "/stop"))
	if err != nil || !strings.Contains(out, "2 个任务") {
		t.Fatalf("%q %v", out, err)
	}
	waitJob(t, s, "first", "cancelled")
	waitJob(t, s, "queued", "cancelled")
	if j := jobByPrompt(t, s, "other"); j.Status != "running" {
		t.Fatalf("other owner affected: %+v", j)
	}
	if calls.Load() > 2 {
		t.Fatal("queued turn executed after stop")
	}
}

func TestAliasesAndLatestResult(t *testing.T) {
	s := testService(t, func(context.Context, string, string) (string, error) { return "这是回答", nil })
	c := testChannel()
	if out, err := s.command(c, testMessage("new", "/new")); err != nil || !strings.Contains(out, "新会话") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := s.command(c, testMessage("task", "你好")); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, s, "你好", "completed")
	if !strings.HasSuffix(j.SessionID, "_g1") {
		t.Fatal(j.SessionID)
	}
	if out, err := s.command(c, testMessage("result", "/result")); err != nil || !strings.Contains(out, "这是回答") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := s.command(c, testMessage("sessions", "/sessions")); err != nil || !strings.Contains(out, "1.") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestSessionSaveFailureDoesNotChangeSelection(t *testing.T) {
	s := testService(t, nil)
	own := owner(testChannel(), testMessage("m", ""))
	path := sessionGensPath(s.cfg)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	} // rename over directory must fail, including as root.
	if err := os.WriteFile(filepath.Join(path, "block"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if out := s.restartSessionLocked(own); !strings.Contains(out, "失败") {
		t.Fatal(out)
	}
	if s.sessions[own] != 0 {
		t.Fatal("failed reset changed memory")
	}
	s.picks[own] = sessionPick{items: []chatSessionInfo{{Gen: 3, Goal: "old"}}, at: time.Now()}
	if out := s.switchListedLocked(own, 1); !strings.Contains(out, "失败") {
		t.Fatal(out)
	}
	if s.sessions[own] != 0 {
		t.Fatal("failed switch changed memory")
	}
	if _, ok := s.picks[own]; !ok {
		t.Fatal("failed switch lost selection for retry")
	}
}

func TestMobileReplyPreservesMeaningLinksAndSessionNumbers(t *testing.T) {
	raw := "结果显示正常。\n日志保存在 /tmp/report。\n工具暂不可用。\n详情：请检查权限。\n任务完成后记得重启。\n支持模式切换。\n1. 当前会话\n2. 历史会话\n[文档](https://example.com/docs)"
	got := formatMobileText(raw)
	for _, want := range []string{"结果显示正常。", "日志保存在 /tmp/report。", "工具暂不可用。", "详情：请检查权限。", "任务完成后记得重启。", "支持模式切换。", "1. 当前会话", "2. 历史会话", "https://example.com/docs"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q: %q", want, got)
		}
	}
}

func TestWecomChineseReplyIsSplitWithoutTruncation(t *testing.T) {
	s := testService(t, nil)
	var chunks []string
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "gettoken") {
			return jsonResponse(map[string]any{"errcode": 0, "access_token": "token"}), nil
		}
		var body struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		chunks = append(chunks, body.Text.Content)
		return jsonResponse(map[string]int{"errcode": 0}), nil
	})}
	c := testChannel()
	c.Platform = "wecom"
	text := strings.Repeat("中", 800)
	if err := s.send(delivery{channel: c, message: testMessage("m", ""), text: text}); err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 || strings.Join(chunks, "") != text {
		t.Fatalf("reply truncated: chunks=%d bytes=%d", len(chunks), len(strings.Join(chunks, "")))
	}
	for _, chunk := range chunks {
		if len(chunk) > 1800 || !utf8.ValidString(chunk) {
			t.Fatal("invalid platform chunk")
		}
	}
}

func TestShutdownDoesNotLaunchQueuedTurns(t *testing.T) {
	s := testService(t, func(context.Context, string, string) (string, error) {
		t.Error("launched after shutdown")
		return "", nil
	})
	s.stop()
	s.mu.Lock()
	s.jobs["queued"] = &Job{ID: "queued", Owner: "owner", Status: "queued"}
	s.pending["queued"] = pendingRun{}
	s.pumpQueueLocked()
	if s.jobs["queued"].Status != "queued" {
		t.Error("queue drained after shutdown")
	}
	s.mu.Unlock()
}
