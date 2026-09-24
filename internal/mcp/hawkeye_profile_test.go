package mcp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHawkEyeV1012ProfileCoversAuthoritativeServerContract(t *testing.T) {
	if got := len(HawkEyeKnownToolNames()); got != 51 {
		t.Fatalf("HawkEye 1.0.12 profile tool count=%d, want 51", got)
	}
	sourcePath := ""
	for _, candidate := range []string{
		filepath.Clean(filepath.Join("..", "..", "hawkeye-mcp-server.mjs")),
		filepath.Clean(filepath.Join("..", "..", "..", "Hx0-Chrome-鹰眼", "mcp-server", "hawkeye-mcp-server.mjs")),
	} {
		if _, err := os.Stat(candidate); err == nil {
			sourcePath = candidate
			break
		}
	}
	if sourcePath == "" {
		t.Skip("authoritative HawkEye server source is not present")
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "const SERVER_VERSION = '1.0.12'") {
		t.Fatalf("expected HawkEye 1.0.12 server, got %s", sourcePath)
	}
	start := strings.Index(source, "const TOOLS = [")
	if start < 0 {
		t.Fatal("HawkEye const TOOLS contract not found")
	}
	end := strings.Index(source[start:], "\n];")
	if end < 0 {
		t.Fatal("HawkEye const TOOLS contract end not found")
	}
	toolBlock := source[start : start+end]
	re := regexp.MustCompile(`(?m)^\s*name:\s*['\"]([^'\"]+)['\"]`)
	matches := re.FindAllStringSubmatch(toolBlock, -1)
	actual := make([]string, 0, len(matches))
	for _, match := range matches {
		actual = append(actual, match[1])
	}
	sort.Strings(actual)
	want := HawkEyeKnownToolNames()
	if strings.Join(actual, "\n") != strings.Join(want, "\n") {
		t.Fatalf("DeepSentry HawkEye contract drift\nactual=%v\nprofile=%v", actual, want)
	}
	for _, fragment := range []string{
		"clickMode", "inputMode", "save_to_file", "file_path", "overwrite",
		"context_budget_chars", "page_token", "next_page_token", "viewport_only",
		"stableId", "exact", "authorized", "release_all", "fuzzingEnabled",
		"hawkeye_capture_inspect", "hawkeye_request_mutate", "hawkeye_script_upsert",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("authoritative HawkEye 1.0.12 contract lost critical field/tool %q", fragment)
		}
	}
}

func TestHawkEyeDetectionSupportsCustomServerAliasButRejectsGenericBrowser(t *testing.T) {
	tools := []ExternalTool{
		{Name: "browser__browser_snapshot", OriginalName: "browser_snapshot", Server: "my-browser"},
		{Name: "browser__hawkeye_capture_state", OriginalName: "hawkeye_capture_state", Server: "my-browser"},
		{Name: "browser__hawkeye_capture_history", OriginalName: "hawkeye_capture_history", Server: "my-browser"},
		{Name: "browser__hawkeye_request_get", OriginalName: "hawkeye_request_get", Server: "my-browser"},
	}
	if !IsHawkEyeTool(&tools[0], tools) {
		t.Fatal("custom-named server with HawkEye capability signature was not recognized")
	}
	generic := ExternalTool{OriginalName: "browser_snapshot", Server: "generic-playwright"}
	if IsHawkEyeTool(&generic, []ExternalTool{generic}) {
		t.Fatal("generic browser MCP was misidentified as HawkEye")
	}
}

func TestHawkEyeRiskIsActionAware(t *testing.T) {
	tests := []struct {
		name string
		args map[string]string
		want string
	}{
		{"hawkeye_capture_history", nil, MCPRiskLow},
		{"browser_screenshot", nil, MCPRiskLow},
		{"browser_screenshot", map[string]string{"save_to_file": "true"}, MCPRiskMedium},
		{"browser_click", map[string]string{"clickMode": "trusted"}, MCPRiskLow},
		{"hawkeye_intercept", map[string]string{"action": "queue"}, MCPRiskLow},
		{"hawkeye_intercept", map[string]string{"action": "enable"}, MCPRiskLow},
		{"hawkeye_intercept", map[string]string{"action": "release"}, MCPRiskHigh},
		{"browser_tabs", map[string]string{"action": "new"}, MCPRiskLow},
		{"browser_tabs", map[string]string{"action": "close"}, MCPRiskMedium},
		{"hawkeye_capture_start", nil, MCPRiskLow},
		{"hawkeye_scope", map[string]string{"action": "get"}, MCPRiskLow},
		{"hawkeye_scope", map[string]string{"action": "set"}, MCPRiskHigh},
		{"hawkeye_request_replay", nil, MCPRiskHigh},
		{"hawkeye_evaluate", map[string]string{"code": "1+1"}, MCPRiskHigh},
		{"hawkeye_evaluate", map[string]string{"code": "const v=document.querySelector('video'); ({paused:v.paused,currentTime:v.currentTime,playbackRate:v.playbackRate})"}, MCPRiskLow},
		{"browser_security", nil, MCPRiskLow},
	}
	for _, test := range tests {
		tool := &ExternalTool{OriginalName: test.name, Server: "hawkeye"}
		got, reason, ok := HawkEyeToolRisk(tool, test.args)
		if !ok || got != test.want || strings.TrimSpace(reason) == "" {
			t.Errorf("%s args=%v risk=%q reason=%q ok=%v, want %q", test.name, test.args, got, reason, ok, test.want)
		}
	}
}

func TestHawkEyeTimeoutsRespectLongOperationsAndExplicitOverride(t *testing.T) {
	if got := HawkEyeToolTimeout("browser_research", 60*time.Second, false); got != 205*time.Second {
		t.Fatalf("research timeout=%s", got)
	}
	if got := HawkEyeToolTimeout("browser_fetch", 60*time.Second, false); got != 130*time.Second {
		t.Fatalf("fetch timeout=%s", got)
	}
	if got := HawkEyeToolTimeout("browser_research", 30*time.Second, true); got != 30*time.Second {
		t.Fatalf("explicit timeout was ignored: %s", got)
	}
}

func TestHawkEyeCallDefaultsAreBoundedAndExplicitValuesWin(t *testing.T) {
	input := map[string]string{"compact": "false", "context_budget_chars": "120000"}
	got := applyHawkEyeCallDefaults("browser_snapshot", input)
	if got["compact"] != "false" || got["context_budget_chars"] != "120000" || got["max_elements"] != "160" || got["viewport_only"] != "true" {
		t.Fatalf("defaults overrode explicit values or were incomplete: %#v", got)
	}
	if _, mutated := input["max_elements"]; mutated {
		t.Fatalf("caller args were mutated: %#v", input)
	}
	press := applyHawkEyeCallDefaults("browser_press_key", map[string]string{"key": "f"})
	if press["inputMode"] != "trusted" {
		t.Fatalf("fullscreen key f should default to trusted input: %#v", press)
	}
	pressJS := applyHawkEyeCallDefaults("browser_press_key", map[string]string{"key": "f", "inputMode": "js"})
	if pressJS["inputMode"] != "js" {
		t.Fatalf("explicit press_key inputMode was overridden: %#v", pressJS)
	}
	click := applyHawkEyeCallDefaults("browser_click", map[string]string{"text": "全屏", "ref": "f0:e30"})
	if click["clickMode"] != "trusted" {
		t.Fatalf("fullscreen click should default to trusted: %#v", click)
	}
	cont := applyHawkEyeCallDefaults("browser_snapshot", map[string]string{
		"page_token": "tok_abcDEF0123456789", "max_elements": "400", "cursor": "12",
	})
	if len(cont) != 1 || cont["page_token"] != "tok_abcDEF0123456789" {
		t.Fatalf("continuation must send page_token alone: %#v", cont)
	}
	alias := applyHawkEyeCallDefaults("browser_read_text", map[string]string{"next_page_token": "opaqueTokenFromContext"})
	if alias["page_token"] != "opaqueTokenFromContext" || alias["next_page_token"] != "" || alias["context_budget_chars"] != "" {
		t.Fatalf("next_page_token should collapse to page_token only: %#v", alias)
	}
	numeric := applyHawkEyeCallDefaults("browser_snapshot", map[string]string{"cursor": "80", "compact": "false"})
	if _, ok := numeric["cursor"]; ok {
		t.Fatalf("numeric cursor must be stripped: %#v", numeric)
	}
	solve := applyHawkEyeCallDefaults("browser_captcha_assist", map[string]string{"action": "solve", "answer": "A8K2"})
	if solve["authorized"] != "true" {
		t.Fatalf("captcha solve should default authorized=true: %#v", solve)
	}
}

func TestHawkEyeSnapshotOutputDropsElementDumpAndKeepsHint(t *testing.T) {
	structured := map[string]any{
		"ok": true,
		"elements": []any{
			map[string]any{"name": "首页", "ref": "f0:e1"},
			map[string]any{"name": "倍速", "ref": "f0:e23"},
		},
		"element_count": 2,
		"url":           "https://www.bilibili.com/video/BV1test/",
		"context":       map[string]any{"complete": false, "next_page_token": "tokContinuePage01"},
	}
	out := formatHawkEyeMCPContent("browser_snapshot", []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "- button \"倍速\" [ref=f0:e23]\n- button \"全屏\" [ref=f0:e30]"},
	}, structured, map[string]string{"ref": "f0:e23"})
	if strings.Contains(out, `"elements"`) || strings.Contains(out, "首页") && strings.Contains(out, `"ref": "f0:e1"`) {
		t.Fatalf("structured element dump should be stripped: %s", out)
	}
	if !strings.Contains(out, "button \"倍速\"") || !strings.Contains(out, "browser_select_option") || !strings.Contains(out, "press_key") {
		t.Fatalf("accessibility tree and interaction hint missing: %s", out)
	}
	if !strings.Contains(out, "next_page_token=tokContinuePage01") || strings.Contains(out, "next_cursor=") {
		t.Fatalf("1.0.12 page token summary missing: %s", out)
	}
	click := formatHawkEyeMCPContent("browser_click", []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "clicked"},
	}, map[string]any{"elements": []any{map[string]any{"name": "noise"}}}, map[string]string{"ref": "f0:e23"})
	if !strings.Contains(click, "没有 text=") {
		t.Fatalf("click without accessible text should warn: %s", click)
	}
}

func TestHawkEyeWorkflowPromptForbidsInstallSelfCheck(t *testing.T) {
	prompt := hawkEyeWorkflowPrompt()
	for _, want := range []string{
		"禁止再用 execute", "browser_select_option", "inputMode=trusted", "禁止 read_file",
		"load_skill", "playbackRate", "canvas.drawImage", "browser_browse", "?p=N",
		"page_token", "site:", "captcha_assist",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("workflow prompt missing %q", want)
		}
	}
}

func TestHawkEyeStaleNavigateRecoveryDetectionIsNarrow(t *testing.T) {
	if !hawkEyeStaleNavigateError("browser_navigate", `{"error":"Test tab no longer exists"}`) {
		t.Fatal("known stale navigate error was not detected")
	}
	if hawkEyeStaleNavigateError("browser_click", `{"error":"Test tab no longer exists"}`) {
		t.Fatal("non-navigation operation would be auto-replayed on a new tab")
	}
	long := strings.Repeat("x", 30000)
	if got := compactHawkEyeMCPOutput("browser_navigate", long); len(got) >= len(long) || !strings.Contains(got, "browser_find") {
		t.Fatalf("navigate output was not compacted safely: len=%d", len(got))
	}
	if got := compactHawkEyeMCPOutput("browser_snapshot", long); len(got) >= len(long) || !strings.Contains(got, "read_file") {
		t.Fatalf("snapshot output was not compacted safely: len=%d", len(got))
	}
}

func TestSDKAnnotationsArePreservedWithoutTrustingMissingPointers(t *testing.T) {
	destructive, openWorld := false, true
	got := convertSDKToolAnnotations(&sdkmcp.ToolAnnotations{
		ReadOnlyHint: true, DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &openWorld,
	})
	if !got.Present || !got.ReadOnlyHint || !got.DestructiveKnown || got.DestructiveHint || !got.IdempotentHint || !got.OpenWorldKnown || !got.OpenWorldHint {
		t.Fatalf("annotation conversion lost fields: %#v", got)
	}
	if got := convertSDKToolAnnotations(nil); got.Present {
		t.Fatalf("nil annotations became present: %#v", got)
	}
}

func TestHawkEyeConnectionStateTracksOnlySuccessfulOwnedChanges(t *testing.T) {
	conn := &sdkConnection{hawkEye: true}
	conn.recordHawkEyeState("hawkeye_intercept", map[string]string{"action": "enable"})
	conn.recordHawkEyeState("hawkeye_capture_start", nil)
	conn.recordHawkEyeState("browser_resize", map[string]string{"width": "1280"})
	if !conn.hawkEyeInterceptArmed || !conn.hawkEyeCaptureOwned || !conn.hawkEyeResizeChanged {
		t.Fatalf("owned HawkEye state not tracked: %#v", conn)
	}
	conn.recordHawkEyeState("hawkeye_intercept", map[string]string{"action": "disable"})
	conn.recordHawkEyeState("hawkeye_capture_stop", nil)
	conn.recordHawkEyeState("browser_resize", map[string]string{"action": "reset"})
	if conn.hawkEyeInterceptArmed || conn.hawkEyeCaptureOwned || conn.hawkEyeResizeChanged {
		t.Fatalf("HawkEye cleanup state not cleared: %#v", conn)
	}
}

func TestPreferExistingHawkEyeHTTPReusesOccupiedPort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": Hx0 HawkEye MCP event stream\n\n"))
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()
	cfg := ServerConfig{Name: "hx0-hawkeye", Command: "node", Args: []string{"/tmp/hawkeye-mcp-server.mjs", "--port", port}}
	got, ok := preferExistingHawkEyeHTTP(cfg)
	if !ok || got.Type != "streamable_http" || got.URL != server.URL+"/mcp" || got.Command != "" {
		t.Fatalf("should reuse existing HawkEye HTTP: ok=%v cfg=%#v", ok, got)
	}
}

func TestPreferExistingHawkEyeHTTPReuses405LikeRealServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "only GET probe", http.StatusBadRequest)
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
	cfg := ServerConfig{Name: "hx0-hawkeye", Command: "node", Args: []string{"/tmp/hawkeye-mcp-server.mjs", "--port", u.Port()}}
	got, ok := preferExistingHawkEyeHTTP(cfg)
	if !ok || got.Type != "streamable_http" || got.URL != server.URL+"/mcp" || got.Command != "" {
		t.Fatalf("405 GET /mcp should reuse Streamable HTTP: ok=%v cfg=%#v", ok, got)
	}
}

func TestPreferExistingHawkEyeHTTPReusesOccupiedTCPPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	cfg := ServerConfig{Name: "hx0-hawkeye", Command: "node", Args: []string{"hawkeye-mcp-server.mjs", "--port", port}}
	got, ok := preferExistingHawkEyeHTTP(cfg)
	if !ok || got.Type != "streamable_http" || got.URL != "http://127.0.0.1:"+port+"/mcp" {
		t.Fatalf("occupied port should reuse Streamable HTTP: ok=%v cfg=%#v", ok, got)
	}
}

func TestPreferExistingHawkEyeHTTPLeavesFreePortAlone(t *testing.T) {
	cfg := ServerConfig{Name: "hx0-hawkeye", Command: "node", Args: []string{"hawkeye-mcp-server.mjs", "--port", "19991"}}
	got, ok := preferExistingHawkEyeHTTP(cfg)
	if ok || got.Type == "streamable_http" {
		t.Fatalf("free port should still spawn stdio: ok=%v cfg=%#v", ok, got)
	}
}

func TestHawkEyePortFromArgs(t *testing.T) {
	if got := hawkEyePortFromArgs([]string{"--port", "19016"}); got != 19016 {
		t.Fatalf("port=%d", got)
	}
	if got := hawkEyePortFromArgs([]string{"--ws-port=19020"}); got != 19020 {
		t.Fatalf("ws-port=%d", got)
	}
}

func TestHawkEyeLongUnicodeOutputKeepsContinuationAndInputEvidence(t *testing.T) {
	payload := map[string]any{"inputDispatched": true, "outcomeVerified": false,
		"category": "input_verification_failed", "context": map[string]any{"complete": false, "next_page_token": "opaque-token-205"}}
	out := formatHawkEyeMCPContent("browser_click", []sdkmcp.Content{&sdkmcp.TextContent{Text: strings.Repeat("中文内容", 10000)}}, payload, nil)
	if !utf8.ValidString(out) {
		t.Fatal("truncated UTF-8")
	}
	for _, want := range []string{"opaque-token-205", "inputDispatched=true", "outcomeVerified=false", "input_verification_failed", "不要直接重复"} {
		if !strings.Contains(out, want) {
			t.Fatalf("lost %q in compact output", want)
		}
	}
}

func TestHawkEyePreservesNativeInputMode(t *testing.T) {
	for tool, key := range map[string]string{"browser_click": "clickMode", "browser_press_key": "inputMode"} {
		got := applyHawkEyeCallDefaults(tool, map[string]string{key: "native", "key": "f"})
		if got[key] != "native" {
			t.Fatalf("%s changed native mode: %v", tool, got)
		}
	}
}
