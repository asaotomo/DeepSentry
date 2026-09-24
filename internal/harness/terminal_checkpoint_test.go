package harness

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/analyzer"
)

func TestRunLoopPersistsEmptyActionTerminalReport(t *testing.T) {
	agent := newCheckpointTestParent(t)
	history := []analyzer.Message{{Role: "user", Content: "检查主机"}}
	result := agent.RunLoop(RunLoopConfig{
		History: &history, MaxSteps: 5, BatchMode: true, UI: &runLoopCaptureUI{},
		ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			return analyzer.AgentResponse{Thought: "still thinking"}, nil
		},
	})
	if result.Status != RunStatusFailed || result.Reason != "empty_action_limit" {
		t.Fatalf("unexpected result: %+v", result)
	}
	loaded, err := LoadCheckpoint(agent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.StepNum != 3 || len(loaded.History) != len(history) || !strings.Contains(loaded.History[len(loaded.History)-1].Content, "异常终止") {
		t.Fatalf("terminal report was not saved: step=%d history=%#v", loaded.StepNum, loaded.History)
	}
}

func TestMainActionCrashRequiresVerificationBeforeIdenticalReplay(t *testing.T) {
	agent := newCheckpointTestParent(t)
	history := []analyzer.Message{{Role: "user", Content: "更新目标配置并复核"}}
	dispatched := 0
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("expected simulated process crash")
			}
		}()
		agent.RunLoop(RunLoopConfig{
			History: &history, MaxSteps: 4, BatchMode: true, UI: &runLoopCaptureUI{},
			ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
				return analyzer.AgentResponse{Action: "execute", Command: "update-target-configuration"}, nil
			},
			ActionHandler: func(*StepContext, *AgentAction) (*ActionResult, error) {
				dispatched++
				panic("simulated crash after dispatch")
			},
		})
	}()
	if dispatched != 1 {
		t.Fatalf("first action dispatched %d times", dispatched)
	}
	saved, err := LoadCheckpoint(agent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State.PendingAction == "" || saved.State.PendingActionHash == "" {
		t.Fatalf("main action boundary missing: %#v", saved.State)
	}
	resumed := &DeepAgent{State: NewAgentState(""), SessionID: agent.SessionID, Checkpoint: agent.Checkpoint}
	resumed.RestoreFromCheckpoint(saved)
	history = saved.History
	steps := 0
	var executed []ActionType
	result := resumed.RunLoop(RunLoopConfig{
		History: &history, MaxSteps: 6, BatchMode: true, UI: &runLoopCaptureUI{},
		ModelStep: func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
			steps++
			if steps == 1 && !strings.Contains((*opts.History)[len(*opts.History)-1].Content, "上一动作可能已经执行") {
				t.Error("recovery warning was not supplied to the model")
			}
			switch steps {
			case 1, 3:
				return analyzer.AgentResponse{Action: "execute", Command: "update-target-configuration"}, nil
			case 2:
				return analyzer.AgentResponse{Action: "read_file", Path: "/tmp/target-state"}, nil
			default:
				return analyzer.AgentResponse{Action: "finish", FinalReport: "verified"}, nil
			}
		},
		ActionHandler: func(ctx *StepContext, action *AgentAction) (*ActionResult, error) {
			executed = append(executed, action.Type)
			if action.Type == ActionFinish {
				return resumed.HandleAction(ctx, action)
			}
			if action.Type == ActionReadFile {
				return &ActionResult{Output: "[视角: 目标机]\nverified state"}, nil
			}
			return &ActionResult{Output: "verified state"}, nil
		},
	})
	if result.Status != RunStatusCompleted || len(executed) != 2 || executed[0] != ActionReadFile || executed[1] != ActionExecute {
		t.Fatalf("recovery allowed duplicate before verification: result=%+v actions=%#v", result, executed)
	}
	final, err := LoadCheckpoint(agent.SessionID)
	if err != nil || final.State.PendingAction != "" || final.State.RecoveryBlockedHash != "" {
		t.Fatalf("recovery boundary not cleared: checkpoint=%#v err=%v", final, err)
	}
}

func TestMainActionDoesNotRunIfBoundaryCannotBeSaved(t *testing.T) {
	agent := newCheckpointTestParent(t)
	history := []analyzer.Message{{Role: "user", Content: "执行目标变更"}}
	executed := false
	result := agent.RunLoop(RunLoopConfig{
		History: &history, MaxSteps: 2, BatchMode: true, UI: &runLoopCaptureUI{},
		ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			agent.Checkpoint.dir = filepath.Join(t.TempDir(), "missing")
			return analyzer.AgentResponse{Action: "execute", Command: "update-target-configuration"}, nil
		},
		ActionHandler: func(*StepContext, *AgentAction) (*ActionResult, error) {
			executed = true
			return &ActionResult{Output: "unexpected"}, nil
		},
	})
	if executed || result.Status != RunStatusFailed || result.Reason != "checkpoint_action_boundary_failed" {
		t.Fatalf("action ran without durable boundary: executed=%v result=%+v", executed, result)
	}
}

func TestFailedReadDoesNotClearUncertainActionGuard(t *testing.T) {
	if clearsRecoveryReplayBlock(AgentAction{Type: ActionReadFile}, &ActionResult{Output: "读取失败: permission denied"}) {
		t.Fatal("failed read was treated as verification")
	}
	if !clearsRecoveryReplayBlock(AgentAction{Type: ActionReadFile}, &ActionResult{Output: "[视角: 目标机]\nconfig=ok"}) {
		t.Fatal("successful read should clear the immediate replay guard")
	}
}

func TestRunLoopPersistsToolFailureBeforeNextModelStep(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler ActionHandlerFunc
		want    string
	}{
		{"error", func(*StepContext, *AgentAction) (*ActionResult, error) { return nil, errors.New("transport failed") }, "transport failed"},
		{"nil-result", func(*StepContext, *AgentAction) (*ActionResult, error) { return nil, nil }, "返回空结果"},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := newCheckpointTestParent(t)
			history := []analyzer.Message{{Role: "user", Content: "检查主机"}}
			steps := 0
			result := agent.RunLoop(RunLoopConfig{
				History: &history, MaxSteps: 2, BatchMode: true, UI: &runLoopCaptureUI{}, ActionHandler: test.handler,
				ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
					steps++
					if steps == 1 {
						return analyzer.AgentResponse{Action: "execute", Command: "hostname"}, nil
					}
					loaded, err := LoadCheckpoint(agent.SessionID)
					if err != nil || loaded.StepNum != 1 || !strings.Contains(loaded.History[len(loaded.History)-1].Content, test.want) {
						t.Fatalf("tool failure was not durable before next model step: checkpoint=%#v err=%v", loaded, err)
					}
					return analyzer.AgentResponse{Action: "finish", FinalReport: "reported failure"}, nil
				},
			})
			if result.Status != RunStatusCompleted || steps != 2 {
				t.Fatalf("unexpected result=%+v steps=%d", result, steps)
			}
		})
	}
}

func TestRunLoopPersistsLoopGuardTerminalReport(t *testing.T) {
	agent := newCheckpointTestParent(t)
	history := []analyzer.Message{{Role: "user", Content: "检查主机"}}
	result := agent.RunLoop(RunLoopConfig{
		History: &history, MaxSteps: 3, BatchMode: true, UI: &runLoopCaptureUI{},
		ModelStep: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			agent.State.StallCount = maxStallWarnings
			return analyzer.AgentResponse{Action: "execute", Command: "pwd"}, nil
		},
	})
	if result.Status != RunStatusFailed || result.Reason != "loop_hygiene_limit" {
		t.Fatalf("unexpected result: %+v", result)
	}
	loaded, err := LoadCheckpoint(agent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.StepNum != 1 || len(loaded.History) != len(history) || !strings.Contains(loaded.History[len(loaded.History)-1].Content, "循环守卫") {
		t.Fatalf("loop guard report was not saved: step=%d history=%#v", loaded.StepNum, loaded.History)
	}
}
