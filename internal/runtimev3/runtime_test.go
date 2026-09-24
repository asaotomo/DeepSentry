package runtimev3

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-edr/internal/executor"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClassifyErrorMatrix(t *testing.T) {
	var network net.Error = timeoutError{}
	tests := []struct {
		err  error
		kind ErrorKind
	}{
		{nil, ""}, {context.Canceled, ErrorCanceled}, {context.DeadlineExceeded, ErrorTimeout},
		{network, ErrorTimeout}, {errors.New("HTTP 429"), ErrorRateLimit}, {errors.New("HTTP 503"), ErrorServer},
		{errors.New("connection reset by peer"), ErrorConnection}, {errors.New("unknown parameter x"), ErrorUnsupported},
		{errors.New("invalid json"), ErrorInvalidOutput}, {errors.New("other"), ErrorUnknown},
		{&ClassifiedError{Kind: ErrorServer, Err: errors.New("wrapped")}, ErrorServer},
	}
	for _, test := range tests {
		if got := ClassifyError(test.err); got != test.kind {
			t.Errorf("ClassifyError(%v)=%s want %s", test.err, got, test.kind)
		}
	}
	classified := &ClassifiedError{Kind: ErrorTimeout, Err: context.DeadlineExceeded}
	if !errors.Is(classified, context.DeadlineExceeded) || classified.Error() == "" {
		t.Fatal("classified error does not unwrap")
	}
}

type badInfoTool struct{ info ToolInfo }

func (b badInfoTool) Info(context.Context) (ToolInfo, error) { return b.info, nil }
func (b badInfoTool) Invoke(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

type concurrencyProbeTool struct {
	info   ToolInfo
	active *atomic.Int32
	peak   *atomic.Int32
}

func (t concurrencyProbeTool) Info(context.Context) (ToolInfo, error) { return t.info, nil }

func (t concurrencyProbeTool) Invoke(context.Context, json.RawMessage) (ToolResult, error) {
	current := t.active.Add(1)
	for {
		peak := t.peak.Load()
		if current <= peak || t.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	time.Sleep(25 * time.Millisecond)
	t.active.Add(-1)
	return ToolResult{}, nil
}

func TestRegistryAndLegacyAdapter(t *testing.T) {
	registry := NewToolRegistry()
	if err := registry.Register(context.Background(), nil); err == nil {
		t.Fatal("nil tool accepted")
	}
	if err := registry.Register(context.Background(), badInfoTool{}); err == nil {
		t.Fatal("empty tool name accepted")
	}
	if err := registry.Register(context.Background(), badInfoTool{info: ToolInfo{Name: "z"}}); err != nil {
		t.Fatal(err)
	}
	if infos := registry.Infos(context.Background()); len(infos) != 1 || infos[0].Name != "z" {
		t.Fatalf("infos=%#v", infos)
	}
	if _, err := (LegacyToolAdapter{Name: "missing"}).Info(context.Background()); err == nil {
		t.Fatal("unknown legacy tool accepted")
	}
	adapter := LegacyToolAdapter{Name: "file_hash", Executor: &executor.LocalExecutor{}, Timeout: time.Second}
	info, err := adapter.Info(context.Background())
	if err != nil || info.Name != "file_hash" || !info.Idempotent || len(info.InputSchema) == 0 {
		t.Fatalf("info=%#v err=%v", info, err)
	}
	if _, err := adapter.Invoke(context.Background(), json.RawMessage(`{bad`)); err == nil {
		t.Fatal("invalid arguments accepted")
	}
	result, err := adapter.Invoke(context.Background(), json.RawMessage(`{"path":"/etc/hosts"}`))
	if err != nil || result.IsError || len(result.Blocks) == 0 || !strings.Contains(strings.ToLower(result.Blocks[0].Text), "sha256") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestEventSinksAndToolExecutorErrors(t *testing.T) {
	seen := 0
	good := EventSinkFunc(func(context.Context, RunEvent) error { seen++; return nil })
	bad := EventSinkFunc(func(context.Context, RunEvent) error { return errors.New("sink") })
	multi := MultiSink{good, nil, bad}
	if err := multi.Emit(context.Background(), RunEvent{}); err == nil || seen != 1 {
		t.Fatalf("multi sink err=%v seen=%d", err, seen)
	}
	sequence := &SequenceSink{Sink: good}
	if err := sequence.Emit(context.Background(), RunEvent{}); err != nil || sequence.Next != 1 {
		t.Fatalf("sequence=%d err=%v", sequence.Next, err)
	}
	if err := (&SequenceSink{}).Emit(context.Background(), RunEvent{}); err != nil {
		t.Fatal(err)
	}
	sequence.AdvanceTo(41)
	sequence.AdvanceTo(7)
	if sequence.Cursor() != 41 {
		t.Fatalf("restored sequence cursor moved backwards: %d", sequence.Cursor())
	}
	if err := sequence.Emit(context.Background(), RunEvent{}); err != nil || sequence.Cursor() != 42 {
		t.Fatalf("resumed sequence=%d err=%v", sequence.Cursor(), err)
	}
	var nilSequence *SequenceSink
	nilSequence.AdvanceTo(9)
	if nilSequence.Cursor() != 0 || nilSequence.Flush(context.Background()) != nil {
		t.Fatal("nil sequence sink must remain a no-op")
	}
	exec := ToolExecutor{}
	results := exec.ExecuteBatch(context.Background(), []ToolCall{{ID: "1", Name: "missing"}})
	if results[0].Err == nil {
		t.Fatal("nil registry should fail")
	}
	exec.Registry = NewToolRegistry()
	results = exec.ExecuteBatch(context.Background(), []ToolCall{{ID: "1", Name: "missing"}})
	if results[0].Err == nil {
		t.Fatal("unknown tool should fail")
	}
}

func TestToolExecutorSerializesMatchingConcurrencyKeys(t *testing.T) {
	var active, peak atomic.Int32
	registry := NewToolRegistry(
		concurrencyProbeTool{info: ToolInfo{Name: "target_a", Idempotent: true, RiskLevel: "low", ConcurrencyKey: "target"}, active: &active, peak: &peak},
		concurrencyProbeTool{info: ToolInfo{Name: "target_b", Idempotent: true, RiskLevel: "low", ConcurrencyKey: "target"}, active: &active, peak: &peak},
		concurrencyProbeTool{info: ToolInfo{Name: "controller", Idempotent: true, RiskLevel: "low"}, active: &active, peak: &peak},
	)
	exec := ToolExecutor{Registry: registry, MaxConcurrency: 3}
	results := exec.ExecuteBatch(context.Background(), []ToolCall{{ID: "1", Name: "target_a"}, {ID: "2", Name: "target_b"}, {ID: "3", Name: "controller"}})
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("batch result: %v", result.Err)
		}
	}
	if peak.Load() != 2 {
		t.Fatalf("matching target keys must serialize while independent tools run in parallel; peak=%d", peak.Load())
	}
}

func TestRouterGuardAndHelpers(t *testing.T) {
	router := Router{FailoverOn: map[ErrorKind]bool{ErrorTimeout: true}}
	if _, err := router.Generate(context.Background(), ModelRequest{}); err == nil {
		t.Fatal("empty endpoint list accepted")
	}
	if !router.shouldFailover(ErrorTimeout) || router.shouldFailover(ErrorServer) {
		t.Fatal("explicit failover policy ignored")
	}
	if delay := router.retryDelay(1); delay < time.Second || delay > 1500*time.Millisecond {
		t.Fatalf("unexpected retry delay %s", delay)
	}
	if safeError(nil) != "" || len(safeError(errors.New(strings.Repeat("x", 800)))) != 500 {
		t.Fatal("safe error bound failed")
	}
}
