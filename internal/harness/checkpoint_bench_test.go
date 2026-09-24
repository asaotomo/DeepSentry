package harness

import (
	"ai-edr/internal/analyzer"
	"strings"
	"testing"
)

func BenchmarkCheckpointSave(b *testing.B) {
	store := &CheckpointStore{dir: b.TempDir(), sessionID: "session_benchmark"}
	history := make([]analyzer.Message, 100)
	for i := range history {
		history[i] = analyzer.Message{Role: "tool", Content: strings.Repeat("observed service status and log evidence ", 25)}
	}
	data := CheckpointData{State: NewAgentState(""), History: history}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Save(data); err != nil {
			b.Fatal(err)
		}
	}
}
