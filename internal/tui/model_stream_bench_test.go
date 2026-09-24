package tui

import "testing"

func BenchmarkStreamDeltaIngestion(b *testing.B) {
	const chunks = 1000
	const delta = `{"thought":"working through the current evidence and tool results. "}`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
		for j := 0; j < chunks; j++ {
			m.appendStreamDelta(delta)
		}
		m.finalizeStream("")
	}
}
