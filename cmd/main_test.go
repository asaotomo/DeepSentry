package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/executor"

	"github.com/spf13/viper"
)

func TestSwitchToLocalModeClearsProtocolAndRemoteFields(t *testing.T) {
	tmp := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)

	oldConfig := config.GlobalConfig
	defer func() {
		config.GlobalConfig = oldConfig
		viper.Reset()
	}()
	viper.Reset()
	viper.SetConfigType("yaml")
	viper.Set("target_protocol", "ssh")
	viper.Set("ssh_host", "127.0.0.1:2222")
	viper.Set("ssh_user", "root")
	viper.Set("ssh_password", "bad")

	config.GlobalConfig = config.Config{
		TargetProtocol: "ssh",
		SSHHost:        "127.0.0.1:2222",
		SSHUser:        "root",
		SSHPassword:    "bad",
	}

	switchToLocalMode()

	if got := config.GlobalConfig.TargetProtocol; got != "local" {
		t.Fatalf("TargetProtocol = %q, want local", got)
	}
	if config.GlobalConfig.SSHHost != "" || viper.GetString("ssh_host") != "" {
		t.Fatalf("SSH host should be cleared, global=%q viper=%q", config.GlobalConfig.SSHHost, viper.GetString("ssh_host"))
	}
	if got := viper.GetString("target_protocol"); got != "local" {
		t.Fatalf("viper target_protocol = %q, want local", got)
	}
	if err := executor.Init(config.GlobalConfig); err != nil {
		t.Fatalf("executor should initialize local mode after switch, got %v", err)
	}
	if got := executor.CurrentMode(); got != "local" {
		t.Fatalf("executor mode = %q, want local", got)
	}
}

func TestContextWindowWizardChoicesUseExactTokenCounts(t *testing.T) {
	tests := []struct {
		choice string
		want   int
	}{
		{contextWindowAutoChoice, 0},
		{"64K (65,536 tokens)", 65_536},
		{"128K (131,072 tokens)", 131_072},
		{"256K (262,144 tokens)", 262_144},
		{"512K (524,288 tokens)", 524_288},
		{"1M (1,048,576 tokens)", 1_048_576},
		{"2M (2,097,152 tokens)", 2_097_152},
	}
	for _, test := range tests {
		got, ok := contextWindowTokensFromChoice(test.choice)
		if !ok || got != test.want {
			t.Fatalf("choice %q = %d,%v want %d,true", test.choice, got, ok, test.want)
		}
		if gotChoice := contextWindowDefaultChoice(test.want); gotChoice != test.choice {
			t.Fatalf("default choice for %d = %q want %q", test.want, gotChoice, test.choice)
		}
	}
	if _, ok := contextWindowTokensFromChoice(contextWindowCustomChoice); ok {
		t.Fatal("custom choice must require a follow-up value")
	}
	if got := contextWindowDefaultChoice(200_000); got != contextWindowCustomChoice {
		t.Fatalf("non-preset existing value should select custom, got %q", got)
	}
}

func TestValidateCustomContextWindow(t *testing.T) {
	for _, value := range []interface{}{"4096", "1048576", 4194304} {
		if err := validateCustomContextWindow(value); err != nil {
			t.Fatalf("valid custom context %v rejected: %v", value, err)
		}
	}
	for _, value := range []interface{}{"", "2M", "2048", "4194305"} {
		if err := validateCustomContextWindow(value); err == nil {
			t.Fatalf("invalid custom context %v accepted", value)
		}
	}
}

func TestWizardProviderOptionsMapToBuiltInPresets(t *testing.T) {
	expected := map[string]string{}
	for _, id := range wizardProviderOrder {
		expected[wizardProviderLabel(id)] = id
	}
	for label, wantID := range expected {
		if got := wizardProviderID(label); got != wantID {
			t.Fatalf("wizardProviderID(%q) = %q, want %q", label, got, wantID)
		}
		preset, ok := config.FindProvider(wantID)
		if !ok || preset.APIURL == "" || (preset.Model == "" && !config.IsLocalProvider(wantID)) {
			t.Fatalf("wizard option %q has no complete preset: %+v", label, preset)
		}
		found := false
		for _, option := range wizardProviderOptions {
			if option == label {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("wizard option %q is not selectable", label)
		}
	}
}

func TestWizardLocalModelSuggestionsUseDiscoveredIDs(t *testing.T) {
	suggest := wizardModelSuggestions("ollama", "qwen3:14b", "gemma4:31b")
	if got := suggest("qwen"); len(got) != 1 || got[0] != "qwen3:14b" {
		t.Fatalf("local suggestions=%v", got)
	}
	if got := wizardModelSuggestions("vllm")("anything"); len(got) != 0 {
		t.Fatalf("vLLM must not suggest an invented model: %v", got)
	}
}

func TestWizardRecommendedProviderIsFirstAndVisionCapable(t *testing.T) {
	if wizardProviderOptions[0] != wizardRecommendedProvider {
		t.Fatalf("recommended provider must remain first, got %q", wizardProviderOptions[0])
	}
	preset, ok := config.FindProvider(wizardProviderID(wizardRecommendedProvider))
	if !ok || preset.Model != "deepseek-flash" {
		t.Fatalf("unexpected recommended preset: %#v", preset)
	}
	capabilities := (config.Config{Provider: string(preset.ID), ModelName: preset.Model}).EffectiveModelCapabilities()
	if !capabilities.SupportsVision || capabilities.ContextWindowTokens != 1_000_000 {
		t.Fatalf("recommended model capabilities=%#v", capabilities)
	}
}

func TestWizardTerminalThemeChoices(t *testing.T) {
	for _, test := range []struct {
		choice string
		want   string
	}{
		{wizardThemeOptions[0], "auto"},
		{wizardThemeOptions[1], "dark"},
		{wizardThemeOptions[2], "light"},
	} {
		if got := wizardTerminalTheme(test.choice); got != test.want {
			t.Fatalf("wizardTerminalTheme(%q)=%q want %q", test.choice, got, test.want)
		}
		if got := wizardThemeDefault(test.want); got != test.choice {
			t.Fatalf("wizardThemeDefault(%q)=%q want %q", test.want, got, test.choice)
		}
	}
}

func TestCaptureStdoutDrainsLargeStartupOutputAndRestoresStdout(t *testing.T) {
	original := os.Stdout
	payload := strings.Repeat("startup-warning\n", 10000)
	got := captureStdout(func() { fmt.Print(payload) })
	if got != payload {
		t.Fatalf("captured %d bytes want %d", len(got), len(payload))
	}
	if os.Stdout != original {
		t.Fatal("stdout was not restored after capture")
	}
}

func TestCreateWebShellArtifactsUsesPrivateUniqueFiles(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("reports", 0o755); err != nil {
		t.Fatal(err)
	}
	f1, p1, r1, err := createWebShellArtifacts("same_stamp")
	if err != nil {
		t.Fatal(err)
	}
	_ = f1.Close()
	f2, p2, r2, err := createWebShellArtifacts("same_stamp")
	if err != nil {
		t.Fatal(err)
	}
	_ = f2.Close()
	if p1 == p2 || r1 == r2 {
		t.Fatalf("webshell artifacts collided: %s %s", p1, p2)
	}
	for _, path := range []string{p1, p2} {
		info, err := os.Stat(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("progress mode=%o want 600", got)
		}
	}
}

func TestResolvedTUIModeFallsBackForPipes(t *testing.T) {
	tests := []struct {
		name                                       string
		requested, noTUI, nonInteractive           bool
		explicitTUI, interactiveTerminal, expected bool
	}{
		{name: "normal terminal", requested: true, interactiveTerminal: true, expected: true},
		{name: "redirected defaults classic", requested: true, expected: false},
		{name: "explicit tui over redirect", requested: true, explicitTUI: true, expected: true},
		{name: "quiet defaults classic", requested: true, nonInteractive: true, interactiveTerminal: true, expected: false},
		{name: "no tui wins", requested: true, noTUI: true, explicitTUI: true, interactiveTerminal: true, expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvedTUIMode(tt.requested, tt.noTUI, tt.nonInteractive, tt.explicitTUI, tt.interactiveTerminal)
			if got != tt.expected {
				t.Fatalf("resolvedTUIMode()=%v want %v", got, tt.expected)
			}
		})
	}
}

func TestNoTUIExplicitTaskIsOneShotEvenWithPseudoTTY(t *testing.T) {
	if !resolvedNonInteractive(false, false, true, true, true) {
		t.Fatal("--no-tui with an explicit task must not wait for pseudo-TTY stdin")
	}
	if resolvedNonInteractive(false, false, true, true, false) {
		t.Fatal("interactive --no-tui without a task should still be able to prompt for the initial goal")
	}
}

func TestWebShellStatusIsPrivateAndRoundTrips(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("reports", 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("reports", "webshell_status_task.json")
	code := 0
	want := webShellTaskStatus{
		SchemaVersion: 1,
		Status:        "completed",
		WorkerPID:     123,
		ReportPath:    "/tmp/report.md",
		ProgressPath:  "/tmp/progress.log",
		StatusPath:    path,
		QueuedAt:      time.Now().Format(time.RFC3339Nano),
		ExitCode:      &code,
	}
	if err := writeWebShellStatus(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("status mode=%o want 600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got webShellTaskStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != want.Status || got.WorkerPID != want.WorkerPID || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("status round trip mismatch: %+v", got)
	}
	if notice := webShellNoticeText(want.ReportPath, want.ProgressPath, path, "/tmp/latest.txt"); !strings.Contains(notice, "任务状态文件") || !strings.Contains(notice, "cat "+path) {
		t.Fatalf("notice does not expose status path: %s", notice)
	}
}

func TestValidateWebShellArtifactPathRestrictsToReports(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("reports", 0o700); err != nil {
		t.Fatal(err)
	}

	valid := map[string]string{
		"report":   filepath.Join("reports", "report_20260722_120000.md"),
		"progress": filepath.Join("reports", "webshell_progress_20260722_120000.log"),
		"status":   filepath.Join("reports", "webshell_status_20260722_120000.json"),
		"latest":   filepath.Join("reports", "latest_webshell.txt"),
	}
	for kind, path := range valid {
		got, err := validateWebShellArtifactPath(path, kind)
		if err != nil {
			t.Fatalf("valid %s path rejected: %v", kind, err)
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("validated %s path is not absolute: %s", kind, got)
		}
	}

	invalid := []struct {
		kind string
		path string
	}{
		{"status", filepath.Join("..", "webshell_status_task.json")},
		{"status", filepath.Join(tmp, "webshell_status_task.json")},
		{"status", filepath.Join("reports", "nested", "webshell_status_task.json")},
		{"status", filepath.Join("reports", "report_task.md")},
		{"report", filepath.Join("reports", "webshell_status_task.json")},
		{"any", filepath.Join("reports", "random.txt")},
	}
	for _, tt := range invalid {
		if got, err := validateWebShellArtifactPath(tt.path, tt.kind); err == nil {
			t.Fatalf("invalid %s path accepted as %s: %s", tt.kind, got, tt.path)
		}
	}
	if err := writePrivateFile(filepath.Join("..", "webshell_status_task.json"), []byte("bad")); err == nil {
		t.Fatal("writePrivateFile accepted a path outside reports/")
	}
}

func TestBoundedCleanupDoesNotBlockProcessExit(t *testing.T) {
	started := time.Now()
	boundedCleanup("test", 20*time.Millisecond, func() { time.Sleep(250 * time.Millisecond) })
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("bounded cleanup took %s", elapsed)
	}
}

func TestBatchApprovalRequirement(t *testing.T) {
	if prompt, err := batchApprovalRequirement(true, false, true); err != nil || !prompt {
		t.Fatalf("interactive batch should prompt once: prompt=%v err=%v", prompt, err)
	}
	if prompt, err := batchApprovalRequirement(true, false, false); err == nil || prompt {
		t.Fatalf("non-interactive batch without -y should fail: prompt=%v err=%v", prompt, err)
	}
	if prompt, err := batchApprovalRequirement(true, true, false); err != nil || prompt {
		t.Fatalf("explicit -y should allow non-interactive batch: prompt=%v err=%v", prompt, err)
	}
}
