package harness

import (
	"fmt"
	"testing"

	"ai-edr/internal/skills"
)

func BenchmarkBuildSystemPrompt(b *testing.B) {
	catalog := &skills.SkillCatalog{}
	for i := 0; i < 100; i++ {
		catalog.Skills = append(catalog.Skills, skills.SkillMeta{
			Name: fmt.Sprintf("skill-%03d", i), Description: "Inspect a specific target and return concise verified evidence", AllowImplicit: true,
		})
	}
	agent := &DeepAgent{State: NewAgentState(""), Middleware: defaultMiddlewareStack(catalog, nil)}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if prompt := agent.BuildSystemPrompt(""); prompt == "" {
			b.Fatal("empty prompt")
		}
	}
}
