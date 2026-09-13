package harness

import (
	"ai-edr/internal/collector"
	"ai-edr/internal/harness/subagent"
	"errors"
	"fmt"
	"sync"
	"testing"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
)

func TestAdaptiveRunContinuesWithEvidenceAndFinishes(t *testing.T) {
	a := &DeepAgent{State: NewAgentState(t.TempDir()), SessionID: "adaptive"}
	h := []analyzer.Message{{Role: "user", Content: "inspect"}}
	policy := config.ExecutionBudgetConfig{Enabled: true, MaxSteps: 8, TotalSteps: 12, ExtensionSteps: 2, NoProgressSteps: 4}
	calls := 0
	result := a.RunLoop(RunLoopConfig{History: &h, ChatReply: true, PlanMode: true, BatchMode: true, MaxSteps: 2, UI: &runLoopCaptureUI{}, Executor: &injectedExecutor{}, ExecutionBudget: &policy,
		ModelStep: func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
			calls++
			if calls == 5 {
				return analyzer.AgentResponse{Action: "finish", FinalReport: "verified"}, nil
			}
			return analyzer.AgentResponse{Action: "read_file", Path: fmt.Sprintf("/evidence/%d", calls)}, nil
		},
		ActionHandler: func(_ *StepContext, action *AgentAction) (*ActionResult, error) {
			return &ActionResult{Output: "verified data for " + action.Path}, nil
		},
	})
	if !result.Successful() || calls != 5 {
		t.Fatalf("%+v calls=%d", result, calls)
	}
	used, _ := a.budgetLedger.snapshot()
	if used != 5 {
		t.Fatal(used)
	}
}

func TestAdaptiveLeaseRejectsRepeatedOutputAndHonorsHardCap(t *testing.T) {
	l := newExecutionLease(2, false, config.ExecutionBudgetConfig{MaxSteps: 4, ExtensionSteps: 2, NoProgressSteps: 3})
	l.stalled++
	l.observe(AgentAction{Type: ActionReadFile}, &ActionResult{Output: "same"}, nil)
	if ok, _ := l.allow(2); !ok || l.limit != 4 {
		t.Fatal("did not extend")
	}
	for i := 0; i < 3; i++ {
		l.stalled++
		l.observe(AgentAction{Type: ActionReadFile, Path: fmt.Sprint(i)}, &ActionResult{Output: "same"}, nil)
	}
	if ok, reason := l.allow(3); ok || reason != "no_progress" {
		t.Fatalf("%v %s", ok, reason)
	}
	l.observe(AgentAction{Type: ActionReadFile}, &ActionResult{Output: "new"}, nil)
	if ok, reason := l.allow(4); ok || reason != "hard_step_budget" {
		t.Fatalf("%v %s", ok, reason)
	}
}

func TestSharedExecutionBudgetConcurrentWorkersReserveParent(t *testing.T) {
	l := &executionLedger{limit: 30}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for l.take(true) {
			}
		}()
	}
	wg.Wait()
	used, _ := l.snapshot()
	if used != 28 {
		t.Fatal(used)
	}
	first, second, extra := l.take(false), l.take(false), l.take(false)
	if !first || !second || extra {
		t.Fatal("parent reserve or global cap broken")
	}
}

func TestAdaptiveErrorsAndPlanningDoNotRenew(t *testing.T) {
	for _, action := range []AgentAction{{Type: ActionTodo}, {Type: ActionRemember}, {Type: ActionLoadSkill}} {
		l := newExecutionLease(1, false, config.ExecutionBudgetConfig{})
		l.observe(action, &ActionResult{Output: "new planning text"}, nil)
		if ok, _ := l.allow(1); ok {
			t.Fatal("planning renewed budget")
		}
	}
	l := newExecutionLease(1, false, config.ExecutionBudgetConfig{})
	l.observe(AgentAction{Type: ActionTool}, &ActionResult{Output: "new error", ToolResults: []ToolCallResult{{Error: "failed"}}}, nil)
	if ok, _ := l.allow(1); ok {
		t.Fatal("failed tool renewed budget")
	}
}

func TestAdaptiveWorkerSharedBudgetReturnsIncompleteEvidence(t *testing.T) {
	parent := &DeepAgent{State: NewAgentState(t.TempDir()), SessionID: "budget-worker", budgetPolicy: config.ExecutionBudgetConfig{Enabled: true}, budgetLedger: &executionLedger{limit: 2}}
	worker := NewSubAgentRunner(parent)
	worker.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		t.Fatal("worker spent parent reserve")
		return analyzer.AgentResponse{}, nil
	}
	_, err := worker.Run(subagent.Spec{Name: "worker", MaxSteps: 3}, "inspect", collector.SystemContext{}, false)
	var incomplete *SubAgentIncompleteError
	if !errors.As(err, &incomplete) || incomplete.Reason != "shared_step_budget" {
		t.Fatalf("%v", err)
	}
}
