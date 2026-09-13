package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/config"
	"ai-edr/internal/memory"
	"ai-edr/internal/runtimev3"
	"ai-edr/internal/tools"
)

type batchFailureMiddleware struct{}

func (batchFailureMiddleware) Name() string { return "batch-failure-test" }

func (batchFailureMiddleware) EnhancePrompt(base string, _ *AgentState) string { return base }

func (batchFailureMiddleware) HandleAction(_ *StepContext, action *AgentAction) (*ActionResult, bool, error) {
	if action.Type != ActionTool {
		return nil, false, nil
	}
	if action.ToolArgs["fail"] == "true" {
		return nil, true, errors.New("fixture tool failure")
	}
	return &ActionResult{Output: "ok"}, true, nil
}

func TestFilesystemMiddlewareControllerWorkspaceLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(home, ".deepsentry", "workspace")
	path := filepath.Join(workspace, "nested", "evidence.txt")
	mw := NewFilesystemMiddleware(nil)
	ctx := &StepContext{State: NewAgentState(workspace)}

	write, handled, err := mw.HandleAction(ctx, &AgentAction{Type: ActionWriteFile, Path: path, Content: "alpha\nneedle one\nneedle two\n"})
	if err != nil || !handled || !strings.Contains(write.Output, "控制端") {
		t.Fatalf("write=%#v handled=%v err=%v", write, handled, err)
	}
	read, handled, err := mw.HandleAction(ctx, &AgentAction{Type: ActionReadFile, Path: path})
	if err != nil || !handled || !strings.Contains(read.Output, "needle one") {
		t.Fatalf("read=%#v handled=%v err=%v", read, handled, err)
	}
	edit, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionEditFile, Path: path, OldString: "needle", NewString: "signal", ReplaceAll: true})
	if err != nil || !strings.Contains(edit.Output, "已编辑") {
		t.Fatalf("edit=%#v err=%v", edit, err)
	}
	grep, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionGrep, Path: path, Pattern: "signal"})
	if err != nil || !strings.Contains(grep.Output, "2:signal one") || !strings.Contains(grep.Output, "3:signal two") {
		t.Fatalf("grep=%#v err=%v", grep, err)
	}
	glob, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionGlob, Path: workspace, GlobPattern: "*.txt"})
	if err != nil || !strings.Contains(glob.Output, "evidence.txt") {
		t.Fatalf("glob=%#v err=%v", glob, err)
	}
	ls, _, err := mw.HandleAction(ctx, &AgentAction{Type: ActionLS, Path: filepath.Dir(path)})
	if err != nil || !strings.Contains(ls.Output, "total 1") || !strings.Contains(ls.Output, "evidence.txt") {
		t.Fatalf("ls=%#v err=%v", ls, err)
	}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionReadFile}); !handled || !strings.Contains(result.Output, "不能为空") {
		t.Fatalf("empty read result=%#v handled=%v", result, handled)
	}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionReadFile, Path: "/tmp/config.yaml"}); !handled || !strings.Contains(result.Output, "禁止") {
		t.Fatalf("protected read result=%#v handled=%v", result, handled)
	}
}

func TestReadableReportArtifactsStayWriteProtected(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports")
	if err := os.MkdirAll(filepath.Join(reports, "mcp-artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(reports, "chat"), 0700); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(reports, "report_20260913_132548.md")
	if err := os.WriteFile(md, []byte("# 巡检结论\n风险与建议\n"), 0600); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(reports, "mcp-artifacts", "shot.png")
	if err := os.WriteFile(png, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(reports, "chat", "daemon.key")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if isProtectedReadPath(md) || isProtectedReadPath(png) {
		t.Fatal("session report and screenshots should be readable")
	}
	if !isProtectedReadPath(secret) || !isProtectedPath(md) {
		t.Fatal("chat secrets and report writes must stay protected")
	}
	mw := NewFilesystemMiddleware(nil)
	ctx := &StepContext{State: NewAgentState("")}
	read, handled, err := mw.HandleAction(ctx, &AgentAction{Type: ActionReadFile, Path: md})
	if err != nil || !handled || !strings.Contains(read.Output, "巡检结论") {
		t.Fatalf("read report=%#v handled=%v err=%v", read, handled, err)
	}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionReadFile, Path: secret}); !handled || !strings.Contains(result.Output, "禁止") {
		t.Fatalf("chat secret read result=%#v handled=%v", result, handled)
	}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionWriteFile, Path: md, Content: "overwrite"}); !handled || !strings.Contains(result.Output, "禁止") {
		t.Fatalf("report write result=%#v handled=%v", result, handled)
	}
}

func TestToolsAndSubAgentMiddlewareValidationPaths(t *testing.T) {
	tools.ConfigureEnabled(nil, nil)
	mw := NewToolsMiddleware()
	ctx := &StepContext{State: NewAgentState("")}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionTool}); !handled || !strings.Contains(result.Output, "tool_name") {
		t.Fatalf("missing tool name result=%#v handled=%v", result, handled)
	}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionTool, ToolName: "tool_catalog", ToolArgs: map[string]string{"name": "read_log"}}); !handled || !strings.Contains(result.Output, "read_log") {
		t.Fatalf("catalog result=%#v handled=%v", result, handled)
	}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionTool, ToolName: "tool_catalog", ToolArgs: map[string]string{"name": "does_not_exist"}}); !handled || !strings.Contains(result.Output, "未找到") {
		t.Fatalf("unknown catalog result=%#v handled=%v", result, handled)
	}
	if result, handled, _ := mw.HandleAction(ctx, &AgentAction{Type: ActionTool, ToolName: "fleet_exec", ToolArgs: map[string]string{"selector": "all"}}); !handled || !strings.Contains(result.Output, "command") {
		t.Fatalf("fleet validation result=%#v handled=%v", result, handled)
	}
	if !skillMarketMutation("install") || !skillMarketMutation("import") || skillMarketMutation("search") || !skillConfigMutation("disable_skill") || skillConfigMutation("status") {
		t.Fatal("skill mutation classifiers are inconsistent")
	}

	sub := NewSubAgentMiddleware()
	if result, handled, _ := sub.HandleAction(ctx, &AgentAction{Type: ActionTask}); !handled || !strings.Contains(result.Output, "task_name") {
		t.Fatalf("empty subagent result=%#v handled=%v", result, handled)
	}
	if result, handled, _ := sub.HandleAction(ctx, &AgentAction{Type: ActionTask, TaskName: "missing", TaskPrompt: "x"}); !handled || !strings.Contains(result.Output, "未知子 Agent") {
		t.Fatalf("unknown subagent result=%#v handled=%v", result, handled)
	}
	if result, handled, _ := sub.HandleAction(ctx, &AgentAction{Type: ActionTask, TaskName: "log-analyst", TaskPrompt: "x"}); !handled || !strings.Contains(result.Output, "未初始化") {
		t.Fatalf("unwired subagent result=%#v handled=%v", result, handled)
	}
	parallel := &AgentAction{Type: ActionTask, ParallelTasks: []SubAgentTaskAction{{TaskName: "log-analyst", TaskPrompt: "one"}, {TaskName: "log-analyst", TaskPrompt: "one"}}}
	if normalized := normalizeParallelTasks(parallel); len(normalized) != 1 || adaptiveSubAgentConcurrency(normalized) != 1 {
		t.Fatalf("normalized=%#v", normalized)
	}
}

func TestDeepAgentHandlesNativeToolBatchThroughMiddleware(t *testing.T) {
	state := NewAgentState("")
	agent := &DeepAgent{State: state, Middleware: []Middleware{NewToolsMiddleware()}}
	action := &AgentAction{
		Type: ActionToolBatch,
		ToolCalls: []ToolCallAction{
			{ID: "catalog_1", Name: "tool_catalog", Args: map[string]string{"name": "read_log"}},
			{ID: "catalog_2", Name: "tool_catalog", Args: map[string]string{"name": "pcap_analyze"}},
		},
		SkipToolCallIDs: map[string]bool{},
	}
	result, err := agent.HandleAction(&StepContext{State: state, StepNum: 1}, action)
	if err != nil || len(result.ToolResults) != 2 || !strings.Contains(result.Output, "read_log") || !strings.Contains(result.Output, "pcap_analyze") {
		t.Fatalf("batch result=%#v err=%v", result, err)
	}
	action.SkipToolCallIDs["catalog_2"] = true
	result, err = agent.HandleAction(&StepContext{State: state, StepNum: 2}, action)
	if err != nil || !strings.Contains(result.ToolResults[1].Output, "checkpoint") {
		t.Fatalf("recovered batch result=%#v err=%v", result, err)
	}
	if result, err := agent.HandleAction(&StepContext{State: state}, &AgentAction{Type: ActionFinish, FinalReport: "done"}); err != nil || !result.ShouldStop {
		t.Fatalf("finish result=%#v err=%v", result, err)
	}
	if result, err := agent.HandleAction(&StepContext{State: state}, &AgentAction{Type: "mystery"}); err != nil || !strings.Contains(result.Output, "未知动作") {
		t.Fatalf("unknown result=%#v err=%v", result, err)
	}
}

func TestToolBatchDoesNotCompleteFailedCalls(t *testing.T) {
	state := NewAgentState("")
	agent := &DeepAgent{State: state, Middleware: []Middleware{batchFailureMiddleware{}}}
	action := AgentAction{
		Type: ActionToolBatch,
		ToolCalls: []ToolCallAction{
			{ID: "ok_call", Name: "tool_catalog", Args: map[string]string{"name": "read_log"}},
			{ID: "failed_call", Name: "tool_catalog", Args: map[string]string{"name": "read_log", "fail": "true"}},
		},
	}
	prepareToolCallExecution(state, &action)
	result, err := agent.HandleAction(&StepContext{State: state}, &action)
	if err != nil || len(result.ToolResults) != 2 || result.ToolResults[1].Error == "" {
		t.Fatalf("batch result=%#v err=%v", result, err)
	}
	completeActionToolCalls(state, action)
	if !state.ToolCallCompleted("ok_call") {
		t.Fatal("successful batch call was not completed")
	}
	if state.ToolCallCompleted("failed_call") {
		t.Fatal("failed batch call was incorrectly completed")
	}
	if _, pending := state.ToolCallPending("failed_call"); pending {
		t.Fatal("failed low-risk call should be released for retry")
	}
}

func TestRuntimeFormattingStateAndTargetHelpers(t *testing.T) {
	state := NewAgentState("")
	state.SetMemory("key", "value")
	if value, ok := state.GetMemory("key"); !ok || value != "value" {
		t.Fatalf("memory=%q ok=%v", value, ok)
	}
	list := FormatTodoList([]TodoItem{
		{ID: "1", Content: "pending", Status: "pending"},
		{ID: "2", Content: "working", Status: "in_progress"},
		{ID: "3", Content: "done", Status: "completed"},
	})
	if !strings.Contains(list, "pending") || !strings.Contains(list, "working") || !strings.Contains(list, "done") {
		t.Fatalf("todo list=%q", list)
	}
	ask := formatAskUserMessage(AgentAction{Question: "choose", Options: []string{"a", "b"}})
	if !strings.Contains(ask, "choose") || !strings.Contains(ask, "a") {
		t.Fatalf("ask=%q", ask)
	}
	action := AgentAction{Type: ActionExecute, Command: "display version"}
	enrichActionExecutionTargetFor(&action, config.TargetConfig{Name: "sw1", Protocol: "ssh", Host: "10.0.0.8"})
	if action.TargetName != "sw1" || action.TargetProtocol != "ssh" || action.TargetHost != "10.0.0.8" {
		t.Fatalf("target action=%#v", action)
	}
	if got := safeArtifactLabel("../tool/call"); strings.ContainsAny(got, "/\\") || got == "" {
		t.Fatalf("artifact label=%q", got)
	}
	if got := escapeJSON("a\\path\"b"); !strings.Contains(got, `\\path`) || !strings.Contains(got, `\"`) {
		t.Fatalf("escaped=%q", got)
	}
	if got := formatTargetSuffix("sw1", "ssh", "10.0.0.8"); !strings.Contains(got, "sw1") || !strings.Contains(got, "10.0.0.8") {
		t.Fatalf("suffix=%q", got)
	}
}

func TestContextMiddlewareArtifactsAndPromptState(t *testing.T) {
	workspace := t.TempDir()
	state := NewAgentStateWithSession(workspace, "session:unsafe/id")
	state.MarkSelectedTool("read_log")
	state.ObserveCoreClues("source_ip=198.51.100.8", "fixture")
	mw := &ContextMiddleware{OutputThreshold: 32}
	output := strings.Repeat("evidence-line\n", 20)
	result := mw.OffloadOutput(state, "tool/call", output)
	if !strings.Contains(result, "artifact") || len(state.Artifacts) != 1 {
		t.Fatalf("result=%q artifacts=%#v", result, state.Artifacts)
	}
	if _, err := os.Stat(state.Artifacts[0].Path); err != nil {
		t.Fatal(err)
	}
	prompt := mw.EnhancePrompt("base", state)
	if !strings.Contains(prompt, "read_log") || !strings.Contains(prompt, "198.51.100.8") {
		t.Fatalf("prompt=%q", prompt)
	}
	if compact := compactPromptText(strings.Repeat("x", 20), 8); !strings.Contains(compact, "精简") {
		t.Fatalf("compact=%q", compact)
	}
}

func TestMemoryTodoAndBuilderOptions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := memory.NewStore("runtime-components")
	if err != nil {
		t.Fatal(err)
	}
	memoryMW := NewMemoryMiddleware(store)
	ctx := &StepContext{State: NewAgentState("")}
	remember, handled, err := memoryMW.HandleAction(ctx, &AgentAction{Type: ActionRemember, MemoryKey: "ioc", MemoryValue: "198.51.100.4"})
	if err != nil || !handled || !strings.Contains(remember.Output, "已保存") {
		t.Fatalf("remember=%#v handled=%v err=%v", remember, handled, err)
	}
	forget, handled, err := memoryMW.HandleAction(ctx, &AgentAction{Type: ActionForget, MemoryKey: "ioc"})
	if err != nil || !handled || !strings.Contains(forget.Output, "已删除") {
		t.Fatalf("forget=%#v handled=%v err=%v", forget, handled, err)
	}

	todoMW := NewTodoMiddleware()
	if !strings.Contains(todoMW.EnhancePrompt("base", ctx.State), "任务规划") {
		t.Fatal("empty todo prompt missing planning contract")
	}
	todos := []TodoItem{{ID: "1", Content: "collect", Status: "in_progress"}}
	result, handled, err := todoMW.HandleAction(ctx, &AgentAction{Type: ActionTodo, Todos: todos})
	if err != nil || !handled || !strings.Contains(result.Output, "collect") || !strings.Contains(todoMW.EnhancePrompt("base", ctx.State), "collect") {
		t.Fatalf("todo result=%#v handled=%v err=%v", result, handled, err)
	}

	builder := &agentBuilder{}
	for _, option := range []Option{
		WithWorkspaceDir("/tmp/workspace"), WithMemoryScope("scope"), WithSessionID("session_options"),
		WithNativeTools(true), WithSkillSources([]string{"/tmp/skills"}), WithMCPServers([]string{"mcp:cmd"}),
		WithMiddleware(todoMW), WithMiddleware(todoMW),
	} {
		option(builder)
	}
	if builder.cfg.SessionID != "session_options" || !builder.cfg.UseNativeTools || len(builder.middleware) != 1 || len(builder.cfg.MCPServers) != 1 {
		t.Fatalf("builder=%#v", builder)
	}
}

func TestNewDeepAgentDefaultsToV3DurableSessionSubscriber(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	previous := config.GlobalConfig
	config.GlobalConfig.AgentRuntime = ""
	config.GlobalConfig.TraceEnabled = false
	config.GlobalConfig.SkillsDisabled = true
	config.GlobalConfig.SkillSources = nil
	config.GlobalConfig.DisabledSkillSources = nil
	defer func() { config.GlobalConfig = previous }()

	agent, err := NewDeepAgent(Config{
		WorkspaceDir: filepath.Join(home, ".deepsentry", "workspace"),
		SessionID:    "session_constructor",
		MemoryScope:  "constructor",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if agent.sessionLog != nil {
			_ = agent.sessionLog.Close()
		}
	}()
	if agent.Events == nil || agent.sessionLog == nil || agent.trace != nil || len(agent.Middleware) < 7 {
		t.Fatalf("agent runtime subscribers/middleware not built: %#v", agent)
	}
	agent.emitRuntime(runtimev3.RunEvent{Kind: runtimev3.EventRunStart})
	if err := agent.flushRuntimeEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(agent.Checkpoint.SessionDir(), "events.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"kind":"run.start"`) {
		t.Fatalf("events=%s err=%v", raw, err)
	}
}

func TestNewDeepAgentExplicitLegacyRollbackSkipsV3Subscribers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	previous := config.GlobalConfig
	config.GlobalConfig.AgentRuntime = "legacy"
	config.GlobalConfig.TraceEnabled = true
	config.GlobalConfig.SkillsDisabled = true
	config.GlobalConfig.SkillSources = nil
	config.GlobalConfig.DisabledSkillSources = nil
	defer func() { config.GlobalConfig = previous }()

	agent, err := NewDeepAgent(Config{
		WorkspaceDir: filepath.Join(home, ".deepsentry", "workspace"),
		SessionID:    "session_legacy_constructor",
		MemoryScope:  "legacy-constructor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Events != nil || agent.sessionLog != nil || agent.trace != nil {
		t.Fatalf("legacy rollback unexpectedly enabled v3 subscribers: %#v", agent)
	}
}
