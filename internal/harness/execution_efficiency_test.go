package harness

import (
	"ai-edr/internal/config"
	"strings"
	"testing"
)

func TestExecutionEfficiencySurvivesPromptProfiles(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	for _, cfg := range []config.Config{
		{Provider: "ollama", ModelName: "qwen-14b-32k"},
		{Provider: "custom", ModelName: "test", ContextWindowTokens: 65536},
		{Provider: "google", ModelName: "gemini-3.1-pro", ContextWindowTokens: 1048576},
	} {
		config.GlobalConfig = cfg
		prompt := (&DeepAgent{}).BuildSystemPrompt("")
		if strings.Count(prompt, executionEfficiencyPrompt) != 1 {
			t.Fatal("efficiency policy missing or duplicated")
		}
		for _, rule := range []string{"macOS/Linux/Windows 统一 CLI-first", "computer_use", "同一截图只识别一次", "不能绕过授权", "验证明确成功立即 finish", "task_wait", "task_context", "schedule_task", "interval_sec", "/schedule", "old_string 必须能唯一命中", "不要改用 write_file 覆盖"} {
			if !strings.Contains(prompt, rule) {
				t.Fatalf("lost %q", rule)
			}
		}
	}
}
