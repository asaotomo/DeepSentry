package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditRequiresUniqueMatchAndPreservesCRLF(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".deepsentry", "workspace", "app.txt")
	mw := NewFilesystemMiddleware(nil)
	ctx := &StepContext{State: NewAgentState(filepath.Dir(path))}
	if _, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionWriteFile, Path: path, Content: "func A()\nneedle\nfunc B()\nneedle\n"}); err != nil {
		t.Fatal(err)
	}
	ambiguous, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionEditFile, Path: path, OldString: "needle", NewString: "signal"})
	if err != nil || !strings.Contains(ambiguous.Output, "匹配了 2 处") {
		t.Fatalf("ambiguous edit: %#v %v", ambiguous, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "signal") {
		t.Fatalf("ambiguous edit changed file: %s %v", raw, err)
	}
	crlf := filepath.Join(filepath.Dir(path), "win.txt")
	if err := os.WriteFile(crlf, []byte("alpha\r\nbeta\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edited, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionEditFile, Path: crlf, OldString: "beta", NewString: "gamma"})
	if err != nil || !strings.Contains(edited.Output, "已编辑") {
		t.Fatalf("crlf edit: %#v %v", edited, err)
	}
	got, err := os.ReadFile(crlf)
	if err != nil || string(got) != "alpha\r\ngamma\r\n" {
		t.Fatalf("newline style changed: %q %v", got, err)
	}
}

func TestReadWindowAndDirectoryGrep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".deepsentry", "workspace", "proj")
	if err := os.MkdirAll(filepath.Join(root, "skip", "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i := 1; i <= 30; i++ {
		body.WriteString(strings.Repeat("x", 20) + "\n")
	}
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte(body.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skip", "node_modules", "hidden.go"), []byte("UNIQUE_TOKEN\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib.go"), []byte("keep UNIQUE_TOKEN here\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mw := NewFilesystemMiddleware(nil)
	ctx := &StepContext{State: NewAgentState(filepath.Dir(root))}
	window, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionReadFile, Path: path, Offset: 28, Limit: 5})
	if err != nil || !strings.Contains(window.Output, "28|") || !strings.Contains(window.Output, "文件结束") {
		t.Fatalf("window: %#v %v", window, err)
	}
	found, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionGrep, Path: root, Pattern: "UNIQUE_TOKEN"})
	if err != nil || !strings.Contains(found.Output, "lib.go:1:") || strings.Contains(found.Output, "hidden.go") {
		t.Fatalf("tree grep: %#v %v", found, err)
	}
}

func TestGrepLocalTreeDoesNotFollowExternalSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local-match"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := grepLocalTree(root, "outside-secret", 80)
	if err != nil || strings.Contains(got, "outside-secret") {
		t.Fatalf("escaped grep root: %q %v", got, err)
	}
	got, err = grepLocalTree(root, "local-match", 80)
	if err != nil || !strings.Contains(got, "local.txt:1:local-match") {
		t.Fatalf("local match missing: %q %v", got, err)
	}
}
