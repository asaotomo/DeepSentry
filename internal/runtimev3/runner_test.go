package runtimev3

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

type scriptedModel struct {
	mu        sync.Mutex
	responses []ModelResponse
	requests  []ModelRequest
}

func (m *scriptedModel) Generate(_ context.Context, request ModelRequest) (ModelResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

func (m *scriptedModel) Stream(context.Context, ModelRequest) (StreamReader, error) { return nil, nil }

type echoTool struct{ calls int }

func (t *echoTool) Info(context.Context) (ToolInfo, error) {
	return ToolInfo{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`), RiskLevel: "low", Idempotent: true}, nil
}

func (t *echoTool) Invoke(_ context.Context, arguments json.RawMessage) (ToolResult, error) {
	t.calls++
	return ToolResult{Name: "echo", Blocks: []ContentBlock{{Kind: ContentText, Text: string(arguments)}}}, nil
}

type memoryCheckpoint struct{ snapshots []ExecutionSnapshot }

func (m *memoryCheckpoint) Save(_ context.Context, snapshot ExecutionSnapshot) error {
	m.snapshots = append(m.snapshots, snapshot)
	return nil
}

func TestRunnerExecutesToolAndCheckpointsSafePoints(t *testing.T) {
	call := ToolCall{ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"value":"ok"}`)}
	model := &scriptedModel{responses: []ModelResponse{
		{Message: Message{Role: "assistant", Blocks: []ContentBlock{{Kind: ContentToolCall, ToolCall: &call}}}},
		{Message: Message{Role: "assistant", Blocks: []ContentBlock{{Kind: ContentText, Text: "done"}}}},
	}}
	tool := &echoTool{}
	checkpoints := &memoryCheckpoint{}
	runner := Runner{
		Models:     &Router{Endpoints: []Endpoint{{ID: "primary", Client: model}}},
		Tools:      &ToolExecutor{Registry: NewToolRegistry(tool)},
		Checkpoint: checkpoints,
		MaxTurns:   3,
	}
	response, err := runner.Run(context.Background(), ModelRequest{Messages: []Message{{ID: "user", Role: "user", Blocks: []ContentBlock{{Kind: ContentText, Text: "go"}}}}})
	if err != nil || response.Message.Blocks[0].Text != "done" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if tool.calls != 1 || len(model.requests) != 2 {
		t.Fatalf("tool calls=%d model requests=%d", tool.calls, len(model.requests))
	}
	foundToolResult := false
	for _, message := range model.requests[1].Messages {
		if message.Role == "tool" {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Fatal("second turn did not receive structured tool result")
	}
	phases := map[string]bool{}
	for _, snapshot := range checkpoints.snapshots {
		phases[snapshot.Phase] = true
	}
	for _, phase := range []string{"model_complete", "tools_pending", "tools_complete"} {
		if !phases[phase] {
			t.Fatalf("missing checkpoint phase %s: %#v", phase, phases)
		}
	}
}

func TestRunnerApprovalDenialProducesStructuredResult(t *testing.T) {
	call := ToolCall{ID: "call_high", Name: "danger", Arguments: json.RawMessage(`{}`)}
	model := &scriptedModel{responses: []ModelResponse{
		{Message: Message{Role: "assistant", Blocks: []ContentBlock{{Kind: ContentToolCall, ToolCall: &call}}}},
		{Message: Message{Role: "assistant", Blocks: []ContentBlock{{Kind: ContentText, Text: "denied safely"}}}},
	}}
	tool := badInfoTool{info: ToolInfo{Name: "danger", InputSchema: json.RawMessage(`{"type":"object"}`), RiskLevel: "high"}}
	runner := Runner{
		Models: &Router{Endpoints: []Endpoint{{ID: "primary", Client: model}}},
		Tools:  &ToolExecutor{Registry: NewToolRegistry(tool)},
		Approval: func(context.Context, ToolCall, ToolInfo) (bool, error) {
			return false, nil
		},
	}
	response, err := runner.Run(context.Background(), ModelRequest{})
	if err != nil || response.Message.Blocks[0].Text != "denied safely" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model requests=%d", len(model.requests))
	}
	foundDenial := false
	for _, message := range model.requests[1].Messages {
		for _, block := range message.Blocks {
			if block.ToolResult != nil && block.ToolResult.CallID == call.ID && block.ToolResult.IsError {
				foundDenial = true
			}
		}
	}
	if !foundDenial {
		t.Fatal("approval denial was not preserved as structured tool result")
	}
}

func TestRunnerGuardAndApprovalError(t *testing.T) {
	if _, err := (&Runner{}).Run(context.Background(), ModelRequest{}); err == nil {
		t.Fatal("runner without router accepted")
	}
	call := ToolCall{ID: "call_high", Name: "danger", Arguments: json.RawMessage(`{}`)}
	model := &scriptedModel{responses: []ModelResponse{{Message: Message{Role: "assistant", Blocks: []ContentBlock{{Kind: ContentToolCall, ToolCall: &call}}}}}}
	tool := badInfoTool{info: ToolInfo{Name: "danger", RiskLevel: "high"}}
	runner := Runner{
		Models: &Router{Endpoints: []Endpoint{{ID: "primary", Client: model}}},
		Tools:  &ToolExecutor{Registry: NewToolRegistry(tool)},
		Approval: func(context.Context, ToolCall, ToolInfo) (bool, error) {
			return false, errors.New("approval unavailable")
		},
	}
	if _, err := runner.Run(context.Background(), ModelRequest{}); err == nil || err.Error() != "approval unavailable" {
		t.Fatalf("approval error=%v", err)
	}
}
