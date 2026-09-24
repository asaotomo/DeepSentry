package runtimev3

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeModel struct {
	response ModelResponse
	err      error
	calls    int
}

func (f *fakeModel) Generate(context.Context, ModelRequest) (ModelResponse, error) {
	f.calls++
	return f.response, f.err
}

func (f *fakeModel) Stream(context.Context, ModelRequest) (StreamReader, error) {
	return nil, errors.New("not implemented")
}

func TestRouterRetriesThenFailsOver(t *testing.T) {
	primary := &fakeModel{err: &ClassifiedError{Kind: ErrorRateLimit, Err: errors.New("429")}}
	fallback := &fakeModel{response: ModelResponse{Message: Message{ID: "ok"}}}
	var mu sync.Mutex
	var events []EventKind
	router := Router{
		Endpoints: []Endpoint{{ID: "primary", Client: primary, MaxRetries: 1}, {ID: "fallback", Client: fallback}},
		Backoff:   func(int) time.Duration { return 0 },
		Events: EventSinkFunc(func(_ context.Context, event RunEvent) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event.Kind)
			return nil
		}),
	}
	response, err := router.Generate(context.Background(), ModelRequest{})
	if err != nil || response.ModelID != "fallback" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if primary.calls != 2 || fallback.calls != 1 {
		t.Fatalf("calls primary=%d fallback=%d", primary.calls, fallback.calls)
	}
	foundFailover := false
	for _, event := range events {
		if event == EventModelFailover {
			foundFailover = true
		}
	}
	if !foundFailover {
		t.Fatalf("missing failover event: %v", events)
	}
}

func TestPatchDanglingToolCalls(t *testing.T) {
	messages := []Message{{ID: "assistant", Role: "assistant", Blocks: []ContentBlock{{Kind: ContentToolCall, ToolCall: &ToolCall{ID: "call_1", Name: "write_file"}}}}}
	patched := PatchDanglingToolCalls(messages)
	if len(patched) != 2 || patched[1].Blocks[0].ToolResult == nil || !patched[1].Blocks[0].ToolResult.IsError {
		t.Fatalf("dangling call was not patched: %#v", patched)
	}
}
