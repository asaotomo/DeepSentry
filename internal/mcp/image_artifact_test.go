package mcp

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestMCPImageContentPersistsAsPrivateArtifactAndExtractsMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSENTRY_MCP_ARTIFACT_DIR", dir)
	data := testPNGBytes(t)
	out := formatMCPContent([]sdkmcp.Content{&sdkmcp.ImageContent{Data: data, MIMEType: "image/png"}}, nil)
	clean, artifacts := ExtractImageArtifacts(out)
	if len(artifacts) != 1 || !strings.Contains(clean, "已保存到") || strings.Contains(clean, imageArtifactMarker) {
		t.Fatalf("clean=%q artifacts=%#v", clean, artifacts)
	}
	artifact := artifacts[0]
	got, err := os.ReadFile(artifact.Path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("saved artifact invalid: path=%s err=%v", artifact.Path, err)
	}
	if filepath.Dir(artifact.Path) != dir || artifact.MediaType != "image/png" || artifact.Size != int64(len(data)) {
		t.Fatalf("artifact metadata invalid: %#v", artifact)
	}
	if stat, err := os.Stat(artifact.Path); err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatalf("artifact permissions=%v err=%v", stat.Mode().Perm(), err)
	}
}

func TestMCPImageSaveFailureKeepsTextualToolResult(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSENTRY_MCP_ARTIFACT_DIR", blocked)
	out := formatMCPContent([]sdkmcp.Content{
		&sdkmcp.TextContent{Text: "screenshot ready"},
		&sdkmcp.ImageContent{Data: testPNGBytes(t), MIMEType: "image/png"},
	}, nil)
	if !strings.Contains(out, "screenshot ready") || !strings.Contains(out, "保存失败") {
		t.Fatalf("save failure discarded original result: %q", out)
	}
}

func TestMCPImageRejectsOversizeArtifactWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSENTRY_MCP_ARTIFACT_DIR", dir)
	data := make([]byte, maxMCPImageBytes+1)
	copy(data, testPNGBytes(t))
	if _, err := persistMCPImage(data, "image/png"); err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("oversize image was not rejected: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("oversize image left artifacts: %v", entries)
	}
}

func TestMCPImageMarkerSurvivesVisibleTextTruncation(t *testing.T) {
	t.Setenv("DEEPSENTRY_MCP_ARTIFACT_DIR", t.TempDir())
	out := formatMCPContent([]sdkmcp.Content{
		&sdkmcp.TextContent{Text: strings.Repeat("x", (1<<20)+2048)},
		&sdkmcp.ImageContent{Data: testPNGBytes(t), MIMEType: "image/png"},
	}, nil)
	clean, artifacts := ExtractImageArtifacts(out)
	if len(artifacts) != 1 {
		t.Fatalf("image marker was truncated: artifacts=%#v", artifacts)
	}
	if strings.Contains(clean, imageArtifactMarker) || !strings.Contains(clean, "已限制") {
		t.Fatalf("unexpected cleaned output: len=%d tail=%q", len(clean), clean[maxIntForTest(0, len(clean)-80):])
	}
}

func maxIntForTest(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestHawkEyePromptPrioritizesWorkflowAndCoreTools(t *testing.T) {
	r := &Registry{tools: map[string]*ExternalTool{}, handlers: map[string]ToolHandler{}, aliases: map[string]string{}, ambiguous: map[string]bool{}}
	handler := func(map[string]string) (string, error) { return "ok", nil }
	for _, name := range []string{"hawkeye_capture_state", "browser_snapshot", "hawkeye_evaluate", "browser_tabs"} {
		r.RegisterHandler("hawkeye__"+name, ExternalTool{Name: "hawkeye__" + name, OriginalName: name, Server: "hawkeye", Description: strings.Repeat(name+" ", 80)}, handler)
	}
	prompt := r.FormatPrompt()
	for _, want := range []string{
		"HawkEye MCP 1.0.12 深度适配工作流", "禁止再用 execute", "browser_select_option",
		"clickMode=trusted", "inputMode=trusted", "GET 无 body", "playbackRate",
		"mcp:hawkeye__browser_tabs",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("HawkEye prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Index(prompt, "mcp:hawkeye__browser_tabs") > strings.Index(prompt, "mcp:hawkeye__hawkeye_evaluate") {
		t.Fatal("high-level tab workflow should be listed before raw evaluate")
	}
}
