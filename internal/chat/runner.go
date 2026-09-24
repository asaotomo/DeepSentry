package chat

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProcessRunner reuses the existing batch Agent, never interpolates a chat message into a shell.
// sessionID, when non-empty, binds the chat thread to a stable DeepSentry checkpoint for multi-turn chat.
func ProcessRunner(executable, configPath, proxy string) Runner {
	var noticesMu sync.Mutex
	notified := map[string]time.Time{}
	return func(ctx context.Context, prompt, sessionID string) (string, error) {
		args := []string{"-c", configPath, "--no-tui", "--quiet", "--no-color", "--reply-final", "--task", prompt}
		if sessionID != "" {
			args = append(args, "--session", sessionID)
		}
		if proxy != "" {
			args = append(args, "--proxy", proxy)
		}
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Env = os.Environ()
		if dir := ConfirmDirFromContext(ctx); dir != "" {
			sendDir := filepath.Join(dir, "send")
			_ = os.MkdirAll(sendDir, 0700)
			cmd.Env = append(cmd.Env, chatConfirmEnv+"="+dir, chatSendDirEnv+"="+sendDir)
		}
		cmd.Stdin = nil
		configureProcess(cmd)
		cmd.WaitDelay = 3 * time.Second
		out := &tailBuffer{limit: 64000}
		cmd.Stdout = out
		diagnostics := &tailBuffer{limit: 64000}
		cmd.Stderr = diagnostics
		watchCtx, stopWatch := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			watchProcessProgress(watchCtx, ConfirmDirFromContext(ctx), ctx.Value(progressKey{}))
		}()
		err := cmd.Run()
		stopWatch()
		<-done

		if dir := ConfirmDirFromContext(ctx); dir != "" && diagnostics.String() != "" {
			_ = os.WriteFile(filepath.Join(dir, "diagnostics.log"), []byte(diagnostics.String()), 0600)
		}
		names, other := splitMCPDiagnostics(diagnostics.String())
		if len(names) > 0 {
			if fn, ok := ctx.Value(noticeKey{}).(func(string)); ok && ctx.Err() == nil {
				noticesMu.Lock()
				for k, t := range notified {
					if time.Since(t) > 24*time.Hour {
						delete(notified, k)
					}
				}
				_, seen := notified[sessionID]
				if !seen || sessionID == "" {
					if len(notified) >= 1000 {
						for k := range notified {
							delete(notified, k)
							break
						}
					}
					notified[sessionID] = time.Now()
				}
				noticesMu.Unlock()
				if !seen || sessionID == "" {
					fn("连接提示：" + strings.Join(names, "、") + " 暂不可用。本会话仅提示一次；不使用这些工具的任务可继续，需要时请检查 MCP 配置。")
				}
			}
		}
		result := out.String()
		if err != nil && strings.TrimSpace(other) != "" {
			result = strings.TrimSpace(result + "\n" + other)
		}
		return result, err
	}
}

type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
	} else {
		b.data = append(b.data, p...)
		if len(b.data) > b.limit {
			b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
		}
	}
	return n, nil
}
func (b *tailBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

func watchProcessProgress(ctx context.Context, dir string, callback any) {
	fn, ok := callback.(func(string))
	if !ok || dir == "" {
		return
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var last int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			raw, err := os.ReadFile(filepath.Join(dir, "progress.json"))
			if err != nil {
				continue
			}
			var p Progress
			if json.Unmarshal(raw, &p) == nil && p.Seq > last {
				last = p.Seq
				fn(p.Text)
			}
		}
	}
}

// Only structured startup notices are separated; genuine execution errors remain.
func splitMCPDiagnostics(raw string) ([]string, string) {
	names := map[string]bool{}
	var other []string
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "DEEPSENTRY_MCP_WARNING:") {
			var notice struct {
				Name  string `json:"name"`
				Error string `json:"error"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "DEEPSENTRY_MCP_WARNING:")), &notice) == nil && notice.Name != "" {
				names[notice.Name] = true
				continue
			}
		}
		other = append(other, line)
	}
	var sorted []string
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	return sorted, strings.Join(other, "\n")
}
