package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf16"
)

// errWinHelperUnavailable means the persistent helper could not carry the
// request (never started, or its stream broke before we wrote anything). The
// caller may safely cold-start a single-shot helper. It is deliberately distinct
// from a helper-reported action error, which must surface instead of retrying.
var errWinHelperUnavailable = errors.New("windows desktop helper unavailable")

func windowsPowerShell() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, `System32\WindowsPowerShell\v1.0\powershell.exe`)
}

func windowsEncodedCommand() string {
	encoded := utf16.Encode([]rune(windowsScript))
	raw := make([]byte, len(encoded)*2)
	for i, c := range encoded {
		binary.LittleEndian.PutUint16(raw[i*2:], c)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// newWinHelperCmd builds the persistent PowerShell command. A background context
// keeps it valid for configureDesktopCommand's Cancel hook without ever
// auto-killing the process; teardown is manual. It is a package variable so
// tests can substitute a cross-platform stub.
var newWinHelperCmd = func() *exec.Cmd {
	cmd := exec.CommandContext(context.Background(), windowsPowerShell(), "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", windowsEncodedCommand())
	cmd.Env = append(os.Environ(), "DEEPSENTRY_DESKTOP_SERVE=1")
	return cmd
}

// winHelper keeps one PowerShell process alive so Add-Type and DPI setup run
// once instead of on every action. Calls are serialized; any transport fault
// tears the process down so the next call starts a clean one (or falls back).
type winHelper struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan []byte
}

var winPersistent = &winHelper{}

func (h *winHelper) startLocked() error {
	cmd := newWinHelperCmd()
	configureDesktopCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	lines := make(chan []byte, 4)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			b := bytes.TrimSpace(sc.Bytes())
			if len(b) == 0 {
				continue
			}
			lines <- append([]byte(nil), b...)
		}
	}()
	h.cmd = cmd
	h.stdin = stdin
	h.lines = lines
	return nil
}

func (h *winHelper) stopLocked() {
	if h.stdin != nil {
		_ = h.stdin.Close()
	}
	if h.cmd != nil {
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		_ = h.cmd.Wait()
	}
	h.cmd = nil
	h.stdin = nil
	h.lines = nil
}

// call sends one request to the persistent helper and returns the raw data
// payload. idempotent marks read-only or repeatable actions (status, capture,
// elements); only those are allowed to report errWinHelperUnavailable after the
// request was written, because re-running them cannot double-apply an input.
func (h *winHelper) call(ctx context.Context, input []byte, idempotent bool) ([]byte, error) {
	if os.Getenv("DEEPSENTRY_COMPUTER_USE_PERSISTENT") == "0" {
		return nil, errWinHelperUnavailable
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cmd == nil {
		if err := h.startLocked(); err != nil {
			h.stopLocked()
			return nil, errWinHelperUnavailable
		}
	}
	line := append(bytes.TrimRight(input, "\r\n"), '\n')
	if _, err := h.stdin.Write(line); err != nil {
		h.stopLocked()
		return nil, errWinHelperUnavailable
	}
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		h.stopLocked()
		return nil, ctx.Err()
	case <-timer.C:
		h.stopLocked()
		if idempotent {
			return nil, errWinHelperUnavailable
		}
		return nil, errors.New("windows desktop helper timed out")
	case raw, ok := <-h.lines:
		if !ok {
			h.stopLocked()
			return nil, errWinHelperUnavailable
		}
		var resp struct {
			OK    bool            `json:"ok"`
			Error string          `json:"error"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &resp) != nil {
			h.stopLocked()
			return nil, errWinHelperUnavailable
		}
		if !resp.OK {
			msg := resp.Error
			if msg == "" {
				msg = "windows desktop helper error"
			}
			return nil, errors.New(msg)
		}
		if len(resp.Data) == 0 || string(resp.Data) == "null" {
			return nil, nil
		}
		return resp.Data, nil
	}
}
