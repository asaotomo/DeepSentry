package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConnectStdioReusesServerAndSerializesConcurrentCalls(t *testing.T) {
	CloseAll()
	t.Cleanup(CloseAll)
	starts := t.TempDir() + "/starts.log"
	cfg := ServerConfig{
		Name:    "test-reusable-server",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]string{
			"GO_WANT_MCP_HELPER": "1",
			"MCP_STARTS_FILE":    starts,
		},
	}
	if err := ConnectStdio(cfg); err != nil {
		t.Fatal(err)
	}
	if err := ConnectStdio(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), "start\n"); got != 1 {
		t.Fatalf("same MCP config started %d child processes, want 1", got)
	}

	const calls = 24
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("value-%d", i)
			got, err := Global().Run("echo_test", map[string]string{"value": want})
			if err != nil {
				errs <- err
				return
			}
			if got != want {
				errs <- fmt.Errorf("call %d got %q want %q", i, got, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestReadJSONRPCLineTimeout(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	start := time.Now()
	_, err := readJSONRPCLineWithTimeout(bufio.NewReader(r), 20*time.Millisecond)
	_ = w.Close()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("blocked MCP handshake should time out promptly, err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestValidateAndCoerceMCPArgs(t *testing.T) {
	schema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []interface{}{"count", "enabled"},
		"properties": map[string]interface{}{
			"count":   map[string]interface{}{"type": "integer"},
			"enabled": map[string]interface{}{"type": "boolean"},
			"tags":    map[string]interface{}{"type": "array"},
			"mode":    map[string]interface{}{"type": "string", "enum": []interface{}{"safe", "fast"}},
		},
	}
	got, err := validateAndCoerceMCPArgs(schema, map[string]string{
		"count": "3", "enabled": "true", "tags": `["prod","web"]`, "mode": "safe",
	})
	if err != nil {
		t.Fatalf("valid MCP args rejected: %v", err)
	}
	if got["count"] != int64(3) || got["enabled"] != true {
		t.Fatalf("MCP scalar types not coerced: %#v", got)
	}
	if tags, ok := got["tags"].([]interface{}); !ok || len(tags) != 2 {
		t.Fatalf("MCP array not coerced: %#v", got["tags"])
	}
	if _, err := validateAndCoerceMCPArgs(schema, map[string]string{"count": "x", "enabled": "true"}); err == nil {
		t.Fatal("invalid integer should be rejected")
	}
	if _, err := validateAndCoerceMCPArgs(schema, map[string]string{"count": "1"}); err == nil {
		t.Fatal("missing required field should be rejected")
	}
	if _, err := validateAndCoerceMCPArgs(schema, map[string]string{"count": "1", "enabled": "true", "extra": "x"}); err == nil {
		t.Fatal("unknown field should be rejected when additionalProperties=false")
	}
	got, err = validateAndCoerceMCPArgs(schema, map[string]string{
		"count": "2", "enabled": "false", "thought": "检查标签页", "final_report": "nope",
	})
	if err != nil {
		t.Fatalf("harness envelope keys should be ignored: %v", err)
	}
	if _, ok := got["thought"]; ok || got["count"] != int64(2) {
		t.Fatalf("envelope key leaked into MCP args: %#v", got)
	}
}

func TestMCPProcessEnvironmentDoesNotImplicitlyInheritCredentials(t *testing.T) {
	got := mcpProcessEnvironment([]string{
		"PATH=/usr/bin", "HOME=/tmp/home", "LANG=zh_CN.UTF-8",
		"DEEPSENTRY_API_KEY=main-secret", "AWS_SECRET_ACCESS_KEY=cloud-secret",
		"SSH_PASSWORD=ssh-secret", "HTTP_PROXY=http://user:pass@proxy",
	}, map[string]string{
		"MCP_API_KEY": "explicit-secret",
		"PATH":        "/custom/bin",
	})
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{"main-secret", "cloud-secret", "ssh-secret", "user:pass@proxy"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("MCP inherited credential %q: %s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "MCP_API_KEY=explicit-secret") || !strings.Contains(joined, "PATH=/custom/bin") || !strings.Contains(joined, "HOME=/tmp/home") {
		t.Fatalf("explicit or runtime-safe env missing: %s", joined)
	}
}

func TestRegistryCanonicalNamesAvoidCrossServerToolCollisions(t *testing.T) {
	r := &Registry{tools: map[string]*ExternalTool{}, handlers: map[string]ToolHandler{}, aliases: map[string]string{}, ambiguous: map[string]bool{}}
	handler := func(map[string]string) (string, error) { return "ok", nil }
	r.registerServerHandler("alpha", ExternalTool{Name: "search", OriginalName: "search", Server: "alpha"}, handler)
	if _, _, ok := r.Get("search"); !ok {
		t.Fatal("single-server short alias should resolve")
	}
	r.registerServerHandler("beta", ExternalTool{Name: "search", OriginalName: "search", Server: "beta"}, handler)
	if _, _, ok := r.Get("search"); ok {
		t.Fatal("ambiguous short alias should not resolve")
	}
	for _, canonical := range []string{"alpha__search", "beta__search"} {
		if _, _, ok := r.Get(canonical); !ok {
			t.Fatalf("canonical name %s should resolve", canonical)
		}
	}
	r.unregisterServer("beta")
	if tool, _, ok := r.Get("search"); !ok || tool.Server != "alpha" {
		t.Fatalf("short alias should recover after collision disappears: %#v ok=%v", tool, ok)
	}
}

func TestRegistryCanonicalizesUnsafeAndLongNamesWithoutCollision(t *testing.T) {
	r := &Registry{tools: map[string]*ExternalTool{}, handlers: map[string]ToolHandler{}, aliases: map[string]string{}, ambiguous: map[string]bool{}}
	handler := func(map[string]string) (string, error) { return "ok", nil }
	first := r.registerServerHandler("docs.server", ExternalTool{Name: "read/file", OriginalName: "read/file", Server: "docs.server"}, handler)
	second := r.registerServerHandler("docs.server", ExternalTool{Name: "read.file", OriginalName: "read.file", Server: "docs.server"}, handler)
	long := r.registerServerHandler(strings.Repeat("server", 20), ExternalTool{Name: strings.Repeat("tool", 30), OriginalName: strings.Repeat("tool", 30), Server: strings.Repeat("server", 20)}, handler)
	if first == second {
		t.Fatalf("unsafe names collapsed to the same canonical name: %q", first)
	}
	for _, name := range []string{first, second, long} {
		if len(name) > 64 {
			t.Fatalf("canonical native function name exceeds provider limit: %d %q", len(name), name)
		}
		if _, _, ok := r.Get(name); !ok {
			t.Fatalf("canonical tool %q missing", name)
		}
	}
	if got := r.ListTools(); len(got) != 3 {
		t.Fatalf("ListTools returned %d tools", len(got))
	}
}

func TestRegistryReplacesServerToolsAtomically(t *testing.T) {
	r := &Registry{tools: map[string]*ExternalTool{}, handlers: map[string]ToolHandler{}, aliases: map[string]string{}, ambiguous: map[string]bool{}}
	handler := func(map[string]string) (string, error) { return "ok", nil }
	r.registerServerHandler("alpha", ExternalTool{Name: "old", OriginalName: "old", Server: "alpha"}, handler)
	r.replaceServerHandlers("alpha", []serverToolHandler{{tool: ExternalTool{Name: "new", OriginalName: "new", Server: "alpha"}, handler: handler}})
	if _, _, ok := r.Get("alpha__old"); ok {
		t.Fatal("old server tool survived replacement")
	}
	if _, _, ok := r.Get("alpha__new"); !ok {
		t.Fatal("new server tool missing after replacement")
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	if path := os.Getenv("MCP_STARTS_FILE"); path != "" {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if f != nil {
			_, _ = f.WriteString("start\n")
			_ = f.Close()
		}
	}
	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		case "notifications/initialized":
		case "tools/list":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"message": "ready"}})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{"tools": []map[string]any{{
					"name":        "echo_test",
					"description": "echo test value",
					"inputSchema": map[string]any{"type": "object"},
				}}},
			})
		case "tools/call":
			args, _ := req.Params["arguments"].(map[string]any)
			value, _ := args["value"].(string)
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{"value": value}})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": value}}},
			})
		}
	}
	os.Exit(0)
}
