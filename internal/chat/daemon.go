package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-edr/internal/config"
)

type daemonMeta struct {
	PID   int    `json:"pid"`
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

const parentPIDEnv = "DEEPSENTRY_CHAT_PARENT_PID"

// Handoff carries enough information to start an owned --chat child.
type Handoff struct {
	Executable string
	ConfigPath string
	Proxy      string
}

// Owner tracks a chat child started by the TUI / main binary.
type Owner struct {
	cmd *exec.Cmd
}

func daemonMetaPath(cfg config.ChatConfig) string {
	store := cfg.Store
	if store == "" {
		store = "reports/chat/jobs.json"
	}
	return filepath.Join(filepath.Dir(store), "daemon.json")
}

func daemonLogPath(cfg config.ChatConfig) string {
	return filepath.Join(filepath.Dir(daemonMetaPath(cfg)), "daemon.log")
}

func writeDaemonMeta(cfg config.ChatConfig, meta daemonMeta) error {
	path := daemonMetaPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readDaemonMeta(cfg config.ChatConfig) (daemonMeta, error) {
	var meta daemonMeta
	data, err := os.ReadFile(daemonMetaPath(cfg))
	if err != nil {
		return meta, err
	}
	if err = json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	if meta.Addr == "" || meta.Token == "" || meta.PID <= 0 {
		return meta, errors.New("daemon metadata incomplete")
	}
	return meta, nil
}

func clearDaemonMeta(cfg config.ChatConfig) {
	_ = os.Remove(daemonMetaPath(cfg))
}

func isStoreBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "正被其他进程使用")
}

// StartOwnedChat starts --chat as a child of the current process. It does not
// detach into its own session, so closing the parent binary can stop it.
func StartOwnedChat(cfg config.ChatConfig, h Handoff) (*exec.Cmd, error) {
	if strings.TrimSpace(h.Executable) == "" || strings.TrimSpace(h.ConfigPath) == "" {
		return nil, errors.New("缺少可执行文件或配置路径，无法启动聊天服务")
	}
	if meta, err := readDaemonMeta(cfg); err == nil && processAlive(meta.PID) {
		return nil, nil
	}
	args := []string{"--chat", "-c", h.ConfigPath}
	if h.Proxy != "" {
		args = append(args, "--proxy", h.Proxy)
	}
	cmd := exec.Command(h.Executable, args...)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", parentPIDEnv, os.Getpid()))
	logPath := daemonLogPath(cfg)
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return nil, err
	}
	if err := rotateDaemonLog(logPath); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureOwned(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if meta, err := readDaemonMeta(cfg); err == nil && processAlive(meta.PID) {
			return cmd, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = killDaemon(cmd.Process.Pid)
	return nil, errors.New("聊天服务启动超时，请查看 reports/chat/daemon.log")
}

func (o *Owner) Ensure(cfg config.ChatConfig, h Handoff) error {
	if o == nil {
		return errors.New("chat owner is nil")
	}
	if meta, err := readDaemonMeta(cfg); err == nil && processAlive(meta.PID) {
		return nil
	}
	cmd, err := StartOwnedChat(cfg, h)
	if err != nil {
		return err
	}
	o.cmd = cmd
	return nil
}

func (o *Owner) Stop(cfg config.ChatConfig) error {
	err := StopDetachedChat(cfg)
	if o != nil && o.cmd != nil && o.cmd.Process != nil {
		_ = killDaemon(o.cmd.Process.Pid)
		o.cmd = nil
	}
	if err != nil && (strings.Contains(err.Error(), "未发现") || strings.Contains(err.Error(), "未运行")) {
		return nil
	}
	return err
}

func watchParent(stop context.CancelFunc) {
	raw := strings.TrimSpace(os.Getenv(parentPIDEnv))
	if raw == "" || stop == nil {
		return
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return
	}
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for range tick.C {
			if !processAlive(pid) {
				stop()
				return
			}
		}
	}()
}

func notifyDaemonReload(cfg config.ChatConfig) error {
	return notifyDaemon(cfg, "POST", "/control/reload")
}

func notifyDaemon(cfg config.ChatConfig, method, path string) error {
	meta, err := readDaemonMeta(cfg)
	if err != nil {
		return err
	}
	if !processAlive(meta.PID) {
		clearDaemonMeta(cfg)
		return errors.New("后台聊天服务未运行")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(method, "http://"+meta.Addr+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+meta.Token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("daemon control %s", resp.Status)
	}
	return nil
}

func daemonStatus(cfg config.ChatConfig) (map[string]string, error) {
	meta, err := readDaemonMeta(cfg)
	if err != nil {
		return nil, err
	}
	if !processAlive(meta.PID) {
		clearDaemonMeta(cfg)
		return nil, errors.New("后台聊天服务未运行")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "http://"+meta.Addr+"/control/status", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+meta.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("daemon status %s", resp.Status)
	}
	var body struct {
		States map[string]string `json:"states"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.States == nil {
		body.States = map[string]string{}
	}
	return body.States, nil
}

// StopDetachedChat asks the background chat daemon to exit.
func StopDetachedChat(cfg config.ChatConfig) error {
	meta, err := readDaemonMeta(cfg)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("未发现后台聊天服务")
		}
		return err
	}
	if !processAlive(meta.PID) {
		clearDaemonMeta(cfg)
		return errors.New("后台聊天服务未运行")
	}
	if err := notifyDaemon(cfg, "POST", "/control/shutdown"); err != nil {
		// Fallback to signal if control endpoint is unavailable.
		if killErr := killDaemon(meta.PID); killErr != nil {
			return fmt.Errorf("停止后台聊天失败: %v / %v", err, killErr)
		}
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(meta.PID) {
			clearDaemonMeta(cfg)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = killDaemon(meta.PID)
	clearDaemonMeta(cfg)
	return nil
}
