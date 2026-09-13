package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatSendFileCopiesLocalFile(t *testing.T) {
	if _, err := ChatSendFile("missing.txt", ""); err == nil || !strings.Contains(err.Error(), "聊天机器人") {
		t.Fatalf("missing env: %v", err)
	}
	send := t.TempDir()
	t.Setenv(chatSendDirEnv, send)
	src := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(src, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := ChatSendFile(src, "发给你.txt")
	if err != nil || !strings.Contains(out, "发给你.txt") {
		t.Fatalf("%s %v", out, err)
	}
	got, err := os.ReadFile(filepath.Join(send, "发给你.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("%s %v", got, err)
	}
}
func TestChatSendFileRejectsDirectory(t *testing.T) {
	send := t.TempDir()
	t.Setenv(chatSendDirEnv, send)
	if _, err := ChatSendFile(t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Fatalf("%v", err)
	}
}

func TestChatSendFilePreservesRepeatedNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(chatSendDirEnv, dir)
	src := filepath.Join(t.TempDir(), "a.txt")
	for _, body := range []string{"first", "second"} {
		if err := os.WriteFile(src, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ChatSendFile(src, "same.txt"); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{"same.txt": "first", "same (1).txt": "second"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != want {
			t.Fatalf("%s %s %v", name, b, err)
		}
	}
}
