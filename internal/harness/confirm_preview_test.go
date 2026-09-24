package harness

import (
	"ai-edr/internal/config"
	"strings"
	"testing"
)

func TestConfirmPreviewExecuteShowsCommand(t *testing.T) {
	got := ConfirmPreview(&AgentAction{Type: ActionExecute, Command: "rm -rf /tmp/example"})
	if got != "rm -rf /tmp/example" {
		t.Fatalf("preview=%q", got)
	}
}

func TestConfirmPreviewFleetExecShowsInnerCommand(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig.Targets = []config.TargetConfig{
		{Name: "prod-ssh", Protocol: "ssh", Tags: []string{"prod"}},
		{Name: "dev-ssh", Protocol: "ssh", Tags: []string{"dev"}},
	}
	got := ConfirmPreview(&AgentAction{
		Type:     ActionTool,
		ToolName: "fleet_exec",
		ToolArgs: map[string]string{"selector": "prod,ssh", "command": "rm -rf /tmp/example"},
	})
	if !strings.Contains(got, "fleet_exec") || !strings.Contains(got, "rm -rf /tmp/example") || !strings.Contains(got, "selector: prod,ssh") || !strings.Contains(got, "matched_targets: 1") {
		t.Fatalf("preview=%q", got)
	}
	if strings.TrimSpace(got) == "fleet_exec" {
		t.Fatal("fleet_exec preview omitted the inner command")
	}
}

func TestConfirmPreviewFleetFileShowsScopeAndPaths(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig.Targets = []config.TargetConfig{{Name: "one", Tags: []string{"prod"}}, {Name: "two", Tags: []string{"dev"}}}
	got := ConfirmPreview(&AgentAction{Type: ActionTool, ToolName: "fleet_file", ToolArgs: map[string]string{
		"selector": "tag:prod", "action": "upload", "remote_path": "/etc/app.conf", "local_path": "/tmp/app.conf",
	}})
	for _, want := range []string{"matched_targets: 1", "selector: tag:prod", "action: upload", "remote_path: /etc/app.conf", "local_path: /tmp/app.conf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview missing %q: %s", want, got)
		}
	}
}

func TestConfirmPreviewTruncatesLongCommand(t *testing.T) {
	command := strings.Repeat("删", confirmPreviewLimit+80)
	got := ConfirmPreview(&AgentAction{Type: ActionExecute, Command: command})
	if !strings.Contains(got, "已截断") {
		t.Fatalf("preview=%q", got)
	}
	if len([]rune(got)) > confirmPreviewLimit {
		t.Fatalf("len=%d limit=%d", len([]rune(got)), confirmPreviewLimit)
	}
	if !strings.HasPrefix(got, strings.Repeat("删", 20)) {
		t.Fatalf("preview=%q", got)
	}
}

func TestConfirmPreviewNil(t *testing.T) {
	if got := ConfirmPreview(nil); got != "" {
		t.Fatalf("preview=%q", got)
	}
}
