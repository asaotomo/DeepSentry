package desktop

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

//go:embed helpers/windows.ps1
var windowsScript string

type nativeBackend struct{}

func newNativeBackend() Backend { return nativeBackend{} }
func command(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	configureDesktopCommand(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := simplifyHelperError(stderr.String())
		if detail == "" {
			detail = stderr.String()
		}
		return nil, fmt.Errorf("%s failed: %w: %.500s", filepath.Base(name), err, strings.TrimSpace(detail))
	}
	return stdout.Bytes(), nil
}

// simplifyHelperError turns Windows PowerShell's redirected CLIXML error
// stream into the first human-readable message. Progress records and stack
// frames are dropped so the model does not treat the XML as a new tool to rebuild.
func simplifyHelperError(stderr string) string {
	if !strings.Contains(stderr, `<S S="Error">`) {
		return stderr
	}
	rest := stderr
	for {
		start := strings.Index(rest, `<S S="Error">`)
		if start < 0 {
			break
		}
		rest = rest[start+len(`<S S="Error">`):]
		end := strings.Index(rest, `</S>`)
		if end < 0 {
			break
		}
		text := decodePowerShellXMLText(rest[:end])
		rest = rest[end+len(`</S>`):]
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "+") || strings.Contains(text, "CategoryInfo") || strings.Contains(text, "FullyQualifiedErrorId") {
			continue
		}
		return text
	}
	return stderr
}

func decodePowerShellXMLText(text string) string {
	replacer := strings.NewReplacer(
		"_x000D__x000A_", "\n",
		"_x000D_", "\r",
		"_x000A_", "\n",
		"&lt;", "<",
		"&gt;", ">",
		"&amp;", "&",
	)
	return replacer.Replace(text)
}

func helperPath() (string, error) {
	if p := os.Getenv("DEEPSENTRY_COMPUTER_HELPER"); p != "" {
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("DEEPSENTRY_COMPUTER_HELPER must be an absolute path")
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	for _, p := range []string{filepath.Join(filepath.Dir(exe), "computer-use-helper"), filepath.Join(filepath.Dir(exe), "bin", "computer-use-helper")} {
		if st, e := os.Stat(p); e == nil && !st.IsDir() {
			return filepath.Abs(p)
		}
	}
	return "", fmt.Errorf("macOS helper missing: place the signed computer-use-helper next to DeepSentry (source builds: scripts/build-computer-helper.sh)")
}
func (nativeBackend) helper(ctx context.Context, r Request) ([]byte, error) {
	input, _ := json.Marshal(r)
	if runtime.GOOS == "darwin" {
		p, err := helperPath()
		if err != nil {
			return nil, err
		}
		return command(ctx, input, p)
	}
	if runtime.GOOS == "windows" {
		idempotent := r.Action == "status" || r.Action == "capture" || r.Action == "elements"
		out, err := winPersistent.call(ctx, input, idempotent)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, errWinHelperUnavailable) {
			return nil, err
		}
		// The persistent stream is unavailable; cold-start a single-shot helper.
		return command(ctx, input, windowsPowerShell(), "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", windowsEncodedCommand())
	}
	return nil, fmt.Errorf("no native helper on %s", runtime.GOOS)
}

// RequestPermissions shows the macOS Accessibility and Screen Recording
// prompts. Other platforms have no consent prompt to trigger.
func RequestPermissions(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	_, err := nativeBackend{}.helper(ctx, Request{Action: "request_permissions"})
	return err
}

func (n nativeBackend) Status(ctx context.Context) (State, error) {
	if runtime.GOOS != "linux" {
		raw, err := n.helper(ctx, Request{Action: "status"})
		if err != nil {
			return State{}, err
		}
		var state State
		err = json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), &state)
		return state, err
	}
	state := State{Driver: "x11-xdotool", Surface: "X11 root desktop"}
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" || os.Getenv("WAYLAND_DISPLAY") != "" {
		state.Reason = "Wayland session: X11 injection is not desktop-wide; a consented RemoteDesktop portal backend is required"
		return state, nil
	}
	if os.Getenv("DISPLAY") == "" {
		state.Reason = "No interactive X11 DISPLAY; run in the logged-in graphical session"
		return state, nil
	}
	for _, name := range []string{"xdotool", "import"} {
		if _, err := exec.LookPath(name); err != nil {
			state.Reason = "Missing " + name + " (install xdotool and ImageMagick for this X11 desktop)"
			return state, nil
		}
	}
	raw, err := command(ctx, nil, "xdotool", "getdisplaygeometry")
	if err != nil {
		return state, err
	}
	if _, err = fmt.Sscanf(string(raw), "%d %d", &state.Width, &state.Height); err != nil {
		return state, err
	}
	raw, err = command(ctx, nil, "xdotool", "getactivewindow")
	if err != nil {
		return state, fmt.Errorf("cannot identify active X11 window: %w", err)
	}
	state.Foreground = strings.TrimSpace(string(raw))
	state.Ready = state.Width > 0 && state.Height > 0
	return state, nil
}
func (n nativeBackend) Capture(ctx context.Context, path string) error {
	if runtime.GOOS == "linux" {
		_, err := command(ctx, nil, "import", "-window", "root", "png:"+path)
		return err
	}
	_, err := n.helper(ctx, Request{Action: "capture", Path: path})
	return err
}
func (n nativeBackend) Elements(ctx context.Context) (string, []Element, error) {
	if runtime.GOOS != "darwin" {
		return "unsupported", nil, nil
	}
	raw, err := n.helper(ctx, Request{Action: "elements"})
	if err != nil {
		return "vision", nil, err
	}
	var payload struct {
		Elements []Element `json:"elements"`
	}
	if err = json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), &payload); err != nil || len(payload.Elements) == 0 {
		return "vision", nil, nil
	}
	return "element", payload.Elements, nil
}
func (n nativeBackend) Input(ctx context.Context, r Request) error {
	if runtime.GOOS != "linux" {
		_, err := n.helper(ctx, r)
		return err
	}
	switch r.Action {
	case "click_element", "press_element", "set_value":
		return fmt.Errorf("click_element is not supported on this platform; use screenshot x,y")
	}
	button := map[string]string{"left": "1", "middle": "2", "right": "3"}[r.Button]
	move := []string{"mousemove", strconv.Itoa(r.X), strconv.Itoa(r.Y)}
	var args []string
	switch r.Action {
	case "move":
		args = move
	case "click":
		args = append(move, "click", button)
	case "double_click":
		args = append(move, "click", "--repeat", "2", "--delay", "100", button)
	case "drag":
		args = append(move, "mousedown", button, "mousemove", strconv.Itoa(r.ToX), strconv.Itoa(r.ToY), "mouseup", button)
	case "scroll":
		button = "5"
		amount := r.Amount
		if amount < 0 {
			button = "4"
			amount = -amount
		}
		args = []string{"click", "--repeat", strconv.Itoa(amount), "--delay", "30", button}
		if r.HasPoint {
			args = append([]string{"mousemove", strconv.Itoa(r.X), strconv.Itoa(r.Y)}, args...)
		}
	case "type":
		_, err := command(ctx, []byte(r.Text), "xdotool", "type", "--clearmodifiers", "--delay", "1", "--file", "-")
		return err
	case "key":
		parts := strings.Split(strings.ToUpper(r.Key), "+")
		aliases := map[string]string{"CTRL": "ctrl", "ALT": "alt", "SHIFT": "shift", "META": "super", "ENTER": "Return", "ESCAPE": "Escape", "SPACE": "space", "TAB": "Tab", "BACKSPACE": "BackSpace", "DELETE": "Delete", "UP": "Up", "DOWN": "Down", "LEFT": "Left", "RIGHT": "Right", "HOME": "Home", "END": "End", "PAGEUP": "Prior", "PAGEDOWN": "Next"}
		for i, p := range parts {
			if alias, ok := aliases[p]; ok {
				parts[i] = alias
			} else {
				parts[i] = strings.ToLower(p)
			}
		}
		args = []string{"key", "--clearmodifiers", strings.Join(parts, "+")}
	default:
		return fmt.Errorf("unsupported native input")
	}
	_, err := command(ctx, nil, "xdotool", args...)
	return err
}
func (n nativeBackend) Release(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		_, err := n.helper(ctx, Request{Action: "release"})
		return err
	}
	_, err := command(ctx, nil, "xdotool", "mouseup", "1", "mouseup", "2", "mouseup", "3", "keyup", "ctrl", "alt", "shift", "super")
	return err
}
