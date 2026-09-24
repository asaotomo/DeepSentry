package benchmark

import (
	"testing"
	"time"
)

func TestRuntimeSecurityTasksCoverAcceptanceMatrix(t *testing.T) {
	tasks := RuntimeSecurityTasks()
	if len(tasks) < 24 {
		t.Fatalf("task count=%d", len(tasks))
	}
	categories := map[string]bool{}
	for _, task := range tasks {
		categories[task.Category] = true
		if task.Fixture == "" || len(task.ExpectedTools) == 0 || len(task.RequiredEvidence) == 0 {
			t.Fatalf("incomplete fixture: %#v", task)
		}
	}
	for _, category := range []string{"应急", "日志", "取证", "Fleet", "Web", "数据库", "CTF/AWD"} {
		if !categories[category] {
			t.Fatalf("missing category %s", category)
		}
	}
}

func TestControlledRuntimeFixturesCoverEveryToolAndEvidenceKey(t *testing.T) {
	fixtures, err := ControlledRuntimeFixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != len(RuntimeSecurityTasks()) {
		t.Fatalf("fixtures=%d tasks=%d", len(fixtures), len(RuntimeSecurityTasks()))
	}
	recoverable := 0
	for _, fixture := range fixtures {
		if fixture.TransientFailures > 0 {
			recoverable++
		}
	}
	if recoverable < 5 {
		t.Fatalf("fault fixtures=%d want at least 5", recoverable)
	}
}

func TestControlledRuntimeFixturesExerciseHarnessAndRecoverFaults(t *testing.T) {
	report, err := RunControlledRuntimeFixtures("v3", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Observations) != 24 {
		t.Fatalf("observations=%d", len(report.Observations))
	}
	if report.Score.TaskSuccessRate != 1 || report.Score.ValidToolCallRate != 1 || report.Score.EvidenceCoverageRate != 1 || report.Score.RecoverableFailureRate != 1 || report.Score.ModifyingDuplicates != 0 {
		t.Fatalf("controlled score=%#v", report.Score)
	}
}

func TestEvaluateOutcomesScoresTaskEvidenceAndRecovery(t *testing.T) {
	tasks := RuntimeSecurityTasks()[:1]
	observation := TaskObservation{TaskID: tasks[0].ID, Success: true, SelectedTools: tasks[0].ExpectedTools, EvidenceKeys: tasks[0].RequiredEvidence, ValidToolCalls: 2, FailureWasRecoverable: true, RecoveredFailure: true, Tokens: 100, Latency: time.Second}
	score := EvaluateOutcomes(tasks, []TaskObservation{observation})
	if score.TaskSuccessRate != 1 || score.CorrectToolSelectionRate != 1 || score.EvidenceCoverageRate != 1 || score.ValidToolCallRate != 1 || score.RecoverableFailureRate != 1 {
		t.Fatalf("unexpected score: %#v", score)
	}
}

func TestRuntimeAndWorkflowAcceptanceGates(t *testing.T) {
	legacy := OutcomeScore{TaskSuccessRate: .70, P95Tokens: 1000, P95Latency: 10 * time.Second}
	v3 := OutcomeScore{TaskSuccessRate: .86, ValidToolCallRate: .995, EvidenceCoverageRate: .96, UnsupportedHighRiskRate: .01, RecoverableFailureRate: .96, P95Tokens: 1080, P95Latency: 11 * time.Second}
	if decision := EvaluateRuntimeV3Gate(legacy, v3); !decision.Passed {
		t.Fatalf("valid runtime rejected: %#v", decision)
	}
	bad := v3
	bad.ModifyingDuplicates = 1
	if decision := EvaluateRuntimeV3Gate(legacy, bad); decision.Passed {
		t.Fatal("duplicate modifying call passed gate")
	}
	if decision := EvaluateWorkflowExperiment(OutcomeScore{TaskSuccessRate: .70, P95Latency: 10 * time.Second}, OutcomeScore{TaskSuccessRate: .81, P95Latency: 11 * time.Second}); !decision.Passed {
		t.Fatalf("valid workflow experiment rejected: %#v", decision)
	}
}

func TestControlledRuntimeGateIgnoresMicrobenchmarkTimingNoise(t *testing.T) {
	legacy := OutcomeScore{TaskSuccessRate: 1, ValidToolCallRate: 1, EvidenceCoverageRate: 1, RecoverableFailureRate: 1, P95Latency: time.Microsecond}
	v3 := OutcomeScore{TaskSuccessRate: 1, ValidToolCallRate: 1, EvidenceCoverageRate: 1, RecoverableFailureRate: 1, P95Latency: time.Second}
	if decision := EvaluateControlledRuntimeV3Gate(legacy, v3); !decision.Passed {
		t.Fatalf("controlled timing noise rejected correctness gate: %#v", decision)
	}
	v3.ModifyingDuplicates = 1
	if decision := EvaluateControlledRuntimeV3Gate(legacy, v3); decision.Passed {
		t.Fatal("controlled correctness gate ignored duplicate mutation")
	}
}
