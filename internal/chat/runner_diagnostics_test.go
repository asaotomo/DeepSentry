package chat

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunnerSeparatesMCPWarningAndDeduplicatesSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent")
	script := `#!/bin/sh
printf '%s\n' 'DEEPSENTRY_MCP_WARNING:{"name":"hawkeye","error":"node missing"}' >&2
printf '%s\n' '服务器版本 Ubuntu 22.04'
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	notices := []string{}
	ctx := context.WithValue(WithConfirmDir(context.Background(), dir), noticeKey{}, func(s string) { notices = append(notices, s) })
	run := ProcessRunner(exe, "config.yaml", "")
	for _, session := range []string{"one", "one", "two"} {
		result, err := run(ctx, "version", session)
		if err != nil || strings.TrimSpace(result) != "服务器版本 Ubuntu 22.04" {
			t.Fatalf("%q %v", result, err)
		}
	}
	if len(notices) != 2 || !strings.Contains(notices[0], "hawkeye") {
		t.Fatal(notices)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "diagnostics.log"))
	if err != nil || !strings.Contains(string(raw), "node missing") {
		t.Fatal("missing diagnostics", err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s' 'required MCP failed' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := run(ctx, "test", "one")
	if err == nil || !strings.Contains(result, "required MCP failed") {
		t.Fatal("execution error hidden", result, err)
	}
}

func TestConnectionNoticeSurvivesFastTaskCompletion(t *testing.T) {
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		ctx.Value(noticeKey{}).(func(string))("连接提示：鹰眼不可用")
		return "正常答案", nil
	})
	c := testChannel()
	s.mu.Lock()
	s.channels[c.Name] = c
	s.mu.Unlock()
	if _, err := s.command(c, testMessage("notice", "查版本")); err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, "查版本", "completed")
	s.box.mu.Lock()
	var notice *OutboxItem
	for _, item := range s.box.items {
		if len(item.Chunks) > 0 && strings.Contains(item.Chunks[0], "连接提示") {
			copy := *item
			notice = &copy
		}
	}
	s.box.mu.Unlock()
	if notice == nil || notice.Ephemeral {
		t.Fatal("notice discarded with progress")
	}
	if _, ok := s.deliveryChannel(notice); !ok {
		t.Fatal("notice invalid after completion")
	}
}
