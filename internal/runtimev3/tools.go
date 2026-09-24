package runtimev3

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"ai-edr/internal/executor"
	legacytools "ai-edr/internal/tools"
)

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry(items ...Tool) *ToolRegistry {
	r := &ToolRegistry{tools: make(map[string]Tool, len(items))}
	for _, item := range items {
		_ = r.Register(context.Background(), item)
	}
	return r
}

func (r *ToolRegistry) Register(ctx context.Context, item Tool) error {
	if item == nil {
		return fmt.Errorf("tool is nil")
	}
	info, err := item.Info(ctx)
	if err != nil {
		return err
	}
	if info.Name == "" {
		return fmt.Errorf("tool name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]Tool)
	}
	r.tools[info.Name] = item
	return nil
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.tools[name]
	return item, ok
}

func (r *ToolRegistry) Infos(ctx context.Context) []ToolInfo {
	r.mu.RLock()
	items := make([]Tool, 0, len(r.tools))
	for _, item := range r.tools {
		items = append(items, item)
	}
	r.mu.RUnlock()
	out := make([]ToolInfo, 0, len(items))
	for _, item := range items {
		if info, err := item.Info(ctx); err == nil {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LegacyToolAdapter lets Runtime v3 consume all existing DeepSentry built-ins
// while preserving their validation, risk metadata and target executor.
type LegacyToolAdapter struct {
	Name      string
	Executor  executor.Executor
	IsWindows bool
	Timeout   time.Duration
}

func (a LegacyToolAdapter) Info(_ context.Context) (ToolInfo, error) {
	legacy, ok := legacytools.Get(a.Name)
	if !ok {
		return ToolInfo{}, fmt.Errorf("unknown legacy tool %q", a.Name)
	}
	schema, err := json.Marshal(legacytools.JSONSchema(a.Name))
	if err != nil {
		return ToolInfo{}, err
	}
	concurrencyKey := ""
	if legacy.Perspective == "target" {
		// Existing SSH/Telnet/FTP executors represent one interactive target
		// session. Calls sharing that session must retain model order even when
		// each command is individually read-only.
		concurrencyKey = "target"
	}
	return ToolInfo{
		Name:           legacy.Name,
		Description:    legacy.Description,
		InputSchema:    schema,
		RiskLevel:      legacy.RiskLevel,
		Perspective:    legacy.Perspective,
		Idempotent:     legacy.RiskLevel == legacytools.RiskLow,
		ConcurrencyKey: concurrencyKey,
		Timeout:        a.Timeout,
	}, nil
}

func (a LegacyToolAdapter) Invoke(ctx context.Context, arguments json.RawMessage) (ToolResult, error) {
	var raw map[string]any
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &raw); err != nil {
			return ToolResult{CallID: "", Name: a.Name, IsError: true}, fmt.Errorf("decode %s arguments: %w", a.Name, err)
		}
	}
	args := make(map[string]string, len(raw))
	for key, value := range raw {
		switch typed := value.(type) {
		case string:
			args[key] = typed
		case nil:
			continue
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return ToolResult{Name: a.Name, IsError: true}, err
			}
			args[key] = string(encoded)
		}
	}
	if err := legacytools.ValidateCall(a.Name, args); err != nil {
		return ToolResult{Name: a.Name, IsError: true}, err
	}
	invokeCtx := ctx
	var cancel context.CancelFunc
	if a.Timeout > 0 {
		invokeCtx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	type response struct {
		output string
		err    error
	}
	done := make(chan response, 1)
	go func() {
		output, _, err := legacytools.RunWithExecutor(a.Name, args, a.IsWindows, a.Executor)
		done <- response{output: output, err: err}
	}()
	select {
	case <-invokeCtx.Done():
		return ToolResult{Name: a.Name, IsError: true}, invokeCtx.Err()
	case result := <-done:
		toolResult := ToolResult{
			Name:    a.Name,
			IsError: result.err != nil,
			Blocks:  []ContentBlock{{Kind: ContentText, Text: result.output}},
		}
		return toolResult, result.err
	}
}

type BatchResult struct {
	Call   ToolCall
	Result ToolResult
	Err    error
}

type ToolExecutor struct {
	Registry       *ToolRegistry
	Events         EventSink
	RunID          string
	TurnID         string
	StepID         string
	MaxConcurrency int
}

// ExecuteBatch runs only idempotent low-risk tools concurrently. Mutating or
// target-conflicting calls are serialized in model order.
func (e *ToolExecutor) ExecuteBatch(ctx context.Context, calls []ToolCall) []BatchResult {
	results := make([]BatchResult, len(calls))
	if e.Registry == nil {
		for i, call := range calls {
			results[i] = BatchResult{Call: call, Err: fmt.Errorf("tool registry is nil")}
		}
		return results
	}
	limit := e.MaxConcurrency
	if limit <= 0 {
		limit = 4
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	keyTails := make(map[string]<-chan struct{})
	run := func(index int, concurrent bool) {
		call := calls[index]
		item, ok := e.Registry.Get(call.Name)
		if !ok {
			results[index] = BatchResult{Call: call, Err: fmt.Errorf("unknown tool %q", call.Name)}
			return
		}
		if concurrent {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[index] = BatchResult{Call: call, Err: ctx.Err()}
				return
			}
		}
		started := time.Now()
		e.emit(ctx, RunEvent{Kind: EventToolStart, ToolCallID: call.ID, ToolName: call.Name})
		result, err := item.Invoke(ctx, call.Arguments)
		result.CallID = call.ID
		result.Name = call.Name
		results[index] = BatchResult{Call: call, Result: result, Err: err}
		kind := EventToolEnd
		if err != nil {
			kind = EventToolError
		}
		e.emit(ctx, RunEvent{Kind: kind, ToolCallID: call.ID, ToolName: call.Name, DurationMS: time.Since(started).Milliseconds(), ErrorClass: ClassifyError(err), Message: safeError(err)})
	}
	for index, call := range calls {
		item, ok := e.Registry.Get(call.Name)
		if !ok {
			run(index, false)
			continue
		}
		info, err := item.Info(ctx)
		if err != nil || !info.Idempotent || info.RiskLevel != legacytools.RiskLow {
			wg.Wait()
			run(index, false)
			continue
		}
		var previous <-chan struct{}
		var finished chan struct{}
		if info.ConcurrencyKey != "" {
			previous = keyTails[info.ConcurrencyKey]
			finished = make(chan struct{})
			keyTails[info.ConcurrencyKey] = finished
		}
		wg.Add(1)
		go func(i int, waitFor <-chan struct{}, done chan struct{}) {
			defer wg.Done()
			if done != nil {
				defer close(done)
			}
			if waitFor != nil {
				select {
				case <-waitFor:
				case <-ctx.Done():
					results[i] = BatchResult{Call: calls[i], Err: ctx.Err()}
					return
				}
			}
			run(i, true)
		}(index, previous, finished)
	}
	wg.Wait()
	return results
}

func (e *ToolExecutor) emit(ctx context.Context, event RunEvent) {
	if e.Events == nil {
		return
	}
	event.RunID = e.RunID
	event.TurnID = e.TurnID
	event.StepID = e.StepID
	event.Component = "tool_executor"
	_ = e.Events.Emit(ctx, event)
}
