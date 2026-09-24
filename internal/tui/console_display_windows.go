//go:build windows

package tui

import (
	"context"
	"os"
	"strings"
	"time"
	"unsafe"

	"ai-edr/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// CONSOLE_FONT_INFOEX has identical layout on 32- and 64-bit Windows.
type consoleFontInfo struct {
	Size   uint32
	Index  uint32
	Cell   windows.Coord
	Family uint32
	Weight uint32
	Face   [32]uint16
}

// Raster/SimSun console fonts have poor Latin/CJK alignment. Use Consolas
// for this TUI only, preserving the original font and size on exit. Leave
// Windows Terminal, user-selected modern fonts and redirected output alone.
func prepareConsoleDisplay() func() {
	noop := func() {}
	if !ui.LegacyConsole() || os.Getenv("DEEPSENTRY_KEEP_CONSOLE_FONT") == "1" || !term.IsTerminal(int(os.Stdout.Fd())) {
		return noop
	}
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	get := kernel.NewProc("GetCurrentConsoleFontEx")
	set := kernel.NewProc("SetCurrentConsoleFontEx")
	if get.Find() != nil || set.Find() != nil {
		return noop
	}
	handle := os.Stdout.Fd()
	old := consoleFontInfo{}
	old.Size = uint32(unsafe.Sizeof(old))
	if ok, _, _ := get.Call(handle, 0, uintptr(unsafe.Pointer(&old))); ok == 0 {
		return noop
	}
	face := strings.ToLower(windows.UTF16ToString(old.Face[:]))
	if old.Family&4 != 0 && face != "simsun" && face != "nsimsun" && face != "宋体" && face != "新宋体" {
		return noop
	}
	font := old
	font.Cell.X = 0
	font.Family, font.Weight = 54, 400
	font.Face = [32]uint16{}
	name, _ := windows.UTF16FromString("Consolas")
	copy(font.Face[:], name)
	if ok, _, _ := set.Call(handle, 0, uintptr(unsafe.Pointer(&font))); ok == 0 {
		return noop
	}
	return func() { set.Call(handle, 0, uintptr(unsafe.Pointer(&old))) }
}

func startConsoleResizeWatcher(p *tea.Program) func() {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ticker := time.NewTicker(200 * time.Millisecond)
	go watchTerminalSize(ctx, ticker.C, func() (int, int, error) { return term.GetSize(int(os.Stdout.Fd())) }, p.Send)
	return func() { cancel(); ticker.Stop() }
}
