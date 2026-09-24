package chat

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestProcessAliveSelfAndBogusPID(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
	if processAlive(1 << 30) {
		t.Fatal("bogus pid should not look alive")
	}
}

func TestWatchParentStopsWhenParentGone(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(parentPIDEnv, strconv.Itoa(cmd.Process.Pid))
	stopped := make(chan struct{})
	watchParent(func() { close(stopped) })
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("parent watch did not stop after parent exited")
	}
}

func TestOwnerStopIgnoresMissingDaemon(t *testing.T) {
	owner := &Owner{}
	if err := owner.Stop(config.ChatConfig{Store: t.TempDir() + "/jobs.json"}); err != nil {
		t.Fatalf("missing daemon should be ignored: %v", err)
	}
}
