package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/harness/subagent"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type checkpointCommandExecutor struct {
	calls int
	stop  chan struct{}
	fail  bool
	crash bool
}

func (e *checkpointCommandExecutor) Run(string) (string, error) {
	e.calls++
	if e.stop != nil {
		close(e.stop)
		e.stop = nil
	}
	if e.fail {
		return "", errors.New("transport failed after dispatch")
	}
	if e.crash {
		panic("simulated process crash after dispatch")
	}
	return "verified output", nil
}
func (*checkpointCommandExecutor) ReadTargetFile(string) ([]byte, error) {
	return nil, errors.New("unused")
}
func (*checkpointCommandExecutor) ListTargetDir(string) ([]string, error) {
	return nil, errors.New("unused")
}
func (*checkpointCommandExecutor) IsRemote() bool { return true }
func (*checkpointCommandExecutor) Close()         {}

func newCheckpointTestParent(t *testing.T) *DeepAgent {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	const sessionID = "session_subagent_resume_test"
	store, err := NewCheckpointStore(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return &DeepAgent{State: NewAgentStateWithSession(t.TempDir(), sessionID), SessionID: sessionID, Checkpoint: store}
}

func TestSubAgentResumesAfterCompletedToolWithoutReplayingIt(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", SystemPrompt: "fixture", MaxSteps: 3}
	stop := make(chan struct{})
	exec := &checkpointCommandExecutor{stop: stop}
	first := NewSubAgentRunner(parent)
	first.Stop, first.Executor, first.MaxStepsCap = stop, exec, 3
	first.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		return analyzer.AgentResponse{Action: "execute", Command: "hostname", ToolCallID: "call_one", ToolCallName: "agent_action"}, nil
	}
	out, err := first.Run(spec, "inspect host", collector.SystemContext{}, true)
	if err != nil || !strings.Contains(out, "已按用户请求停止") || exec.calls != 1 {
		t.Fatalf("first run out=%q err=%v calls=%d", out, err, exec.calls)
	}
	snapshots, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(snapshots) != 1 || snapshots[0].StepNum != 1 || snapshots[0].SubAgent.Phase != "ready" {
		t.Fatalf("saved child boundary=%#v err=%v", snapshots, err)
	}
	if !strings.Contains(snapshots[0].History[len(snapshots[0].History)-1].Content, "verified output") {
		t.Fatalf("completed tool evidence missing: %#v", snapshots[0].History)
	}

	second := NewSubAgentRunner(parent)
	second.Executor, second.MaxStepsCap = exec, 3
	second.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
		if !strings.Contains((*opts.History)[len(*opts.History)-1].Content, "verified output") {
			t.Fatalf("resumed model did not receive completed tool result: %#v", *opts.History)
		}
		return analyzer.AgentResponse{Action: "finish", FinalReport: "done from checkpoint"}, nil
	}
	out, err = second.Run(spec, "inspect host", collector.SystemContext{}, true)
	if err != nil || out != "done from checkpoint" || exec.calls != 1 {
		t.Fatalf("resumed run out=%q err=%v calls=%d", out, err, exec.calls)
	}
}

func TestSubAgentUncertainActionIsNotReplayed(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", SystemPrompt: "fixture", MaxSteps: 3}
	exec := &checkpointCommandExecutor{crash: true}
	first := NewSubAgentRunner(parent)
	first.Executor, first.MaxStepsCap = exec, 3
	first.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		return analyzer.AgentResponse{Action: "execute", Command: "change-setting"}, nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated crash")
			}
		}()
		_, _ = first.Run(spec, "change setting", collector.SystemContext{}, true)
	}()
	exec.crash = false
	snapshots, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(snapshots) != 1 || snapshots[0].SubAgent.Phase != "action_pending" {
		t.Fatalf("uncertain action boundary=%#v err=%v", snapshots, err)
	}
	second := NewSubAgentRunner(parent)
	second.Executor, second.MaxStepsCap = exec, 3
	second.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
		if !strings.Contains((*opts.History)[len(*opts.History)-1].Content, "不要直接重放修改") {
			t.Fatalf("missing verify-before-retry instruction: %#v", *opts.History)
		}
		return analyzer.AgentResponse{Action: "finish", FinalReport: "needs read-only verification"}, nil
	}
	if _, err := second.Run(spec, "change setting", collector.SystemContext{}, true); err != nil || exec.calls != 1 {
		t.Fatalf("uncertain command replayed: err=%v calls=%d", err, exec.calls)
	}
}

func TestSubAgentInterruptedCommandTextKeepsPendingBoundary(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", MaxSteps: 2}
	stop := make(chan struct{})
	exec := &checkpointCommandExecutor{stop: stop, fail: true}
	child := NewSubAgentRunner(parent)
	child.Stop, child.Executor, child.MaxStepsCap = stop, exec, 2
	child.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		return analyzer.AgentResponse{Action: "execute", Command: "change-setting"}, nil
	}
	out, err := child.Run(spec, "change setting", collector.SystemContext{}, true)
	if err != nil || !strings.Contains(out, "结果未确认") {
		t.Fatalf("interrupted command out=%q err=%v", out, err)
	}
	snapshots, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(snapshots) != 1 || snapshots[0].SubAgent.Phase != "action_pending" {
		t.Fatalf("interrupted command lost uncertain boundary: %#v err=%v", snapshots, err)
	}
}

func TestParentResumeContinuesSavedChildAndAcknowledgesCompletion(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", SystemPrompt: "fixture", MaxSteps: 3}
	stop := make(chan struct{})
	exec := &checkpointCommandExecutor{stop: stop}
	child := NewSubAgentRunner(parent)
	child.Stop, child.Executor, child.MaxStepsCap = stop, exec, 3
	child.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		return analyzer.AgentResponse{Action: "execute", Command: "hostname"}, nil
	}
	if _, err := child.Run(spec, "inspect host", collector.SystemContext{}, true); err != nil {
		t.Fatal(err)
	}
	parentHistory := []analyzer.Message{{Role: "user", Content: "inspect host"}}
	if err := parent.Checkpoint.Save(CheckpointData{StepNum: 1, State: parent.State, History: parentHistory}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint(parent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := &DeepAgent{State: NewAgentState(""), SessionID: parent.SessionID, Checkpoint: parent.Checkpoint}
	resumed.RestoreFromCheckpoint(loaded)

	originalFactory := makeSubAgentRunner
	makeSubAgentRunner = func(agent *DeepAgent) *SubAgentRunner {
		runner := NewSubAgentRunner(agent)
		runner.Executor = exec
		runner.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
			if !strings.Contains((*opts.History)[len(*opts.History)-1].Content, "verified output") {
				t.Fatalf("parent resumed child without its saved result: %#v", *opts.History)
			}
			return analyzer.AgentResponse{Action: "finish", FinalReport: "child resumed"}, nil
		}
		return runner
	}
	defer func() { makeSubAgentRunner = originalFactory }()
	runResult := resumed.RunLoop(RunLoopConfig{
		History: &parentHistory, SubAgentMaxSteps: 3, BatchMode: true,
		MaxSteps: 2, UI: &runLoopCaptureUI{},
		ModelStep: func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
			if !strings.Contains((*opts.History)[len(*opts.History)-1].Content, "child resumed") {
				t.Fatalf("parent model ran before child recovery: %#v", *opts.History)
			}
			return analyzer.AgentResponse{Action: "finish", FinalReport: "parent done"}, nil
		},
	})
	if runResult.Status != RunStatusCompleted {
		t.Fatalf("parent run result=%+v", runResult)
	}
	joined := ""
	for _, message := range parentHistory {
		joined += message.Content
	}
	if exec.calls != 1 || !strings.Contains(joined, "child resumed") {
		t.Fatalf("parent did not receive resumed child: calls=%d history=%#v", exec.calls, parentHistory)
	}
	snapshots, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("completed child checkpoint not acknowledged: %#v err=%v", snapshots, err)
	}
}

func TestParentResumeCollectsAllCompletedChildrenBeforeAcknowledging(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", MaxSteps: 2}
	for _, task := range []string{"inspect logs", "inspect network"} {
		runner := NewSubAgentRunner(parent)
		runner.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			return analyzer.AgentResponse{Action: "finish", FinalReport: "completed " + task}, nil
		}
		if _, err := runner.Run(spec, task, collector.SystemContext{}, false); err != nil {
			t.Fatal(err)
		}
	}
	history := []analyzer.Message{{Role: "user", Content: "inspect"}}
	if err := parent.Checkpoint.Save(CheckpointData{State: parent.State, History: history}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint(parent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := &DeepAgent{State: NewAgentState(""), SessionID: parent.SessionID, Checkpoint: parent.Checkpoint}
	resumed.RestoreFromCheckpoint(loaded)
	if err := resumed.resumePendingSubAgents(RunLoopConfig{History: &history}, &runLoopCaptureUI{}, 0); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range history {
		joined += message.Content
	}
	if len(history) != 3 || !strings.Contains(joined, "completed inspect logs") || !strings.Contains(joined, "completed inspect network") {
		t.Fatalf("lost one completed child during parent recovery: %#v", history)
	}
	snapshots, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("completed snapshots were not acknowledged: %#v err=%v", snapshots, err)
	}
}

func TestParentDoesNotAutoResumeOldChildForNewUserTask(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", MaxSteps: 2}
	stop := make(chan struct{})
	close(stop)
	child := NewSubAgentRunner(parent)
	child.Stop = stop
	if _, err := child.Run(spec, "old task", collector.SystemContext{}, false); err != nil {
		t.Fatal(err)
	}
	completed := NewSubAgentRunner(parent)
	completed.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		return analyzer.AgentResponse{Action: "finish", FinalReport: "old result not yet delivered"}, nil
	}
	if _, err := completed.Run(spec, "other old task", collector.SystemContext{}, false); err != nil {
		t.Fatal(err)
	}
	history := []analyzer.Message{{Role: "user", Content: "old task"}}
	if err := parent.Checkpoint.Save(CheckpointData{State: parent.State, History: history}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint(parent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := &DeepAgent{State: NewAgentState(""), SessionID: parent.SessionID, Checkpoint: parent.Checkpoint}
	resumed.RestoreFromCheckpoint(loaded)
	history = append(history, analyzer.Message{Role: "user", Content: "现在改查另一台服务器"})
	if err := resumed.resumePendingSubAgents(RunLoopConfig{History: &history}, &runLoopCaptureUI{}, 0); err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("old child resumed despite new user task: %#v", history)
	}
	if !resumed.saveCheckpointUI(0, &history, &runLoopCaptureUI{}) {
		t.Fatal("parent failed to save new task")
	}
	snapshots, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("old child checkpoint should remain available: %#v err=%v", snapshots, err)
	}
	newTaskCheckpoint, err := LoadCheckpoint(parent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if newTaskCheckpoint.SubAgentKeys == nil || len(*newTaskCheckpoint.SubAgentKeys) != 0 {
		t.Fatalf("new task checkpoint still references old children: %#v", newTaskCheckpoint.SubAgentKeys)
	}
	stalePath := filepath.Join(parent.Checkpoint.SessionDir(), "subagents", snapshots[0].SubAgent.Key, "checkpoint.json")
	if err := os.WriteFile(stalePath, []byte("corrupt unrelated child"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := &DeepAgent{State: NewAgentState(""), SessionID: parent.SessionID, Checkpoint: parent.Checkpoint}
	again.RestoreFromCheckpoint(newTaskCheckpoint)
	history = append(history, analyzer.Message{Role: "user", Content: "需求：继续"})
	if err := again.resumePendingSubAgents(RunLoopConfig{History: &history}, &runLoopCaptureUI{}, 0); err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("disk resume revived children from prior task: %#v", history)
	}
}

func TestSameAgentContinueResumesOnlyInterruptedTurnChildren(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", SystemPrompt: "fixture", MaxSteps: 3}
	stoppedOld := make(chan struct{})
	close(stoppedOld)
	old := NewSubAgentRunner(parent)
	old.Stop = stoppedOld
	if _, err := old.Run(spec, "abandoned old task", collector.SystemContext{}, true); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	exec := &checkpointCommandExecutor{stop: stop}
	history := []analyzer.Message{{Role: "user", Content: "需求：inspect current host"}}
	first := parent.RunLoop(RunLoopConfig{
		History: &history, Stop: stop, MaxSteps: 3, BatchMode: true, UI: &runLoopCaptureUI{},
		ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			child := NewSubAgentRunner(parent)
			child.Stop, child.Executor = stop, exec
			child.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
				return analyzer.AgentResponse{Action: "execute", Command: "hostname"}, nil
			}
			if _, err := child.Run(spec, "inspect current host", collector.SystemContext{}, true); err != nil {
				t.Fatal(err)
			}
			return analyzer.AgentResponse{Action: "finish", FinalReport: "interrupted"}, nil
		},
	})
	if first.Status != RunStatusCancelled || !parent.resumedFromCheckpoint {
		t.Fatalf("same-process stop did not arm child recovery: result=%+v armed=%v", first, parent.resumedFromCheckpoint)
	}
	saved, err := LoadCheckpoint(parent.SessionID)
	if err != nil || saved.SubAgentKeys == nil || len(*saved.SubAgentKeys) != 1 {
		t.Fatalf("interrupted parent did not persist current child reference: checkpoint=%#v err=%v", saved, err)
	}
	history = append(history, analyzer.Message{Role: "user", Content: "需求：继续"})
	originalFactory := makeSubAgentRunner
	makeSubAgentRunner = func(agent *DeepAgent) *SubAgentRunner {
		runner := NewSubAgentRunner(agent)
		runner.Executor = exec
		runner.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			return analyzer.AgentResponse{Action: "finish", FinalReport: "current child resumed"}, nil
		}
		return runner
	}
	defer func() { makeSubAgentRunner = originalFactory }()
	second := parent.RunLoop(RunLoopConfig{
		History: &history, MaxSteps: 3, BatchMode: true, UI: &runLoopCaptureUI{},
		ModelStep: func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
			if !strings.Contains((*opts.History)[len(*opts.History)-1].Content, "current child resumed") {
				t.Fatalf("parent did not receive resumed child result: %#v", *opts.History)
			}
			return analyzer.AgentResponse{Action: "finish", FinalReport: "done"}, nil
		},
	})
	if second.Status != RunStatusCompleted || exec.calls != 1 {
		t.Fatalf("same-process recovery failed: result=%+v calls=%d", second, exec.calls)
	}
	snapshots, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(snapshots) != 1 || snapshots[0].SubAgent.TaskPrompt != "abandoned old task" {
		t.Fatalf("recovery touched unrelated child: snapshots=%#v err=%v", snapshots, err)
	}
}

func TestParentIndexesChildBeforeProcessCrashInsideChild(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", MaxSteps: 3}
	history := []analyzer.Message{{Role: "user", Content: "需求：inspect current host"}}
	exec := &checkpointCommandExecutor{crash: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated process crash")
			}
		}()
		parent.RunLoop(RunLoopConfig{
			History: &history, MaxSteps: 3, BatchMode: true, UI: &runLoopCaptureUI{},
			ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
				child := NewSubAgentRunner(parent)
				child.Executor = exec
				child.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
					return analyzer.AgentResponse{Action: "execute", Command: "change-setting"}, nil
				}
				_, _ = child.Run(spec, "inspect current host", collector.SystemContext{}, true)
				return analyzer.AgentResponse{Action: "finish"}, nil
			},
		})
	}()
	parentSnapshot, err := LoadCheckpoint(parent.SessionID)
	if err != nil || parentSnapshot.SubAgentKeys == nil || len(*parentSnapshot.SubAgentKeys) != 1 {
		t.Fatalf("parent did not durably index running child: snapshot=%#v err=%v", parentSnapshot, err)
	}
	children, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(children) != 1 || children[0].SubAgent.Phase != "action_pending" || children[0].SubAgent.Key != (*parentSnapshot.SubAgentKeys)[0] {
		t.Fatalf("child crash boundary not recoverable through parent: children=%#v err=%v", children, err)
	}
}

func TestFailedParentHandoffKeepsCompletedChildUntilResultIsDelivered(t *testing.T) {
	parent := newCheckpointTestParent(t)
	spec := subagent.Spec{Name: "general-purpose", MaxSteps: 2}
	history := []analyzer.Message{{Role: "user", Content: "检查两个方向"}}
	modelCalls := 0
	handled := 0
	result := parent.RunLoop(RunLoopConfig{
		History: &history, MaxSteps: 3, BatchMode: true, UI: &runLoopCaptureUI{},
		ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			modelCalls++
			if modelCalls <= 2 {
				return analyzer.AgentResponse{Action: "task", TaskName: spec.Name, TaskPrompt: "direction"}, nil
			}
			return analyzer.AgentResponse{Action: "finish", FinalReport: "handoff complete"}, nil
		},
		ActionHandler: func(*StepContext, *AgentAction) (*ActionResult, error) {
			handled++
			assignment := "undelivered child"
			if handled == 2 {
				assignment = "delivered child"
			}
			child := NewSubAgentRunner(parent)
			child.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
				return analyzer.AgentResponse{Action: "finish", FinalReport: assignment + " result"}, nil
			}
			out, err := child.Run(spec, assignment, collector.SystemContext{}, true)
			if err != nil {
				return nil, err
			}
			if handled == 1 {
				return nil, errors.New("parent handoff failed after child completion")
			}
			return &ActionResult{Output: out}, nil
		},
	})
	if result.Status != RunStatusCompleted || handled != 2 {
		t.Fatalf("unexpected parent result=%+v handled=%d", result, handled)
	}
	children, err := listSubAgentCheckpoints(parent.Checkpoint)
	if err != nil || len(children) != 1 || children[0].SubAgent.TaskPrompt != "undelivered child" {
		t.Fatalf("undelivered child was cleaned or delivered child was retained: children=%#v err=%v", children, err)
	}
	loaded, err := LoadCheckpoint(parent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := &DeepAgent{State: NewAgentState(""), SessionID: parent.SessionID, Checkpoint: parent.Checkpoint}
	resumed.RestoreFromCheckpoint(loaded)
	recoveredHistory := append([]analyzer.Message(nil), loaded.History...)
	if err := resumed.resumePendingSubAgents(RunLoopConfig{History: &recoveredHistory}, &runLoopCaptureUI{}, loaded.StepNum); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recoveredHistory[len(recoveredHistory)-1].Content, "undelivered child result") {
		t.Fatalf("undelivered child result was not recovered: %#v", recoveredHistory)
	}
}

func TestSubAgentCheckpointExcludesTargetCredentials(t *testing.T) {
	parent := newCheckpointTestParent(t)
	const secret = "credential-only-in-current-config-7425"
	target := config.TargetConfig{Name: "target-a", Protocol: "ssh", Host: "192.0.2.15", User: "admin", Password: secret}
	runner := NewSubAgentRunner(parent)
	runner.Target = target
	runner.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		return analyzer.AgentResponse{Action: "finish", FinalReport: "checked"}, nil
	}
	spec := subagent.Spec{Name: "general-purpose", MaxSteps: 2}
	if _, err := runner.Run(spec, "inspect target", collector.SystemContext{}, false); err != nil {
		t.Fatal(err)
	}
	key := subAgentCheckpointKey(parent.SessionID, spec.Name, "inspect target", target)
	raw, err := os.ReadFile(filepath.Join(parent.Checkpoint.SessionDir(), "subagents", key, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("target password leaked into child checkpoint")
	}
	if subAgentCheckpointKey(parent.SessionID, spec.Name, "inspect target", config.TargetConfig{Name: "target-b", Protocol: "ssh", Host: "192.0.2.16"}) == key {
		t.Fatal("different targets share a child recovery identity")
	}
}
