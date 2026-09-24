package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/term"
)

// mouseTrackingOff disables X10, button-event, any-event and SGR mouse
// reporting. A terminal that still has one of these on writes ESC[<35;x;yM
// for every pointer move; programs that are not reading mouse events then
// redraw on each report.
const mouseTrackingOff = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l"

// DisableMouseTracking asks the terminal to stop sending pointer reports.
// It does not change raw mode, the alternate screen, or the cursor.
func DisableMouseTracking() {
	if !term.IsTerminal(int(os.Stdout.Fd())) && !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	_, _ = fmt.Fprint(os.Stdout, mouseTrackingOff)
}

// ResetTerminalState 恢复终端到正常 shell 模式。
// survey / TUI / ANSI 流式输出可能留下 raw mode、鼠标模式、备用屏或 bracketed paste，导致 zsh 无法识别后续命令。
func ResetTerminalState() {
	if !term.IsTerminal(int(os.Stdout.Fd())) && !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	const seq = "\x1b[0m" +
		"\x1b[?25h" + // show cursor
		mouseTrackingOff +
		"\x1b[?2004l" + // bracketed paste
		"\x1b[?1049l\x1b[?47l" // alt screen
	_, _ = fmt.Fprint(os.Stdout, seq)
	_, _ = fmt.Fprint(os.Stderr, seq)
	// survey 结束后光标常停在行中列，仅 \n 会换行但不回列首，导致下一行输出右偏
	_, _ = fmt.Fprint(os.Stdout, "\r\n")
	if runtime.GOOS != "windows" && term.IsTerminal(int(os.Stdin.Fd())) {
		args := sttySaneArgs(runtime.GOOS)
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		_ = exec.CommandContext(ctx, "stty", args...).Run()
	}
}

func sttySaneArgs(goos string) []string {
	if goos == "darwin" || goos == "freebsd" || goos == "openbsd" || goos == "netbsd" {
		return []string{"-f", "/dev/tty", "sane"}
	}
	return []string{"-F", "/dev/tty", "sane"}
}

// Exit 在退出前恢复终端（os.Exit 不会执行 defer）。
func Exit(code int) {
	ResetTerminalState()
	os.Exit(code)
}
