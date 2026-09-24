package benchmark

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness"
	"ai-edr/internal/runtimev3"
)

type ControlledRuntimeReport struct {
	Mode         string            `json:"mode"`
	Repetitions  int               `json:"repetitions"`
	Observations []TaskObservation `json:"observations"`
	Score        OutcomeScore      `json:"score"`
}

// RunControlledRuntimeFixtures executes the real Harness lifecycle against
// deterministic model decisions and simulated target/tool outputs. It is a CI
// correctness gate for parsing, approvals, tool spans, retries, history and
// recovery; the separate runtime-probe supplies real-model quality/cost data.
func RunControlledRuntimeFixtures(mode string, repetitions int) (ControlledRuntimeReport, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "legacy" && mode != "v3" {
		return ControlledRuntimeReport{}, fmt.Errorf("unsupported runtime mode %q", mode)
	}
	if repetitions <= 0 {
		repetitions = 1
	}
	fixtures, err := ControlledRuntimeFixtures()
	if err != nil {
		return ControlledRuntimeReport{}, err
	}
	tasks := RuntimeSecurityTasks()
	fixtureByID := make(map[string]ControlledRuntimeFixture, len(fixtures))
	for _, fixture := range fixtures {
		fixtureByID[fixture.TaskID] = fixture
	}

	previous := config.GlobalConfig
	config.GlobalConfig.AgentRuntime = mode
	config.GlobalConfig.UseNativeTools = true
	defer func() { config.GlobalConfig = previous }()

	report := ControlledRuntimeReport{Mode: mode, Repetitions: repetitions}
	for repetition := 0; repetition < repetitions; repetition++ {
		for _, task := range tasks {
			observation, runErr := runControlledTask(task, fixtureByID[task.ID], repetition)
			if runErr != nil {
				return ControlledRuntimeReport{}, fmt.Errorf("%s repetition %d: %w", task.ID, repetition+1, runErr)
			}
			report.Observations = append(report.Observations, observation)
		}
	}
	report.Score = EvaluateOutcomesRepeated(tasks, report.Observations)
	return report, nil
}

func EvaluateOutcomesRepeated(tasks []SecurityTaskFixture, observations []TaskObservation) OutcomeScore {
	if len(observations) == 0 {
		return OutcomeScore{}
	}
	expanded := make([]SecurityTaskFixture, 0, len(observations))
	seen := make(map[string]int)
	for _, observation := range observations {
		var task SecurityTaskFixture
		for _, candidate := range tasks {
			if candidate.ID == observation.TaskID {
				task = candidate
				break
			}
		}
		seen[task.ID]++
		task.ID = fmt.Sprintf("%s#%d", task.ID, seen[task.ID])
		expanded = append(expanded, task)
	}
	indexed := make([]TaskObservation, len(observations))
	seen = make(map[string]int)
	for i, observation := range observations {
		seen[observation.TaskID]++
		observation.TaskID = fmt.Sprintf("%s#%d", observation.TaskID, seen[observation.TaskID])
		indexed[i] = observation
	}
	return EvaluateOutcomes(expanded, indexed)
}

func runControlledTask(task SecurityTaskFixture, fixture ControlledRuntimeFixture, repetition int) (TaskObservation, error) {
	started := time.Now()
	selected := make(map[string]bool)
	validCalls := 0
	invalidCalls := 0
	remainingFailures := fixture.TransientFailures
	recovered := false
	succeeded := false
	toolOutputs := make([]string, 0, len(task.ExpectedTools))
	finalReport := ""

	modelCalls := 0
	modelStep := func(_ analyzer.StepOptions) (analyzer.AgentResponse, error) {
		modelCalls++
		if succeeded {
			finalReport = "受控任务完成；关键证据：" + strings.Join(toolOutputs, "; ")
			return analyzer.AgentResponse{Action: "finish", IsFinished: true, FinalReport: finalReport}, nil
		}
		calls := make([]analyzer.NativeToolCall, 0, len(task.ExpectedTools))
		for index, name := range task.ExpectedTools {
			calls = append(calls, analyzer.NativeToolCall{
				ID:        fmt.Sprintf("%s_r%d_m%d_c%d", strings.ToLower(task.ID), repetition+1, modelCalls, index+1),
				Name:      name,
				Arguments: `{}`,
			})
		}
		return analyzer.AgentResponse{Action: "tool_batch", NativeToolCalls: calls}, nil
	}

	actionHandler := func(_ *harness.StepContext, action *harness.AgentAction) (*harness.ActionResult, error) {
		if action.Type != harness.ActionToolBatch {
			invalidCalls++
			return &harness.ActionResult{Output: "controlled fixture accepts tool_batch only"}, errors.New("unexpected controlled action")
		}
		for _, call := range action.ToolCalls {
			if _, ok := fixture.Outputs[call.Name]; !ok {
				invalidCalls++
				return &harness.ActionResult{Output: "unknown fixture tool: " + call.Name}, errors.New("invalid fixture tool")
			}
			validCalls++
			selected[call.Name] = true
		}
		if remainingFailures > 0 {
			remainingFailures--
			return &harness.ActionResult{Output: "simulated recoverable connection reset"}, errors.New("connection reset by controlled fixture")
		}
		results := make([]harness.ToolCallResult, 0, len(action.ToolCalls))
		toolOutputs = toolOutputs[:0]
		for _, call := range action.ToolCalls {
			output := fixture.Outputs[call.Name]
			results = append(results, harness.ToolCallResult{ID: call.ID, Name: call.Name, Output: output})
			toolOutputs = append(toolOutputs, output)
		}
		sort.Strings(toolOutputs)
		succeeded = true
		recovered = fixture.TransientFailures > 0
		return &harness.ActionResult{Output: strings.Join(toolOutputs, "\n"), ToolResults: results}, nil
	}

	ui := &controlledUISink{}
	agent := &harness.DeepAgent{
		State:          harness.NewAgentState(""),
		UseNativeTools: true,
		SessionID:      "session_controlled",
		RunID:          runtimev3.NewID("controlled"),
	}
	history := []analyzer.Message{{Role: "user", Content: task.Goal}}
	agent.RunLoop(harness.RunLoopConfig{
		History:       &history,
		BatchMode:     true,
		PlanMode:      true,
		MaxSteps:      4 + fixture.TransientFailures,
		UI:            ui,
		ModelStep:     modelStep,
		Executor:      controlledExecutor{},
		ActionHandler: actionHandler,
	})
	for _, event := range ui.events {
		if event.Kind == harness.EventFinish {
			finalReport = event.Message
		}
	}
	evidenceKeys := make([]string, 0, len(task.RequiredEvidence))
	for _, key := range task.RequiredEvidence {
		if strings.Contains(finalReport, key+"=") || strings.Contains(finalReport, key+":") {
			evidenceKeys = append(evidenceKeys, key)
		}
	}
	selectedTools := make([]string, 0, len(selected))
	for name := range selected {
		selectedTools = append(selectedTools, name)
	}
	sort.Strings(selectedTools)
	success := succeeded && finalReport != "" && overlap(task.ExpectedTools, selectedTools) == len(task.ExpectedTools) && len(evidenceKeys) == len(task.RequiredEvidence)
	return TaskObservation{
		TaskID:                task.ID,
		Success:               success,
		SelectedTools:         selectedTools,
		EvidenceKeys:          evidenceKeys,
		ValidToolCalls:        validCalls,
		InvalidToolCalls:      invalidCalls,
		FailureWasRecoverable: fixture.TransientFailures > 0,
		RecoveredFailure:      recovered,
		Tokens:                estimateControlledTokens(history),
		Latency:               time.Since(started),
	}, nil
}

func estimateControlledTokens(history []analyzer.Message) int {
	total := 0
	for _, message := range history {
		total += len([]rune(message.Content))
	}
	if total == 0 {
		return 0
	}
	return (total + 3) / 4
}

type controlledUISink struct{ events []harness.UIEvent }

func (s *controlledUISink) Emit(event harness.UIEvent) { s.events = append(s.events, event) }

type controlledExecutor struct{}

var _ executor.Executor = controlledExecutor{}

func (controlledExecutor) Run(string) (string, error) {
	return "", errors.New("execute not allowed in controlled fixture")
}
func (controlledExecutor) ReadTargetFile(string) ([]byte, error)  { return nil, errors.New("unused") }
func (controlledExecutor) ListTargetDir(string) ([]string, error) { return nil, errors.New("unused") }
func (controlledExecutor) IsRemote() bool                         { return false }
func (controlledExecutor) Close()                                 {}
