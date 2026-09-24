package benchmark

import (
	"ai-edr/internal/analyzer"
	"testing"
	"time"
)

func TestSummarizeRuntimeProbe(t *testing.T) {
	report := &RuntimeProbeReport{Observations: []RuntimeProbeObservation{
		{ExpectedTools: []string{"a"}, SelectedTools: []string{"a"}, Success: true, ValidCalls: 1, Tokens: 100, Latency: time.Second},
		{ExpectedTools: []string{"a", "b"}, SelectedTools: []string{"a"}, ValidCalls: 1, InvalidCalls: 1, Tokens: 200, Latency: 2 * time.Second},
	}}
	summarizeRuntimeProbe(report)
	if report.TaskSuccessRate != 0.5 || report.CorrectToolSelectionRate != 2.0/3.0 || report.ValidToolCallRate != 2.0/3.0 {
		t.Fatalf("unexpected rates: %#v", report)
	}
	if report.P95Tokens != 200 || report.P95Latency != 2*time.Second {
		t.Fatalf("unexpected p95: %#v", report)
	}
}

func TestClassifyProbeCallSeparatesValidityFromSelection(t *testing.T) {
	tests := []struct {
		name     string
		call     analyzer.LLMToolCall
		valid    bool
		selected string
		catalog  bool
	}{
		{"direct", analyzer.LLMToolCall{Name: "file_hash", Arguments: `{"path":"/tmp/a"}`}, true, "file_hash", false},
		{"catalog", analyzer.LLMToolCall{Name: "tool_catalog", Arguments: `{"query":"hash"}`}, true, "", true},
		{"valid non-tool action", analyzer.LLMToolCall{Name: "agent_action", Arguments: `{"thought":"done","action":"finish","final_report":"x"}`}, true, "", false},
		{"invalid json", analyzer.LLMToolCall{Name: "file_hash", Arguments: `{bad`}, false, "", false},
		{"unknown", analyzer.LLMToolCall{Name: "missing", Arguments: `{}`}, false, "", false},
	}
	for _, test := range tests {
		got := classifyProbeCall(test.call)
		if got.Valid != test.valid || got.SelectedTool != test.selected || got.Catalog != test.catalog {
			t.Errorf("%s classification=%#v", test.name, got)
		}
	}
}

func TestEvenlySampleProbeTasksSpansSuite(t *testing.T) {
	tasks := RuntimeSecurityTasks()
	sampled := evenlySampleProbeTasks(tasks, 8)
	if len(sampled) != 8 || sampled[0].ID != tasks[0].ID || sampled[len(sampled)-1].ID == tasks[7].ID {
		t.Fatalf("sample did not span suite: %#v", sampled)
	}
	categories := map[string]bool{}
	for _, task := range sampled {
		categories[task.Category] = true
	}
	if len(categories) < 5 {
		t.Fatalf("sample covered only %d categories: %#v", len(categories), categories)
	}
}

func TestEvaluateRuntimeProbeABRequiresFullComparableSuite(t *testing.T) {
	makeReport := func(mode string) *RuntimeProbeReport {
		report := &RuntimeProbeReport{
			Mode: mode, Provider: "custom", Model: "same-model", Repetitions: 3,
			TaskCount: len(RuntimeSecurityTasks()), TaskSuccessRate: .90,
			CorrectToolSelectionRate: .96, ValidToolCallRate: 1,
			P95Tokens: 1000, P95Latency: 10 * time.Second,
		}
		for run := 1; run <= report.Repetitions; run++ {
			for _, task := range RuntimeSecurityTasks() {
				report.Observations = append(report.Observations, RuntimeProbeObservation{TaskID: task.ID, Run: run})
			}
		}
		return report
	}
	legacy := makeReport("legacy")
	v3 := makeReport("v3")
	v3.P95Tokens = 1099
	v3.P95Latency = 11 * time.Second
	if decision := EvaluateRuntimeProbeAB(legacy, v3); !decision.Passed {
		t.Fatalf("complete comparable A/B rejected: %#v", decision)
	}
	v3.Observations = v3.Observations[:len(v3.Observations)-1]
	if decision := EvaluateRuntimeProbeAB(legacy, v3); decision.Passed {
		t.Fatal("incomplete real-model suite passed")
	}
}
