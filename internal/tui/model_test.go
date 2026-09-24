package tui

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/harness"
	"ai-edr/internal/skills"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/viper"
)

func writeTUIImageFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "screen.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImageSlashCommandAddsValidatedDraftChip(t *testing.T) {
	m := NewAgentModel(nil, "deepseek-v4-flash-vision-exp", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 100, 24
	m.recalcLayout()
	cmd := m.handleSlashCommand("/image " + writeTUIImageFixture(t))
	if cmd == nil {
		t.Fatal("/image did not start attachment preparation")
	}
	updated, _ := m.Update(cmd())
	m = updated.(AgentModel)
	if len(m.draftImages) != 1 || m.draftImages[0].MediaType != "image/png" {
		t.Fatalf("image draft missing: %#v", m.draftImages)
	}
	if rendered := stripANSIForTest(m.draftDisplayPrefix()); !strings.Contains(rendered, "[图片 1]") || !strings.Contains(rendered, "screen.png") {
		t.Fatalf("image chip missing: %q", rendered)
	}
	m.clearInputDraft()
	if len(m.draftImages) != 0 {
		t.Fatal("clearing input must remove image drafts")
	}
}

func TestImageSlashCommandRejectsNonImageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-image.png")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	cmd := m.handleSlashCommand("/image " + path)
	updated, _ := m.Update(cmd())
	m = updated.(AgentModel)
	if len(m.draftImages) != 0 || len(m.lines) == 0 || !strings.Contains(m.lines[len(m.lines)-1].content, "附加图片失败") {
		t.Fatalf("invalid image was accepted or error missing: images=%#v lines=%#v", m.draftImages, m.lines)
	}
}

func TestWindowsPasteTargetFocused(t *testing.T) {
	ancestors := []int{20, 10}
	if !windowsPasteTargetFocused(30, ancestors, 30) || !windowsPasteTargetFocused(30, ancestors, 20) {
		t.Fatal("self and ancestor windows should receive Ctrl+V")
	}
	if windowsPasteTargetFocused(30, ancestors, 99) || windowsPasteTargetFocused(0, ancestors, 10) {
		t.Fatal("another process must not receive Ctrl+V")
	}
}

func TestWindowsClipboardCommandKeepsPathInsideScript(t *testing.T) {
	path := `E:\Deepsentry\reports\attachments\session_1\clipboard_20260923_084030.png`
	exe, args := windowsClipboardPowerShell(path)
	if !strings.HasSuffix(exe, `powershell.exe`) {
		t.Fatalf("powershell path: %s", exe)
	}
	if len(args) != 6 || args[3] != "-STA" || args[4] != "-EncodedCommand" {
		t.Fatalf("args: %#v", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, `E:\`) {
			t.Fatalf("path leaked into argv: %#v", args)
		}
	}
	script := decodePowerShellCommand(t, args[5])
	if !strings.Contains(script, `$img.Save('`+path+`',`) || !strings.Contains(script, `GetDataPresent`) || strings.Contains(script, "-Command") {
		t.Fatalf("script: %s", script)
	}
	if got := decodePowerShellCommand(t, argsFromPowerShell(`E:\a'b.png`)); !strings.Contains(got, `$img.Save('E:\a''b.png',`) {
		t.Fatalf("quote escape: %s", got)
	}
}

func decodePowerShellCommand(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	return string(utf16.Decode(units))
}

func argsFromPowerShell(path string) string {
	_, args := windowsClipboardPowerShell(path)
	return args[5]
}

func TestClipboardImageErrorIgnoresEmptyTextSuccess(t *testing.T) {
	result := resolveClipboardPaste(
		"session",
		func(string) (string, error) { return "", errors.New("剪贴板中没有可读取的 PNG 图片") },
		func() (string, error) { return "", errors.New("The operation completed successfully.") },
		analyzer.PrepareImageAttachment,
	)
	if result.err == nil || strings.Contains(result.err.Error(), "读取文本也失败") || strings.Contains(result.err.Error(), "operation completed") {
		t.Fatalf("text success masked the image error: %v", result.err)
	}
}

func TestCtrlVPrioritizesClipboardImageAndFallsBackToText(t *testing.T) {
	imagePath := writeTUIImageFixture(t)
	imageResult := resolveClipboardPaste(
		"session",
		func(string) (string, error) { return imagePath, nil },
		func() (string, error) { return "text fallback must not win", nil },
		analyzer.PrepareImageAttachment,
	)
	if imageResult.err != nil || imageResult.attachment.Path == "" || imageResult.text != "" {
		t.Fatalf("Ctrl+V did not prioritize image: %#v", imageResult)
	}

	textResult := resolveClipboardPaste(
		"session",
		func(string) (string, error) { return "", errors.New("no image") },
		func() (string, error) { return "clipboard text", nil },
		analyzer.PrepareImageAttachment,
	)
	if textResult.err != nil || textResult.text != "clipboard text" || textResult.attachment.Path != "" {
		t.Fatalf("Ctrl+V text fallback failed: %#v", textResult)
	}

	m := NewAgentModel(nil, "vision-model", "local", 30, true, false, StartupInfo{})
	m.input.Focus()
	updated, _ := m.Update(imageResult)
	m = updated.(AgentModel)
	if len(m.draftImages) != 1 {
		t.Fatalf("Ctrl+V image was not attached to draft: %#v", m.draftImages)
	}
	updated, _ = m.Update(textResult)
	m = updated.(AgentModel)
	if got := decodeInputValue(m.input.Value()); got != "clipboard text" {
		t.Fatalf("Ctrl+V text was not inserted: %q", got)
	}
}

func TestCtrlVRemovesRejectedClipboardImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clipboard.png")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := resolveClipboardPaste(
		"session",
		func(string) (string, error) { return path, nil },
		func() (string, error) { return "unused", nil },
		analyzer.PrepareImageAttachment,
	)
	if result.err == nil {
		t.Fatal("invalid clipboard image was accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected clipboard image was not removed: %v", err)
	}
}

func TestCtrlVShortcutStartsDirectClipboardPaste(t *testing.T) {
	m := NewAgentModel(nil, "vision-model", "local", 30, false, false, StartupInfo{})
	if m.input.Focused() {
		t.Fatal("input should start blurred when there is no pending goal")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(AgentModel)
	if cmd == nil {
		t.Fatal("Ctrl+V did not start clipboard paste")
	}
	if len(m.lines) == 0 || !strings.Contains(m.lines[len(m.lines)-1].content, "图片优先") {
		t.Fatalf("Ctrl+V feedback missing: %#v", m.lines)
	}
	if !m.input.Focused() {
		t.Fatal("Ctrl+V should focus the input before reading the clipboard")
	}
}

func TestCmdVShortcutStartsDirectClipboardPaste(t *testing.T) {
	m := NewAgentModel(nil, "vision-model", "local", 30, true, false, StartupInfo{})
	m.input.Focus()
	if !isClipboardPasteShortcut(tea.KeyMsg{Type: tea.KeyCtrlV}) {
		t.Fatal("ctrl+v must be a paste shortcut")
	}
	updated, cmd := m.Update(macosCmdVMsg{})
	m = updated.(AgentModel)
	if cmd == nil {
		t.Fatal("macOS Command+V watcher message did not start image paste")
	}
	updated, _ = m.Update(macosCmdVIgnoredMsg{})
	if _, ok := updated.(AgentModel); !ok {
		t.Fatal("ignored Command+V image miss should be a no-op")
	}
	dup := NewAgentModel(nil, "vision-model", "local", 30, true, false, StartupInfo{})
	updated, first := dup.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	dup = updated.(AgentModel)
	_, second := dup.Update(macosCmdVMsg{})
	if first == nil || second != nil {
		t.Fatal("console Ctrl+V and the key watcher must attach the clipboard only once")
	}
	blurred := NewAgentModel(nil, "vision-model", "local", 30, false, false, StartupInfo{})
	updated, cmd = blurred.Update(macosCmdVMsg{})
	blurred = updated.(AgentModel)
	if cmd == nil || !blurred.input.Focused() {
		t.Fatal("image paste chord should focus the input and read the clipboard")
	}
}

func TestLooksLikeLocalImagePath(t *testing.T) {
	path := writeTUIImageFixture(t)
	if got, ok := looksLikeLocalImagePath(path); !ok || got != path {
		t.Fatalf("absolute image path should attach: got=%q ok=%v", got, ok)
	}
	if _, ok := looksLikeLocalImagePath("not-an-image.txt"); ok {
		t.Fatal("non-image path was treated as an image")
	}
	if got, ok := looksLikeLocalImagePath(path + "\n"); !ok || got != path {
		t.Fatalf("trailing newline should still attach the image path: got=%q ok=%v", got, ok)
	}
}

func TestCtrlVIsNotSwallowedWhenTerminalMarksItAsPaste(t *testing.T) {
	m := NewAgentModel(nil, "vision-model", "local", 30, true, false, StartupInfo{})
	m.input.Focus()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV, Paste: true})
	m = updated.(AgentModel)
	if cmd == nil {
		t.Fatal("Ctrl+V with Paste=true was swallowed as an empty text paste")
	}
	if len(m.lines) == 0 || !strings.Contains(m.lines[len(m.lines)-1].content, "正在读取剪贴板") {
		t.Fatalf("Ctrl+V feedback missing: %#v", m.lines)
	}
}

func TestEmptyBracketedPasteFallsBackToNativeClipboardReader(t *testing.T) {
	m := NewAgentModel(nil, "vision-model", "local", 30, true, false, StartupInfo{})
	m.input.Focus()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Paste: true})
	m = updated.(AgentModel)
	if cmd == nil {
		t.Fatal("empty bracketed paste did not start native clipboard read")
	}
}

func TestSkillCommandFieldsAcceptQuotedAndEscapedPackagePaths(t *testing.T) {
	fields, err := splitSkillCommandFields(`import "/tmp/My Skills/demo.skill" acknowledge-risk`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"import", "/tmp/My Skills/demo.skill", "acknowledge-risk"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("quoted fields=%#v want=%#v", fields, want)
	}
	fields, err = splitSkillCommandFields(`/skill\ packages/demo.skill force`)
	if err != nil || len(fields) != 2 || fields[0] != "/skill packages/demo.skill" {
		t.Fatalf("escaped fields=%#v err=%v", fields, err)
	}
	fields, err = splitSkillCommandFields(`import "C:\Program Files\Skills\demo.skill"`)
	if err != nil || len(fields) != 2 || fields[1] != `C:\Program Files\Skills\demo.skill` {
		t.Fatalf("Windows path fields=%#v err=%v", fields, err)
	}
	fields, err = splitSkillCommandFields(`import "\\server\shared skills\demo.skill"`)
	if err != nil || len(fields) != 2 || fields[1] != `\\server\shared skills\demo.skill` {
		t.Fatalf("UNC path fields=%#v err=%v", fields, err)
	}
	fields, err = splitSkillCommandFields(`add "C:\Skills\"`)
	if err != nil || len(fields) != 2 || fields[1] != `C:\Skills\` {
		t.Fatalf("trailing Windows separator fields=%#v err=%v", fields, err)
	}
}

func TestImageDraftRejectsBatchOverSizeLimitBeforeSubmit(t *testing.T) {
	m := NewAgentModel(nil, "vision-model", "local", 30, true, false, StartupInfo{})
	m.draftImages = []analyzer.ImageAttachment{{Name: "first.png", Size: analyzer.MaxImageBatchBytes - 1}}
	updated, _ := m.Update(imageAttachResultMsg{attachment: analyzer.ImageAttachment{Name: "second.png", MediaType: "image/png", Size: 2}, source: "文件"})
	m = updated.(AgentModel)
	if len(m.draftImages) != 1 || len(m.lines) == 0 || !strings.Contains(m.lines[len(m.lines)-1].content, "总大小") {
		t.Fatalf("oversized image batch was accepted: images=%#v lines=%#v", m.draftImages, m.lines)
	}
}

func TestRestoreConversationHistoryShowsDialogueWithoutToolFeedback(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	finishRaw, err := json.Marshal(harness.AgentAction{Type: harness.ActionFinish, FinalReport: "最终结论：系统正常"})
	if err != nil {
		t.Fatal(err)
	}
	m.restoreConversationHistory([]analyzer.Message{
		{Role: "user", Content: "需求：检查系统版本"},
		{Role: "assistant", Content: `{"action":"execute","command":"uname -a"}`},
		{Role: "user", Content: "Output:\nDarwin secret-internal-output"},
		{Role: "assistant", Content: string(finishRaw)},
		{Role: "system", Content: "【系统】本轮已结束"},
	})
	m.width, m.height = 100, 30
	m.recalcLayout()
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	for _, want := range []string{"已恢复 2 条历史对话记录", "检查系统版本", "最终结论：系统正常"} {
		if !strings.Contains(view, want) {
			t.Fatalf("restored view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "secret-internal-output") || strings.Contains(view, "uname -a") {
		t.Fatalf("tool feedback/action should stay hidden from restored dialogue:\n%s", view)
	}
}

func TestMemoryCluesSlashShowsAndClearsSessionBoard(t *testing.T) {
	state := harness.NewAgentState(t.TempDir())
	state.ObserveCoreClues("关键结论：攻击源 192.0.2.44", "test")
	agent := &harness.DeepAgent{State: state}
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.ctrl = newSessionController(SessionConfig{Agent: agent})
	m.handleMemorySlash("clues")
	m.refreshViewport()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "192.0.2.44") {
		t.Fatalf("/memory clues did not render clue board:\n%s", view)
	}
	m.handleMemorySlash("clues clear")
	if len(state.CoreCluesSnapshot()) != 0 {
		t.Fatal("/memory clues clear did not clear session board")
	}
}

func TestRenderWrappedKeepsToolOutputWithinViewport(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 48
	m.height = 18
	m.recalcLayout()

	longPorts := strings.Repeat(":8080|:8081|:8082|:8083|:8084|", 8)
	m.appendLine("tool", "Shell · ss -tuln | grep -E '"+longPorts+"'", longPorts)
	m.refreshViewport()

	for _, line := range strings.Split(m.viewport.View(), "\n") {
		if got := lipgloss.Width(line); got > m.viewport.Width {
			t.Fatalf("rendered line width = %d, want <= %d: %q", got, m.viewport.Width, stripANSIForTest(line))
		}
	}
}

func TestInputFocusedAbsorbsGlobalShortcuts(t *testing.T) {
	shortcuts := []string{"q", "e", "j", "k"}
	for _, k := range shortcuts {
		m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
		if !m.inputFocused() {
			t.Fatalf("key %q: expected input focused", k)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		model := updated.(AgentModel)
		if model.quitting {
			t.Fatalf("key %q should not quit when input focused", k)
		}
		if !strings.Contains(model.input.Value(), k) {
			t.Fatalf("key %q should be typed into input, got %q", k, model.input.Value())
		}
	}
}

func TestGlobalQuitOnlyWhenInputBlurred(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.done = true
	m.input.Blur()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(AgentModel)
	if !model.quitting {
		t.Fatal("q should quit when input blurred and session idle")
	}
	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

func TestQDoesNotDiscardRunningTask(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.running = true
	m.sessionLive = true
	m.input.Blur()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(AgentModel)
	if model.quitting || cmd != nil {
		t.Fatal("q must not quit and discard an active task; Esc is the stop command")
	}
}

func TestSlashCommandRestoresViewportAfterSuggestionMenu(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 24
	m.input.SetValue("/")
	m.recalcLayout()
	withMenu := m.viewport.Height

	m.handleSlashCommand("/status")
	if m.input.Value() != "" {
		t.Fatalf("slash command should clear input, got %q", m.input.Value())
	}
	if m.viewport.Height <= withMenu {
		t.Fatalf("viewport height remained reserved for closed suggestions: before=%d after=%d", withMenu, m.viewport.Height)
	}
}

func TestSkillListSlashRefreshesAndRendersActualResult(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 100, 24
	m.recalcLayout()
	m.refreshViewport()
	m.input.SetValue("/skill list")
	m.input.SetCursor(len([]rune(m.input.Value())))

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(AgentModel)
	if cmd != nil {
		t.Fatal("/skill list should complete synchronously")
	}
	if m.input.Value() != "" {
		t.Fatalf("submitted slash command should clear input, got %q", m.input.Value())
	}
	if len(m.lines) == 0 || m.lines[len(m.lines)-1].kind != "result" {
		t.Fatalf("/skill list did not append a result: %#v", m.lines)
	}
	if m.lines[len(m.lines)-1].content != m.lines[len(m.lines)-1].raw {
		t.Fatalf("skill list stored command text as result detail: %#v", m.lines[len(m.lines)-1])
	}
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "当前没有发现 Skill") {
		t.Fatalf("/skill list result was not refreshed into the viewport:\n%s", view)
	}
	if strings.Contains(view, "└ skill list") {
		t.Fatalf("viewport rendered the command name instead of its result:\n%s", view)
	}
}

func TestSkillListSlashReturnsScrolledViewToResult(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 18
	m.recalcLayout()
	for i := 0; i < 60; i++ {
		m.appendLine("info", fmt.Sprintf("old output %02d", i), "")
	}
	m.refreshViewport()
	m.autoScroll = false
	m.viewport.GotoTop()
	if m.viewport.AtBottom() {
		t.Fatal("test setup must start away from the live tail")
	}
	m.input.SetValue("/skill list")
	m.input.SetCursor(len([]rune(m.input.Value())))

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(AgentModel)
	if cmd != nil {
		t.Fatal("/skill list should complete synchronously")
	}
	if !m.autoScroll || !m.viewport.AtBottom() {
		t.Fatalf("slash submission did not return to live tail: auto=%v offset=%d", m.autoScroll, m.viewport.YOffset)
	}
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "当前没有发现 Skill") {
		t.Fatalf("latest slash result is not visible after submitting from scrollback:\n%s", view)
	}
}

func TestSkillRescanDiscoversManualLandingWithoutRestart(t *testing.T) {
	root := t.TempDir()
	catalog, err := skills.LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	state := harness.NewAgentState(root)
	agent := &harness.DeepAgent{Catalog: catalog, State: state}
	m := NewAgentModel(newSessionController(SessionConfig{Agent: agent}), "model", "local", 30, true, false, StartupInfo{})
	dir := filepath.Join(root, "landed-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: landed-skill\ndescription: Manual landing test\n---\n# Landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.handleSkillSlash("rescan")
	if _, ok := catalog.FindSkill("landed-skill"); !ok {
		t.Fatalf("catalog after rescan=%#v", catalog.Skills)
	}
	m.loadCurrentSkill("LANDED-SKILL")
	if _, ok := state.LoadedSkills["landed-skill"]; !ok {
		t.Fatalf("loaded skills=%#v", state.LoadedSkills)
	}
}

func TestSkillUnloadDisablesByNameEvenWhenNotLoaded(t *testing.T) {
	oldConfig := config.GlobalConfig
	t.Cleanup(func() {
		config.GlobalConfig = oldConfig
		viper.Reset()
	})

	root := t.TempDir()
	dir := filepath.Join(root, "fun-brainstorming")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: fun-brainstorming\ndescription: Creative workflow\n---\n# Body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("skill_sources:\n  - \""+root+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.InitConfig(configPath); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	state := harness.NewAgentState(root)
	agent := &harness.DeepAgent{Catalog: catalog, State: state}
	m := NewAgentModel(newSessionController(SessionConfig{Agent: agent}), "model", "local", 30, true, false, StartupInfo{})

	m.handleSkillSlash("unload fun-brainstorming")
	if len(state.LoadedSkills) != 0 {
		t.Fatalf("test Skill was never loaded but state changed unexpectedly: %#v", state.LoadedSkills)
	}
	if !catalog.IsDisabled("FUN-BRAINSTORMING") {
		t.Fatal("unload did not persist a name-level disable policy")
	}
	if _, ok := catalog.FindSkill("fun-brainstorming"); ok {
		t.Fatal("disabled Skill remained discoverable")
	}
	if view := m.formatLocalSkillList(); !strings.Contains(view, "fun-brainstorming [已禁用]") {
		t.Fatalf("disabled Skill missing from list output:\n%s", view)
	}

	m.handleSkillSlash("on fun-brainstorming")
	if catalog.IsDisabled("fun-brainstorming") {
		t.Fatal("/skill on did not clear the denylist")
	}
	if _, ok := catalog.FindSkill("fun-brainstorming"); !ok {
		t.Fatal("/skill on did not rediscover the Skill immediately")
	}

	m.loadCurrentSkill("fun-brainstorming")
	if len(state.LoadedSkills) != 1 {
		t.Fatalf("failed to set up loaded Skill: %#v", state.LoadedSkills)
	}
	m.handleSkillSlash("off")
	if !config.GlobalConfig.SkillsDisabled || len(catalog.Skills) != 0 || len(state.LoadedSkills) != 0 {
		t.Fatalf("parameterless /skill off did not disable all Skills: global=%v catalog=%#v loaded=%#v", config.GlobalConfig.SkillsDisabled, catalog.Skills, state.LoadedSkills)
	}
	m.handleSkillSlash("on")
	if config.GlobalConfig.SkillsDisabled {
		t.Fatal("parameterless /skill on did not restore the global switch")
	}
	if _, ok := catalog.FindSkill("fun-brainstorming"); !ok {
		t.Fatal("global /skill on did not rediscover Skills")
	}
}

func TestSkillOnlyEnablesSelectedAndDisablesAllOtherDiscoveredSkills(t *testing.T) {
	oldConfig := config.GlobalConfig
	t.Cleanup(func() {
		config.GlobalConfig = oldConfig
		viper.Reset()
	})

	root := t.TempDir()
	for _, name := range []string{"fofamap", "fun-brainstorming", "log-audit"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		doc := fmt.Sprintf("---\nname: %s\ndescription: %s test\n---\n# %s\n", name, name, name)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("skills_disabled: true\nskill_sources:\n  - \""+root+"\"\ndisabled_skills:\n  - fofamap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.InitConfig(configPath); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	catalog.ApplyDisabledSkills(config.GlobalConfig.EffectiveDisabledSkills())
	state := harness.NewAgentState(root)
	state.LoadedSkills = map[string]string{"fun-brainstorming": "old", "log-audit": "old"}
	agent := &harness.DeepAgent{Catalog: catalog, State: state}
	m := NewAgentModel(newSessionController(SessionConfig{Agent: agent}), "model", "local", 30, true, false, StartupInfo{})

	m.handleSkillSlash("only fofamap")
	if config.GlobalConfig.SkillsDisabled {
		t.Fatal("/skill only left the global switch disabled")
	}
	if len(catalog.Skills) != 1 || !strings.EqualFold(catalog.Skills[0].Name, "fofamap") {
		t.Fatalf("available Skills=%#v, want only fofamap", catalog.Skills)
	}
	if len(state.LoadedSkills) != 0 {
		t.Fatalf("disabled Skills remained loaded: %#v", state.LoadedSkills)
	}
	for _, want := range []string{"fun-brainstorming", "log-audit"} {
		if !catalog.IsDisabled(want) {
			t.Fatalf("%s was not disabled", want)
		}
	}
	if catalog.IsDisabled("fofamap") {
		t.Fatal("selected Skill remained disabled")
	}
	if len(m.lines) == 0 || !strings.Contains(m.lines[len(m.lines)-1].content, "保留 fofamap") {
		t.Fatalf("missing only confirmation: %#v", m.lines)
	}
}

func TestSudoResultRestoresTerminalBeforeAgentResumes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ok   bool
	}{
		{name: "success", ok: true},
		{name: "failure", err: errors.New("exit status 1"), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
			m.width, m.height = 100, 30
			m.selecting = true
			m.selActive = true
			respCh := make(chan bool, 1)

			updated, cmd := m.Update(sudoAuthResultMsg{respCh: respCh, err: tt.err})
			m = updated.(AgentModel)
			if cmd == nil {
				t.Fatal("sudo result must schedule alternate-screen, mouse-mode and repaint restoration")
			}
			select {
			case <-respCh:
				t.Fatal("Agent resumed before the terminal restoration sequence completed")
			default:
			}

			updated, _ = m.Update(sudoTerminalRestoredMsg{respCh: respCh, ok: tt.ok})
			m = updated.(AgentModel)
			if m.selecting || m.selActive {
				t.Fatal("stale mouse selection survived sudo terminal restoration")
			}
			select {
			case got := <-respCh:
				if got != tt.ok {
					t.Fatalf("sudo response = %v, want %v", got, tt.ok)
				}
			default:
				t.Fatal("Agent was not resumed after terminal restoration")
			}
		})
	}
}

func TestWrappedInputFocusChangesRecalculateFooter(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 24
	value := strings.Repeat("中文输入", 80)
	m.input.SetValue(value)
	m.input.SetCursor(len([]rune(value)))
	m.recalcLayout()
	focusedHeight := m.viewport.Height

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(AgentModel)
	if m.inputFocused() {
		t.Fatal("Esc should blur input")
	}
	if m.viewport.Height <= focusedHeight {
		t.Fatalf("blurred wrapped input left stale footer space: focused=%d blurred=%d", focusedHeight, m.viewport.Height)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(AgentModel)
	if !m.inputFocused() {
		t.Fatal("Tab should focus input")
	}
	if m.viewport.Height != focusedHeight {
		t.Fatalf("refocused wrapped input layout=%d want %d", m.viewport.Height, focusedHeight)
	}
}

func TestTruncateStrIsUnicodeSafe(t *testing.T) {
	got := truncateStr("这个任务会检查 Windows Terminal 里的 emoji ✅ 和中文宽度", 18)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateStr returned invalid UTF-8: %q", got)
	}
	if width := lipgloss.Width(got); width > 18 {
		t.Fatalf("truncateStr width = %d, want <= 18: %q", width, got)
	}
}

func TestTargetStatusUpdatesCurrentTarget(t *testing.T) {
	m := NewAgentModel(nil, "model", "Fleet 多目标: 2 台", 30, true, false, StartupInfo{})
	m.applyEvent(harness.UIEvent{
		Kind:           harness.EventTargetStatus,
		Status:         "running",
		Message:        "fleet_exec uptime",
		TargetName:     "web-01",
		TargetProtocol: "ssh",
		TargetHost:     "10.0.0.1:22",
	})
	if !strings.Contains(m.currentTarget, "web-01") {
		t.Fatalf("current target not updated: %q", m.currentTarget)
	}
	if len(m.lines) == 0 || m.lines[len(m.lines)-1].kind != "target" {
		t.Fatalf("target event not appended: %#v", m.lines)
	}
}

func TestFormatActionLineShowsExecutionTarget(t *testing.T) {
	local := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "date", TargetProtocol: "local"})
	if !strings.Contains(local, "控制端本机") || !strings.Contains(local, "date") {
		t.Fatalf("local action line missing target: %q", local)
	}
	if strings.Contains(local, "远端") {
		t.Fatalf("local action line should not be remote: %q", local)
	}

	localRun := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "local_run hostname", TargetProtocol: "local", TargetHost: "198.51.100.42:2222"})
	if !strings.Contains(localRun, "控制端本机") || strings.Contains(localRun, "远端") {
		t.Fatalf("local_run action line should stay local even with stale host: %q", localRun)
	}

	remote := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "id", TargetProtocol: "ssh", TargetHost: "198.51.100.42:2222"})
	for _, want := range []string{"远端 SSH", "198.51.100.42:2222", "id"} {
		if !strings.Contains(remote, want) {
			t.Fatalf("remote action line missing %q: %q", want, remote)
		}
	}

	fleet := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "uptime", TargetName: "web-01", TargetProtocol: "ssh", TargetHost: "10.0.0.1:22"})
	for _, want := range []string{"远端 SSH", "web-01", "10.0.0.1:22", "uptime"} {
		if !strings.Contains(fleet, want) {
			t.Fatalf("fleet action line missing %q: %q", want, fleet)
		}
	}
}

func TestFormatActionLineShowsIncompleteSubAgentTask(t *testing.T) {
	line := FormatActionLine(&harness.AgentAction{Type: harness.ActionTask})
	if !strings.Contains(line, "参数不完整") {
		t.Fatalf("empty sub-agent task should show incomplete params: %q", line)
	}
	if strings.Contains(line, "->") || strings.Contains(line, "→") {
		t.Fatalf("empty sub-agent task should not render an empty arrow: %q", line)
	}
}

func TestCommandOutputGroupCollapsesAndExpands(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width = 100
	m.height = 24
	m.recalcLayout()

	m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: "cat /etc/passwd"}})
	m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "root:x:0:0:root:/root:/bin/bash\n"})
	m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n"})
	group := m.activeCmdGroup
	if group <= 0 {
		t.Fatal("expected active command output group")
	}

	m.collapseCommandOutputGroup(group)
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "命令输出已折叠") || !strings.Contains(view, "[e 全部展开]") {
		t.Fatalf("collapsed command output summary missing:\n%s", view)
	}
	if strings.Contains(view, "daemon:x") {
		t.Fatalf("collapsed command output should hide later lines:\n%s", view)
	}

	m.toggleLastCollapsible()
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "root:x") || !strings.Contains(view, "daemon:x") {
		t.Fatalf("expanded command output should show original lines:\n%s", view)
	}
}

func TestETogglesEveryCollapsibleAsOneGlobalState(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 100, 24
	m.recalcLayout()
	m.lines = []logLine{
		{kind: "result", content: "工具摘要一", raw: "工具完整结果一", collapsed: true},
		{kind: "result", content: "工具摘要二", raw: "工具完整结果二"},
		{kind: "cmdout", content: "group one / line one", raw: "group one / line one", group: 1, groupHead: true, collapsed: true},
		{kind: "cmdout", content: "group one / line two", raw: "group one / line two", group: 1, collapsed: true},
		{kind: "cmdout", content: "group two", raw: "group two", group: 2, groupHead: true},
		{kind: "subagent_result", content: "子 Agent 结果", raw: "子 Agent 完整结果", collapsed: true},
		{kind: "stream", content: "已完成思考", raw: "已完成思考完整内容", settled: true},
		{kind: "stream", content: "正在思考", raw: "正在思考完整内容"},
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(AgentModel)
	for i := 0; i < len(m.lines)-1; i++ {
		if m.lines[i].collapsed {
			t.Fatalf("first e should expand every collapsible; line %d remains collapsed: %#v", i, m.lines[i])
		}
	}
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("active reasoning stream must not be changed by e")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(AgentModel)
	for i := 0; i < len(m.lines)-1; i++ {
		if !m.lines[i].collapsed {
			t.Fatalf("second e should collapse every collapsible; line %d remains expanded: %#v", i, m.lines[i])
		}
	}
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("active reasoning stream must remain visible after the second e")
	}
}

func TestCommandCompletionSchedulesCollapse(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: "seq 3"}})
	m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "1\n"})

	updated, cmd := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventResult, Message: "命令执行完成"}))
	model := updated.(AgentModel)
	if cmd == nil {
		t.Fatal("expected command output collapse timer")
	}
	if model.activeCmdGroup != 0 {
		t.Fatalf("active command group should reset after command completion, got %d", model.activeCmdGroup)
	}
}

func TestAutomaticCommandCollapsePreservesShortOutputAndReadingPosition(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.startCommandOutputGroup()
	group := m.activeCmdGroup
	m.appendCommandOutputLine("short result", "short result")
	updated, _ := m.Update(cmdOutputCollapseMsg{group: group})
	m = updated.(AgentModel)
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("short command output should remain visible")
	}

	for i := 0; i < 20; i++ {
		m.appendCommandOutputLine(fmt.Sprintf("long line %d", i), fmt.Sprintf("long line %d", i))
	}
	m.autoScroll = false
	updated, _ = m.Update(cmdOutputCollapseMsg{group: group})
	m = updated.(AgentModel)
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("automatic collapse must not shift content while user is reading above")
	}

	m.autoScroll = true
	updated, _ = m.Update(cmdOutputCollapseMsg{group: group})
	m = updated.(AgentModel)
	if !m.lines[len(m.lines)-1].collapsed {
		t.Fatal("long output should auto-collapse while following the live tail")
	}
}

func TestCommandOutputRepaintsAreCoalesced(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})

	updated, firstCmd := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "line 1\n"}))
	m = updated.(AgentModel)
	if firstCmd == nil || !m.commandTick {
		t.Fatal("first command line should schedule a coalesced viewport repaint")
	}

	updated, secondCmd := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "line 2\n"}))
	m = updated.(AgentModel)
	if secondCmd != nil {
		t.Fatal("additional command lines in the same window should reuse the pending repaint")
	}

	updated, _ = m.Update(commandOutputRefreshMsg{})
	m = updated.(AgentModel)
	if m.commandTick {
		t.Fatal("refresh tick should clear the coalescing flag")
	}
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "line 1") || !strings.Contains(view, "line 2") {
		t.Fatalf("coalesced repaint lost command output:\n%s", view)
	}
}

func TestStreamCollapseControlsAppearOnlyAfterCompletion(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 20
	m.recalcLayout()
	raw := `{"action":"finish","final_report":"最终答案"}`
	m.appendStreamDelta(raw)
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "正在整理最终报告") {
		t.Fatalf("active stream progress missing:\n%s", view)
	}
	if strings.Contains(view, "[e 全部折叠]") || strings.Contains(view, "[e 全部展开]") {
		t.Fatalf("active stream must not show collapse controls:\n%s", view)
	}
	m.toggleLastCollapsible()
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("e must not collapse an active stream")
	}

	m.finalizeStream(raw)
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if strings.Contains(view, "[e 全部折叠]") || strings.Contains(view, "[e 全部展开]") {
		t.Fatalf("stream end alone must not expose controls before its result is rendered:\n%s", view)
	}
	m.toggleLastCollapsible()
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("e must not collapse a completed stream before its result is rendered")
	}

	updated, _ := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventFinish, Message: "最终答案"}))
	m = updated.(AgentModel)
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "最终答案") || !strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("stream should become collapsible only with its rendered result:\n%s", view)
	}
	m.toggleLastCollapsible()
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "[e 全部展开]") || strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("collapsed completed stream should expose expand control:\n%s", view)
	}
}

func TestStreamDeltasMaterializeOnRefreshAndEndRestoresFullText(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.appendStreamDelta(`{"thought":"hello`)
	m.appendStreamDelta(` world"}`)
	if m.lines[m.streamIdx].raw != "" {
		t.Fatal("stream was rebuilt before a viewport refresh")
	}
	m.refreshViewport()
	if got := m.lines[m.streamIdx].raw; got != `{"thought":"hello world"}` {
		t.Fatalf("materialized stream = %q", got)
	}
	if !strings.Contains(m.lines[m.streamIdx].content, "hello world") {
		t.Fatalf("stream display = %q", m.lines[m.streamIdx].content)
	}
	m.finalizeStream(`{"thought":"hello world from the full end event"}`)
	if got := m.lines[len(m.lines)-1].raw; got != `{"thought":"hello world from the full end event"}` {
		t.Fatalf("end event did not restore complete stream: %q", got)
	}
	if len(m.streamRaw) != 0 || m.streamDirty {
		t.Fatal("finished stream buffer was retained")
	}
}

func TestStreamEndRestoresLineWhenAllPreviewDeltasWereDropped(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	full := `{"thought":"complete despite display backpressure"}`
	m.finalizeStream(full)
	if len(m.lines) == 0 || m.lines[len(m.lines)-1].kind != "stream" || m.lines[len(m.lines)-1].raw != full {
		t.Fatalf("full stream event was lost: %#v", m.lines)
	}
}

func TestTrimmedCommandGroupStillHasCollapsibleHead(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.appendLine("user", "You: 最初的对话", "最初的对话")
	m.startCommandOutputGroup()
	for i := 0; i < 1020; i++ {
		m.appendCommandOutputLine(fmt.Sprintf("line %03d", i), fmt.Sprintf("line %03d", i))
	}
	if len(m.lines) != 1000 {
		t.Fatalf("retained lines=%d want 1000", len(m.lines))
	}
	if m.lines[0].kind != "user" || !strings.Contains(m.lines[0].content, "最初的对话") {
		t.Fatalf("initial user conversation was evicted: %#v", m.lines[0])
	}
	if !m.lines[1].groupHead {
		t.Fatal("first retained command row must become the group head after trimming")
	}
	m.collapseCommandOutputGroup(m.activeCmdGroup)
	m.width, m.height = 80, 20
	m.recalcLayout()
	m.refreshViewport()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "命令输出已折叠：999 行") {
		t.Fatalf("trimmed group disappeared after collapse:\n%s", view)
	}
}

func TestToolResultKeepsFullDetailAndCanExpand(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 24
	m.recalcLayout()
	detail := strings.Join([]string{
		"finding 01", "finding 02", "finding 03", "finding 04", "finding 05",
		"finding 06", "finding 07", "finding 08", "finding 09", "FINAL_EVIDENCE",
	}, "\n")
	m.applyEvent(harness.UIEvent{Kind: harness.EventResult, Message: "tool returned 10 findings...", Detail: detail})
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "展开完整结果") || strings.Contains(view, "FINAL_EVIDENCE") {
		t.Fatalf("long tool result should start collapsed:\n%s", view)
	}
	m.toggleLastCollapsible()
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "FINAL_EVIDENCE") || !strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("expanded tool result lost full detail:\n%s", view)
	}
}

func TestSubAgentResultExpansionShowsFullDetail(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 24
	m.recalcLayout()
	detail := strings.Join(append([]string{"summary"}, make([]string, 15)...), "\n") + "\nSUBAGENT_FINAL_EVIDENCE"
	m.applyEvent(harness.UIEvent{Kind: harness.EventSubAgentResult, Message: "worker", Detail: detail})
	m.refreshViewport()
	if strings.Contains(stripANSIForTest(m.viewport.View()), "SUBAGENT_FINAL_EVIDENCE") {
		t.Fatal("sub-agent result should start collapsed")
	}
	m.toggleLastCollapsible()
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "SUBAGENT_FINAL_EVIDENCE") || !strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("expanded sub-agent result lost full detail:\n%s", view)
	}
}

func TestStatusContextAvoidsNoActivityPlaceholder(t *testing.T) {
	m := NewAgentModel(nil, "model", "SSH -> 1.2.3.4:22", 30, true, false, StartupInfo{})
	if got := m.statusContextText(); got != "就绪/等待任务" {
		t.Fatalf("idle status=%q", got)
	}
	m.running = true
	if got := m.statusContextText(); got != "执行中" {
		t.Fatalf("running status=%q", got)
	}
	m.thinking = true
	if got := m.statusContextText(); got != "模型思考中" {
		t.Fatalf("thinking status=%q", got)
	}
	m.currentTarget = "web-01 (ssh 10.0.0.1:22)"
	if got := m.statusContextText(); !strings.Contains(got, "web-01") {
		t.Fatalf("target status=%q", got)
	}
}

func TestAwaitUserEventAndAskMsgDeduplicate(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	action := &harness.AgentAction{
		Type:     harness.ActionAskUser,
		Question: "请选择 CPU 告警阈值",
		Options:  []string{"CPU阈值 80%", "CPU阈值 90%"},
	}
	m.applyEvent(harness.UIEvent{
		Kind:    harness.EventAwaitUser,
		Message: "请选择 CPU 告警阈值\n\n可选：\n1. CPU阈值 80%\n2. CPU阈值 90%",
		Action:  action,
	})
	// The sink and direct interaction queue can interleave a thought between
	// EventAwaitUser and askMsg. This is the ordering seen in the screenshot.
	m.applyEvent(harness.UIEvent{Kind: harness.EventThought, Message: "先引导用户说明具体需求"})
	updated, _ := m.Update(askMsg{
		action:  action,
		prompt:  action.Question,
		options: action.Options,
		respCh:  make(chan string, 1),
	})
	model := updated.(AgentModel)
	askLines := 0
	for _, line := range model.lines {
		if line.kind == "ask" {
			askLines++
			if strings.Count(line.content, "CPU阈值 80%") != 1 {
				t.Fatalf("option rendered more than once: %q", line.content)
			}
		}
	}
	if askLines != 1 {
		t.Fatalf("expected one ask line, got %d: %#v", askLines, model.lines)
	}
	if model.lines[len(model.lines)-1].kind != "ask" {
		t.Fatalf("deduplicated question should be immediately above input: %#v", model.lines)
	}
}

func TestHistoryNavigationJumpsToTopAndBottom(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 80, 14
	m.recalcLayout()
	for i := 0; i < 80; i++ {
		m.appendLine("info", fmt.Sprintf("history line %03d", i), "history")
	}
	m.refreshViewport()
	if m.viewport.YOffset == 0 {
		t.Fatal("test requires scrollable history at the bottom")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(AgentModel)
	if m.viewport.YOffset != 0 || m.autoScroll {
		t.Fatalf("g should jump to history top: offset=%d auto=%v", m.viewport.YOffset, m.autoScroll)
	}
	if got := m.scrollPositionText(); got != "滚动 顶部" {
		t.Fatalf("top scroll status=%q", got)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(AgentModel)
	if !m.viewport.AtBottom() || !m.autoScroll {
		t.Fatalf("G should return to live tail: offset=%d auto=%v", m.viewport.YOffset, m.autoScroll)
	}
	if got := m.scrollPositionText(); got != "滚动 底部" {
		t.Fatalf("bottom scroll status=%q", got)
	}

	// Waiting-for-answer mode keeps the input focused; navigation must still
	// provide direct top/bottom jumps without discarding the draft.
	m.input.Focus()
	m.input.SetValue("draft answer")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	m = updated.(AgentModel)
	if m.viewport.YOffset != 0 || m.input.Value() != "draft answer" {
		t.Fatalf("Ctrl+Home should browse without changing input: offset=%d value=%q", m.viewport.YOffset, m.input.Value())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	m = updated.(AgentModel)
	if !m.viewport.AtBottom() || m.input.Value() != "draft answer" {
		t.Fatalf("Ctrl+End should return to tail without changing input: offset=%d value=%q", m.viewport.YOffset, m.input.Value())
	}
}

func TestHistoryTrimmingPreservesConversationAndShowsNotice(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 20
	m.recalcLayout()
	m.appendLine("user", "You: ORIGINAL_USER_MESSAGE", "ORIGINAL_USER_MESSAGE")
	m.appendLine("ask", "ORIGINAL_QUESTION", "ORIGINAL_QUESTION")
	m.appendLine("success", "ORIGINAL_FINAL_REPORT", "ORIGINAL_FINAL_REPORT")
	for i := 0; i < 1100; i++ {
		m.appendLine("info", fmt.Sprintf("volatile log %04d", i), "volatile")
	}
	for _, want := range []string{"ORIGINAL_USER_MESSAGE", "ORIGINAL_QUESTION", "ORIGINAL_FINAL_REPORT"} {
		found := false
		for _, line := range m.lines {
			if strings.Contains(line.raw, want) || strings.Contains(line.content, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("preserved conversation entry %q was trimmed", want)
		}
	}
	if m.trimmedLines == 0 {
		t.Fatal("expected volatile logs to be compacted")
	}
	m.refreshViewport()
	m.viewport.GotoTop()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "用户对话、询问和最终结论已保留") {
		t.Fatalf("history compaction notice missing:\n%s", view)
	}
}

func TestFocusedEmptyInputLeavesHintOffTheCompositionLine(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 80, 24
	m.sessionLive = true
	m.input.Focus()
	m.recalcLayout()

	rows, _, _ := m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	rendered := stripANSIForTest(strings.Join(rows, "\n"))
	if strings.Contains(rendered, "追问上一题") || strings.Contains(rendered, "Enter 发送") {
		t.Fatalf("hint was painted into the focused input row: %q", rendered)
	}
	view := stripANSIForTest(m.View())
	if !strings.Contains(view, "追问上一题或继续排查，Enter 发送") {
		t.Fatalf("hint should stay on the help line:\n%s", view)
	}

	m.input.SetValue("yi")
	m.input.SetCursor(2)
	rows, _, _ = m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	rendered = stripANSIForTest(strings.Join(rows, "\n"))
	if strings.Contains(rendered, "追问上一题") {
		t.Fatalf("hint leaked into typed input: %q", rendered)
	}
	if !strings.Contains(rendered, "yi") {
		t.Fatalf("typed text missing: %q", rendered)
	}
}

func TestInputViewRendersFocusedInputAboveHelpLine(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.input.SetValue("ni")
	m.input.SetCursor(2)

	view := m.View()
	row, col, ok := m.inputCursorAnchor()
	if !ok {
		t.Fatal("focused input should expose terminal cursor anchor")
	}
	if strings.Contains(view, fmt.Sprintf("\x1b[%d;%dH", row, col)) {
		t.Fatalf("view should not embed cursor movement; anchor must run after renderer")
	}
	if row <= 0 || col <= 0 {
		t.Fatalf("invalid cursor anchor row=%d col=%d", row, col)
	}
	m.scheduleInputCursorAnchor()
	lines := strings.Split(stripANSIForTest(view), "\n")
	var inputLine string
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "ni") {
			inputLine = lines[i]
			break
		}
	}
	if inputLine == "" {
		t.Fatalf("focused input should render in bordered box, got footer %#v", lines[len(lines)-4:])
	}
	lastLine := strings.TrimRight(lines[len(lines)-1], " ")
	if !strings.Contains(lastLine, "Enter 发送") {
		t.Fatalf("help line should stay below input, got %q", lastLine)
	}
}

func TestViewSelfHealsStaleViewportAndPreservesCompleteIMEInput(t *testing.T) {
	m := NewAgentModel(nil, "provider / model", "本地模式", 50, true, false, StartupInfo{})
	m.width, m.height = 213, 57
	m.input.Focus()
	m.input.SetValue("你锚点")
	m.input.SetCursor(1)
	for i := 0; i < 180; i++ {
		m.appendLine("info", fmt.Sprintf("长报告内容 %03d %s", i, strings.Repeat("奥特曼资料 ", 18)), "report")
	}
	m.recalcLayout()
	m.refreshViewport()

	// Reproduce the failure mode from the screenshot: the viewport still
	// carries an older/taller frame budget when the focused footer is drawn.
	// View must repair this itself instead of relying on bottom clipping.
	m.viewport.Height = m.height
	view := m.View()
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("rendered frame height=%d, want terminal height=%d", got, m.height)
	}
	plain := stripANSIForTest(view)
	row, col, ok := renderedMarkerPosition(plain, "锚点")
	if !ok {
		t.Fatalf("focused Chinese input disappeared from the footer:\n%s", plain)
	}
	lines := strings.Split(plain, "\n")
	if row < 2 || row+1 >= len(lines) {
		t.Fatalf("input row %d does not leave room for both borders/help (frame rows=%d)", row, len(lines))
	}
	topBorder := strings.Contains(lines[row-2], "╭") || strings.Contains(lines[row-2], "+-")
	bottomBorder := strings.Contains(lines[row], "╰") || strings.Contains(lines[row], "+-")
	if !topBorder || !bottomBorder {
		t.Fatalf("input box is incomplete around row %d: top=%q content=%q bottom=%q", row, lines[row-2], lines[row-1], lines[row])
	}
	if !strings.Contains(lines[row+1], "Enter 发送") {
		t.Fatalf("input help line was clipped: %q", lines[row+1])
	}
	anchorRow, anchorCol, mode := m.cursorAnchor.snapshot()
	if mode != cursorAnchorVisible || anchorRow != row || anchorCol != col {
		t.Fatalf("IME anchor=%d,%d mode=%d; rendered cursor cell=%d,%d", anchorRow, anchorCol, mode, row, col)
	}
}

func TestFocusedInputCursorAnchorUsesDisplayColumns(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.input.SetValue("你好abc")
	m.input.SetCursor(2)

	_, col, ok := m.inputCursorAnchor()
	if !ok {
		t.Fatal("expected cursor anchor")
	}
	if col != 7 {
		t.Fatalf("cursor col = %d, want 7 for two CJK runes plus input chrome", col)
	}
	content := m.renderFocusedInputContent(20)
	if !strings.Contains(stripANSIForTest(content), "你好abc") {
		t.Fatalf("focused input should preserve text, got %q", stripANSIForTest(content))
	}
}

func TestInputCursorAnchorStaysOnVisibleInputAcrossResizeAndWrap(t *testing.T) {
	for _, size := range [][2]int{{30, 12}, {80, 24}, {120, 30}} {
		m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
		m.width, m.height = size[0], size[1]
		value := strings.Repeat("你好abc", 12)
		m.input.SetValue(value)
		m.input.SetCursor(len([]rune(value)))
		m.recalcLayout()
		row, col, ok := m.inputCursorAnchor()
		if !ok {
			t.Fatalf("size=%v missing cursor anchor", size)
		}
		if row < 1 || row > size[1] || col < 1 || col > size[0] {
			t.Fatalf("size=%v cursor outside terminal: row=%d col=%d", size, row, col)
		}
		// The last visible input row sits directly above the input bottom border
		// and help row, regardless of how many wrapped draft rows are visible.
		if row != size[1]-2 {
			t.Fatalf("size=%v cursor row=%d want %d", size, row, size[1]-2)
		}
	}
}

func TestFocusedInputWrapsAndGrowsWithLongText(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 36
	m.height = 24
	m.recalcLayout()
	longText := strings.Repeat("你好世界", 8)
	m.input.SetValue(longText)
	m.input.SetCursor(len([]rune(longText)))
	m.recalcLayout()

	rows, cursorRow, _ := m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	if len(rows) < 2 {
		t.Fatalf("expected long focused input to wrap into multiple rows, got %d: %q", len(rows), stripANSIForTest(strings.Join(rows, "\n")))
	}
	if cursorRow != len(rows)-1 {
		t.Fatalf("cursor should stay on visible tail row, cursorRow=%d rows=%d", cursorRow, len(rows))
	}

	view := stripANSIForTest(m.View())
	if !strings.Contains(view, "你好世界") {
		t.Fatalf("wrapped input should render original text, got:\n%s", view)
	}
}

func TestShortPasteStaysInlineInsteadOfChip(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 24
	m.recalcLayout()
	mask := "'?uali?s?d?d?d'\n"
	m.acceptPaste(mask)
	m.recalcLayout()

	if m.hasPasteBlocks() {
		t.Fatalf("short 2-line paste must stay inline, parts=%#v", m.draftParts)
	}
	if got := decodeInputValue(m.input.Value()); got != mask {
		t.Fatalf("short paste should insert original text, got %q", got)
	}
	rows, _, _ := m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	rendered := stripANSIForTest(strings.Join(rows, "\n"))
	if strings.Contains(rendered, "粘贴文本") {
		t.Fatalf("short paste must not render a chip, got %q", rendered)
	}
	if !strings.Contains(rendered, "?uali?s?d?d?d") {
		t.Fatalf("short paste should show original content, got %q", rendered)
	}
}

func TestIsLargePasteMatchesClaudeCodeThreshold(t *testing.T) {
	if isLargePaste("'?uali?s?d?d?d'\n") {
		t.Fatal("2-line 16-character paste should stay inline")
	}
	if isLargePaste(strings.Repeat("a", largePasteMaxRunes)) {
		t.Fatal("exactly 800 characters should stay inline")
	}
	if !isLargePaste(strings.Repeat("a", largePasteMaxRunes+1)) {
		t.Fatal("801 characters should collapse")
	}
	if !isLargePaste("one\ntwo\nthree") {
		t.Fatal("3-line paste should collapse")
	}
}

func TestLargePasteStillUsesSummaryInsteadOfExpandingInput(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.acceptPaste(strings.Repeat("line\n", 30))
	m.recalcLayout()

	rows, _, _ := m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	if len(rows) > 2 {
		t.Fatalf("large paste summary should stay compact, rows=%d text=%q", len(rows), stripANSIForTest(strings.Join(rows, "\n")))
	}
	if m.input.Value() != "" || len(m.draftParts) != 1 || !m.draftParts[0].pasted {
		t.Fatalf("large paste should become a separate chip, input=%q parts=%#v", m.input.Value(), m.draftParts)
	}
	if rendered := stripANSIForTest(strings.Join(rows, "\n")); !strings.Contains(rendered, "粘贴文本 #1") {
		t.Fatalf("large paste chip missing, got %q", rendered)
	}
}

func TestMultipleLargePastesRenderSeparateChipsAndPreservePayload(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 120
	m.height = 24
	m.recalcLayout()
	first := strings.Repeat("甲", 801)
	second := strings.Repeat("乙", 900)
	m.acceptPaste(first)
	m.acceptPaste(second)
	m.recalcLayout()

	if len(m.draftParts) != 2 || !m.draftParts[0].pasted || !m.draftParts[1].pasted {
		t.Fatalf("two pastes should remain two ordered blocks: %#v", m.draftParts)
	}
	if got, want := m.currentInputValue(), first+"\n"+second; got != want {
		t.Fatalf("full payload mismatch: got=%d runes want=%d", len([]rune(got)), len([]rune(want)))
	}
	rendered := stripANSIForTest(m.renderFocusedInputContent(ChromeContentWidth(m.width) - 2))
	for _, want := range []string{"[粘贴文本 #1 · 1 行 · 801 字符]", "[粘贴文本 #2 · 1 行 · 900 字符]"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing independent paste chip %q in %q", want, rendered)
		}
	}
}

func TestBracketedHTMLPasteEventPreservesHeadMiddleAndTail(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 100, 24
	m.pendingAsk = &askState{respCh: make(chan string, 1)}
	m.input.Focus()
	m.recalcLayout()
	html := "<!doctype html>\n<head>HEAD_MARKER</head>\n" + strings.Repeat("<p>MIDDLE_MARKER</p>\n", 200) + "</html>"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(html), Paste: true})
	m = updated.(AgentModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" 代码啥意思")})
	m = updated.(AgentModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(AgentModel)
	if len(m.lines) == 0 {
		t.Fatal("pasted user line missing")
	}
	raw := m.lines[len(m.lines)-1].raw
	for _, marker := range []string{"<!doctype html>", "HEAD_MARKER", "MIDDLE_MARKER", "</html>", "代码啥意思"} {
		if !strings.Contains(raw, marker) {
			t.Fatalf("bracketed paste lost %q; raw length=%d", marker, len([]rune(raw)))
		}
	}
}

func TestCROnlyLongHTMLPasteExpandsFromStartWithoutTerminalOverwrite(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 120, 30
	m.pendingAsk = &askState{respCh: make(chan string, 1)}
	m.input.Focus()
	m.recalcLayout()

	// Browser/editor clipboards can contain CR-only line endings. Before the
	// normalization fix, sanitizeTUIText correctly treated those as command
	// progress redraws, so expansion visually collapsed to the final </html>.
	html := "<!DOCTYPE html>\r<html>\r<head>HEAD_MARKER</head>\r" +
		strings.Repeat("<div>MIDDLE_MARKER</div>\r", 2800) + "</html>"
	if len([]rune(html)) < 70000 {
		t.Fatalf("regression fixture should exercise a 70K-class paste, got %d", len([]rune(html)))
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(html), Paste: true})
	m = updated.(AgentModel)
	if len(m.draftParts) != 1 || strings.ContainsRune(m.draftParts[0].text, '\r') {
		t.Fatalf("pasted block must normalize CR at ingress: %#v", m.draftParts)
	}
	if summary := pasteSummary(m.draftParts[0].text, 1); strings.Contains(summary, "· 1 行 ·") {
		t.Fatalf("CR-only HTML must not be mislabeled as one line: %q", summary)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" 分析一下字段代码")})
	m = updated.(AgentModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(AgentModel)
	line := m.lines[len(m.lines)-1]
	if strings.ContainsRune(line.raw, '\r') {
		t.Fatal("submitted user payload still contains terminal-active carriage returns")
	}
	for _, marker := range []string{"<!DOCTYPE html>", "HEAD_MARKER", "MIDDLE_MARKER", "</html>", "分析一下字段代码"} {
		if !strings.Contains(line.raw, marker) {
			t.Fatalf("submitted payload lost %q", marker)
		}
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(AgentModel)
	view := stripANSIForTest(m.viewport.View())
	if strings.ContainsRune(view, '\r') {
		t.Fatal("expanded viewport leaked a carriage return")
	}
	for _, marker := range []string{"<!DOCTYPE html>", "HEAD_MARKER"} {
		if !strings.Contains(view, marker) {
			t.Fatalf("expanded view should open at the HTML start and contain %q:\n%s", marker, view)
		}
	}
}

func TestExpandingLongUserTextKeepsReadingAnchorInsteadOfJumpingToTail(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 80, 16
	m.recalcLayout()
	raw := "HEAD_MARKER\n" + strings.Repeat("middle evidence line\n", 80) + "</html> TAIL_MARKER"
	m.appendLine("user", "You: "+summarizeUserText(raw), raw)
	m.lines[len(m.lines)-1].collapsed = true
	m.refreshViewport()
	m.viewport.GotoBottom()
	m.autoScroll = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(AgentModel)
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "HEAD_MARKER") {
		t.Fatalf("expanded view should remain at the document start, got:\n%s", view)
	}
	if m.autoScroll {
		t.Fatal("expanding long text must leave live-tail mode")
	}
}

func TestTypingAfterLargePasteStaysVisibleAndPreservesFullDraft(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 120
	m.height = 24
	m.recalcLayout()
	pasted := strings.Repeat("证据", 401)
	m.acceptPaste(pasted)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("补充说明")})
	m = updated.(AgentModel)
	if got := m.currentInputValue(); got != pasted+"\n补充说明" {
		t.Fatalf("full draft was not preserved, got length=%d", len([]rune(got)))
	}
	if !strings.Contains(m.input.Value(), "补充说明") {
		t.Fatalf("typing after paste should remain directly editable, got %q", m.input.Value())
	}
	rows, _, _ := m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	if rendered := stripANSIForTest(strings.Join(rows, "\n")); !strings.Contains(rendered, "粘贴文本 #1") || !strings.Contains(rendered, "补充说明") {
		t.Fatalf("paste chip and editable suffix should render together, got %q", rendered)
	}
}

func TestSubmittedPastesEchoIndependentPreviewsAndTypedSuffix(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 120, 24
	m.pendingAsk = &askState{respCh: make(chan string, 1)}
	m.input.Focus()
	first := "FIRST_PREVIEW " + strings.Repeat("甲", 801)
	second := "SECOND_PREVIEW " + strings.Repeat("乙", 801)
	m.acceptPaste(first)
	m.acceptPaste(second)
	m.input.SetValue("你好呀")
	m.input.SetCursor(len([]rune(m.input.Value())))
	m.recalcLayout()
	m.refreshViewport()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(AgentModel)
	if len(m.lines) == 0 {
		t.Fatal("submitted user line missing")
	}
	line := m.lines[len(m.lines)-1]
	if line.kind != "user" || !line.collapsed {
		t.Fatalf("pasted submission should be a collapsed user summary: %#v", line)
	}
	for _, want := range []string{"粘贴文本 #1", "FIRST_PREVIEW", "粘贴文本 #2", "SECOND_PREVIEW", "补充文字：你好呀"} {
		if !strings.Contains(line.content, want) {
			t.Fatalf("submitted summary missing %q: %q", want, line.content)
		}
	}
	for _, want := range []string{first, second, "你好呀"} {
		if !strings.Contains(line.raw, want) {
			t.Fatalf("full submitted payload missing content %q", truncateStr(want, 24))
		}
	}
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	for _, want := range []string{"FIRST_PREVIEW", "SECOND_PREVIEW", "你好呀", "全部展开原文"} {
		if !strings.Contains(view, want) {
			t.Fatalf("live viewport should show compact submitted content %q:\n%s", want, view)
		}
	}
}

func TestMultilineArrowsMoveCursorBeforeHistory(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.inputHistory = []string{"历史指令"}
	m.input.SetValue(encodeInputValue("第一行\n第二行"))
	m.input.SetCursor(len([]rune("第一行\n第二")))

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(AgentModel)
	if decodeInputValue(m.input.Value()) != "第一行\n第二行" || m.historyIdx != -1 {
		t.Fatalf("up inside multiline input must move cursor, not recall history: value=%q history=%d", m.input.Value(), m.historyIdx)
	}
	if got, want := m.input.Position(), len([]rune("第一")); got != want {
		t.Fatalf("cursor=%d want=%d", got, want)
	}
}

func TestWrappedInputArrowsMoveCursorBeforeHistory(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 24, 20
	m.recalcLayout()
	m.inputHistory = []string{"历史指令"}
	m.input.SetValue(strings.Repeat("甲", 30))
	m.input.SetCursor(25)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(AgentModel)
	if m.historyIdx != -1 || m.input.Value() != strings.Repeat("甲", 30) {
		t.Fatalf("up inside wrapped input must move cursor, not history: pos=%d history=%d", m.input.Position(), m.historyIdx)
	}
	if m.input.Position() >= 25 {
		t.Fatalf("cursor should move to the previous visual row, got %d", m.input.Position())
	}
}

func TestSubmitWhileBrowsingReturnsToLiveTail(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 80, 14
	for i := 0; i < 80; i++ {
		m.appendLine("info", fmt.Sprintf("history line %03d", i), "history")
	}
	m.pendingAsk = &askState{respCh: make(chan string, 1)}
	m.input.Focus()
	m.input.SetValue("新的回复")
	m.recalcLayout()
	m.refreshViewport()
	m.autoScroll = false
	m.viewport.GotoTop()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(AgentModel)
	if !m.autoScroll || !m.viewport.AtBottom() {
		t.Fatalf("submitting from history must return to live tail: offset=%d auto=%v", m.viewport.YOffset, m.autoScroll)
	}
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "You: 新的回复") {
		t.Fatalf("submitted message should be visible at live tail:\n%s", view)
	}
}

func TestLongUserMessageShowsFullTextAndCanCollapse(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 100, 30
	m.recalcLayout()
	raw := strings.Repeat("长文证据", 100) + " UNIQUE_USER_TAIL"
	m.appendLine("user", "You: "+summarizeUserText(raw), raw)
	m.refreshViewport()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "UNIQUE_USER_TAIL") {
		t.Fatalf("submitted user text should be visible in full by default:\n%s", view)
	}

	m.toggleAllCollapsible()
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if strings.Contains(view, "UNIQUE_USER_TAIL") || !strings.Contains(view, "全部展开原文") {
		t.Fatalf("long user text should collapse to an expandable summary:\n%s", view)
	}
}

func TestPasteInsertsAtCursorAndMovesCursorToEndOfPaste(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.input.SetValue("hello world")
	m.input.SetCursor(6)

	m.acceptPaste("dear ")

	if got, want := m.input.Value(), "hello dear world"; got != want {
		t.Fatalf("paste should insert at cursor, got %q want %q", got, want)
	}
	if got, want := m.input.Position(), len([]rune("hello dear ")); got != want {
		t.Fatalf("cursor should move after pasted text, got %d want %d", got, want)
	}
}

func TestSlashCommandTabCompletesFirstSuggestion(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.input.SetValue("/c")
	m.input.SetCursor(2)
	m.recalcLayout()
	withSuggestionsHeight := m.viewport.Height

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	model := updated.(AgentModel)
	if got := model.input.Value(); got != "/clear" {
		t.Fatalf("tab completion = %q, want /clear", got)
	}
	if model.hasSlashSuggestions() {
		t.Fatalf("slash suggestions should close after exact completion")
	}
	if model.slashSuggestionLineCount() != 0 {
		t.Fatalf("suggestion rows should be gone after completion")
	}
	if model.viewport.Height <= withSuggestionsHeight {
		t.Fatalf("viewport height should return after suggestions close: got=%d before=%d", model.viewport.Height, withSuggestionsHeight)
	}
}

func TestSlashCommandSuggestionsRenderAboveInput(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.input.SetValue("/c")
	m.input.SetCursor(2)
	m.recalcLayout()

	view := stripANSIForTest(m.View())
	if !strings.Contains(view, "/clear") {
		t.Fatalf("slash suggestions should include /clear: %q", view)
	}
	lines := strings.Split(view, "\n")
	clearIdx, inputIdx, helpIdx := -1, -1, -1
	for i, line := range lines {
		if clearIdx < 0 && strings.Contains(line, "/clear") {
			clearIdx = i
		}
		if inputIdx < 0 && strings.Contains(line, "/c") && !strings.Contains(line, "/clear") {
			inputIdx = i
		}
		if strings.Contains(line, "Enter 发送") {
			helpIdx = i
		}
	}
	if clearIdx < 0 || inputIdx < 0 || helpIdx < 0 {
		t.Fatalf("missing suggestion/input/help rows: clear=%d input=%d help=%d\n%s", clearIdx, inputIdx, helpIdx, view)
	}
	if !(clearIdx < inputIdx && inputIdx < helpIdx) {
		t.Fatalf("expected suggestions above input and help below input, got clear=%d input=%d help=%d", clearIdx, inputIdx, helpIdx)
	}
}

func TestExactSlashCommandDoesNotRenderSuggestionBox(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.input.SetValue("/clear")
	m.input.SetCursor(6)
	m.recalcLayout()

	view := stripANSIForTest(m.View())
	if count := strings.Count(view, "/clear"); count != 1 {
		t.Fatalf("exact slash command should appear only in input, got %d occurrences:\n%s", count, view)
	}
	if got, want := m.slashSuggestionLineCount(), 0; got != want {
		t.Fatalf("slash suggestion line count=%d want %d", got, want)
	}
}

func TestSplitSlashCommandKeepsArgument(t *testing.T) {
	cmd, arg := splitSlashCommand("/new 排查 nginx 和 php-fpm")
	if cmd != "new" {
		t.Fatalf("cmd=%q want new", cmd)
	}
	if arg != "排查 nginx 和 php-fpm" {
		t.Fatalf("arg=%q", arg)
	}
}

func TestSlashCommandNamesIncludeNewAndCost(t *testing.T) {
	names := slashCommandNames()
	for _, want := range []string{"/new", "/restart", "/cost", "/tsecbench", "/sudo", "/mcp", "/skill", "/schedule", "/exit", "/quit"} {
		if !strings.Contains(names, want) {
			t.Fatalf("slash commands missing %s: %s", want, names)
		}
	}
}

func TestTSecBenchModePrompt(t *testing.T) {
	prompt := tsecbenchModePrompt("先跑 easy")
	for _, want := range []string{"TSecBench", "tsecbench", "benchmark_base_url", "benchmark_token", "不要明文输出", "先跑 easy"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("tsecbench prompt missing %q: %s", want, prompt)
		}
	}
}

func TestTokenUsagePrefersRealUsageOverEstimate(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.applyEvent(harness.UIEvent{
		Kind:             harness.EventTokenUsage,
		PromptTokens:     1000,
		CompletionTokens: 250,
		TotalTokens:      1250,
	})
	got := m.tokenUsageLabel(SessionStats{ApproxTokens: 99})
	if got != "1.2k tok" {
		t.Fatalf("token label=%q", got)
	}
}

func TestHeaderStatsExpandsWhenWidthAllows(t *testing.T) {
	stats := SessionStats{
		SessionID:    "session_example_001",
		Turns:        5,
		Messages:     8,
		ApproxTokens: 14200,
	}
	wide := formatHeaderStats(stats, false, "~14.2k tok", 90)
	for _, want := range []string{"会话 sid example_001", "状态 idle", "轮次 5", "消息 8", "token ~14.2k"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide header missing %q: %q", want, wide)
		}
	}

	narrow := formatHeaderStats(stats, false, "~14.2k tok", 42)
	if strings.Contains(narrow, "状态") || strings.Contains(narrow, "轮次") {
		t.Fatalf("narrow header should compact labels, got %q", narrow)
	}
	if !strings.Contains(narrow, "sid 001") {
		t.Fatalf("narrow header should keep short session id, got %q", narrow)
	}
}

func TestHeaderShowsModelContextLabel(t *testing.T) {
	title := "custom / qianfan-code-latest · ctx≈131.1K[安全默认]"
	m := NewAgentModel(nil, title, "local", 30, false, false, StartupInfo{})
	header := stripANSIForTest(m.renderHeader(165))
	if !strings.Contains(header, title) {
		t.Fatalf("header lost model context label: %q", header)
	}
}

func TestAgentViewFitsTerminalAcrossSizesAndStates(t *testing.T) {
	type stateCase struct {
		name  string
		setup func(*AgentModel)
	}
	states := []stateCase{
		{
			name: "focused slash menu",
			setup: func(m *AgentModel) {
				m.awaitGoal = true
				m.input.Focus()
				m.input.SetValue("/")
			},
		},
		{
			name: "running noisy command",
			setup: func(m *AgentModel) {
				m.running = true
				m.input.Blur()
				m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: strings.Repeat("sqlmap --url http://target/?id=1 ", 5)}})
				m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "10%\r50%\r\x1b[2J100% injectable\x1b[999;1H\n"})
				m.thinking = true
			},
		},
		{
			name: "markdown ask",
			setup: func(m *AgentModel) {
				action := &harness.AgentAction{Type: harness.ActionAskUser, Question: "**请选择目标**\n\n| 名称 | 状态 |\n|---|---|\n| a-05 | available |", Options: []string{"启动 a-05", "取消"}}
				m.applyEvent(harness.UIEvent{Kind: harness.EventAwaitUser, Action: action})
			},
		},
		{
			name: "risk confirmation",
			setup: func(m *AgentModel) {
				m.pendingConfirm = &confirmState{prompt: "**高风险命令**\n\n```sh\n" + strings.Repeat("rm -rf /tmp/example ", 8) + "\n```"}
				m.appendConfirmLine(m.pendingConfirm.prompt)
			},
		},
	}

	for _, width := range []int{1, 2, 4, 7, 20, 30, 48, 79, 80, 120} {
		for _, height := range []int{2, 5, 12, 18, 30} {
			for _, state := range states {
				t.Run(fmt.Sprintf("%s/%dx%d", state.name, width, height), func(t *testing.T) {
					m := NewAgentModel(nil, "provider / model-with-a-long-name", "Fleet 多目标: 12 台远程服务器", 100, true, false, StartupInfo{
						Version: "2.0", ModelInfo: "provider / model-with-a-long-name", ConnInfo: "Fleet 多目标: 12 台", AwaitGoal: true,
					})
					m.width, m.height = width, height
					state.setup(&m)
					m.recalcLayout()
					m.refreshViewport()
					view := m.View()
					if got := lipgloss.Height(view); got > height {
						t.Fatalf("view height=%d exceeds terminal height=%d\n%s", got, height, stripANSIForTest(view))
					}
					for row, line := range strings.Split(view, "\n") {
						if got := lipgloss.Width(line); (width > 1 && got >= width) || got > width {
							t.Fatalf("row %d width=%d reaches terminal autowrap column=%d: %q", row, got, width, stripANSIForTest(line))
						}
					}
				})
			}
		}
	}
}

func TestRunningInputFooterSurvivesLongOutputAndScrolling(t *testing.T) {
	m := NewAgentModel(nil, "provider / model", "local", 100, true, false, StartupInfo{})
	m.width, m.height = 152, 44
	m.running = true
	m.awaitGoal = false
	m.sessionLive = true
	m.input.Blur()
	m.recalcLayout()

	m.applyEvent(harness.UIEvent{
		Kind: harness.EventAction,
		Action: &harness.AgentAction{
			Type:    harness.ActionExecute,
			Command: strings.Repeat("sqlmap --batch --url http://target/?id=1 ", 6),
		},
	})
	for i := 0; i < 120; i++ {
		m.applyEvent(harness.UIEvent{
			Kind:    harness.EventCommandOutput,
			Message: fmt.Sprintf("[%03d] %s 数据库探测结果 🚦\r", i, strings.Repeat("column=value ", 16)),
		})
	}
	m.thinking = true
	m.refreshViewport()

	assertRunningFooterVisible(t, m)

	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = updated.(AgentModel)
	assertRunningFooterVisible(t, m)

	for range 8 {
		updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		m = updated.(AgentModel)
	}
	assertRunningFooterVisible(t, m)
}

func TestIdleFocusedInputFooterSurvivesHistoryScrolling(t *testing.T) {
	m := NewAgentModel(nil, "provider / model", "local", 100, true, false, StartupInfo{AwaitGoal: true})
	m.width, m.height = 152, 44
	m.input.Focus()
	for i := 0; i < 160; i++ {
		m.lines = append(m.lines, logLine{
			kind:    "info",
			content: fmt.Sprintf("session_%03d · step %d · restored history", i, i%100),
		})
	}
	m.recalcLayout()
	m.refreshViewport()

	assertFocusedInputFooterVisible(t, m)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(AgentModel)
	assertFocusedInputFooterVisible(t, m)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(AgentModel)
	assertFocusedInputFooterVisible(t, m)
}

func TestScrollingInvalidatesEveryInputFooterRowWithoutMovingIMEAnchor(t *testing.T) {
	m := NewAgentModel(nil, "provider / model", "local", 100, true, false, StartupInfo{AwaitGoal: true})
	m.width, m.height = 90, 20
	m.input.Focus()
	m.input.SetValue("中文输入")
	m.input.SetCursor(len([]rune(m.input.Value())))
	for i := 0; i < 120; i++ {
		m.appendLine("info", fmt.Sprintf("history %03d", i), "")
	}
	m.recalcLayout()
	m.refreshViewport()

	before := strings.Split(m.View(), "\n")
	beforeRow, beforeCol, beforeMode := m.cursorAnchor.snapshot()
	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = updated.(AgentModel)
	after := strings.Split(m.View(), "\n")
	afterRow, afterCol, afterMode := m.cursorAnchor.snapshot()

	if m.footerVersion == 0 {
		t.Fatal("scroll did not invalidate the fixed footer")
	}
	if beforeRow != afterRow || beforeCol != afterCol || beforeMode != afterMode {
		t.Fatalf("scroll moved IME anchor: before=%d,%d/%d after=%d,%d/%d", beforeRow, beforeCol, beforeMode, afterRow, afterCol, afterMode)
	}
	if len(before) < 4 || len(after) < 4 {
		t.Fatalf("unexpected short frames: before=%d after=%d", len(before), len(after))
	}
	beforeFooter := before[len(before)-4:]
	afterFooter := after[len(after)-4:]
	if stripANSIForTest(strings.Join(beforeFooter, "\n")) != stripANSIForTest(strings.Join(afterFooter, "\n")) {
		t.Fatalf("scroll changed the visible input footer:\nbefore=%q\nafter=%q", stripANSIForTest(strings.Join(beforeFooter, "\n")), stripANSIForTest(strings.Join(afterFooter, "\n")))
	}
	for i := range beforeFooter {
		if beforeFooter[i] == afterFooter[i] {
			t.Fatalf("footer row %d was left cache-identical after scroll", i)
		}
	}
}

func TestViewportRefreshInvalidatesWholeFrameWithoutMovingIMEAnchor(t *testing.T) {
	m := NewAgentModel(nil, "provider / model", "local", 50, true, false, StartupInfo{AwaitGoal: true})
	m.width, m.height = 90, 20
	m.input.Focus()
	m.input.SetValue("中文输入")
	m.input.SetCursor(len([]rune(m.input.Value())))
	m.appendLine("info", "stable content", "stable content")
	m.recalcLayout()
	m.refreshViewport()

	before := m.View()
	beforeRow, beforeCol, beforeMode := m.cursorAnchor.snapshot()
	m.refreshViewport()
	after := m.View()
	afterRow, afterCol, afterMode := m.cursorAnchor.snapshot()

	if beforeRow != afterRow || beforeCol != afterCol || beforeMode != afterMode {
		t.Fatalf("full repaint moved IME anchor: before=%d,%d/%d after=%d,%d/%d", beforeRow, beforeCol, beforeMode, afterRow, afterCol, afterMode)
	}
	beforeLines, afterLines := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatalf("frame height changed across identical refresh: %d != %d", len(beforeLines), len(afterLines))
	}
	for i := range beforeLines {
		if beforeLines[i] == afterLines[i] {
			t.Fatalf("row %d remained renderer-cache-identical across full refresh", i)
		}
	}
	_, beforeClean := stripFrameMarkers([]byte(before))
	_, afterClean := stripFrameMarkers([]byte(after))
	if string(beforeClean) != string(afterClean) {
		t.Fatal("private repaint markers changed the visible frame")
	}
}

func TestDuplicateThoughtAndAskDisplayAreCoalesced(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 50, true, false, StartupInfo{})
	m.currentStep = 1
	m.applyEvent(harness.UIEvent{Kind: harness.EventThought, Message: "同一段思考"})
	m.applyEvent(harness.UIEvent{Kind: harness.EventThought, Message: "同一段思考"})
	if len(m.lines) != 1 {
		t.Fatalf("duplicate thought events were both retained: %#v", m.lines)
	}

	prompt := "请补充比赛题目\n请补充比赛题目\n题目文件在哪里？"
	m.appendAskLine(prompt, nil)
	got := m.lines[len(m.lines)-1].content
	if strings.Count(got, "请补充比赛题目") != 1 {
		t.Fatalf("duplicate ask display line was retained: %q", got)
	}
	if m.lines[len(m.lines)-1].raw != prompt {
		t.Fatal("display deduplication modified the raw ask/checkpoint content")
	}
}

func assertFocusedInputFooterVisible(t *testing.T, m AgentModel) {
	t.Helper()
	view := stripANSIForTest(m.View())
	lines := strings.Split(view, "\n")
	footerStart := max(0, len(lines)-5)
	footer := strings.Join(lines[footerStart:], "\n")
	if !strings.Contains(footer, "描述安全任务，Enter 开始") {
		t.Fatalf("focused input footer is missing from the bottom of the view:\n%s", footer)
	}
}

func assertRunningFooterVisible(t *testing.T, m AgentModel) {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	footerStart := max(0, len(lines)-5)
	footer := stripANSIForTest(strings.Join(lines[footerStart:], "\n"))
	if !strings.Contains(footer, "Agent 执行中...") {
		t.Fatalf("running input footer is missing from the bottom of the view:\n%s", footer)
	}
	for row, line := range lines {
		if got := lipgloss.Width(line); got >= m.width {
			t.Fatalf("row %d width=%d reaches terminal autowrap column=%d", row, got, m.width)
		}
	}
}

func TestConfirmPromptResolvesInline(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 80, 20
	m.recalcLayout()
	ch := make(chan approvalDecision, 1)
	updated, _ := m.Update(confirmMsg{prompt: "**高风险命令**\n\n`rm -rf /tmp/example`", respCh: ch})
	m = updated.(AgentModel)
	m.refreshViewport()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "需要确认") || !strings.Contains(view, "rm -rf") {
		t.Fatalf("confirmation should be visible inside viewport:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(AgentModel)
	if decision := <-ch; decision != approvalDeny {
		t.Fatal("n should reject confirmation")
	}
	view := stripANSIForTest(m.viewport.View())
	if strings.Contains(view, "Y 批准") || !strings.Contains(view, "已拒绝高风险操作") {
		t.Fatalf("resolved confirmation should become compact history:\n%s", view)
	}
}

func TestConfirmAcceptsUppercaseAndEnterSafelyRejects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      tea.KeyMsg
		decision approvalDecision
	}{
		{name: "uppercase approve", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}}, decision: approvalAllowOnce},
		{name: "session approve", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, decision: approvalAllowSession},
		{name: "allow all session", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}}, decision: approvalAllowAllSession},
		{name: "enter rejects", key: tea.KeyMsg{Type: tea.KeyEnter}, decision: approvalDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
			m.width, m.height = 80, 20
			m.recalcLayout()
			m.input.Focus()
			m.input.SetValue("preserved draft")
			ch := make(chan approvalDecision, 1)
			updated, _ := m.Update(confirmMsg{prompt: "confirm", respCh: ch})
			m = updated.(AgentModel)
			if m.inputFocused() {
				t.Fatal("confirmation should temporarily take input focus")
			}
			updated, _ = m.Update(tc.key)
			m = updated.(AgentModel)
			if got := <-ch; got != tc.decision {
				t.Fatalf("approval decision=%v want %v", got, tc.decision)
			}
			if !m.inputFocused() || m.input.Value() != "preserved draft" {
				t.Fatalf("previous input focus/draft was not restored: focused=%v value=%q", m.inputFocused(), m.input.Value())
			}
		})
	}
}

func TestNormalizeAskAnswerRequiresExactOptionNumber(t *testing.T) {
	options := []string{"first", "second"}
	if got := normalizeAskAnswer("2", options); got != "second" {
		t.Fatalf("exact option number=%q", got)
	}
	if got := normalizeAskAnswer("2abc", options); got != "2abc" {
		t.Fatalf("mixed text must not be mistaken for option number: %q", got)
	}
}

func stripANSIForTest(s string) string {
	var b strings.Builder
	inSeq := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inSeq {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				inSeq = false
			}
			continue
		}
		if ch == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			inSeq = true
			i++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
