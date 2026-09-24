package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFileOperationsCannotEscapeThroughSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(home, ".deepsentry", "workspace")
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("do-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}

	if data, err := readTargetOrLocalWithExecutor(filepath.Join(workspace, "escape", "secret.txt"), nil); err == nil {
		t.Fatalf("symlink escape read succeeded: %q", data)
	}
	if err := writeTargetOrLocalWithExecutor(filepath.Join(workspace, "escape", "new.txt"), []byte("bad"), nil); err == nil {
		t.Fatal("symlink escape write succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file was created: %v", err)
	}
	matches, err := globWorkspace(workspace, "*.txt", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		if filepath.Base(match) == "secret.txt" {
			t.Fatalf("glob traversed workspace symlink: %#v", matches)
		}
	}
}

func TestWorkspaceWritesUsePrivatePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".deepsentry", "workspace", "nested", "result.txt")
	if err := writeTargetOrLocalWithExecutor(path, []byte("ok"), nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("workspace file mode=%o, want 600", got)
	}
}
