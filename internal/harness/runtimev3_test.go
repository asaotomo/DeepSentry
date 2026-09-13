package harness

import (
	"context"
	"errors"
	"testing"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/runtimev3"
)

type injectedExecutor struct {
	commands []string
}

func (e *injectedExecutor) Run(command string) (string, error) {
	e.commands = append(e.commands, command)
	if command == "fail" {
		return "", errors.New("fixture failure")
	}
	return "Linux fixture-host 6.6.0", nil
}
func (e *injectedExecutor) ReadTargetFile(string) ([]byte, error)  { return nil, errors.New("unused") }
func (e *injectedExecutor) ListTargetDir(string) ([]string, error) { return nil, errors.New("unused") }
func (e *injectedExecutor) IsRemote() bool                         { return true }
func (e *injectedExecutor) Close()                                 {}

type runtimeCaptureSink struct{ events []runtimev3.RunEvent }

func (s *runtimeCaptureSink) Emit(_ context.Context, event runtimev3.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}

type runLoopCaptureUI struct{ events []UIEvent }

func (s *runLoopCaptureUI) Emit(event UIEvent) { s.events = append(s.events, event) }

func TestParseActionPreservesNativeToolBatchAndNestedArguments(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{Action: "tool_batch", NativeToolCalls: []analyzer.NativeToolCall{
		{ID: "call_1", Name: "process_list", Arguments: `{"limit":20}`},
		{ID: "call_2", Name: "custom", Arguments: `{"filters":{"status":"open"}}`},
	}})
	if action.Type != ActionToolBatch || len(action.ToolCalls) != 2 || action.ToolCalls[0].ID != "call_1" || action.ToolCalls[0].Args["limit"] != "20" || action.ToolCalls[1].Args["filters"] != `{"status":"open"}` {
		t.Fatalf("action=%#v", action)
	}
}

func TestParseActionUnwrapsProviderNativeToolEnvelope(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{Action: "tool_batch", NativeToolCalls: []analyzer.NativeToolCall{{
		ID:   "call_download",
		Name: "file_download",
		Arguments: `{"action":"tool","tool_name":"file_download","tool_args":{"remote_path":"/evidence.bin","local_path":"/tmp/evidence.bin"},` +
			`"command":"","content":"","is_finished":false,"options":null,"todos":null}`,
	}}})
	if len(action.ToolCalls) != 1 {
		t.Fatalf("tool calls=%#v", action.ToolCalls)
	}
	args := action.ToolCalls[0].Args
	if len(args) != 2 || args["remote_path"] != "/evidence.bin" || args["local_path"] != "/tmp/evidence.bin" {
		t.Fatalf("provider envelope was not unwrapped: %#v", args)
	}
}

func TestParseActionUnwrapsSingleMCPToolEnvelope(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{
		Action:   "tool",
		ToolName: "fofamap__fofa_icon_search",
		ToolArgs: parseNativeToolArgs(`{"action":"tool","tool_name":"fofamap__fofa_icon_search","tool_args":{"url":"https://www.bilibili.com","size":"3"},"thought":"x","is_finished":false}`),
	})
	if action.Type != ActionTool || action.ToolArgs["url"] != "https://www.bilibili.com" || action.ToolArgs["size"] != "3" || action.ToolArgs["tool_args"] != "" {
		t.Fatalf("single MCP envelope was not unwrapped: %#v", action)
	}
}

func TestParseActionUnwrapsMCPEnvelopeWithoutExactToolName(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{
		Action:   "tool",
		ToolName: "fofamap__fofa_host_profile",
		ToolArgs: parseNativeToolArgs(`{"action":"tool","tool_args":{"host":"1.1.1.1"},"thought":"x"}`),
	})
	if action.ToolArgs["host"] != "1.1.1.1" || action.ToolArgs["thought"] != "" {
		t.Fatalf("empty tool_name envelope should still unwrap: %#v", action.ToolArgs)
	}
}

func TestNativeToolEnvelopeRequiresMatchingToolName(t *testing.T) {
	args := parseNativeToolArgs(`{"action":"tool","tool_name":"file_upload","tool_args":{"remote_path":"/evidence.bin"}}`)
	got := unwrapNativeToolEnvelope("file_download", args)
	if got["tool_name"] != "file_upload" || got["remote_path"] != "" {
		t.Fatalf("mismatched envelope must remain untouched: %#v", got)
	}
}

func TestAppendActionResultHistoryUsesProviderToolProtocol(t *testing.T) {
	action := AgentAction{Type: ActionToolBatch, ReasoningContent: "signed-thought", ToolCalls: []ToolCallAction{{ID: "call_1", Name: "process_list", Args: map[string]string{"limit": "20"}}}}
	result := &ActionResult{ToolResults: []ToolCallResult{{ID: "call_1", Name: "process_list", Output: "evidence"}}}
	var history []analyzer.Message
	appendActionResultHistory(&history, action, result)
	if len(history) != 2 || history[0].Role != "assistant" || history[0].ReasoningContent != "signed-thought" || len(history[0].ToolCalls) != 1 || history[1].Role != "tool" || history[1].ToolCallID != "call_1" {
		t.Fatalf("history=%#v", history)
	}
}

func TestAppendActionResultHistorySplitsLargeImageEvidenceBatch(t *testing.T) {
	action := AgentAction{Type: ActionToolBatch, ToolCalls: []ToolCallAction{{ID: "call_1", Name: "browser_screenshot"}}}
	attachments := make([]analyzer.ImageAttachment, analyzer.MaxImagesPerMessage+1)
	for i := range attachments {
		attachments[i] = analyzer.ImageAttachment{Path: "/tmp/example.png", Size: 1}
	}
	result := &ActionResult{ToolResults: []ToolCallResult{{ID: "call_1", Name: "browser_screenshot", Output: "ok", Attachments: attachments}}}
	var history []analyzer.Message
	appendActionResultHistory(&history, action, result)
	if len(history) != 4 || len(history[2].Attachments) != analyzer.MaxImagesPerMessage || len(history[3].Attachments) != 1 {
		t.Fatalf("image evidence was not split safely: %#v", history)
	}
}

func TestPrepareToolCallExecutionSkipsRecoveredModifyingCall(t *testing.T) {
	state := NewAgentState("")
	state.BeginToolCall(ToolCallRecord{ID: "call_1", Name: "config_manage", Risk: "high"})
	action := AgentAction{Type: ActionTool, ToolCallID: "call_1", ToolName: "config_manage", ToolArgs: map[string]string{"action": "set"}}
	prepareToolCallExecution(state, &action)
	if !action.SkipToolCallIDs["call_1"] {
		t.Fatal("pending modifying call was scheduled twice")
	}
}

func TestPrepareToolCallExecutionSkipsRecoveredMutationWithRegeneratedID(t *testing.T) {
	state := NewAgentState("")
	original := AgentAction{Type: ActionTool, ToolCallID: "call_old", ToolName: "config_manage", ToolArgs: map[string]string{"action": "set_ssh", "host": "10.0.0.1", "user": "root"}}
	prepareToolCallExecution(state, &original)
	if original.SkipToolCallIDs["call_old"] {
		t.Fatal("first call should be recorded, not skipped")
	}

	recovered := AgentAction{Type: ActionTool, ToolCallID: "call_new", ToolName: "config_manage", ToolArgs: map[string]string{"user": "root", "host": "10.0.0.1", "action": "set_ssh"}}
	prepareToolCallExecution(state, &recovered)
	if !recovered.SkipToolCallIDs["call_new"] {
		t.Fatal("same pending mutation with a regenerated provider ID was scheduled twice")
	}
}

func TestFailedLowRiskToolCanRetryButMutationRemainsPending(t *testing.T) {
	state := NewAgentState("")
	state.BeginToolCall(ToolCallRecord{ID: "read_1", Name: "read_log", Risk: "low"})
	state.BeginToolCall(ToolCallRecord{ID: "write_1", Name: "config_manage", Risk: "high"})
	state.FailToolCall("read_1")
	state.FailToolCall("write_1")
	if _, ok := state.ToolCallPending("read_1"); ok {
		t.Fatal("failed idempotent call should be released for retry")
	}
	if _, ok := state.ToolCallPending("write_1"); !ok {
		t.Fatal("failed modifying call must remain pending until explicitly reconciled")
	}
}

func TestRunLoopUsesInjectedModelAndExecutorEndToEnd(t *testing.T) {
	exec := &injectedExecutor{}
	ui := &runLoopCaptureUI{}
	runtimeEvents := &runtimeCaptureSink{}
	agent := &DeepAgent{
		State:          NewAgentState(""),
		UseNativeTools: true,
		SessionID:      "session_fixture",
		RunID:          "run_fixture",
		Events:         &runtimev3.SequenceSink{Sink: runtimeEvents},
	}
	history := []analyzer.Message{{Role: "user", Content: "inspect fixture host"}}
	step := 0
	model := func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
		step++
		if opts.History != &history {
			t.Fatal("runtime did not pass its owned history to injected model")
		}
		if step == 1 {
			return analyzer.AgentResponse{
				Action:       "execute",
				Command:      "uname -a",
				ToolCallID:   "call_1",
				ToolCallName: "agent_action",
			}, nil
		}
		return analyzer.AgentResponse{Action: "finish", FinalReport: "fixture complete", IsFinished: true}, nil
	}

	runResult := agent.RunLoop(RunLoopConfig{
		History:   &history,
		BatchMode: true,
		PlanMode:  true,
		MaxSteps:  3,
		UI:        ui,
		ModelStep: model,
		Executor:  exec,
	})
	if runResult.Status != RunStatusCompleted || runResult.Reason != "agent_finish" || runResult.Step != 2 {
		t.Fatalf("unexpected run result: %+v", runResult)
	}

	if step != 2 || len(exec.commands) != 1 || exec.commands[0] != "uname -a" {
		t.Fatalf("steps=%d commands=%#v", step, exec.commands)
	}
	if len(history) < 3 || history[1].Role != "assistant" || len(history[1].ToolCalls) != 1 || history[2].Role != "tool" || history[2].ToolCallID != "call_1" {
		t.Fatalf("provider-native history was not preserved: %#v", history)
	}
	seenToolEnd := false
	for _, event := range runtimeEvents.events {
		if event.Kind == runtimev3.EventToolEnd && event.ToolCallID == "call_1" {
			seenToolEnd = true
		}
	}
	if !seenToolEnd {
		t.Fatalf("tool span was not emitted: %#v", runtimeEvents.events)
	}
	seenFinish := false
	for _, event := range ui.events {
		if event.Kind == EventFinish && event.Message == "fixture complete" {
			seenFinish = true
		}
	}
	if !seenFinish {
		t.Fatalf("finish event missing: %#v", ui.events)
	}
}

func TestRunLoopCoversAskEmptyErrorDenialAndToolFailureLifecycles(t *testing.T) {
	tests := []struct {
		name           string
		responses      []analyzer.AgentResponse
		modelErr       error
		nonInteractive bool
		confirm        func(*AgentAction) bool
		handler        ActionHandlerFunc
		wantKind       EventKind
		wantExec       int
		wantStatus     RunStatus
	}{
		{
			name: "noninteractive ask continues",
			responses: []analyzer.AgentResponse{
				{Action: "ask_user", Question: "optional?"},
				{Action: "finish", IsFinished: true, FinalReport: "continued"},
			},
			nonInteractive: true, wantKind: EventFinish, wantStatus: RunStatusCompleted,
		},
		{
			name:       "interactive ask checkpoints",
			responses:  []analyzer.AgentResponse{{Action: "ask_user", Question: "required?"}},
			wantKind:   EventAwaitUser,
			wantStatus: RunStatusAwaitingInput,
		},
		{
			name:      "empty action stops after guard",
			responses: []analyzer.AgentResponse{{}, {}, {}},
			wantKind:  EventFinish, wantStatus: RunStatusFailed,
		},
		{
			name:     "model error checkpoints and exits",
			modelErr: errors.New("provider unavailable"), wantKind: EventError, wantStatus: RunStatusFailed,
		},
		{
			name:      "high risk denial returns to model",
			responses: []analyzer.AgentResponse{{Action: "execute", Command: "rm -rf /tmp/fixture"}, {Action: "finish", IsFinished: true, FinalReport: "denied safely"}},
			confirm:   func(*AgentAction) bool { return false }, wantKind: EventDenied, wantStatus: RunStatusCompleted,
		},
		{
			name:      "tool failure returns structured feedback",
			responses: []analyzer.AgentResponse{{Action: "tool", ToolName: "read_log", ToolCallID: "read_fail", ToolCallName: "read_log"}, {Action: "finish", IsFinished: true, FinalReport: "recovered"}},
			handler: func(_ *StepContext, _ *AgentAction) (*ActionResult, error) {
				return &ActionResult{Output: "fixture read failed"}, errors.New("connection reset")
			},
			wantKind: EventError, wantStatus: RunStatusCompleted,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := &injectedExecutor{}
			ui := &runLoopCaptureUI{}
			agent := &DeepAgent{State: NewAgentState(""), RunID: "run_branches", SessionID: "session_branches"}
			history := []analyzer.Message{{Role: "user", Content: "fixture"}}
			index := 0
			model := func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
				if tc.modelErr != nil {
					return analyzer.AgentResponse{}, tc.modelErr
				}
				if index >= len(tc.responses) {
					return analyzer.AgentResponse{Action: "finish", IsFinished: true, FinalReport: "fallback"}, nil
				}
				response := tc.responses[index]
				index++
				return response, nil
			}
			runResult := agent.RunLoop(RunLoopConfig{
				History: &history, BatchMode: false, NonInteractive: tc.nonInteractive, PlanMode: true,
				MaxSteps: 4, UI: ui, ModelStep: model, Executor: exec, ConfirmFn: tc.confirm, ActionHandler: tc.handler,
			})
			if runResult.Status != tc.wantStatus {
				t.Fatalf("status=%s want %s (%+v)", runResult.Status, tc.wantStatus, runResult)
			}
			seen := false
			for _, event := range ui.events {
				if event.Kind == tc.wantKind {
					seen = true
				}
			}
			if !seen {
				t.Fatalf("missing event %s in %#v", tc.wantKind, ui.events)
			}
			if len(exec.commands) != tc.wantExec {
				t.Fatalf("commands=%#v", exec.commands)
			}
		})
	}
}

func TestChatReplyResetsExhaustedStepBudget(t *testing.T) {
	ui := &runLoopCaptureUI{}
	agent := &DeepAgent{State: NewAgentState(""), RunID: "run_chat_budget", SessionID: "session_chat_budget", StartStep: 30}
	history := []analyzer.Message{{Role: "user", Content: "在吗"}}
	called := 0
	model := func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		called++
		return analyzer.AgentResponse{Action: "finish", IsFinished: true, FinalReport: "在的"}, nil
	}
	runResult := agent.RunLoop(RunLoopConfig{
		History: &history, ChatReply: true, PlanMode: true, MaxSteps: 30, UI: ui, ModelStep: model, Executor: &injectedExecutor{},
	})
	if runResult.Status != RunStatusCompleted {
		t.Fatalf("status=%s want completed (%+v) called=%d", runResult.Status, runResult, called)
	}
	if called != 1 {
		t.Fatalf("model steps=%d want 1", called)
	}

	agent.StartStep = 30
	called = 0
	runResult = agent.RunLoop(RunLoopConfig{
		History: &history, ChatReply: false, PlanMode: true, MaxSteps: 30, UI: ui, ModelStep: model, Executor: &injectedExecutor{},
	})
	if runResult.Status != RunStatusMaxSteps || called != 0 {
		t.Fatalf("classic resume should stay exhausted: status=%s called=%d", runResult.Status, called)
	}
}
