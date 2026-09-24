//go:build darwin

package tui

import "testing"

func TestPasteTargetFocusedRequiresHostWindow(t *testing.T) {
	session := macOSPasteSession{hostPID: 100, hostName: "Terminal", tty: "ttys002"}
	if pasteTargetFocused(session, 86131, "ttys002") {
		t.Fatal("Cursor (other app) must not steal Command+V")
	}
	if pasteTargetFocused(session, 100, "ttys009") {
		t.Fatal("another Terminal tab/window must not steal Command+V")
	}
	if !pasteTargetFocused(session, 100, "ttys002") {
		t.Fatal("focused DeepSentry Terminal tab should accept Command+V")
	}
	if !pasteTargetFocused(session, 100, "/dev/ttys002") {
		t.Fatal("TTY /dev prefix should still match")
	}

	cursorHost := macOSPasteSession{hostPID: 86131, hostName: "Cursor", tty: "ttys012"}
	if !pasteTargetFocused(cursorHost, 86131, "") {
		t.Fatal("DeepSentry inside Cursor should accept Command+V when Cursor is frontmost")
	}
	if pasteTargetFocused(cursorHost, 100, "") {
		t.Fatal("DeepSentry inside Cursor must ignore Terminal Command+V")
	}
	if pasteTargetFocused(macOSPasteSession{}, 86131, "") {
		t.Fatal("unknown host session must not accept Command+V")
	}

	tmux := macOSPasteSession{hostPID: 100, hostName: "Terminal", tty: "ttys004", skipTTY: true}
	if !pasteTargetFocused(tmux, 100, "ttys002") {
		t.Fatal("tmux/screen should accept Command+V based on host window PID")
	}
}

func TestIsPasteHostApp(t *testing.T) {
	if !isPasteHostApp("Terminal") || !isPasteHostApp("/Applications/iTerm.app/Contents/MacOS/iTerm2") {
		t.Fatal("terminal hosts should match")
	}
	if !isPasteHostApp("Cursor") {
		t.Fatal("Cursor should match as a possible integrated-terminal host")
	}
	if isPasteHostApp("Cursor Helper (GPU)") {
		t.Fatal("Electron helpers must not be treated as the host window")
	}
	if isPasteHostApp("Google Chrome") || isPasteHostApp("Finder") || isPasteHostApp("zsh") {
		t.Fatal("unrelated apps and shells must not be treated as the host window")
	}
}

func TestParseLSAppPID(t *testing.T) {
	pid, err := parseLSAppPID("\"pid\"=86131\n")
	if err != nil || pid != 86131 {
		t.Fatalf("parse lsappinfo pid: pid=%d err=%v", pid, err)
	}
}

func TestNormalizeTTY(t *testing.T) {
	if got := normalizeTTY(" /dev/ttys002\n"); got != "ttys002" {
		t.Fatalf("normalizeTTY=%q", got)
	}
	if sameTTY("/dev/ttys002", "ttys002") != true {
		t.Fatal("same TTY should match")
	}
}
