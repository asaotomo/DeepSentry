package runtimev3

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestJSONLTraceStructurallyRedactsPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	sink, err := NewJSONLTraceSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Emit(context.Background(), RunEvent{RunID: "run", Kind: EventToolError, Message: "api_key=trace-secret-123", SafeMetadata: []byte(`{"password":"trace-pass-456"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "trace-secret-123") || strings.Contains(text, "trace-pass-456") || !strings.Contains(text, "***") {
		t.Fatalf("trace redaction failed: %s", text)
	}
}

func TestSequenceSinkCursorAndFlushAreConcurrencySafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	fileSink, err := NewJSONLTraceSink(path)
	if err != nil {
		t.Fatal(err)
	}
	sink := &SequenceSink{Sink: MultiSink{fileSink}}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sink.Emit(context.Background(), RunEvent{RunID: "run", Kind: EventModelDelta}); err != nil {
				t.Errorf("emit: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := sink.Cursor(); got != 32 {
		t.Fatalf("cursor=%d want 32", got)
	}
	if err := sink.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fileSink.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; got != 32 {
		t.Fatalf("event lines=%d want 32", got)
	}
}
