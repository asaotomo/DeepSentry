//go:build windows
// +build windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"ai-edr/internal/ui"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// enableWindowsANSI 负责 Windows 平台的控制台初始化：
// 1. 强制开启 UTF-8 输入/输出编码 (解决中文乱码)
// 2. 强制开启虚拟终端处理 (解决颜色与 TUI 按键/鼠标乱码)
func enableWindowsANSI() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")

	// --- 1. 设置控制台输入/输出代码页为 UTF-8 (Code Page 65001) ---
	setConsoleCP := kernel32.NewProc("SetConsoleCP")
	setConsoleOutputCP := kernel32.NewProc("SetConsoleOutputCP")
	setConsoleCP.Call(uintptr(65001))
	setConsoleOutputCP.Call(uintptr(65001))

	// --- 2. 开启 ANSI 颜色支持 (Virtual Terminal Processing) ---
	stdout := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(stdout, &mode); err == nil {
		mode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
		if err := windows.SetConsoleMode(stdout, mode); err != nil {
			_ = os.Setenv("DEEPSENTRY_NO_VT", "1")
			_ = os.Setenv("DEEPSENTRY_NO_COLOR", "1")
		}
	}

	// QuickEdit freezes all console output once the user clicks the window,
	// until Enter or Esc. Startup then looks like a black screen that only
	// continues after Enter. The TUI has its own mouse selection and copy.
	//
	// ENABLE_MOUSE_INPUT together with virtual-terminal input makes conhost
	// turn every mouse move into an SGR report (ESC[<35;x;yM). The setup
	// wizard reads those bytes as keystrokes and redraws on each one, so the
	// window flashes for as long as the pointer moves. VT mouse tracking,
	// which the TUI enables itself, does not need this flag.
	stdin := windows.Handle(os.Stdin.Fd())
	if err := windows.GetConsoleMode(stdin, &mode); err == nil {
		windows.SetConsoleMode(stdin, windowsConsoleInputMode(mode))
	}
	// A previous crash can leave ?1003h active in Windows Terminal. Those
	// reports arrive even after ENABLE_MOUSE_INPUT is cleared.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		ui.DisableMouseTracking()
	}
}

// useSurveyConsoleInput makes the setup wizard see Up/Down/Left/Right and Tab
// as navigation keys. The returned function restores the previous mode.
func useSurveyConsoleInput() func() {
	stdin := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(stdin, &mode); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(stdin, windowsSurveyInputMode(mode)); err != nil {
		return func() {}
	}
	return func() {
		_ = windows.SetConsoleMode(stdin, mode)
	}
}

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}
