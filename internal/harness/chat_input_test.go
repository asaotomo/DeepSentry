package harness

import (
	"ai-edr/internal/analyzer"
	"testing"
)

func TestChatSupplementDuringInferencePreventsStaleAction(t *testing.T) {
	agent := &DeepAgent{State: NewAgentState(""), UseNativeTools: true, SessionID: "chat_input_fixture"}
	exec := &injectedExecutor{}
	history := []analyzer.Message{{Role: "user", Content: "inspect"}}
	pending := false
	steps := 0
	result := agent.RunLoop(RunLoopConfig{History: &history, BatchMode: true, PlanMode: true, MaxSteps: 3, UI: &runLoopCaptureUI{}, Executor: exec,
		DrainInput: func() []analyzer.Message {
			if !pending {
				return nil
			}
			pending = false
			return []analyzer.Message{{Role: "user", Content: "do not execute; just explain"}}
		},
		ModelStep: func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
			steps++
			if steps == 1 {
				pending = true
				return analyzer.AgentResponse{Action: "execute", Command: "should-not-run"}, nil
			}
			found := false
			for _, m := range *opts.History {
				if m.Content == "do not execute; just explain" {
					found = true
				}
			}
			if !found {
				t.Fatal("supplement absent from next model call")
			}
			return analyzer.AgentResponse{Action: "finish", FinalReport: "explained", IsFinished: true}, nil
		},
	})
	if !result.Successful() || steps != 2 || len(exec.commands) != 0 {
		t.Fatalf("stale action executed: %+v commands=%v", result, exec.commands)
	}
}
