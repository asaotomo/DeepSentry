package chat

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T, run Runner) *Service {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := config.ChatConfig{Store: filepath.Join(dir, "jobs.json"), BindingFile: filepath.Join(dir, "connections.json"), AllowBatch: true, Listen: "127.0.0.1:0"}
	if run == nil {
		run = func(context.Context, string, string) (string, error) { return "", nil }
	}
	s, err := New(ctx, cfg, run)
	if err != nil {
		t.Fatal(err)
	}
	s.listSessions = func(string) []chatSessionInfo { return nil }
	t.Cleanup(func() { s.stop(); s.wg.Wait() })
	return s
}
func testChannel() config.ChatChannel {
	return config.ChatChannel{Name: "test", Platform: "wechat", AllowedUsers: []string{"alice", "bob"}}
}
func testMessage(id, text string) Message {
	return Message{ID: id, User: "alice", Chat: "private", Text: text}
}
func onlyJob(t *testing.T, s *Service) Job {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		return *j
	}
	t.Fatal("no job")
	return Job{}
}
func waitStatus(t *testing.T, s *Service, want string) Job {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		j := onlyJob(t, s)
		if j.Status == want {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("status=%s want=%s", onlyJob(t, s).Status, want)
	return Job{}
}
func TestTaskLifecycleAuthorizationDedupAndOwnership(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	s := testService(t, func(ctx context.Context, prompt, sessionID string) (string, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return "stopped", ctx.Err()
	})
	c := testChannel()
	outsider := testMessage("bad", "/ds run hello")
	outsider.User = "mallory"
	if _, err := s.command(c, outsider); err == nil {
		t.Fatal("unauthorized user accepted")
	}
	group := testMessage("group", "/ds run hello")
	group.Group = true
	if _, err := s.command(c, group); err == nil {
		t.Fatal("unlisted group accepted")
	}
	if out, err := s.command(c, testMessage("msg1", "/ds run inspect host")); err != nil || out != "" {
		t.Fatalf("ack should be silent, got %q %v", out, err)
	}
	<-started
	j := onlyJob(t, s)
	if out, err := s.command(c, testMessage("msg1", "/ds run inspect host")); err != nil || out != "" {
		t.Fatalf("duplicate response=%q err=%v", out, err)
	}
	queued, err := s.command(c, testMessage("msg2", "/ds run another"))
	if err != nil || queued != "" {
		t.Fatalf("same-session follow-up should queue silently, got %q %v", queued, err)
	}
	s.mu.Lock()
	var queuedJob *Job
	for _, cand := range s.jobs {
		if cand.Prompt == "another" {
			copyJob := *cand
			queuedJob = &copyJob
		}
	}
	s.mu.Unlock()
	if queuedJob == nil || queuedJob.Status != "queued" {
		t.Fatalf("follow-up not queued: %+v", queuedJob)
	}
	if calls.Load() != 1 {
		t.Fatal("queued follow-up launched early")
	}
	if out, err := s.command(c, testMessage("msg2c", "/ds cancel "+queuedJob.ID)); err != nil || !strings.Contains(out, "已取消") {
		t.Fatalf("cancel queued=%q %v", out, err)
	}
	other := testMessage("msg3", "/ds cancel "+j.ID)
	other.User = "bob"
	out, _ := s.command(c, other)
	if !strings.Contains(out, "未找到") {
		t.Fatal("cross-user cancellation allowed")
	}
	other = testMessage("msg4", "/ds result "+j.ID)
	other.Chat = "another"
	out, _ = s.command(c, other)
	if !strings.Contains(out, "未找到") {
		t.Fatal("cross-chat result leaked")
	}
	_, err = s.command(c, testMessage("msg5", "/ds cancel "+j.ID))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, "cancelled")
	if calls.Load() != 1 {
		t.Fatal("duplicate task launched")
	}
}
func TestPersistedResultRestartAndPagination(t *testing.T) {
	s := testService(t, func(context.Context, string, string) (string, error) {
		return strings.Repeat("中文结果。", 1600), nil
	})
	c := testChannel()
	_, err := s.command(c, testMessage("message", "/ds run hello"))
	if err != nil {
		t.Fatal(err)
	}
	j := waitStatus(t, s, "completed")
	s2, err := New(context.Background(), s.cfg, func(context.Context, string, string) (string, error) { t.Fatal("replayed task"); return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s2.stop()
	out, err := s2.command(c, testMessage("message", "/ds run hello"))
	if err != nil || out != "" {
		t.Fatalf("persistent replay: %q %v", out, err)
	}
	out, err = s2.command(c, testMessage("page", "/ds result "+j.ID+" 2"))
	if err != nil || !strings.Contains(out, "2/") || !strings.Contains(out, "中文结果") || len(out) > 1800 {
		t.Fatalf("page len=%d out=%q err=%v", len(out), clip(out, 100), err)
	}
	info, err := os.Stat(s.cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("world readable store %v", info.Mode())
	}
}
func TestRestartMarksInterruptedAndExecutionOptIn(t *testing.T) {
	s := testService(t, func(context.Context, string, string) (string, error) {
		t.Fatal("execution not opted in")
		return "", nil
	})
	s.cfg.AllowBatch = false
	out, _ := s.command(testChannel(), testMessage("1", "/ds run hi"))
	if !strings.Contains(out, "未授权") {
		t.Fatal(out)
	}
	jobs := []Job{{ID: "old", Status: "running"}, {ID: "queued-old", Status: "queued"}}
	raw, _ := json.Marshal(jobs)
	if err := os.MkdirAll(filepath.Dir(s.cfg.Store), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfg.Store, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s2, err := New(context.Background(), s.cfg, s.run)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.stop()
	if s2.jobs["old"].Status != "interrupted" {
		t.Fatal(s2.jobs["old"])
	}
	if s2.jobs["queued-old"].Status != "interrupted" {
		t.Fatal(s2.jobs["queued-old"])
	}
}
func TestPairingIsPrivateOneTimeAndPersistent(t *testing.T) {
	s := testService(t, func(context.Context, string, string) (string, error) { return "", nil })
	b := binding{Platform: "wecom", AppID: "bot", Secret: "secret", PairCode: "one-time-code"}
	if err := changeBinding(s.cfg, "wecom", &b); err != nil {
		t.Fatal(err)
	}
	c := channelFor("wecom", b)
	c.Generation = 1
	s.generations[c.Name] = 1
	s.connections[c.Name] = func() {}
	m := testMessage("1", "/ds pair "+b.PairCode)
	m.Group = true
	if _, err := s.command(c, m); err == nil {
		t.Fatal("group paired")
	}
	m.Group = false
	out, err := s.command(c, m)
	if err != nil || !strings.Contains(out, "已绑定") || !strings.Contains(out, "DeepSentry") {
		t.Fatalf("pair=%q %v", out, err)
	}
	m.User = "bob"
	if _, err = s.command(c, m); err == nil {
		t.Fatal("pair code reused")
	}
	all, err := loadBindings(s.cfg)
	if err != nil || all["wecom"].User != "alice" || all["wecom"].PairCode != "" {
		t.Fatal("pair was not saved")
	}
	s.Disconnect(c.Name)
	m.User = "alice"
	m.Text = "/ds run hi"
	if _, err = s.command(c, m); err == nil {
		t.Fatal("disconnected callback executed")
	}
}
func TestStoreLockAndGracefulServiceShutdown(t *testing.T) {
	s := testService(t, func(context.Context, string, string) (string, error) { return "", nil })
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	select {
	case <-s.Ready:
	case <-time.After(3 * time.Second):
		t.Fatal("not ready")
	}
	if unlock, err := lockStore(s.cfg.Store); err == nil {
		unlock()
		t.Fatal("second store lock accepted")
	}
	s.stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked")
	}
	unlock, err := lockStore(s.cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestHelpShowsDashboardAndPlainChatRuns(t *testing.T) {
	var mu sync.Mutex
	var gotPrompt, gotSession string
	s := testService(t, func(_ context.Context, prompt, sessionID string) (string, error) {
		mu.Lock()
		gotPrompt, gotSession = prompt, sessionID
		mu.Unlock()
		return "ok", nil
	})
	c := testChannel()
	out, err := s.command(c, testMessage("help", "/ds help"))
	if err != nil || !strings.Contains(out, "DeepSentry") || !strings.Contains(out, "模型") || !strings.Contains(out, "/ds new") || !strings.Contains(out, "切换会话") {
		t.Fatalf("help=%q err=%v", out, err)
	}
	out, err = s.command(c, testMessage("plain", "查看系统运行状态"))
	if err != nil || out != "" {
		t.Fatalf("plain ack should be silent, got %q err=%v", out, err)
	}
	waitStatus(t, s, "completed")
	mu.Lock()
	if gotPrompt != "查看系统运行状态" || !strings.HasPrefix(gotSession, "session_chat_") {
		t.Fatalf("prompt=%q session=%q", gotPrompt, gotSession)
	}
	firstSession := gotSession
	mu.Unlock()

	group := testMessage("group-plain", "查看系统运行状态")
	group.Group = true
	group.Chat = "g1"
	groupChannel := c
	groupChannel.AllowedChats = []string{"g1"}
	out, err = s.command(groupChannel, group)
	if err != nil || out != "" {
		t.Fatalf("group plain should be ignored: %q %v", out, err)
	}

	out, err = s.command(c, testMessage("nat", "/ds 审计监听端口"))
	if err != nil || out != "" {
		t.Fatalf("natural ack should be silent, got %q err=%v", out, err)
	}
	waitJob(t, s, "审计监听端口", "completed")
	mu.Lock()
	if gotPrompt != "审计监听端口" {
		t.Fatalf("natural prompt=%q", gotPrompt)
	}
	mu.Unlock()

	out, err = s.command(c, testMessage("new", "/ds new"))
	if err != nil || !strings.Contains(out, "新会话") {
		t.Fatalf("new=%q err=%v", out, err)
	}
	out, err = s.command(c, testMessage("again", "/ds run 第二次"))
	if err != nil || out != "" {
		t.Fatalf("again ack should be silent, got %q err=%v", out, err)
	}
	waitJob(t, s, "第二次", "completed")
	mu.Lock()
	defer mu.Unlock()
	if gotSession == firstSession || !strings.Contains(gotSession, "_g1") {
		t.Fatalf("session should rotate after /ds new: first=%q next=%q", firstSession, gotSession)
	}
}

func TestFailedEmptyResultHasUserVisibleReason(t *testing.T) {
	s := testService(t, func(context.Context, string, string) (string, error) {
		return "", fmt.Errorf("exit status 2")
	})
	if _, err := s.command(testChannel(), testMessage("fail", "在吗")); err != nil {
		t.Fatal(err)
	}
	j := waitStatus(t, s, "failed")
	if !strings.Contains(j.Result, "/ds new") && !strings.Contains(j.Result, "重启会话") {
		t.Fatalf("empty failure should tell user how to recover, got %q", j.Result)
	}
}

func TestRestartAndSwitchSessionsFromChat(t *testing.T) {
	var mu sync.Mutex
	var gotSession, gotPrompt string
	var calls int
	s := testService(t, func(_ context.Context, prompt, sessionID string) (string, error) {
		mu.Lock()
		calls++
		gotPrompt, gotSession = prompt, sessionID
		mu.Unlock()
		return "ok", nil
	})
	c := testChannel()
	s.listSessions = func(prefix string) []chatSessionInfo {
		now := time.Now()
		return []chatSessionInfo{
			{Gen: 0, ID: prefix + "0", Goal: "检查磁盘", SavedAt: now.Add(-time.Hour)},
			{Gen: 1, ID: prefix + "1", Goal: "你叫什么名字", SavedAt: now.Add(-time.Minute)},
		}
	}

	out, err := s.command(c, testMessage("restart", "帮我重启会话。"))
	if err != nil || !strings.Contains(out, "已开启新会话") {
		t.Fatalf("restart=%q err=%v", out, err)
	}
	mu.Lock()
	if calls != 0 {
		t.Fatalf("restart should not launch a task, calls=%d", calls)
	}
	mu.Unlock()

	out, err = s.command(c, testMessage("switch", "切换会话"))
	if err != nil || !strings.Contains(out, "近期会话") || !strings.Contains(out, "1.") || !strings.Contains(out, "你叫什么名字") {
		t.Fatalf("switch list=%q err=%v", out, err)
	}

	out, err = s.command(c, testMessage("pick", "第2个"))
	if err != nil || !strings.Contains(out, "已切换到第 2 个会话") {
		t.Fatalf("pick=%q err=%v", out, err)
	}

	out, err = s.command(c, testMessage("hi", "在吗"))
	if err != nil || out != "" {
		t.Fatalf("follow-up ack=%q err=%v", out, err)
	}
	waitStatus(t, s, "completed")
	mu.Lock()
	if gotPrompt != "在吗" || !strings.HasSuffix(gotSession, "_g1") {
		t.Fatalf("switched session prompt=%q session=%q", gotPrompt, gotSession)
	}
	switched := gotSession
	mu.Unlock()

	out, err = s.command(c, testMessage("restart2", "重启会话"))
	if err != nil || !strings.Contains(out, "已开启新会话") {
		t.Fatalf("second restart=%q err=%v", out, err)
	}
	if _, err = s.command(c, testMessage("after", "你好")); err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, "你好", "completed")
	mu.Lock()
	defer mu.Unlock()
	if gotSession == switched || !strings.HasSuffix(gotSession, "_g2") {
		t.Fatalf("restart after switch should open a fresh generation: old=%q new=%q", switched, gotSession)
	}
}

func TestSessionPickAbandonsOnNormalChat(t *testing.T) {
	var gotPrompt string
	s := testService(t, func(_ context.Context, prompt, _ string) (string, error) {
		gotPrompt = prompt
		return "ok", nil
	})
	c := testChannel()
	if _, err := s.command(c, testMessage("list", "切换会话")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.command(c, testMessage("plain", "查看系统运行状态")); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, "completed")
	if gotPrompt != "查看系统运行状态" {
		t.Fatalf("abandoned pick should run as chat, got %q", gotPrompt)
	}
}
