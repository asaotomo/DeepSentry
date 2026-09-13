package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const ownedParentPIDEnv = "DEEPSENTRY_MCP_PARENT_PID"

type ownedMCPMeta struct {
	PID       int    `json:"pid"`
	ParentPID int    `json:"parent_pid"`
	Port      int    `json:"port"`
	Name      string `json:"name"`
}

func newOwnedStdioCommand(cfg ServerConfig) *exec.Cmd {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	env := mcpProcessEnvironment(os.Environ(), cfg.Env)
	env = append(env, fmt.Sprintf("%s=%d", ownedParentPIDEnv, os.Getpid()))
	cmd.Env = env
	if strings.TrimSpace(cfg.CWD) != "" {
		cmd.Dir = cfg.CWD
	}
	return cmd
}

func shouldReuseHawkEyeHTTP(cfg ServerConfig) (ServerConfig, bool) {
	reused, ok := preferExistingHawkEyeHTTP(cfg)
	if ok {
		return reused, true
	}
	if isOrphanHawkEye(cfg) {
		reapOrphanHawkEye(cfg)
	}
	return cfg, false
}

func isOrphanHawkEye(cfg ServerConfig) bool {
	port := hawkEyePortFromConfig(cfg)
	listener := listenerPID(port)
	command := processCommandLine(listener)
	meta, _ := readOwnedMCPMeta(cfg)
	return classifyHawkEyeListener(listener, processPPID(listener), command, meta, processAlive) == "reap"
}

func classifyHawkEyeListener(listenerPID, listenerPPID int, listenerCmd string, meta *ownedMCPMeta, alive func(int) bool) string {
	if listenerPID <= 0 {
		return "spawn"
	}
	if meta != nil && meta.PID == listenerPID && meta.ParentPID > 0 &&
		isHawkEyeCommandLine(listenerCmd) && (listenerPPID == 1 || listenerPPID == meta.ParentPID) && !alive(meta.ParentPID) {
		return "reap"
	}
	return "reuse"
}

func isHawkEyeCommandLine(command string) bool {
	blob := strings.ToLower(command)
	return strings.Contains(blob, "hawkeye-mcp-server") || strings.Contains(blob, "hx0-hawkeye") || strings.Contains(blob, "hawkeye-mcp")
}

func reapOrphanHawkEye(cfg ServerConfig) {
	// Revalidate live identity at the destructive boundary. A stale PID file
	// must never authorize killing an unrelated process that reused the PID.
	meta, err := readOwnedMCPMeta(cfg)
	if err != nil {
		return
	}
	pid := listenerPID(hawkEyePortFromConfig(cfg))
	if classifyHawkEyeListener(pid, processPPID(pid), processCommandLine(pid), meta, processAlive) != "reap" {
		return
	}
	killOwnedPID(pid)
	waitPIDGone(pid, 800*time.Millisecond)
	clearOwnedMCPMeta(cfg)
}

func rememberOwnedProcess(cfg ServerConfig, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || !isHawkEyeStdioConfig(cfg) {
		return
	}
	_ = writeOwnedMCPMeta(cfg, ownedMCPMeta{
		PID:       cmd.Process.Pid,
		ParentPID: os.Getpid(),
		Port:      hawkEyePortFromConfig(cfg),
		Name:      strings.TrimSpace(cfg.Name),
	})
}

func forgetOwnedProcess(cfg ServerConfig, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	killOwnedCmd(cmd)
	if meta, err := readOwnedMCPMeta(cfg); err == nil && meta.PID == pid {
		clearOwnedMCPMeta(cfg)
	}
}

func killOwnedCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Keep the os.Process identity/Wait state; do not turn a reaped child back
	// into a raw PID signal that could target a newly reused process ID.
	_ = cmd.Process.Kill()
}

func waitPIDGone(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func ownedMCPMetaPath(cfg ServerConfig) string {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = "hawkeye"
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	return filepath.Join(ownedMCPMetaDir(), fmt.Sprintf("%s-%d.json", safe, hawkEyePortFromConfig(cfg)))
}

func ownedMCPMetaDir() string {
	if dir := strings.TrimSpace(os.Getenv("DEEPSENTRY_MCP_OWNED_DIR")); dir != "" {
		return dir
	}
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		cwd = "."
	}
	return filepath.Join(cwd, "reports", "mcp")
}

func writeOwnedMCPMeta(cfg ServerConfig, meta ownedMCPMeta) error {
	path := ownedMCPMetaPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".owned-mcp-*")
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

func readOwnedMCPMeta(cfg ServerConfig) (*ownedMCPMeta, error) {
	data, err := os.ReadFile(ownedMCPMetaPath(cfg))
	if err != nil {
		return nil, err
	}
	var meta ownedMCPMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.PID <= 0 {
		return nil, fmt.Errorf("owned MCP metadata incomplete")
	}
	return &meta, nil
}

func clearOwnedMCPMeta(cfg ServerConfig) {
	_ = os.Remove(ownedMCPMetaPath(cfg))
}
