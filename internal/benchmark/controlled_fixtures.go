package benchmark

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed testdata/runtime_fixtures.json
var controlledFixtureFS embed.FS

type ControlledRuntimeFixture struct {
	TaskID            string            `json:"task_id"`
	Outputs           map[string]string `json:"outputs"`
	TransientFailures int               `json:"transient_failures,omitempty"`
}

func ControlledRuntimeFixtures() ([]ControlledRuntimeFixture, error) {
	raw, err := controlledFixtureFS.ReadFile("testdata/runtime_fixtures.json")
	if err != nil {
		return nil, err
	}
	var fixtures []ControlledRuntimeFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		return nil, fmt.Errorf("decode controlled runtime fixtures: %w", err)
	}
	if err := validateControlledRuntimeFixtures(fixtures); err != nil {
		return nil, err
	}
	return fixtures, nil
}

func validateControlledRuntimeFixtures(fixtures []ControlledRuntimeFixture) error {
	tasks := RuntimeSecurityTasks()
	byID := make(map[string]ControlledRuntimeFixture, len(fixtures))
	for _, fixture := range fixtures {
		if strings.TrimSpace(fixture.TaskID) == "" {
			return fmt.Errorf("controlled fixture task_id is empty")
		}
		if _, duplicate := byID[fixture.TaskID]; duplicate {
			return fmt.Errorf("duplicate controlled fixture %s", fixture.TaskID)
		}
		byID[fixture.TaskID] = fixture
	}
	for _, task := range tasks {
		fixture, ok := byID[task.ID]
		if !ok {
			return fmt.Errorf("missing controlled fixture %s", task.ID)
		}
		for _, tool := range task.ExpectedTools {
			if strings.TrimSpace(fixture.Outputs[tool]) == "" {
				return fmt.Errorf("fixture %s has no output for tool %s", task.ID, tool)
			}
		}
		joined := make([]string, 0, len(fixture.Outputs))
		for _, output := range fixture.Outputs {
			joined = append(joined, output)
		}
		sort.Strings(joined)
		evidence := strings.Join(joined, "\n")
		for _, key := range task.RequiredEvidence {
			if !strings.Contains(evidence, key+"=") && !strings.Contains(evidence, key+":") {
				return fmt.Errorf("fixture %s does not contain evidence key %s", task.ID, key)
			}
		}
	}
	if len(byID) != len(tasks) {
		return fmt.Errorf("controlled fixture count=%d task count=%d", len(byID), len(tasks))
	}
	return nil
}
