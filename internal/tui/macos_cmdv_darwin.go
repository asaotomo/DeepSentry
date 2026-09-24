//go:build darwin

package tui

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ebitengine/purego"
)

const (
	cgEventSourceStateHID = 1
	keyCodeCommand        = 0x37
	keyCodeRightCommand   = 0x36
	keyCodeShift          = 0x38
	keyCodeRightShift     = 0x3C
	keyCodeA              = 0x00
	keyCodeZ              = 0x06
	keyCodeV              = 0x09
)

var (
	hidOnce     sync.Once
	hidKeyState func(int32, uint16) bool

	macEditOnce  sync.Once
	macEditTapOn atomic.Bool
	macEditRunCF uintptr

	cgEventTapCreate            func(uint32, uint32, uint32, uint64, uintptr, uintptr) uintptr
	cgEventTapEnable            func(uintptr, bool)
	cgEventGetFlags             func(uintptr) uint64
	cgEventGetIntegerValueField func(uintptr, uint32) int64
	cfMachPortCreateRunLoopSrc  func(uintptr, uintptr, int) uintptr
	cfRunLoopGetCurrent         func() uintptr
	cfRunLoopAddSource          func(uintptr, uintptr, uintptr)
	cfRunLoopRun                func()
	cfRunLoopStop               func(uintptr)
	kCFRunLoopCommonModes       uintptr
)

type macOSPasteSession struct {
	hostPID  int
	hostName string
	tty      string
	skipTTY  bool
}

func startMacOSCmdVWatcher(p *tea.Program) func() {
	if p == nil {
		return func() {}
	}
	loadHIDKeyState()
	if hidKeyState == nil {
		return func() {}
	}
	session := resolveMacOSPasteSession(os.Getpid(), currentTTYPath())
	stop := make(chan struct{})
	fire := func(msg tea.Msg) {
		defer func() { _ = recover() }()
		p.Send(msg)
	}
	stopTap := startMacEditTap(session, fire)
	go pollMacOSCmdV(stop, session, fire)
	return func() {
		close(stop)
		stopTap()
	}
}

func loadHIDKeyState() {
	hidOnce.Do(func() {
		core, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW)
		if err != nil {
			return
		}
		purego.RegisterLibFunc(&hidKeyState, core, "CGEventSourceKeyState")
	})
}

func startMacEditTap(_ macOSPasteSession, fire func(tea.Msg)) func() {
	loadMacEditTap()
	if cgEventTapCreate == nil || cfRunLoopRun == nil {
		return func() {}
	}
	callback := purego.NewCallback(func(_, typ, event, _ uintptr) uintptr {
		const (
			keyDown         = 10
			tapDisabled     = 0xFFFFFFFE
			keyboardKeycode = 9
		)
		if typ == tapDisabled {
			if tap := macEditTap; tap != 0 && cgEventTapEnable != nil {
				cgEventTapEnable(tap, true)
			}
			return 0
		}
		if typ != keyDown || event == 0 || cgEventGetFlags == nil || !macHostFocused.Load() {
			return event
		}
		msg, swallow := macEditChordToSwallow(cgEventGetFlags(event), cgEventGetIntegerValueField(event, keyboardKeycode))
		if !swallow {
			return event
		}
		if teaMsg, ok := msg.(tea.Msg); ok {
			fire(teaMsg)
		}
		return 0
	})
	const (
		sessionTap  = 1
		headInsert  = 0
		defaultOpts = 0
		keyDown     = 10
		eventMask   = uint64(1) << keyDown
	)
	tap := cgEventTapCreate(sessionTap, headInsert, defaultOpts, eventMask, callback, 0)
	if tap == 0 {
		return func() {}
	}
	source := cfMachPortCreateRunLoopSrc(0, tap, 0)
	if source == 0 {
		return func() {}
	}
	macEditTap = tap
	macEditTapOn.Store(true)
	go func() {
		rl := cfRunLoopGetCurrent()
		macEditRunCF = rl
		cfRunLoopAddSource(rl, source, kCFRunLoopCommonModes)
		cgEventTapEnable(tap, true)
		cfRunLoopRun()
	}()
	return func() {
		if rl := macEditRunCF; rl != 0 && cfRunLoopStop != nil {
			cfRunLoopStop(rl)
		}
	}
}

var (
	macHostFocused atomic.Bool
	macEditTap     uintptr
)

func loadMacEditTap() {
	macEditOnce.Do(func() {
		defer func() { _ = recover() }()
		core, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW)
		if err != nil {
			return
		}
		foundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW)
		if err != nil {
			return
		}
		purego.RegisterLibFunc(&cgEventTapCreate, core, "CGEventTapCreate")
		purego.RegisterLibFunc(&cgEventTapEnable, core, "CGEventTapEnable")
		purego.RegisterLibFunc(&cgEventGetFlags, core, "CGEventGetFlags")
		purego.RegisterLibFunc(&cgEventGetIntegerValueField, core, "CGEventGetIntegerValueField")
		purego.RegisterLibFunc(&cfMachPortCreateRunLoopSrc, foundation, "CFMachPortCreateRunLoopSource")
		purego.RegisterLibFunc(&cfRunLoopGetCurrent, foundation, "CFRunLoopGetCurrent")
		purego.RegisterLibFunc(&cfRunLoopAddSource, foundation, "CFRunLoopAddSource")
		purego.RegisterLibFunc(&cfRunLoopRun, foundation, "CFRunLoopRun")
		purego.RegisterLibFunc(&cfRunLoopStop, foundation, "CFRunLoopStop")
		sym, err := purego.Dlsym(foundation, "kCFRunLoopCommonModes")
		if err != nil || sym == 0 {
			cgEventTapCreate = nil
			return
		}
		kCFRunLoopCommonModes = **(**uintptr)(unsafe.Pointer(&sym))
	})
}

func pollMacOSCmdV(stop <-chan struct{}, session macOSPasteSession, fire func(tea.Msg)) {
	var wasV, wasA, wasZ bool
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			v, a, z := macCommandChords()
			focused := deepSentryWindowFocused(session)
			macHostFocused.Store(focused)
			if !focused {
				wasV, wasA, wasZ = v, a, z
				continue
			}
			if v && !wasV {
				fire(macosCmdVMsg{})
			}
			// The event tap already delivers and swallows these. Polling them
			// too would undo twice, and would not stop the terminal select-all.
			if !macEditTapOn.Load() {
				if a && !wasA {
					fire(macosCmdAMsg{})
				}
				if z && !wasZ {
					fire(macosCmdZMsg{})
				}
			}
			wasV, wasA, wasZ = v, a, z
		}
	}
}

func macCommandChords() (v, a, z bool) {
	if hidKeyState == nil {
		return false, false, false
	}
	cmd := hidKeyState(cgEventSourceStateHID, keyCodeCommand) || hidKeyState(cgEventSourceStateHID, keyCodeRightCommand)
	if !cmd {
		return false, false, false
	}
	shift := hidKeyState(cgEventSourceStateHID, keyCodeShift) || hidKeyState(cgEventSourceStateHID, keyCodeRightShift)
	return hidKeyState(cgEventSourceStateHID, keyCodeV),
		hidKeyState(cgEventSourceStateHID, keyCodeA),
		!shift && hidKeyState(cgEventSourceStateHID, keyCodeZ)
}

func deepSentryWindowFocused(session macOSPasteSession) bool {
	frontPID, err := frontmostAppPID()
	if err != nil || frontPID <= 0 || session.hostPID != frontPID {
		return false
	}
	frontTTY := ""
	if !session.skipTTY {
		frontTTY = frontTerminalTTY(session.hostName)
	}
	return pasteTargetFocused(session, frontPID, frontTTY)
}

func pasteTargetFocused(session macOSPasteSession, frontPID int, frontTTY string) bool {
	if session.hostPID <= 0 || frontPID <= 0 || session.hostPID != frontPID {
		return false
	}
	if session.skipTTY || session.tty == "" || frontTTY == "" {
		return true
	}
	return sameTTY(session.tty, frontTTY)
}

func resolveMacOSPasteSession(pid int, tty string) macOSPasteSession {
	hostPID, hostName := resolvePasteHost(pid)
	return macOSPasteSession{
		hostPID:  hostPID,
		hostName: hostName,
		tty:      tty,
		skipTTY:  os.Getenv("TMUX") != "" || os.Getenv("STY") != "",
	}
}

func resolvePasteHost(pid int) (int, string) {
	seen := map[int]bool{}
	for i := 0; i < 24 && pid > 1 && !seen[pid]; i++ {
		seen[pid] = true
		name := processComm(pid)
		if isPasteHostApp(name) {
			return pid, name
		}
		ppid := processPPID(pid)
		if ppid <= 1 || ppid == pid {
			break
		}
		pid = ppid
	}
	return 0, ""
}

func isPasteHostApp(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	if strings.Contains(base, "helper") {
		return false
	}
	for _, hint := range []string{
		"terminal", "iterm", "alacritty", "kitty", "ghostty", "warp", "wezterm",
		"tabby", "hyper", "rio", "cursor", "code", "windsurf", "zed", "vscode",
	} {
		if strings.Contains(base, hint) {
			return true
		}
	}
	return false
}

func currentTTYPath() string {
	if tty := normalizeTTY(runPS("-o", "tty=", "-p", strconv.Itoa(os.Getpid()))); tty != "" {
		return tty
	}
	cmd := exec.Command("/usr/bin/tty")
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return normalizeTTY(string(out))
}

func normalizeTTY(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.TrimPrefix(value, "/dev/")
	if value == "" || value == "not a tty" || value == "??" {
		return ""
	}
	return value
}

func sameTTY(left, right string) bool {
	return normalizeTTY(left) != "" && normalizeTTY(left) == normalizeTTY(right)
}

func processComm(pid int) string {
	return strings.TrimSpace(runPS("-o", "comm=", "-p", strconv.Itoa(pid)))
}

func processPPID(pid int) int {
	raw := strings.TrimSpace(runPS("-o", "ppid=", "-p", strconv.Itoa(pid)))
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

func runPS(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/ps", args...).Output()
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(out))
}

func frontmostAppPID() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	asn, err := exec.CommandContext(ctx, "/usr/bin/lsappinfo", "front").Output()
	if err != nil {
		return 0, err
	}
	info, err := exec.CommandContext(ctx, "/usr/bin/lsappinfo", "info", "-only", "pid", strings.TrimSpace(string(asn))).Output()
	if err != nil {
		return 0, err
	}
	return parseLSAppPID(string(info))
}

func parseLSAppPID(raw string) (int, error) {
	// "pid"=86131
	trimmed := strings.TrimSpace(raw)
	idx := strings.LastIndex(trimmed, "=")
	if idx < 0 {
		return 0, os.ErrInvalid
	}
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(trimmed[idx+1:]), `"`))
	if err != nil || n <= 0 {
		return 0, os.ErrInvalid
	}
	return n, nil
}

func frontTerminalTTY(hostName string) string {
	name := strings.ToLower(hostName)
	script := ""
	switch {
	case strings.Contains(name, "iterm"):
		script = `tell application "iTerm" to tty of current session of current window`
	case strings.Contains(name, "terminal"):
		script = `tell application "Terminal" to get tty of selected tab of front window`
	default:
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script).Output()
	if err != nil {
		return ""
	}
	return normalizeTTY(string(out))
}
