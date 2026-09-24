package harness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskContextSurvivesRestoreAndPrompt(t *testing.T) {
	state := NewAgentStateWithSession(t.TempDir(), "session_long_test")
	step := &StepContext{State: state}
	handleTaskContext(step, &AgentAction{ToolArgs: map[string]string{"action": "update", "goal": "验证下载后解压", "progress": "下载已开始", "next_step": "核验 sha256", "side_effects": "已经开始下载，勿重复", "evidence": "/tmp/output.zip"}})
	raw, e := json.Marshal(CheckpointData{State: state})
	if e != nil {
		t.Fatal(e)
	}
	var restored CheckpointData
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	prompt := NewContextMiddleware().EnhancePrompt("", restored.State)
	for _, s := range []string{"核验 sha256", "勿重复", "/tmp/output.zip"} {
		if !strings.Contains(prompt, s) {
			t.Fatalf("lost context %q", s)
		}
	}
	handleTaskContext(step, &AgentAction{ToolArgs: map[string]string{"action": "update", "goal": "changed", "evidence": strings.Repeat("字", 1501)}})
	if !strings.Contains(state.workContextPrompt(), "验证下载后解压") {
		t.Fatal("invalid update partially changed state")
	}
}
func TestWaitToolStopsWithoutModelPolling(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	start := time.Now()
	r := handleTaskWait(&StepContext{Stop: stop}, &AgentAction{ToolArgs: map[string]string{"mode": "delay", "timeout_sec": "7200"}})
	if time.Since(start) > time.Second || !strings.Contains(r.Output, "cancelled") {
		t.Fatalf("stop failed: %s", r.Output)
	}
}
