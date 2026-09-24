//go:build !windows

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"
)

func TestWinHelperReusesProcessAndReturnsData(t *testing.T) {
	orig := newWinHelperCmd
	t.Cleanup(func() { newWinHelperCmd = orig })
	starts := 0
	newWinHelperCmd = func() *exec.Cmd {
		starts++
		return exec.CommandContext(context.Background(), "sh", "-c", `while IFS= read -r line; do printf '%s\n' '{"ok":true,"data":{"driver":"test","ready":true,"width":100,"height":100}}'; done`)
	}
	h := &winHelper{}
	t.Cleanup(func() { h.mu.Lock(); h.stopLocked(); h.mu.Unlock() })

	for i := 0; i < 3; i++ {
		out, err := h.call(context.Background(), []byte(`{"action":"status"}`), true)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		var st State
		if err := json.Unmarshal(out, &st); err != nil {
			t.Fatalf("call %d bad payload %q: %v", i, out, err)
		}
		if st.Width != 100 || !st.Ready {
			t.Fatalf("call %d unexpected state %+v", i, st)
		}
	}
	if starts != 1 {
		t.Fatalf("persistent helper restarted %d times", starts)
	}
}

func TestWinHelperActionErrorDoesNotFallBack(t *testing.T) {
	orig := newWinHelperCmd
	t.Cleanup(func() { newWinHelperCmd = orig })
	newWinHelperCmd = func() *exec.Cmd {
		return exec.CommandContext(context.Background(), "sh", "-c", `while IFS= read -r line; do printf '%s\n' '{"ok":false,"error":"UIPI blocked"}'; done`)
	}
	h := &winHelper{}
	t.Cleanup(func() { h.mu.Lock(); h.stopLocked(); h.mu.Unlock() })

	_, err := h.call(context.Background(), []byte(`{"action":"click","x":1,"y":1}`), false)
	if err == nil || errors.Is(err, errWinHelperUnavailable) {
		t.Fatalf("action error must surface, got %v", err)
	}
	if err.Error() != "UIPI blocked" {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestWinHelperBrokenStreamFallsBack(t *testing.T) {
	orig := newWinHelperCmd
	t.Cleanup(func() { newWinHelperCmd = orig })
	newWinHelperCmd = func() *exec.Cmd { return exec.CommandContext(context.Background(), "sh", "-c", "exit 0") }
	h := &winHelper{}
	t.Cleanup(func() { h.mu.Lock(); h.stopLocked(); h.mu.Unlock() })

	_, err := h.call(context.Background(), []byte(`{"action":"status"}`), true)
	if !errors.Is(err, errWinHelperUnavailable) {
		t.Fatalf("dead helper should report unavailable for fallback, got %v", err)
	}
}

func TestWinHelperDisabledByEnv(t *testing.T) {
	t.Setenv("DEEPSENTRY_COMPUTER_USE_PERSISTENT", "0")
	h := &winHelper{}
	if _, err := h.call(context.Background(), []byte(`{"action":"status"}`), true); !errors.Is(err, errWinHelperUnavailable) {
		t.Fatalf("opt-out should disable the persistent helper, got %v", err)
	}
}
