package chat

import (
	"strings"
	"testing"

	"ai-edr/internal/config"
	"ai-edr/internal/ui"
)

func TestFormatDashboardIncludesBannerFields(t *testing.T) {
	prev := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = prev })
	config.GlobalConfig = config.Config{
		Provider:       "glm",
		ModelName:      "glm-5.3-flash",
		UseNativeTools: true,
		MaxSteps:       30,
		MCPServerConfigs: []config.MCPServerConfig{
			{Name: "fofamap"},
			{Name: "hx0-hawkeye"},
		},
	}
	out := FormatDashboard(true)
	for _, want := range []string{
		"DeepSentry " + ui.Version,
		"深海哨兵",
		ui.StudioName,
		"模型",
		"能力",
		"MCP",
		"fofamap",
		"聊天",
		"未开启",
		"聊天执行已授权",
		"就绪",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, out)
		}
	}
}
