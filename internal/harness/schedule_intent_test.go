package harness

import (
	"strings"
	"testing"

	"ai-edr/internal/config"
)

func TestSchedulePromptUsesStructuredInterval(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	config.GlobalConfig = config.Config{Provider: "custom", ModelName: "test", ContextWindowTokens: 65536}
	prompt := (&DeepAgent{}).BuildSystemPrompt("")
	for _, want := range []string{"interval_sec", "每分钟=60", "/schedule", "schedule_task"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("schedule prompt missing %q", want)
		}
	}
}
