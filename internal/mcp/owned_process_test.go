package mcp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestClassifyHawkEyeListenerReapsOrphansOnly(t *testing.T) {
	alive := func(pid int) bool { return pid == 42 || pid == 7 }
	if got := classifyHawkEyeListener(88, 1, "node hawkeye-mcp-server.mjs --port 19016", &ownedMCPMeta{PID: 88, ParentPID: 99}, alive); got != "reap" {
		t.Fatalf("dead DeepSentry parent should reap, got %s", got)
	}
	if got := classifyHawkEyeListener(88, 42, "node hawkeye-mcp-server.mjs --port 19016", &ownedMCPMeta{PID: 88, ParentPID: 42}, alive); got != "reuse" {
		t.Fatalf("live DeepSentry/other parent should reuse, got %s", got)
	}
	if got := classifyHawkEyeListener(77, 1, "node /tmp/hawkeye-mcp-server.mjs", nil, alive); got != "reuse" {
		t.Fatalf("daemon/foreign hawkeye should reuse via HTTP, got %s", got)
	}
	if got := classifyHawkEyeListener(77, 42, "node /tmp/hawkeye-mcp-server.mjs", nil, alive); got != "reuse" {
		t.Fatalf("live foreign hawkeye should reuse, got %s", got)
	}
	for _, process := range []struct {
		ppid    int
		command string
	}{{1, "node unrelated-service.mjs"}, {42, "node hawkeye-mcp-server.mjs"}, {0, ""}} {
		if got := classifyHawkEyeListener(88, process.ppid, process.command, &ownedMCPMeta{PID: 88, ParentPID: 99}, alive); got != "reuse" {
			t.Fatalf("stale metadata authorized reaping %+v: %s", process, got)
		}
	}
	if got := classifyHawkEyeListener(0, 0, "", nil, alive); got != "spawn" {
		t.Fatalf("no listener should spawn, got %s", got)
	}
}

func TestIsHawkEyeCommandLine(t *testing.T) {
	if !isHawkEyeCommandLine("node /Users/me/hawkeye-mcp-server.mjs --port 19016") {
		t.Fatal("expected hawkeye script to match")
	}
	if isHawkEyeCommandLine("node other-mcp.mjs") {
		t.Fatal("generic node MCP should not match")
	}
}

func TestNewOwnedStdioCommandInjectsParentPID(t *testing.T) {
	cmd := newOwnedStdioCommand(ServerConfig{Command: "node", Args: []string{"hawkeye-mcp-server.mjs"}})
	found := false
	want := ownedParentPIDEnv + "=" + strconv.Itoa(os.Getpid())
	for _, item := range cmd.Env {
		if item == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("owned stdio command missing %s", want)
	}
}

func TestKillOwnedCmdStopsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sleep")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	killOwnedCmd(cmd)
	_ = cmd.Wait()
	if processAlive(pid) {
		t.Fatalf("owned child %d still alive", pid)
	}
}

func TestOwnedMCPMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSENTRY_MCP_OWNED_DIR", dir)
	cfg := ServerConfig{Name: "hx0-hawkeye", Args: []string{"--port", "19016"}}
	want := ownedMCPMeta{PID: 1234, ParentPID: 56, Port: 19016, Name: "hx0-hawkeye"}
	if err := writeOwnedMCPMeta(cfg, want); err != nil {
		t.Fatal(err)
	}
	got, err := readOwnedMCPMeta(cfg)
	if err != nil || got.PID != want.PID || got.ParentPID != want.ParentPID {
		t.Fatalf("meta=%#v err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hx0-hawkeye-19016.json")); err != nil {
		t.Fatal(err)
	}
	clearOwnedMCPMeta(cfg)
	if _, err := readOwnedMCPMeta(cfg); err == nil {
		t.Fatal("cleared meta should be gone")
	}
}

func TestShouldReuseHawkEyeHTTPReuses405EvenWithStaleMeta(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSENTRY_MCP_OWNED_DIR", dir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	cfg := ServerConfig{Name: "hx0-hawkeye", Command: "node", Args: []string{"hawkeye-mcp-server.mjs", "--port", u.Port()}}
	if err := writeOwnedMCPMeta(cfg, ownedMCPMeta{PID: 1, ParentPID: 1, Port: port, Name: "hx0-hawkeye"}); err != nil {
		t.Fatal(err)
	}
	got, ok := shouldReuseHawkEyeHTTP(cfg)
	if !ok || got.Type != "streamable_http" || got.URL != server.URL+"/mcp" {
		t.Fatalf("occupied HawkEye must be reused, not reaped: ok=%v cfg=%#v", ok, got)
	}
}

func TestShouldReuseHawkEyeHTTPLeavesOrphanToBeSpawned(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSENTRY_MCP_OWNED_DIR", dir)
	cfg := ServerConfig{Name: "hx0-hawkeye", Command: "node", Args: []string{"hawkeye-mcp-server.mjs", "--port", "19992"}}
	if err := writeOwnedMCPMeta(cfg, ownedMCPMeta{PID: 1, ParentPID: 1, Port: 19992, Name: "hx0-hawkeye"}); err != nil {
		t.Fatal(err)
	}
	// Port is free, so reuse is false regardless of stale meta.
	got, ok := shouldReuseHawkEyeHTTP(cfg)
	if ok || got.Type == "streamable_http" {
		t.Fatalf("free port must not reuse HTTP: ok=%v cfg=%#v", ok, got)
	}
}

func TestHawkEyeServerExitsWhenOwnedByDeepSentry(t *testing.T) {
	script, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", "hawkeye-mcp-server.mjs")))
	if err != nil {
		t.Skip(err)
	}
	text := string(script)
	for _, fragment := range []string{"DEEPSENTRY_MCP_PARENT_PID", "ownedByDeepSentry", "owned MCP server exiting with DeepSentry"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("vendored HawkEye server missing owned lifecycle %q", fragment)
		}
	}
}
