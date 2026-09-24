package harness

import (
	"context"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/desktop"
	"ai-edr/internal/tools"
)

type desktopFixtureBackend struct{ calls int }

func (b *desktopFixtureBackend) Status(context.Context) (desktop.State, error) {
	return desktop.State{Ready: true, Width: 40, Height: 30, Foreground: "fixture"}, nil
}
func (b *desktopFixtureBackend) Capture(_ context.Context, p string) error {
	b.calls++
	f, e := os.Create(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return png.Encode(f, image.NewRGBA(image.Rect(0, 0, 40, 30)))
}
func (b *desktopFixtureBackend) Input(context.Context, desktop.Request) error { return nil }
func (b *desktopFixtureBackend) Release(context.Context) error                { return nil }
func (b *desktopFixtureBackend) Elements(context.Context) (string, []desktop.Element, error) {
	return "vision", nil, nil
}
func TestComputerUseImageReachesAgentAndVisionIsRequired(t *testing.T) {
	oldService, oldConfig := desktop.Default, config.GlobalConfig
	t.Cleanup(func() { desktop.Default = oldService; config.GlobalConfig = oldConfig })
	backend := &desktopFixtureBackend{}
	desktop.Default = desktop.New(backend, t.TempDir())
	step := &StepContext{SessionID: "desktop-test"}
	action := &AgentAction{Type: ActionTool, ToolName: "computer_use", ToolArgs: map[string]string{"action": "observe"}}
	config.GlobalConfig.VisionMode = "disabled"
	denied := handleComputerUse(step, action)
	if backend.calls != 0 || len(denied.Attachments) != 0 {
		t.Fatal("text-only model captured desktop")
	}
	config.GlobalConfig.VisionMode = "enabled"
	result, handled, err := NewToolsMiddleware(nil).HandleAction(step, action)
	if err != nil || !handled || len(result.Attachments) != 1 {
		t.Fatalf("screenshot not attached: %#v %v", result, err)
	}
	attachment := result.Attachments[0]
	if attachment.Detail != "original" || attachment.SHA256 == "" {
		t.Fatal("missing image integrity/detail")
	}
	// Clean up the real OS file lock and idle timer created by observation.
	handleComputerUse(step, &AgentAction{ToolArgs: map[string]string{"action": "release"}})
}
func TestComputerUseRiskAndSchema(t *testing.T) {
	for _, action := range []string{"status", "observe", "stop", "release", "wait"} {
		risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: "computer_use", ToolArgs: map[string]string{"action": action}})
		if risk != tools.RiskLow {
			t.Fatalf("%s risk %s", action, risk)
		}
	}
	args := map[string]string{"action": "click", "x": "0", "y": "0", "action_id": "click-0001", "observation_id": "obs"}
	risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: "computer_use", ToolArgs: args})
	if risk != tools.RiskHigh {
		t.Fatal("desktop input bypasses approval")
	}
	delete(args, "observation_id")
	if tools.ValidateCall("computer_use", args) == nil {
		t.Fatal("schema permits input without screenshot")
	}
	if tools.ValidateCall("computer_use", map[string]string{"action": "click_element", "action_id": "element-01"}) == nil {
		t.Fatal("schema permits element click without a screenshot")
	}
}

func TestSubAgentSharesParentDesktopSession(t *testing.T) {
	for in, want := range map[string]string{
		"session_abc":             "session_abc",
		"session_abc-sub-3":       "session_abc",
		"session_abc-sub-3-sub-9": "session_abc",
		"":                        "",
	} {
		if got := desktopSession(in); got != want {
			t.Fatalf("%q -> %q want %q", in, got, want)
		}
	}
}

func TestBatchModeStillConfirmsDesktopInput(t *testing.T) {
	t.Setenv(computerUseUnattendedEnv, "")
	click := AgentAction{Type: ActionTool, ToolName: "computer_use", ToolArgs: map[string]string{"action": "click"}}
	if !needsAttendedDesktopApproval(click) {
		t.Fatal("batch mode would click without confirmation")
	}
	observe := AgentAction{Type: ActionTool, ToolName: "computer_use", ToolArgs: map[string]string{"action": "observe"}}
	if needsAttendedDesktopApproval(observe) {
		t.Fatal("read-only observe should not need confirmation")
	}
	t.Setenv(computerUseUnattendedEnv, "1")
	if needsAttendedDesktopApproval(click) {
		t.Fatal("explicit unattended opt-in ignored")
	}
}

func TestLatestDesktopScreenshotReplacesOlderFrames(t *testing.T) {
	history := []analyzer.Message{{
		Role: "user", Content: "old",
		Attachments: []analyzer.ImageAttachment{{Path: "/tmp/reports/computer-use/screen-old.png"}},
	}, {
		Role: "user", Content: "mine",
		Attachments: []analyzer.ImageAttachment{{Path: "/tmp/photo.png"}},
	}}
	appendActionResultHistory(&history, AgentAction{}, &ActionResult{
		Output:      "new",
		Attachments: []analyzer.ImageAttachment{{Path: "/tmp/reports/computer-use/screen-new.png"}},
	})
	shots := 0
	kept := ""
	photo := false
	for _, msg := range history {
		for _, attachment := range msg.Attachments {
			if isDesktopShot(attachment.Path) {
				shots++
				kept = attachment.Path
			}
			if strings.HasSuffix(attachment.Path, "photo.png") {
				photo = true
			}
		}
	}
	if shots != 1 || !strings.Contains(kept, "screen-new.png") || !photo {
		t.Fatalf("shots=%d kept=%s photo=%v history=%#v", shots, kept, photo, history)
	}
	if !strings.Contains(history[0].Content, "较早桌面截图已省略") {
		t.Fatalf("old frame not replaced: %q", history[0].Content)
	}
}
