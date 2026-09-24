package runtimev3

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type ApprovalFunc func(context.Context, ToolCall, ToolInfo) (bool, error)

type ExecutionSnapshot struct {
	SchemaVersion int                 `json:"schema_version"`
	RunID         string              `json:"run_id"`
	Turn          int                 `json:"turn"`
	Phase         string              `json:"phase"`
	Messages      []Message           `json:"messages"`
	Pending       map[string]ToolCall `json:"pending"`
	Completed     map[string]bool     `json:"completed"`
	EventCursor   int64               `json:"event_cursor,omitempty"`
	SavedAt       time.Time           `json:"saved_at"`
}

type CheckpointWriter interface {
	Save(context.Context, ExecutionSnapshot) error
}

// Runner is the dependency-injected Runtime v3 turn loop. Existing Harness
// behavior can migrate to it one action family at a time through adapters.
type Runner struct {
	Models     *Router
	Tools      *ToolExecutor
	Approval   ApprovalFunc
	Checkpoint CheckpointWriter
	Events     EventSink
	RunID      string
	MaxTurns   int
}

func (r *Runner) Run(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	if r.Models == nil {
		return ModelResponse{}, fmt.Errorf("runtime v3 runner requires a model router")
	}
	if r.RunID == "" {
		r.RunID = NewID("run")
	}
	if r.Models.RunID == "" {
		r.Models.RunID = r.RunID
	}
	maxTurns := r.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 30
	}
	state := ExecutionSnapshot{SchemaVersion: 1, RunID: r.RunID, Messages: append([]Message(nil), request.Messages...), Pending: map[string]ToolCall{}, Completed: map[string]bool{}}
	r.emit(ctx, RunEvent{Kind: EventRunStart, Component: "runner"})
	defer r.emit(context.Background(), RunEvent{Kind: EventRunEnd, Component: "runner"})

	for turn := 1; turn <= maxTurns; turn++ {
		state.Turn = turn
		r.Models.TurnID = fmt.Sprintf("turn_%d", turn)
		modelRequest := request
		modelRequest.Messages = PatchDanglingToolCalls(state.Messages)
		response, err := r.Models.Generate(ctx, modelRequest)
		if err != nil {
			return ModelResponse{}, err
		}
		if response.Message.ID == "" {
			response.Message.ID = NewID("msg")
		}
		if response.Message.CreatedAt.IsZero() {
			response.Message.CreatedAt = time.Now().UTC()
		}
		state.Messages = append(state.Messages, response.Message)
		state.Phase = "model_complete"
		if err := r.save(ctx, state); err != nil {
			return ModelResponse{}, err
		}
		// Safe cancellation point: never start a tool after cancellation was
		// requested while a model response was streaming.
		if err := ctx.Err(); err != nil {
			return ModelResponse{}, err
		}

		calls := toolCalls(response.Message)
		if len(calls) == 0 {
			return response, nil
		}
		if r.Tools == nil || r.Tools.Registry == nil {
			return ModelResponse{}, fmt.Errorf("model requested tools but no tool executor is configured")
		}
		approved := make([]ToolCall, 0, len(calls))
		for _, call := range calls {
			if state.Completed[call.ID] {
				state.Messages = append(state.Messages, toolResultMessage(ToolResult{CallID: call.ID, Name: call.Name, IsError: true, Blocks: []ContentBlock{{Kind: ContentText, Text: "already completed; skipped during recovery"}}}))
				continue
			}
			item, ok := r.Tools.Registry.Get(call.Name)
			if !ok {
				state.Messages = append(state.Messages, toolResultMessage(ToolResult{CallID: call.ID, Name: call.Name, IsError: true, Blocks: []ContentBlock{{Kind: ContentText, Text: "unknown tool"}}}))
				continue
			}
			info, err := item.Info(ctx)
			if err != nil {
				return ModelResponse{}, err
			}
			if r.Approval != nil && info.RiskLevel != "low" {
				allowed, err := r.Approval(ctx, call, info)
				if err != nil {
					return ModelResponse{}, err
				}
				if !allowed {
					state.Messages = append(state.Messages, toolResultMessage(ToolResult{CallID: call.ID, Name: call.Name, IsError: true, Blocks: []ContentBlock{{Kind: ContentText, Text: "approval denied"}}}))
					state.Completed[call.ID] = true
					continue
				}
			}
			state.Pending[call.ID] = call
			approved = append(approved, call)
		}
		state.Phase = "tools_pending"
		if err := r.save(ctx, state); err != nil {
			return ModelResponse{}, err
		}
		results := r.Tools.ExecuteBatch(ctx, approved)
		for _, result := range results {
			if result.Result.CallID == "" {
				result.Result.CallID = result.Call.ID
			}
			if result.Result.Name == "" {
				result.Result.Name = result.Call.Name
			}
			if result.Err != nil && len(result.Result.Blocks) == 0 {
				result.Result.IsError = true
				result.Result.Blocks = []ContentBlock{{Kind: ContentText, Text: result.Err.Error()}}
			}
			state.Messages = append(state.Messages, toolResultMessage(result.Result))
			delete(state.Pending, result.Call.ID)
			state.Completed[result.Call.ID] = true
		}
		state.Phase = "tools_complete"
		if err := r.save(ctx, state); err != nil {
			return ModelResponse{}, err
		}
		// Safe cancellation point after the complete batch is durably recorded.
		if err := ctx.Err(); err != nil {
			return ModelResponse{}, err
		}
	}
	return ModelResponse{}, fmt.Errorf("runtime v3 exceeded max turns (%d)", maxTurns)
}

func toolCalls(message Message) []ToolCall {
	var calls []ToolCall
	for _, block := range message.Blocks {
		if block.Kind == ContentToolCall && block.ToolCall != nil {
			calls = append(calls, *block.ToolCall)
		}
	}
	return calls
}

func toolResultMessage(result ToolResult) Message {
	return Message{ID: NewID("msg"), Role: "tool", CreatedAt: time.Now().UTC(), Blocks: []ContentBlock{{Kind: ContentToolResult, ToolResult: &result}}}
}

func (r *Runner) save(ctx context.Context, state ExecutionSnapshot) error {
	if r.Checkpoint == nil {
		return nil
	}
	state.SavedAt = time.Now().UTC()
	if sequenced, ok := r.Events.(*SequenceSink); ok {
		state.EventCursor = sequenced.Cursor()
	}
	if durable, ok := r.Events.(interface{ Flush(context.Context) error }); ok {
		if err := durable.Flush(ctx); err != nil {
			return fmt.Errorf("flush runtime events before checkpoint: %w", err)
		}
	}
	if err := r.Checkpoint.Save(ctx, state); err != nil {
		return fmt.Errorf("save runtime checkpoint: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{"turn": state.Turn, "phase": state.Phase})
	r.emit(ctx, RunEvent{Kind: EventCheckpoint, Component: "runner", TurnID: fmt.Sprintf("turn_%d", state.Turn), SafeMetadata: metadata})
	return nil
}

func (r *Runner) emit(ctx context.Context, event RunEvent) {
	if r.Events == nil {
		return
	}
	event.RunID = r.RunID
	_ = r.Events.Emit(ctx, event)
}
