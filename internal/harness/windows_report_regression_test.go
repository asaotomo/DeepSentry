package harness

import (
	"ai-edr/internal/executor"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTurnCanRecoverAfterLoopHalt(t *testing.T) {
	state := NewAgentState(t.TempDir())
	state.StallCount = maxStallWarnings
	state.Memory["evidence"] = "keep"
	state.ResetLoopTurn()
	if d := state.LoopBeforeExecute(AgentAction{Type: ActionReadFile, Path: "different.txt"}); !d.Allow {
		t.Fatalf("new turn blocked: %+v", d)
	}
	if state.Memory["evidence"] != "keep" {
		t.Fatal("evidence discarded")
	}
}

func TestReadFileDoesNotExposeBinaryDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "block.db")
	if err := os.WriteFile(path, []byte("SQLite format 3\x00private binary records"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &FilesystemMiddleware{}
	result, _, err := m.readFile(&StepContext{Executor: &executor.LocalExecutor{}}, path, 0, 0)
	if err != nil || !strings.Contains(result.Output, "二进制") || strings.Contains(result.Output, "private binary") {
		t.Fatalf("unexpected binary result: %+v %v", result, err)
	}
}
