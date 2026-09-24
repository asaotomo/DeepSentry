package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReconnectReplacesDisconnectedSessionWithoutReplayingTools(t *testing.T) {
	CloseAll()
	t.Cleanup(CloseAll)

	var toolCalls atomic.Int32
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "reconnect-test", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "probe", Description: "count one explicit invocation"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
			toolCalls.Add(1)
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil, nil
		})
	httpServer := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))

	cfg := ServerConfig{Name: "reconnectable", Type: "streamable_http", URL: httpServer.URL}
	defer func() {
		Disconnect(cfg.Name)
		httpServer.Close()
	}()
	if err := Connect(cfg); err != nil {
		t.Fatal(err)
	}
	sdkConnections.RLock()
	first := sdkConnections.byName[cfg.Name]
	sdkConnections.RUnlock()
	if first == nil {
		t.Fatal("initial MCP session was not recorded")
	}
	first.mu.Lock()
	first.status.State = "disconnected"
	first.mu.Unlock()

	// Re-enabling the same fingerprint must replace a stale session instead of
	// treating it as an already connected no-op.
	if err := Connect(cfg); err != nil {
		t.Fatal(err)
	}
	sdkConnections.RLock()
	second := sdkConnections.byName[cfg.Name]
	sdkConnections.RUnlock()
	if second == nil || second == first {
		t.Fatal("same-config reconnect did not replace the disconnected session")
	}
	if toolCalls.Load() != 0 {
		t.Fatalf("reconnect unexpectedly replayed a tool call: %d", toolCalls.Load())
	}

	if err := Reconnect(cfg.Name); err != nil {
		t.Fatal(err)
	}
	sdkConnections.RLock()
	third := sdkConnections.byName[cfg.Name]
	sdkConnections.RUnlock()
	if third == nil || third == second {
		t.Fatal("explicit reconnect did not replace the active session")
	}
	got, err := Global().Run(cfg.Name+"__probe", map[string]string{})
	if err != nil || got != "ok" {
		t.Fatalf("tool after reconnect=%q err=%v", got, err)
	}
	if toolCalls.Load() != 1 {
		t.Fatalf("expected only the explicit tool invocation, got %d calls", toolCalls.Load())
	}
}

func TestFormatOfflineStatusUsesAuthAppropriateRecoveryCommand(t *testing.T) {
	CloseAll()
	t.Cleanup(CloseAll)
	sdkConnections.Lock()
	sdkConnections.byName["plain"] = &sdkConnection{
		serverName: "plain",
		status:     ServerStatus{Name: "plain", State: "failed", Auth: "bearer-env"},
	}
	sdkConnections.byName["oauth-docs"] = &sdkConnection{
		serverName: "oauth-docs",
		status:     ServerStatus{Name: "oauth-docs", State: "disconnected", Auth: "oauth"},
	}
	sdkConnections.Unlock()

	status := FormatServerStatus()
	if !strings.Contains(status, "retry=/mcp reconnect plain") {
		t.Fatalf("plain server reconnect hint missing: %s", status)
	}
	if !strings.Contains(status, "retry=/mcp login oauth-docs") {
		t.Fatalf("OAuth login hint missing: %s", status)
	}
}

type sdkGreetingArgs struct {
	Name string `json:"name"`
}

type sdkNavigateArgs struct {
	URL string `json:"url"`
}

type sdkTabsArgs struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	Active bool   `json:"active"`
	Bind   bool   `json:"bind"`
}

func TestConnectStreamableHTTPDiscoversAndUsesAllServerPrimitives(t *testing.T) {
	CloseAll()
	t.Cleanup(CloseAll)

	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "deepsentry-sdk-test", Version: "1"},
		&sdkmcp.ServerOptions{Instructions: "Prefer the documented resource."},
	)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "greet", Description: "return a greeting"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, args sdkGreetingArgs) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello " + args.Name}}}, nil, nil
		})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "hidden"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{}, nil, nil
		})
	server.AddResource(&sdkmcp.Resource{Name: "guide", URI: "docs://guide", MIMEType: "text/plain"},
		func(_ context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "resource body"}}}, nil
		})
	server.AddResourceTemplate(&sdkmcp.ResourceTemplate{Name: "topic", URITemplate: "docs://{topic}", MIMEType: "text/plain"}, nil)
	server.AddPrompt(&sdkmcp.Prompt{Name: "review", Description: "review a topic", Arguments: []*sdkmcp.PromptArgument{{Name: "topic", Required: true}}},
		func(_ context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
			return &sdkmcp.GetPromptResult{Messages: []*sdkmcp.PromptMessage{{Role: "user", Content: &sdkmcp.TextContent{Text: "review " + req.Params.Arguments["topic"]}}}}, nil
		})

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	var authSeen, headerSeen bool
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = authSeen || r.Header.Get("Authorization") == "Bearer test-secret"
		headerSeen = headerSeen || r.Header.Get("X-DeepSentry-Test") == "yes"
		handler.ServeHTTP(w, r)
	}))
	defer func() {
		Disconnect("sdk")
		httpServer.Close()
	}()

	const tokenEnv = "DEEPSENTRY_MCP_SDK_TEST_TOKEN"
	if err := os.Setenv(tokenEnv, "test-secret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(tokenEnv) })
	if err := Connect(ServerConfig{
		Name: "sdk", Type: "streamable_http", URL: httpServer.URL,
		Headers: map[string]string{"X-DeepSentry-Test": "yes"}, BearerTokenEnvVar: tokenEnv,
		EnabledTools: []string{"greet", "dynamic"},
	}); err != nil {
		t.Fatal(err)
	}

	if !authSeen || !headerSeen {
		t.Fatalf("expected configured HTTP authentication/header, auth=%v header=%v", authSeen, headerSeen)
	}
	if _, _, ok := Global().Get("sdk__greet"); !ok {
		t.Fatal("canonical MCP tool was not registered")
	}
	if _, _, ok := Global().Get("greet"); !ok {
		t.Fatal("unambiguous short alias was not registered")
	}
	if _, _, ok := Global().Get("sdk__hidden"); ok {
		t.Fatal("tool outside enabled_tools was exposed")
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "dynamic"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "dynamic result"}}}, nil, nil
		})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, ok := Global().Get("sdk__dynamic"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tools/list_changed did not refresh the registry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := Global().Run("sdk__greet", map[string]string{"name": "DeepSentry"})
	if err != nil || got != "hello DeepSentry" {
		t.Fatalf("tool result=%q err=%v", got, err)
	}
	if resources := ListResources("sdk"); len(resources) != 1 || resources[0].URI != "docs://guide" {
		t.Fatalf("unexpected resources: %#v", resources)
	}
	if templates := ListResourceTemplates("sdk"); len(templates) != 1 || templates[0].URITemplate != "docs://{topic}" {
		t.Fatalf("unexpected resource templates: %#v", templates)
	}
	if got, err := ReadResource("sdk", "docs://guide"); err != nil || got != "resource body" {
		t.Fatalf("resource result=%q err=%v", got, err)
	}
	if prompts := ListPrompts("sdk"); len(prompts) != 1 || prompts[0].Name != "review" {
		t.Fatalf("unexpected prompts: %#v", prompts)
	}
	if got, err := GetPrompt("sdk", "review", map[string]string{"topic": "auth"}); err != nil || !strings.Contains(got, "review auth") {
		t.Fatalf("prompt result=%q err=%v", got, err)
	}
	statuses := ListServerStatuses()
	if len(statuses) != 1 || statuses[0].Protocol == "" || statuses[0].Tools != 2 || statuses[0].Resources != 1 || statuses[0].Templates != 1 || statuses[0].Prompts != 1 {
		t.Fatalf("unexpected status: %#v", statuses)
	}
	if !strings.Contains(FormatServerInstructions(), "Prefer the documented resource") {
		t.Fatal("server instructions were not exposed to the agent prompt")
	}
}

func TestHawkEyeNavigateAutoRecoversStaleBindingThroughTabsNew(t *testing.T) {
	CloseAll()
	t.Cleanup(CloseAll)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "hawkeye-test", Version: "1.0.6"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "browser_navigate"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, args sdkNavigateArgs) (*sdkmcp.CallToolResult, any, error) {
			if args.URL != "https://example.test/video" {
				t.Fatalf("navigate URL=%q", args.URL)
			}
			return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"ok":false,"error":"Test tab no longer exists"}`}}}, nil, nil
		})
	var recovered sdkTabsArgs
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "browser_tabs"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, args sdkTabsArgs) (*sdkmcp.CallToolResult, any, error) {
			recovered = args
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"ok":true,"bound":true}`}}}, nil, nil
		})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	if err := Connect(ServerConfig{Name: "hawkeye-auto-heal", Type: "streamable_http", URL: httpServer.URL}); err != nil {
		t.Fatal(err)
	}
	out, err := Global().Run("hawkeye-auto-heal__browser_navigate", map[string]string{"url": "https://example.test/video"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "自动修复") || recovered.Action != "new" || recovered.URL != "https://example.test/video" || !recovered.Active || !recovered.Bind {
		t.Fatalf("stale binding was not recovered safely: out=%q args=%#v", out, recovered)
	}
}

func TestConnectReusesOccupiedHawkEyePortViaStreamableHTTP(t *testing.T) {
	CloseAll()
	t.Cleanup(CloseAll)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "hawkeye-reuse", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "browser_snapshot", Description: "probe"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil, nil
		})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" && r.URL.Path != "/mcp/" {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		handler.ServeHTTP(w, r2)
	}))
	t.Cleanup(httpServer.Close)
	u, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ServerConfig{
		Name:    "hx0-hawkeye-occupied",
		Type:    "stdio",
		Command: "node",
		Args:    []string{"/tmp/hawkeye-mcp-server.mjs", "--port", u.Port()},
	}
	if err := Connect(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Disconnect(cfg.Name) })
	sdkConnections.RLock()
	conn := sdkConnections.byName[cfg.Name]
	sdkConnections.RUnlock()
	if conn == nil || conn.transport != "streamable_http" || conn.status.State != "connected" {
		t.Fatalf("occupied HawkEye port should connect via Streamable HTTP: %#v", conn)
	}
	got, err := Global().Run(cfg.Name+"__browser_snapshot", map[string]string{})
	if err != nil || !strings.Contains(got, "ok") {
		t.Fatalf("reused HawkEye tool=%q err=%v", got, err)
	}
}

func TestValidateRemoteMCPURL(t *testing.T) {
	for _, valid := range []string{"https://example.com/mcp", "http://localhost:8080/mcp", "http://127.0.0.1/mcp"} {
		if err := validateRemoteMCPURL(valid); err != nil {
			t.Errorf("valid URL %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"http://example.com/mcp", "file:///tmp/mcp", "https://user:pass@example.com/mcp"} {
		if err := validateRemoteMCPURL(invalid); err == nil {
			t.Errorf("unsafe URL %q accepted", invalid)
		}
	}
}

func TestMCPHTTPClientRejectsCredentialedCrossOriginRedirect(t *testing.T) {
	var destinationHit bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHit = true
		if r.Header.Get("Authorization") != "" {
			t.Error("authorization leaked to redirect destination")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	t.Setenv("DEEPSENTRY_MCP_REDIRECT_TOKEN", "redirect-secret")
	client := mcpHTTPClient(ServerConfig{URL: origin.URL, BearerTokenEnvVar: "DEEPSENTRY_MCP_REDIRECT_TOKEN"})
	resp, err := client.Get(origin.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "跨域重定向") {
		t.Fatalf("cross-origin redirect should be rejected, err=%v", err)
	}
	if destinationHit {
		t.Fatal("redirect destination should not be contacted")
	}
}

func TestFormatConnectedInventoryListsServersAndCounts(t *testing.T) {
	if got := formatConnectedInventory(nil); got != "" {
		t.Fatalf("empty inventory should be blank, got %q", got)
	}
	got := formatConnectedInventory([]ServerInventory{
		{Name: "hx0-hawkeye", ToolCount: 51},
		{Name: "fofamap", ToolCount: 15},
	})
	want := "2 个已连接 · hx0-hawkeye 51 · fofamap 15"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	fromTools := inventoryFromTools([]ExternalTool{
		{Name: "a", Server: "hx0-hawkeye"},
		{Name: "b", Server: "hx0-hawkeye"},
		{Name: "c", Server: "fofamap"},
		{Name: "d", Server: ""},
	})
	if len(fromTools) != 2 || fromTools[0].Name != "fofamap" || fromTools[0].ToolCount != 1 || fromTools[1].ToolCount != 2 {
		t.Fatalf("unexpected registry inventory: %#v", fromTools)
	}

	fromStatus := inventoryFromStatuses([]ServerStatus{
		{Name: "fofamap", State: "connected", Tools: 15},
		{Name: "hx0-hawkeye", State: "error", Tools: 0},
		{Name: "idle", State: "connecting", Tools: 0},
	})
	if len(fromStatus) != 1 || fromStatus[0].Name != "fofamap" || fromStatus[0].ToolCount != 15 {
		t.Fatalf("only connected servers should be counted: %#v", fromStatus)
	}
}
