//go:build windows

package tui

import (
	"os"
	"strings"
	"time"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/windows"
)

const (
	vkControl  = 0x11
	vkLControl = 0xA2
	vkRControl = 0xA3
	vkV        = 0x56

	gaRootOwner = 3
	gwOwner     = 4
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	procGetAncestor         = user32.NewProc("GetAncestor")
	procGetWindow           = user32.NewProc("GetWindow")
	procGetConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
)

func startWindowsCtrlVWatcher(p *tea.Program) func() {
	if p == nil {
		return func() {}
	}
	stop := make(chan struct{})
	go pollWindowsCtrlV(stop, os.Getpid(), func() {
		defer func() { _ = recover() }()
		p.Send(macosCmdVMsg{})
	})
	return func() { close(stop) }
}

func pollWindowsCtrlV(stop <-chan struct{}, self int, fire func()) {
	wasDown := false
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	var ancestors []int
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			down := controlVPressed()
			if down && !wasDown {
				if ancestors == nil {
					ancestors = windowsAncestorPIDs(self)
				}
				if windowsConsoleFocused(self, ancestors) {
					fire()
				}
			}
			wasDown = down
		}
	}
}

func controlVPressed() bool {
	ctrl := keyDown(vkControl) || keyDown(vkLControl) || keyDown(vkRControl)
	return ctrl && keyDown(vkV)
}

func keyDown(vk uintptr) bool {
	r, _, _ := procGetAsyncKeyState.Call(vk)
	return r&0x8000 != 0
}

// windowsConsoleFocused covers both console hosts. Classic conhost owns a real
// console window. Windows Terminal (the Win11 default) hosts a hidden
// pseudo-console window owned by the terminal window, and after default-
// terminal handoff WindowsTerminal.exe is not in our parent chain at all.
func windowsConsoleFocused(self int, ancestors []int) bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	console, _, _ := procGetConsoleWindow.Call()
	if console != 0 {
		if console == fg {
			return true
		}
		if owner, _, _ := procGetWindow.Call(console, gwOwner); owner != 0 && owner == fg {
			return true
		}
		if root, _, _ := procGetAncestor.Call(console, gaRootOwner); root != 0 && root == fg {
			return true
		}
	}
	var pid uint32
	procGetWindowThreadPID.Call(fg, uintptr(unsafe.Pointer(&pid)))
	if windowsPasteTargetFocused(self, ancestors, int(pid)) {
		return true
	}
	// Older Windows Terminal builds leave the pseudo-console window unowned.
	// Then the terminal being frontmost is the best signal available.
	if console != 0 {
		if owner, _, _ := procGetWindow.Call(console, gwOwner); owner == 0 {
			_, name := windowsProcessParent(pid)
			return isWindowsTerminalHost(name)
		}
	}
	return false
}

func isWindowsTerminalHost(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "windowsterminal.exe" || name == "wt.exe"
}

func windowsAncestorPIDs(self int) []int {
	out := []int{}
	pid := uint32(self)
	seen := map[uint32]bool{}
	for i := 0; i < 24 && pid > 1 && !seen[pid]; i++ {
		seen[pid] = true
		parent, _ := windowsProcessParent(pid)
		if parent == 0 || parent == pid {
			break
		}
		out = append(out, int(parent))
		pid = parent
	}
	return out
}

func windowsProcessParent(pid uint32) (uint32, string) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, ""
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return 0, ""
	}
	for {
		if entry.ProcessID == pid {
			name := windows.UTF16ToString(entry.ExeFile[:])
			return entry.ParentProcessID, name
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return 0, ""
}
