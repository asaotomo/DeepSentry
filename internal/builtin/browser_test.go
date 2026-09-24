package builtin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ai-edr/internal/config"
)

func TestRenderedBrowserHelpersParseDOM(t *testing.T) {
	html := `<html><head><title>DeepSentry Test</title></head><body><main id="app"><form action="/login"></form><a href="/next">next</a><p>hello world</p></main></body></html>`
	if title := firstMatch(html, `(?is)<title[^>]*>(.*?)</title>`); title != "DeepSentry Test" {
		t.Fatalf("title = %q", title)
	}
	if fragment := selectRenderedFragment(html, "#app"); !strings.Contains(fragment, "/login") {
		t.Fatalf("selector did not return main fragment: %s", fragment)
	}
	var b strings.Builder
	writeLinks(&b, html)
	if !strings.Contains(b.String(), "/next") {
		t.Fatalf("links missing: %s", b.String())
	}
	if text := renderedText(html, 100); !strings.Contains(text, "hello world") {
		t.Fatalf("text missing: %s", text)
	}
}

func TestBrowserTimeoutDefaultDoesNotRequireConfigInit(t *testing.T) {
	old := config.GlobalConfig.BrowserTimeoutSec
	config.GlobalConfig.BrowserTimeoutSec = 0
	defer func() { config.GlobalConfig.BrowserTimeoutSec = old }()
	if got := browserTimeout().Seconds(); got != 20 {
		t.Fatalf("timeout = %.0f, want 20", got)
	}
}

func TestNormalizeBrowserMode(t *testing.T) {
	tests := map[string]string{
		"":           "snapshot",
		"SNAPSHOT":   "snapshot",
		"text":       "text",
		" forms ":    "forms",
		"links":      "links",
		"screenshot": "screenshot",
		"unknown":    "snapshot",
	}
	for in, want := range tests {
		if got := normalizeBrowserMode(in); got != want {
			t.Fatalf("normalizeBrowserMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeURLRejectsNonHTTPNavigation(t *testing.T) {
	if _, err := normalizeURL("javascript:alert(1)"); err == nil {
		t.Fatal("javascript URL should be rejected")
	}
}

func TestLimitedStringBufferTruncates(t *testing.T) {
	var b limitedStringBuffer
	b.max = 5
	n, err := b.Write([]byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if n != len("hello world") {
		t.Fatalf("Write returned %d, want %d", n, len("hello world"))
	}
	if got := b.String(); got != "hello" {
		t.Fatalf("buffer = %q, want hello", got)
	}
	if !b.truncated {
		t.Fatal("expected truncated flag")
	}
}

func TestBrowserSelectorReference(t *testing.T) {
	if got := browserSelector("@e12"); got != `[data-deepsentry-ref="e12"]` {
		t.Fatalf("browserSelector reference = %q", got)
	}
	if got := browserSelector("#search"); got != "#search" {
		t.Fatalf("browserSelector css = %q", got)
	}
}

func TestSliceBrowserTextUsesRuneOffsets(t *testing.T) {
	page, total, next := sliceBrowserText("甲乙丙丁戊", 2, 2)
	if page != "丙丁" || total != 5 || next != 4 {
		t.Fatalf("sliceBrowserText = page=%q total=%d next=%d", page, total, next)
	}
}

func TestBrowserInstallGuideIsActionable(t *testing.T) {
	guide := browserInstallGuide()
	for _, want := range []string{"Browser installation guide", "browser_binary", "Chrome"} {
		if !strings.Contains(guide, want) {
			t.Fatalf("install guide missing %q: %s", want, guide)
		}
	}
}

func TestControlledHeadlessBrowserSession(t *testing.T) {
	if bin, _ := findBrowserBinary(); bin == "" {
		t.Skip("Chrome/Chromium is not installed in the test environment")
	}
	oldTimeout := config.GlobalConfig.BrowserTimeoutSec
	config.GlobalConfig.BrowserTimeoutSec = 60
	t.Cleanup(func() { config.GlobalConfig.BrowserTimeoutSec = oldTimeout })
	// GitHub-hosted Ubuntu runners disable the user namespaces Chromium needs.
	// The test only visits the loopback httptest server inside an ephemeral VM.
	if os.Getenv("CI") != "" {
		t.Setenv("DEEPSENTRY_BROWSER_NO_SANDBOX", "1")
		if !browserNoSandboxEnabled() {
			t.Fatal("CI browser compatibility flag was not applied")
		}
	}
	CloseBrowserSessions()
	defer CloseBrowserSessions()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Deep Result Page</title></head><body><p>followed link successfully</p></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Browser Control Test</title></head><body>
<input id="query" aria-label="query"><button id="apply" onclick="document.getElementById('result').textContent=document.getElementById('query').value">Apply</button>
<a id="next" href="/next">Read details</a><p id="result">waiting</p></body></html>`))
	}))
	defer server.Close()

	out, err := BrowserBrowse("open", "", server.URL, "headless", "", 100, 5000, 0, 0, 120)
	if err != nil {
		t.Fatalf("open headless browser: %v", err)
	}
	if !strings.Contains(out, "Browser Control Test") || !strings.Contains(out, "@e") {
		t.Fatalf("unexpected open snapshot: %s", out)
	}
	if _, err := BrowserInteract("type", "", "#query", "hello browser", "", "", true, false, 50, 5000, 0, 0, 120); err != nil {
		t.Fatalf("type: %v", err)
	}
	out, err = BrowserInteract("click", "", "#apply", "", "", "", true, false, 50, 5000, 0, 0, 120)
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if !strings.Contains(out, "hello browser") {
		t.Fatalf("interaction result missing from snapshot: %s", out)
	}
	out, err = BrowserBrowse("follow", "", "", "", "#next", 50, 5000, 0, 0, 120)
	if err != nil {
		t.Fatalf("follow link: %v", err)
	}
	if !strings.Contains(out, "Deep Result Page") || !strings.Contains(out, "followed link successfully") {
		t.Fatalf("follow result missing destination snapshot: %s", out)
	}
}

func TestBrowserNoSandboxRequiresExplicitOptIn(t *testing.T) {
	t.Setenv("DEEPSENTRY_BROWSER_NO_SANDBOX", "")
	if browserNoSandboxEnabled() {
		t.Fatal("browser sandbox must be enabled by default")
	}
	t.Setenv("DEEPSENTRY_BROWSER_NO_SANDBOX", "true")
	if !browserNoSandboxEnabled() {
		t.Fatal("explicit test-container opt-in should disable the browser sandbox")
	}
}
