package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/mcp"
	"ai-edr/internal/skills"
	"ai-edr/internal/tools"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseActionInfersTool(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{
		Thought:  "ping host",
		ToolName: "ping",
		ToolArgs: map[string]string{"host": "127.0.0.1"},
	})
	if action.Type != ActionTool {
		t.Fatalf("expected ActionTool, got %s", action.Type)
	}
}

func TestBuildSystemPromptAdaptsDensityToModelProfile(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	agent := &DeepAgent{}

	config.GlobalConfig = config.Config{Provider: "ollama", ModelName: "qwen-14b-32k"}
	compact := agent.BuildSystemPrompt("")
	config.GlobalConfig = config.Config{Provider: "google", ModelName: "gemini-3.1-pro", ContextWindowTokens: 1_048_576}
	full := agent.BuildSystemPrompt("")

	if len(compact) >= len(full) {
		t.Fatalf("compact prompt was not reduced: compact=%d full=%d", len(compact), len(full))
	}
	for _, required := range []string{"tool_catalog", "config_manage", "风险确认", "验证结果", "Skill 安装只允许 skill_market", "curl -k/--insecure"} {
		if !strings.Contains(compact, required) {
			t.Fatalf("compact prompt lost required rule %q", required)
		}
	}
}

func TestBuildSystemPromptAddsCredentialFreeProxyRouting(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig = config.Config{
		Provider:        "custom",
		ModelName:       "test-model",
		ControllerProxy: "socks5://proxy-user:proxy-secret@127.0.0.1:1080",
	}
	prompt := (&DeepAgent{}).BuildSystemPrompt("")
	for _, want := range []string{"控制端代理路由", "socks5://127.0.0.1:1080", "原生 TCP 扫描", "不得改用 execute"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("proxy prompt missing %q", want)
		}
	}
	for _, secret := range []string{"proxy-user", "proxy-secret"} {
		if strings.Contains(prompt, secret) {
			t.Fatalf("proxy prompt leaked %q", secret)
		}
	}
}

func TestBasePromptDoesNotSuppressOrReshapeSummarySections(t *testing.T) {
	prompt := (&DeepAgent{}).deepAgentBasePrompt()
	for _, unwanted := range []string{
		"不要自行添加“本次收获",
		"项目 | 内容",
		"不要因栏目名称自动删除",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("base prompt should leave summary content/shape to the model, found %q", unwanted)
		}
	}
}

func TestCompetitionModePromptEncodesScoringAndCorrectionContract(t *testing.T) {
	prompt := competitionModePrompt()
	for _, want := range []string{"10 分钟", "network_device_diagnose", "network_device_baseline", "AI 复核与纠错", "关键证据", "风险与回滚"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("competition prompt missing %q", want)
		}
	}
}

func TestReconcileLoadedSkillsHotReloadsAndRemovesMissing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hot-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: hot-skill\ndescription: Hot skill\n---\nversion two\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	state := NewAgentState(root)
	state.LoadedSkills["HOT-SKILL"] = "version one"
	refreshed, unloaded := reconcileLoadedSkills(state, catalog)
	if refreshed != 1 || unloaded != 0 || !strings.Contains(state.LoadedSkills["hot-skill"], "version two") {
		t.Fatalf("refresh=%d unload=%d loaded=%#v", refreshed, unloaded, state.LoadedSkills)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(); err != nil {
		t.Fatal(err)
	}
	refreshed, unloaded = reconcileLoadedSkills(state, catalog)
	if refreshed != 0 || unloaded != 1 || len(state.LoadedSkills) != 0 {
		t.Fatalf("refresh=%d unload=%d loaded=%#v", refreshed, unloaded, state.LoadedSkills)
	}
}

func TestRestoreCheckpointCannotResurrectDisabledSkill(t *testing.T) {
	catalog := &skills.SkillCatalog{DisabledSkills: []string{"blocked-skill"}}
	agent := &DeepAgent{Catalog: catalog, State: NewAgentState("")}
	state := NewAgentState("")
	state.LoadedSkills["blocked-skill"] = "stale checkpoint instructions"
	agent.RestoreFromCheckpoint(&CheckpointData{State: state})
	if len(agent.State.LoadedSkills) != 0 {
		t.Fatalf("disabled checkpoint skill was restored: %#v", agent.State.LoadedSkills)
	}
}

func TestParseActionInfersAskUser(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{
		Thought:  "need webhook",
		Question: "请提供钉钉 Webhook 地址",
		Options:  []string{"稍后提供", "跳过通知"},
	})
	if action.Type != ActionAskUser {
		t.Fatalf("expected ActionAskUser, got %s", action.Type)
	}
	if action.Question == "" || len(action.Options) != 2 {
		t.Fatalf("ask fields not carried: %#v", action)
	}
}

func TestParseActionInfersReadFile(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{Path: "/etc/hosts"})
	if action.Type != ActionReadFile {
		t.Fatalf("expected ActionReadFile, got %s", action.Type)
	}
}

func TestParseActionCarriesTargetSelector(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{
		Action:         string(ActionTask),
		TaskName:       "log-analyst",
		TaskPrompt:     "check logs",
		TaskMaxSteps:   24,
		TargetSelector: "prod",
	})
	if action.TargetSelector != "prod" || action.TaskName != "log-analyst" || action.TaskMaxSteps != 24 {
		t.Fatalf("target fields not carried: %#v", action)
	}
}

func TestParseActionCarriesParallelTasks(t *testing.T) {
	action := ParseAction(analyzer.AgentResponse{
		Action: string(ActionTask),
		ParallelTasks: []analyzer.TaskSpec{
			{TaskName: "log-analyst", TaskPrompt: "分析 auth.log", TaskMaxSteps: 20},
			{TaskName: "network-analyst", TaskPrompt: "分析异常连接", TargetSelector: "prod", TaskMaxSteps: 12},
		},
	})
	if action.Type != ActionTask || len(action.ParallelTasks) != 2 {
		t.Fatalf("parallel tasks not carried: %#v", action)
	}
	if action.ParallelTasks[1].TargetSelector != "prod" || action.ParallelTasks[0].TaskMaxSteps != 20 {
		t.Fatalf("parallel task fields not carried: %#v", action.ParallelTasks)
	}
}

func TestActionToJSONPreservesFields(t *testing.T) {
	raw := actionToJSON(AgentAction{
		Thought:   "test",
		Type:      ActionTool,
		ToolName:  "net_connections",
		ToolArgs:  map[string]string{"filter": "listen"},
		Path:      "/var/log/syslog",
		TaskName:  "log-analyst",
		MemoryKey: "foo",
	})
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m["tool_name"] != "net_connections" {
		t.Fatalf("tool_name lost: %v", m)
	}
	if m["path"] != "/var/log/syslog" {
		t.Fatalf("path lost: %v", m)
	}
}

func TestOffloadOutputIsSessionScoped(t *testing.T) {
	dir := t.TempDir()
	mw := &ContextMiddleware{OutputThreshold: 10}
	outA := strings.Repeat("A", 32)
	outB := strings.Repeat("B", 32)

	msgA := mw.OffloadOutput(NewAgentStateWithSession(dir, "session_a"), "step1", outA)
	msgB := mw.OffloadOutput(NewAgentStateWithSession(dir, "session_b"), "step1", outB)
	if !strings.Contains(msgA, filepath.Join("sessions", "session_a", "output_step1.txt")) {
		t.Fatalf("session_a path missing from output: %s", msgA)
	}
	if !strings.Contains(msgB, filepath.Join("sessions", "session_b", "output_step1.txt")) {
		t.Fatalf("session_b path missing from output: %s", msgB)
	}

	dataA, err := os.ReadFile(filepath.Join(dir, "sessions", "session_a", "output_step1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := os.ReadFile(filepath.Join(dir, "sessions", "session_b", "output_step1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(dataA) != outA || string(dataB) != outB {
		t.Fatalf("offloaded outputs were not isolated: A=%q B=%q", string(dataA), string(dataB))
	}
}

func TestBrowserAndBoundedReadOutputsStayInModelContext(t *testing.T) {
	dir := t.TempDir()
	agent := &DeepAgent{
		State:      NewAgentStateWithSession(dir, "browser_context"),
		Middleware: []Middleware{&ContextMiddleware{OutputThreshold: 8000}},
	}
	browserOutput := "Browser session: browser_test\n" + strings.Repeat("browser-page-content ", 900)
	browserAction := &AgentAction{Type: ActionTool, ToolName: "browser_browse"}
	if got := agent.prepareActionOutput(1, browserAction, browserOutput); got != browserOutput {
		t.Fatalf("browser snapshot should stay inline, got prefix %q", safeUTF8BytePrefix(got, 120))
	}

	readOutput := "[视角: 控制端]\n" + strings.Repeat("R", maxFileReadDisplay)
	if got := agent.prepareActionOutput(2, &AgentAction{Type: ActionReadFile}, readOutput); got != readOutput {
		t.Fatalf("bounded read_file output should not be recursively offloaded: %q", safeUTF8BytePrefix(got, 120))
	}

	ordinary := strings.Repeat("ordinary-output ", 900)
	got := agent.prepareActionOutput(3, &AgentAction{Type: ActionTool, ToolName: "process_list"}, ordinary)
	if !strings.Contains(got, "output_step3.txt") {
		t.Fatalf("ordinary large tool output should still offload: %q", got)
	}
}

func TestIsEmptyAction(t *testing.T) {
	if isEmptyAction(AgentAction{ToolName: "ping"}) {
		t.Fatal("tool_name should not be empty action")
	}
	if !isEmptyAction(AgentAction{Thought: "thinking"}) {
		t.Fatal("thought-only should be empty action")
	}
}

func TestAppendSudoGuidanceExplainsRemotePasswordPolicy(t *testing.T) {
	out := appendSudoGuidance("sudo: a password is required", true, false)
	for _, want := range []string{"SUDO_REQUIRED", "SSH 密码", "NOPASSWD"} {
		if !strings.Contains(out, want) {
			t.Fatalf("sudo guidance missing %q: %s", want, out)
		}
	}
	if got := appendSudoGuidance("ok", true, false); got != "ok" {
		t.Fatalf("successful sudo output should be unchanged: %q", got)
	}
}

func TestHandleExecuteForcesRemoteSudoNonInteractive(t *testing.T) {
	ex := &capturingSudoExecutor{}
	agent := &DeepAgent{}
	action := AgentAction{Type: ActionExecute, Command: "sudo du -sh /root"}
	result, err := agent.HandleAction(&StepContext{Executor: ex}, &action)
	if err != nil {
		t.Fatalf("HandleAction sudo: %v", err)
	}
	if ex.command != "sudo -n du -sh /root" {
		t.Fatalf("remote sudo command=%q", ex.command)
	}
	if result == nil || !strings.Contains(result.Output, "SUDO_REQUIRED") || !strings.Contains(result.Output, "NOPASSWD") {
		t.Fatalf("missing safe remote sudo guidance: %#v", result)
	}
}

func TestUnknownUploadActionGuidance(t *testing.T) {
	got := unknownActionGuidance(AgentAction{Type: ActionType("upload")})
	for _, want := range []string{`action="execute"`, "upload <本地路径> <远程路径>", "不是独立 action"} {
		if !strings.Contains(got, want) {
			t.Fatalf("guidance missing %q: %s", want, got)
		}
	}
}

func TestNonInteractiveAskAnswerTellsAgentToContinue(t *testing.T) {
	got := nonInteractiveAskAnswer(AgentAction{
		Thought:  "need webhook",
		Question: "请提供 webhook",
		Options:  []string{"跳过通知", "稍后配置"},
	})
	for _, want := range []string{"非交互模式", "采用保守默认方案继续", "跳过对应功能", "不要再次 ask_user", "跳过通知"} {
		if !strings.Contains(got, want) {
			t.Fatalf("non-interactive answer missing %q: %s", want, got)
		}
	}
}

func TestWebshellPromptForbidsAskUser(t *testing.T) {
	got := nonInteractivePrompt(true)
	for _, want := range []string{"非交互模式", "不要使用 action=\"ask_user\"", "多步骤命令的关键输出"} {
		if !strings.Contains(got, want) {
			t.Fatalf("webshell prompt missing %q: %s", want, got)
		}
	}
	for _, banned := range []string{"保存 checkpoint", "--resume"} {
		if strings.Contains(got, banned) {
			t.Fatalf("webshell prompt should not encourage ask_user checkpoint, found %q in %s", banned, got)
		}
	}
}

func TestAskResumeMessageWebshellShowsSupplementCommand(t *testing.T) {
	got := askResumeMessage("session_test", true)
	for _, want := range []string{"deepsentry --webshell --resume session_test", `--task "在这里填写补充内容"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("resume message missing %q: %s", want, got)
		}
	}
}

func TestBlockDeepSentryConfigShell(t *testing.T) {
	cases := []string{
		"cat /root/config.yaml",
		"sed -i 's/a/b/' /root/config.yaml",
		"cat ./config.yaml",
		"python3 - <<'PY'\nopen('config.yaml').read()\nPY",
		"local_run cat /tmp/.deepsentry_backups/config_20260714_105154_000000000.yaml",
	}
	for _, cmd := range cases {
		out, blocked := blockDeepSentryConfigShell(cmd, fakeRemoteExecutor{})
		if !blocked {
			t.Fatalf("expected config command blocked: %s", cmd)
		}
		if !strings.Contains(out, "config_manage") || !strings.Contains(out, "远端目标机") {
			t.Fatalf("unexpected guidance for %s:\n%s", cmd, out)
		}
	}

	if _, blocked := blockDeepSentryConfigShell("cat /var/www/app/config.yaml", fakeRemoteExecutor{}); blocked {
		t.Fatal("business config path should not be blocked")
	}
	if !isProtectedPath("/tmp/.deepsentry_backups/config_20260714_105154_000000000.yaml") {
		t.Fatal("managed config backup must be a protected path")
	}
}

func TestResolveFleetExecRiskUsesInnerCommand(t *testing.T) {
	tool, ok := tools.Get("fleet_exec")
	if !ok {
		t.Fatal("fleet_exec tool should exist")
	}

	lowAction := AgentAction{
		Type:     ActionTool,
		ToolName: "fleet_exec",
		ToolArgs: map[string]string{
			"selector": "target-01",
			"command":  "ls -la /tmp/flag.txt /tmp/flag.zip && echo '---CONTENT---' && cat /tmp/flag.txt",
		},
	}
	risk, reason := resolveToolRisk(lowAction, tool)
	if risk != tools.RiskLow {
		t.Fatalf("read-only fleet_exec should be low risk, got %s (%s)", risk, reason)
	}

	aliasAction := AgentAction{
		Type:     ActionTool,
		ToolName: "fleet_exec",
		ToolArgs: map[string]string{
			"selector": "target-01",
			"cmd":      "cat /tmp/flag.txt",
		},
	}
	risk, reason = resolveToolRisk(aliasAction, tool)
	if risk != tools.RiskLow {
		t.Fatalf("read-only fleet_exec cmd alias should be low risk, got %s (%s)", risk, reason)
	}

	highAction := AgentAction{
		Type:     ActionTool,
		ToolName: "fleet_exec",
		ToolArgs: map[string]string{
			"selector": "target-01",
			"command":  "rm -rf /tmp/flag.txt",
		},
	}
	risk, reason = resolveToolRisk(highAction, tool)
	if risk != tools.RiskHigh {
		t.Fatalf("destructive fleet_exec should be high risk, got %s (%s)", risk, reason)
	}
}

func TestToolDiscoveryAndInvalidCallsDoNotRequestRiskApproval(t *testing.T) {
	for _, action := range []AgentAction{
		{Type: ActionTool, ToolName: "tool_catalog", ToolArgs: map[string]string{"name": "config_manage"}},
		{Type: ActionTool, ToolName: "config_manage", ToolArgs: map[string]string{"action": "ssh_add_target"}},
		{Type: ActionTool, ToolName: "config_manage", ToolArgs: map[string]string{"action": "status"}},
	} {
		risk, reason := classifyToolRisk(action)
		if risk != tools.RiskLow {
			t.Fatalf("%s should be low risk before execution, got %s (%s)", action.ToolName, risk, reason)
		}
	}
	write := AgentAction{Type: ActionTool, ToolName: "config_manage", ToolArgs: map[string]string{
		"action": "add_target", "protocol": "ssh", "host": "10.0.0.8", "port": "2222", "user": "root",
	}}
	if risk, _ := classifyToolRisk(write); risk != tools.RiskHigh {
		t.Fatalf("valid config mutation should still require approval, got %s", risk)
	}
}

func TestClassifyUnknownToolRequiresConfirmation(t *testing.T) {
	risk, reason := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: "not_registered_anywhere"})
	if risk != tools.RiskHigh || !strings.Contains(reason, "未知工具") {
		t.Fatalf("unknown tool risk=%q reason=%q", risk, reason)
	}
}

func TestClassifyHawkEyeMCPUsesToolAndActionRisk(t *testing.T) {
	handler := func(map[string]string) (string, error) { return "ok", nil }
	register := func(canonical, original string) {
		mcp.Global().RegisterHandler(canonical, mcp.ExternalTool{
			Name: canonical, OriginalName: original, Server: "hawkeye-risk-test",
		}, handler)
	}
	register("hawkeye_risk__capture_history", "hawkeye_capture_history")
	register("hawkeye_risk__intercept", "hawkeye_intercept")
	register("hawkeye_risk__evaluate", "hawkeye_evaluate")

	if risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: "hawkeye_risk__capture_history"}); risk != tools.RiskLow {
		t.Fatalf("HawkEye capture history should be low risk, got %s", risk)
	}
	if risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: "hawkeye_risk__intercept", ToolArgs: map[string]string{"action": "enable"}}); risk != tools.RiskLow {
		t.Fatalf("HawkEye intercept enable should not interrupt the normal workflow, got %s", risk)
	}
	if risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: "hawkeye_risk__intercept", ToolArgs: map[string]string{"action": "release"}}); risk != tools.RiskHigh {
		t.Fatalf("HawkEye intercept release should be high risk, got %s", risk)
	}
	if risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: "hawkeye_risk__evaluate", ToolArgs: map[string]string{"code": "1+1"}}); risk != tools.RiskHigh {
		t.Fatalf("HawkEye evaluate should remain high risk, got %s", risk)
	}
}

func TestClassifyFofaMapMCPUsesLocalActionRisk(t *testing.T) {
	handler := func(map[string]string) (string, error) { return "ok", nil }
	register := func(canonical, original string) {
		mcp.Global().RegisterHandler(canonical, mcp.ExternalTool{
			Name: canonical, OriginalName: original, Server: "fofamap-risk-test",
		}, handler)
	}
	register("fofamap_risk__search", "fofa_search")
	register("fofamap_risk__export", "fofa_export")
	register("fofamap_risk__plan", "nuclei_plan")
	register("fofamap_risk__execute", "nuclei_execute")

	tests := []struct {
		name string
		want string
	}{
		{"fofamap_risk__search", tools.RiskLow},
		{"fofamap_risk__export", tools.RiskMedium},
		{"fofamap_risk__plan", tools.RiskMedium},
		{"fofamap_risk__execute", tools.RiskHigh},
	}
	for _, test := range tests {
		if risk, _ := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: test.name}); risk != test.want {
			t.Fatalf("%s risk=%s, want %s", test.name, risk, test.want)
		}
	}
}

func TestResolveInspectionRunRiskUsesAction(t *testing.T) {
	tool, ok := tools.Get("inspection_run")
	if !ok {
		t.Fatal("inspection_run tool should exist")
	}
	for _, actionName := range []string{"", "inventory", "report"} {
		if risk, _ := resolveToolRisk(AgentAction{ToolName: tool.Name, ToolArgs: map[string]string{"action": actionName}}, tool); risk != tools.RiskLow {
			t.Fatalf("inspection_run %q should be low risk, got %s", actionName, risk)
		}
	}
	if risk, _ := resolveToolRisk(AgentAction{ToolName: tool.Name, ToolArgs: map[string]string{"action": "collect"}}, tool); risk != tools.RiskMedium {
		t.Fatalf("inspection_run collect should stay medium risk, got %s", risk)
	}
}

func TestResolveZIPPasswordRecoverRiskUsesAction(t *testing.T) {
	tool, ok := tools.Get("zip_password_recover")
	if !ok {
		t.Fatal("zip_password_recover tool should exist")
	}
	if risk, _ := resolveToolRisk(AgentAction{ToolName: tool.Name, ToolArgs: map[string]string{"action": "inspect", "source": "/tmp/evidence.zip"}}, tool); risk != tools.RiskLow {
		t.Fatalf("ZIP inspect should be low risk, got %s", risk)
	}
	for _, action := range []string{"recover", "repair", "auto", "crc32"} {
		if risk, _ := resolveToolRisk(AgentAction{ToolName: tool.Name, ToolArgs: map[string]string{"action": action, "source": "/tmp/evidence.zip"}}, tool); risk != tools.RiskHigh {
			t.Fatalf("ZIP %s should be high risk, got %s", action, risk)
		}
	}
}

func TestResolveFleetFileRiskUsesAction(t *testing.T) {
	tool, ok := tools.Get("fleet_file")
	if !ok {
		t.Fatal("fleet_file tool should exist")
	}
	for _, actionName := range []string{"ls", "read", "download"} {
		risk, reason := resolveToolRisk(AgentAction{
			Type:     ActionTool,
			ToolName: "fleet_file",
			ToolArgs: map[string]string{"action": actionName},
		}, tool)
		if risk != tools.RiskLow {
			t.Fatalf("fleet_file %s should be low risk, got %s (%s)", actionName, risk, reason)
		}
	}

	risk, reason := resolveToolRisk(AgentAction{
		Type:     ActionTool,
		ToolName: "fleet_file",
		ToolArgs: map[string]string{"action": "upload"},
	}, tool)
	if risk != tools.RiskHigh {
		t.Fatalf("fleet_file upload should be high risk, got %s (%s)", risk, reason)
	}
}

func TestSafeUTF8BytePrefixNeverSplitsRune(t *testing.T) {
	got := safeUTF8BytePrefix("你好abc", 4)
	if got != "你" {
		t.Fatalf("safe prefix=%q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("safe prefix is invalid UTF-8: %q", got)
	}
}

func TestEnrichActionExecutionTargetLocalRunOverridesRemote(t *testing.T) {
	origExecutor := executor.Current
	origHost := config.GlobalConfig.SSHHost
	executor.Current = fakeSSHExecutor{}
	config.GlobalConfig.SSHHost = "198.51.100.42:2222"
	defer func() {
		executor.Current = origExecutor
		config.GlobalConfig.SSHHost = origHost
	}()

	action := AgentAction{Type: ActionExecute, Command: "local_run sshpass -p x ssh root@10.0.0.1 hostname"}
	enrichActionExecutionTarget(&action)
	if action.TargetProtocol != "local" || action.TargetHost != "" || action.TargetName != "" {
		t.Fatalf("local_run target = name=%q proto=%q host=%q, want local without remote host", action.TargetName, action.TargetProtocol, action.TargetHost)
	}

	remote := AgentAction{Type: ActionExecute, Command: "hostname"}
	enrichActionExecutionTarget(&remote)
	if remote.TargetProtocol != "ssh" || remote.TargetHost != "198.51.100.42:2222" {
		t.Fatalf("remote target = proto=%q host=%q, want ssh host", remote.TargetProtocol, remote.TargetHost)
	}
}

type fakeRemoteExecutor struct{}

func (fakeRemoteExecutor) Run(string) (string, error) { return "", errors.New("not implemented") }
func (fakeRemoteExecutor) ReadTargetFile(string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (fakeRemoteExecutor) ListTargetDir(string) ([]string, error) {
	return nil, errors.New("not implemented")
}
func (fakeRemoteExecutor) IsRemote() bool { return true }
func (fakeRemoteExecutor) Close()         {}

type fakeSSHExecutor struct{ fakeRemoteExecutor }

func (fakeSSHExecutor) Mode() string { return "ssh" }

type capturingSudoExecutor struct{ command string }

func (e *capturingSudoExecutor) Run(command string) (string, error) {
	e.command = command
	return "sudo: a password is required", nil
}
func (*capturingSudoExecutor) ReadTargetFile(string) ([]byte, error) {
	return nil, errors.New("unused")
}
func (*capturingSudoExecutor) ListTargetDir(string) ([]string, error) {
	return nil, errors.New("unused")
}
func (*capturingSudoExecutor) IsRemote() bool { return true }
func (*capturingSudoExecutor) Close()         {}

func TestParseMCPServerSpecAcceptsScriptPathAndLegacyName(t *testing.T) {
	cfg, ok := parseMCPServerSpec("/opt/hawkeye/hawkeye-mcp-server.mjs,--port,19016")
	if !ok || cfg.Command != "node" || cfg.Name != "hx0-hawkeye" || len(cfg.Args) != 3 || cfg.Args[2] != "19016" {
		t.Fatalf("script spec: %#v ok=%v", cfg, ok)
	}
	legacy, ok := parseMCPServerSpec("browser:node:/tmp/server.mjs,--port,9")
	if !ok || legacy.Name != "browser" || legacy.Command != "node" || legacy.Args[0] != "/tmp/server.mjs" {
		t.Fatalf("legacy spec: %#v ok=%v", legacy, ok)
	}
	if _, ok := parseMCPServerSpec("not-a-spec"); ok {
		t.Fatal("invalid spec should fail")
	}
}

func TestMCPSpecCoveredByStructuredMatchesHawkEyeScript(t *testing.T) {
	cfg, ok := parseMCPServerSpec("/opt/hawkeye/hawkeye-mcp-server.mjs,--port,19016")
	if !ok {
		t.Fatal("parse")
	}
	if !mcpSpecCoveredByStructured(cfg, []config.MCPServerConfig{{
		Name: "hx0-hawkeye", Command: "node",
		Args: []string{"/opt/hawkeye/hawkeye-mcp-server.mjs", "--port", "19016"},
	}}) {
		t.Fatal("structured HawkEye config should cover leftover mcp_servers path")
	}
}
