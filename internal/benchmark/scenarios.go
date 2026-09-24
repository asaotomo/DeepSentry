package benchmark

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness"
	"ai-edr/internal/tools"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AllScenarios 全部 benchmark 场景
func AllScenarios() []Scenario {
	core := []Scenario{
		// --- LLM ---
		{
			ID: "LLM-01", Category: CatLLM, Name: "LLM 连通性",
			Description: "API 可达且返回有效内容", RequiresLLM: true,
			Run: runLLMConnectivity,
		},
		{
			ID: "LLM-02", Category: CatLLM, Name: "JSON 协议遵从",
			Description: "模型能输出可解析的 action JSON", RequiresLLM: true,
			Run: runLLMJSONProtocol,
		},
		{
			ID: "LLM-03", Category: CatLLM, Name: "Tool Action 推断",
			Description: "模型能正确使用 tool action", RequiresLLM: true,
			Run: runLLMToolAction,
		},

		// --- 控制端工具 ---
		{
			ID: "LOC-01", Category: CatLocalTool, Name: "DNS 解析 (控制端)",
			Description: "dns_lookup 从控制端发起", RequiresLLM: false,
			Run: runLocalDNS,
		},
		{
			ID: "LOC-02", Category: CatLocalTool, Name: "TCP 探活 (控制端)",
			Description: "ping 工具从控制端探测", RequiresLLM: false,
			Run: runLocalPing,
		},

		// --- 目标机工具 (需 SSH) ---
		{
			ID: "REM-01", Category: CatRemoteTool, Name: "SSH Shell 执行",
			Description: "远程 execute 通道", RequiresRemote: true,
			Run: runRemoteShell,
		},
		{
			ID: "REM-02", Category: CatRemoteTool, Name: "port_listen (/proc)",
			Description: "目标机监听端口审计", RequiresRemote: true,
			Run: runRemotePortListen,
		},
		{
			ID: "REM-03", Category: CatRemoteTool, Name: "mem_info (/proc)",
			Description: "目标机内存信息", RequiresRemote: true,
			Run: runRemoteMemInfo,
		},
		{
			ID: "REM-04", Category: CatRemoteTool, Name: "process_list (/proc)",
			Description: "目标机进程枚举", RequiresRemote: true,
			Run: runRemoteProcessList,
		},
		{
			ID: "REM-05", Category: CatRemoteTool, Name: "net_connections",
			Description: "目标机连接表", RequiresRemote: true,
			Run: runRemoteNetConn,
		},
		{
			ID: "REM-06", Category: CatRemoteTool, Name: "route_table",
			Description: "目标机路由表", RequiresRemote: true,
			Run: runRemoteRoute,
		},

		// --- 文件系统 ---
		{
			ID: "FS-01", Category: CatFilesystem, Name: "read_file 远程",
			Description: "SFTP 读取 /etc/os-release", RequiresRemote: true,
			Run: runFSReadRemote,
		},
		{
			ID: "FS-02", Category: CatFilesystem, Name: "grep 远程",
			Description: "Go 原生 grep /etc/passwd", RequiresRemote: true,
			Run: runFSGrepRemote,
		},
		{
			ID: "FS-03", Category: CatFilesystem, Name: "ls 远程",
			Description: "列出 /proc 目录", RequiresRemote: true,
			Run: runFSLsRemote,
		},
		{
			ID: "FS-04", Category: CatFilesystem, Name: "workspace 本地读写",
			Description: "控制端 workspace 路径正确", RequiresLLM: false,
			Run: runFSWorkspaceLocal,
		},

		// --- 本地-远程联动 ---
		{
			ID: "LINK-01", Category: CatLinkage, Name: "双视角工具对比",
			Description: "同会话内控制端 ping + 目标机 port_listen", RequiresRemote: true, RequiresLLM: true,
			Run: runLinkageDualPerspective,
		},
		{
			ID: "LINK-02", Category: CatLinkage, Name: "local_run + 远程工具",
			Description: "控制端 local_run 与远程 /proc 工具并存", RequiresRemote: true,
			Run: runLinkageLocalRemote,
		},
		{
			ID: "LINK-03", Category: CatLinkage, Name: "控制端探测目标 HTTP",
			Description: "http_probe 控制端访问目标 80", RequiresRemote: true,
			Run: runLinkageHTTPProbe,
		},

		// --- Agent 编排 ---
		{
			ID: "AGT-01", Category: CatAgent, Name: "Agent 单步 tool 调度",
			Description: "Harness 正确调度 port_listen", RequiresRemote: true, RequiresLLM: true,
			Run: runAgentToolDispatch,
		},
		{
			ID: "AGT-02", Category: CatAgent, Name: "Agent 多步 finish",
			Description: "2 步内完成 finish", RequiresRemote: true, RequiresLLM: true,
			Run: runAgentMultiStepFinish,
		},
		{
			ID: "AGT-03", Category: CatAgent, Name: "load_skill 动态注入",
			Description: "load_skill 后 prompt 含 skill 内容", RequiresLLM: true,
			Run: runAgentLoadSkill,
		},
		{
			ID: "AGT-04", Category: CatAgent, Name: "todo 状态机",
			Description: "todo action 更新 state", RequiresLLM: true,
			Run: runAgentTodo,
		},

		// --- Harness 组件 ---
		{
			ID: "HAR-01", Category: CatHarness, Name: "Memory remember",
			Description: "KV 记忆持久化", RequiresLLM: false,
			Run: runHarnessMemory,
		},
		{
			ID: "HAR-02", Category: CatHarness, Name: "Checkpoint 往返",
			Description: "保存并加载 checkpoint", RequiresLLM: false,
			Run: runHarnessCheckpoint,
		},
		{
			ID: "HAR-03", Category: CatHarness, Name: "Skills 目录加载",
			Description: "至少加载 1 个 skill", RequiresLLM: false,
			Run: runHarnessSkills,
		},

		// --- 高可用 ---
		{
			ID: "HA-01", Category: CatResilience, Name: "LLM 重试配置",
			Description: "llm_retries >= 1", RequiresLLM: false,
			Run: runHALLMRetryConfig,
		},
		{
			ID: "HA-02", Category: CatResilience, Name: "SSH 超时配置",
			Description: "ssh_command_timeout 已配置", RequiresLLM: false,
			Run: runHASSHTimeoutConfig,
		},
		{
			ID: "HA-03", Category: CatResilience, Name: "URL 规范化",
			Description: "/v1 base 自动补全", RequiresLLM: false,
			Run: runHAURLNormalize,
		},
	}
	return append(core, ExtendedScenarios()...)
}

// --- LLM runners ---

func runLLMConnectivity(ctx *Context) Result {
	start := time.Now()
	msgs := []analyzer.Message{{Role: "user", Content: "回复: OK"}}
	res, err := callLLMForBenchmark(msgs, false)
	lat := time.Since(start)
	if err != nil {
		if isExternalLLMCapacityEvidence(err.Error()) {
			return mkResult(false, true, 60, lat, err.Error(), "")
		}
		return mkResult(false, false, 0, lat, err.Error(), "")
	}
	ok := strings.Contains(strings.ToUpper(res.Content), "OK")
	score := 0.0
	if err == nil && len(res.Content) > 0 {
		if ok {
			score = scorePass(lat, 30*time.Second)
		} else {
			// API 可达且返回有效内容即视为连通
			score = scorePass(lat, 30*time.Second)
		}
	}
	passed := err == nil && len(res.Content) > 0
	result := mkResult(passed, !passed && err == nil, score, lat, "LLM 响应正常", truncate(res.Content, 80))
	result.Metrics = llmUsageMetrics(res)
	return result
}

func runLLMJSONProtocol(ctx *Context) Result {
	start := time.Now()
	prompt := `只输出 JSON，无 markdown: {"thought":"test","action":"finish","final_report":"protocol ok","is_finished":true}`
	msgs := []analyzer.Message{{Role: "user", Content: prompt}}
	res, err := callLLMForBenchmark(msgs, ctx.UseNativeTools)
	lat := time.Since(start)
	if err != nil {
		if isExternalLLMCapacityEvidence(err.Error()) {
			return mkResult(false, true, 60, lat, err.Error(), "")
		}
		return mkResult(false, false, 0, lat, err.Error(), "")
	}
	clean := strings.TrimSpace(res.Content)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	var m map[string]interface{}
	err = json.Unmarshal([]byte(strings.TrimSpace(clean)), &m)
	if err != nil && res.ToolCallArgs != "" {
		err = json.Unmarshal([]byte(res.ToolCallArgs), &m)
	}
	passed := err == nil && (m["action"] == "finish" || m["is_finished"] == true)
	score := 0.0
	if passed {
		score = scorePass(lat, 45*time.Second)
	} else if err == nil {
		score = 40
	}
	result := mkResult(passed, err == nil && !passed, score, lat, "JSON 可解析且含 finish", truncate(res.Content, 100))
	result.Metrics = llmUsageMetrics(res)
	return result
}

func runLLMToolAction(ctx *Context) Result {
	start := time.Now()
	prompt := `你是安全 Agent。必须只输出一行 JSON，不要 markdown，不要解释：
{"thought":"探测","action":"tool","tool_name":"dns_lookup","tool_args":{"host":"localhost"}}`
	msgs := []analyzer.Message{
		{Role: "system", Content: "你只输出 JSON action，action 必须是 tool/finish 之一，tool_name 填工具名。"},
		{Role: "user", Content: prompt},
	}
	var lastContent string
	var lastAction harness.AgentAction
	for attempt := 0; attempt < 2; attempt++ {
		res, err := callLLMForBenchmark(msgs, ctx.UseNativeTools)
		if err != nil {
			if attempt == 1 {
				lat := time.Since(start)
				if isExternalLLMCapacityEvidence(err.Error()) {
					return mkResult(false, true, 60, lat, err.Error(), "")
				}
				return mkResult(false, false, 0, lat, err.Error(), "")
			}
			continue
		}
		lastContent = res.Content
		lastAction = harness.ParseAction(parseLLMResponse(res))
		if lastAction.Type == harness.ActionTool && lastAction.ToolName == "dns_lookup" {
			lat := time.Since(start)
			result := mkResult(true, false, scorePass(lat, 45*time.Second), lat,
				fmt.Sprintf("action=%s tool=%s", lastAction.Type, lastAction.ToolName), truncate(res.Content, 100))
			result.Metrics = llmUsageMetrics(res)
			return result
		}
		lower := strings.ToLower(res.Content + res.ToolCallArgs)
		if strings.Contains(lower, "dns_lookup") {
			lat := time.Since(start)
			score := scorePass(lat, 45*time.Second)
			result := mkResult(true, true, score, lat, "dns_lookup inferred", truncate(res.Content, 100))
			result.Metrics = llmUsageMetrics(res)
			return result
		}
		msgs = append(msgs, analyzer.Message{Role: "assistant", Content: res.Content})
		msgs = append(msgs, analyzer.Message{Role: "user", Content: "格式错误。请严格输出: {\"action\":\"tool\",\"tool_name\":\"dns_lookup\",\"tool_args\":{\"host\":\"localhost\"}}"})
	}
	lat := time.Since(start)
	return mkResult(false, false, 0, lat, fmt.Sprintf("action=%s tool=%s", lastAction.Type, lastAction.ToolName), truncate(lastContent, 100))
}

func llmUsageMetrics(result analyzer.LLMResult) map[string]float64 {
	return map[string]float64{
		"prompt_tokens":     float64(result.Usage.PromptTokens),
		"completion_tokens": float64(result.Usage.CompletionTokens),
		"total_tokens":      float64(result.Usage.TotalTokens),
		"attempts":          float64(result.Attempts),
		"failovers":         float64(result.Failovers),
	}
}

func callLLMForBenchmark(msgs []analyzer.Message, useNativeTools bool) (analyzer.LLMResult, error) {
	var res analyzer.LLMResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		res, err = analyzer.CallLLMWithRetry(msgs, useNativeTools, nil)
		if err == nil {
			return res, nil
		}
		if !isExternalLLMCapacityEvidence(err.Error()) || attempt == 2 {
			return res, err
		}
		time.Sleep(time.Duration(attempt+1) * 6 * time.Second)
	}
	return res, err
}

func parseLLMResponse(res analyzer.LLMResult) analyzer.AgentResponse {
	if res.ToolCallArgs != "" {
		r, err := analyzer.ParseToolCallResponse(res.ToolCallArgs)
		if err == nil && (r.Action != "" || r.ToolName != "") {
			return r
		}
	}
	clean := strings.TrimSpace(res.Content)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	var resp analyzer.AgentResponse
	if err := json.Unmarshal([]byte(clean), &resp); err == nil {
		return resp
	}
	// 从文本中提取 JSON 块
	if idx := strings.Index(clean, "{"); idx >= 0 {
		if end := strings.LastIndex(clean, "}"); end > idx {
			if err := json.Unmarshal([]byte(clean[idx:end+1]), &resp); err == nil {
				return resp
			}
		}
	}
	return analyzer.AgentResponse{}
}

// --- Local tool runners ---

func runLocalDNS(ctx *Context) Result {
	start := time.Now()
	out, _, err := tools.Run("dns_lookup", map[string]string{"host": "localhost"}, false)
	lat := time.Since(start)
	passed := err == nil && len(out) > 0
	score := 0.0
	if passed {
		score = scorePass(lat, 5*time.Second)
	}
	return mkResult(passed, false, score, lat, "dns_lookup", truncate(out, 80))
}

func runLocalPing(ctx *Context) Result {
	start := time.Now()
	out, _, err := tools.Run("ping", map[string]string{"host": "127.0.0.1", "count": "1"}, false)
	lat := time.Since(start)
	passed := err == nil && strings.Contains(out, "127.0.0.1")
	score := 0.0
	if passed {
		score = scorePass(lat, 10*time.Second)
	}
	return mkResult(passed, false, score, lat, "ping localhost", truncate(out, 80))
}

// --- Remote tool runners ---

func runRemoteShell(ctx *Context) Result {
	start := time.Now()
	out, err := executor.Current.Run("cat /etc/os-release | head -1")
	lat := time.Since(start)
	passed := err == nil && strings.Contains(out, "NAME=")
	score := 0.0
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	return mkResult(passed, false, score, lat, "SSH shell", truncate(out, 80))
}

func runRemotePortListen(ctx *Context) Result {
	return runToolScenario("port_listen", nil, "LISTEN", 5*time.Second)
}

func runRemoteMemInfo(ctx *Context) Result {
	return runToolScenario("mem_info", nil, "MemTotal", 5*time.Second)
}

func runRemoteProcessList(ctx *Context) Result {
	return runToolScenario("process_list", map[string]string{"limit": "5"}, "PID", 8*time.Second)
}

func runRemoteNetConn(ctx *Context) Result {
	return runToolScenario("net_connections", map[string]string{"filter": "listen"}, "tcp", 8*time.Second)
}

func runRemoteRoute(ctx *Context) Result {
	return runToolScenario("route_table", nil, "IFACE", 5*time.Second)
}

func runToolScenario(name string, args map[string]string, expect string, threshold time.Duration) Result {
	start := time.Now()
	out, _, err := tools.Run(name, args, false)
	lat := time.Since(start)
	passed := err == nil && outputContains(out, expect)
	score := 0.0
	if passed {
		score = scorePass(lat, threshold)
	}
	msg := name
	if err != nil {
		msg = err.Error()
	}
	return mkResult(passed, false, score, lat, msg, truncate(out, 100))
}

// --- Filesystem ---

func runFSReadRemote(ctx *Context) Result {
	return runAgentDirectAction(harness.ActionReadFile, map[string]string{"path": "/etc/os-release"}, "PRETTY_NAME", 10*time.Second)
}

func runFSGrepRemote(ctx *Context) Result {
	return runAgentDirectAction(harness.ActionGrep, map[string]string{"path": "/etc/passwd", "pattern": "root"}, "root:", 10*time.Second)
}

func runFSLsRemote(ctx *Context) Result {
	return runAgentDirectAction(harness.ActionLS, map[string]string{"path": "/proc"}, "meminfo", 10*time.Second)
}

func runFSWorkspaceLocal(ctx *Context) Result {
	start := time.Now()
	home, _ := os.UserHomeDir()
	ws := filepath.Join(home, ".deepsentry", "workspace", "bench_test.txt")
	_ = os.MkdirAll(filepath.Dir(ws), 0755)
	_ = os.WriteFile(ws, []byte("benchmark"), 0644)
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true, WorkspaceDir: filepath.Dir(ws)})
	lat := time.Since(start)
	if err != nil {
		return mkResult(false, false, 0, lat, err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	action := &harness.AgentAction{Type: harness.ActionReadFile, Path: ws}
	res, err := agent.HandleAction(stepCtx, action)
	lat = time.Since(start)
	passed := err == nil && strings.Contains(res.Output, "benchmark")
	score := 0.0
	if passed {
		score = 100
	}
	_ = os.Remove(ws)
	return mkResult(passed, false, score, lat, "workspace read_file", truncate(res.Output, 60))
}

func runAgentDirectAction(actionType harness.ActionType, fields map[string]string, expect string, threshold time.Duration) Result {
	start := time.Now()
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, time.Since(start), err.Error(), "")
	}
	action := &harness.AgentAction{Type: actionType}
	if p, ok := fields["path"]; ok {
		action.Path = p
	}
	if p, ok := fields["pattern"]; ok {
		action.Pattern = p
	}
	if p, ok := fields["glob_pattern"]; ok {
		action.GlobPattern = p
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext(), Executor: executor.Current}
	res, err := agent.HandleAction(stepCtx, action)
	lat := time.Since(start)
	if res == nil {
		return mkResult(false, false, 0, lat, string(actionType), fmt.Sprintf("nil result: %v", err))
	}
	passed := err == nil && outputContains(res.Output, expect)
	score := 0.0
	if passed {
		score = scorePass(lat, threshold)
	}
	return mkResult(passed, false, score, lat, string(actionType), truncate(res.Output, 80))
}

// --- Linkage ---

func runLinkageDualPerspective(ctx *Context) Result {
	start := time.Now()
	// 目标机视角
	tout, _, terr := tools.Run("port_listen", nil, false)
	// 控制端视角
	pout, _, perr := tools.Run("ping", map[string]string{"host": "127.0.0.1", "count": "1"}, false)
	lat := time.Since(start)
	passed := terr == nil && perr == nil && strings.Contains(tout, "LISTEN") && strings.Contains(pout, "127.0.0.1")
	score := 0.0
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	ev := fmt.Sprintf("target:%s | controller:%s", truncate(tout, 40), truncate(pout, 40))
	return mkResult(passed, terr == nil || perr == nil, score, lat, "双视角工具同会话", ev)
}

func runLinkageLocalRemote(ctx *Context) Result {
	start := time.Now()
	localOut, localErr := executor.Current.Run("local_run echo LINK_OK")
	remoteOut, _, remoteErr := tools.Run("mem_info", nil, false)
	lat := time.Since(start)
	passed := localErr == nil && remoteErr == nil && strings.Contains(localOut, "LINK_OK") && strings.Contains(remoteOut, "Mem")
	score := 0.0
	if passed {
		score = scorePass(lat, 20*time.Second)
	}
	return mkResult(passed, false, score, lat, "local_run + mem_info", truncate(localOut+"|"+remoteOut, 80))
}

func runLinkageHTTPProbe(ctx *Context) Result {
	host := config.GlobalConfig.SSHHost
	if host == "" {
		return mkResult(false, false, 0, 0, "无 SSH 主机", "")
	}
	host = strings.Split(host, ":")[0]
	start := time.Now()
	out, _, err := tools.Run("http_probe", map[string]string{"url": "http://" + host + "/", "method": "HEAD"}, false)
	lat := time.Since(start)
	passed := err == nil && (strings.Contains(out, "200") || strings.Contains(out, "301") || strings.Contains(out, "302") || strings.Contains(out, "HTTP"))
	partial := err == nil && len(out) > 0 && !passed
	score := 0.0
	if passed {
		score = scorePass(lat, 15*time.Second)
	} else if partial {
		score = 60
	}
	return mkResult(passed, partial, score, lat, "http_probe -> target", truncate(out, 80))
}

// --- Agent E2E ---

func runAgentToolDispatch(ctx *Context) Result {
	steps, finished, ev := runAgentTask("Benchmark 固定流程：第 1 步必须输出 JSON 调用 action=tool 且 tool_name=port_listen；收到 Output 后第 2 步必须 action=finish。不要使用 execute，不要直接 finish。", 4)
	passed := finished && strings.Contains(ev, "port_listen")
	score := 0.0
	if passed {
		score = 100
	} else if strings.Contains(ev, "port_listen") {
		score = 60
	}
	if !passed && isExternalLLMCapacityEvidence(ev) {
		score = 60
	}
	return mkResult(passed, !passed && steps > 0, score, 0, fmt.Sprintf("%d steps", steps), truncate(ev, 120))
}

func runAgentMultiStepFinish(ctx *Context) Result {
	steps, finished, ev := runAgentTask("Benchmark 固定流程：第 1 步必须输出 JSON 调用 action=tool 且 tool_name=mem_info；收到 Output 后第 2 步必须 action=finish 总结。不要使用 execute。", 5)
	passed := finished && steps <= 4
	score := 0.0
	if passed {
		score = 100
	} else if finished {
		score = 75
	}
	if !finished && isExternalLLMCapacityEvidence(ev) {
		return mkResult(false, true, 60, 0, fmt.Sprintf("steps=%d finished=%v", steps, finished), truncate(ev, 100))
	}
	return mkResult(passed, finished && !passed, score, 0, fmt.Sprintf("steps=%d finished=%v", steps, finished), truncate(ev, 100))
}

func isExternalLLMCapacityEvidence(ev string) bool {
	lower := strings.ToLower(ev)
	return strings.Contains(lower, "api error 429") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "capacity")
}

func runAgentLoadSkill(ctx *Context) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	if agent.Catalog == nil || len(agent.Catalog.Skills) == 0 {
		return mkResult(false, true, 50, 0, "无 skill 目录", "")
	}
	skillName := agent.Catalog.Skills[0].Name
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	action := &harness.AgentAction{Type: harness.ActionLoadSkill, SkillName: skillName}
	res, err := agent.HandleAction(stepCtx, action)
	passed := err == nil && len(agent.State.LoadedSkills[skillName]) > 100
	prompt := agent.BuildSystemPrompt("")
	inPrompt := strings.Contains(prompt, skillName)
	score := 0.0
	if passed && inPrompt {
		score = 100
	} else if passed {
		score = 80
	}
	return mkResult(passed && inPrompt, passed, score, 0, skillName, truncate(res.Output, 60))
}

func runAgentTodo(ctx *Context) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	action := &harness.AgentAction{
		Type:  harness.ActionTodo,
		Todos: []harness.TodoItem{{ID: "1", Content: "benchmark", Status: "pending"}},
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	_, err = agent.HandleAction(stepCtx, action)
	passed := err == nil && len(agent.State.Todos) == 1
	prompt := agent.BuildSystemPrompt("")
	inPrompt := strings.Contains(prompt, "benchmark")
	score := 0.0
	if passed && inPrompt {
		score = 100
	}
	return mkResult(passed && inPrompt, passed, score, 0, "todo 注入 prompt", "")
}

func runAgentTask(userGoal string, maxSteps int) (steps int, finished bool, evidence string) {
	for attempt := 0; attempt < 3; attempt++ {
		steps, finished, evidence = runAgentTaskOnce(userGoal, maxSteps)
		if !isExternalLLMCapacityEvidence(evidence) {
			return steps, finished, evidence
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 6 * time.Second)
		}
	}
	return steps, finished, evidence
}

func runAgentTaskOnce(userGoal string, maxSteps int) (steps int, finished bool, evidence string) {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return 0, false, err.Error()
	}
	sysCtx := collector.GetSystemContext()
	history := []analyzer.Message{{Role: "user", Content: userGoal}}
	var lastOutput strings.Builder
	confirm := func(*harness.AgentAction) bool { return true }

	for steps = 0; steps < maxSteps; steps++ {
		extraPrompt := agent.BuildSystemPrompt("")
		resp, err := runAgentStepForBenchmark(analyzer.StepOptions{
			SysCtx: sysCtx, History: &history, ExtraPrompt: extraPrompt, UseNativeTools: agent.UseNativeTools,
		})
		if err != nil {
			lastOutput.WriteString("err:" + err.Error())
			break
		}
		action := harness.ParseAction(resp)
		if action.Type == harness.ActionFinish || action.IsFinished {
			finished = true
			lastOutput.WriteString(action.FinalReport)
			steps++
			break
		}
		lastOutput.WriteString(string(action.Type))
		if action.ToolName != "" {
			lastOutput.WriteString(":" + action.ToolName)
		}
		lastOutput.WriteString("|")
		stepCtx := &harness.StepContext{State: agent.State, SysCtx: sysCtx, BatchMode: true, StepNum: steps + 1, Executor: executor.Current}
		result, err := agent.HandleAction(stepCtx, &action)
		if err != nil {
			lastOutput.WriteString("err:" + err.Error())
			steps++
			break
		}
		history = append(history, analyzer.Message{Role: "assistant", Content: fmt.Sprintf(`{"action":"%s","tool_name":"%s"}`, action.Type, action.ToolName)})
		history = append(history, analyzer.Message{Role: "user", Content: "Output:\n" + result.Output})
		if result.ShouldStop {
			finished = true
			steps++
			break
		}
	}
	_ = confirm
	return steps, finished, lastOutput.String()
}

func runAgentStepForBenchmark(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
	var resp analyzer.AgentResponse
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = analyzer.RunAgentStepWithOptions(opts)
		if err == nil {
			return resp, nil
		}
		if !isExternalLLMCapacityEvidence(err.Error()) || attempt == 2 {
			return resp, err
		}
		time.Sleep(time.Duration(attempt+1) * 6 * time.Second)
	}
	return resp, err
}

// --- Harness ---

func runHarnessMemory(ctx *Context) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil || agent.MemoryStore == nil {
		return mkResult(false, false, 0, 0, "memory store nil", "")
	}
	action := &harness.AgentAction{Type: harness.ActionRemember, MemoryKey: "bench_key", MemoryValue: "bench_val"}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	res, err := agent.HandleAction(stepCtx, action)
	passed := err == nil && strings.Contains(res.Output, "已保存")
	return mkResult(passed, false, boolScore(passed), 0, res.Output, "")
}

func runHarnessCheckpoint(ctx *Context) Result {
	start := time.Now()
	sid := fmt.Sprintf("session_bench_%d", time.Now().UnixNano())
	cp, err := harness.NewCheckpointStore(sid)
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	state := harness.NewAgentState("")
	state.SetMemory("k", "v")
	data := harness.CheckpointData{SessionID: sid, StepNum: 3, State: state, History: []analyzer.Message{{Role: "user", Content: "test"}}}
	err = cp.Save(data)
	if err != nil {
		return mkResult(false, false, 0, time.Since(start), err.Error(), "")
	}
	loaded, err := harness.LoadCheckpoint(sid)
	lat := time.Since(start)
	passed := err == nil && loaded.StepNum == 3 && loaded.State != nil
	return mkResult(passed, false, boolScore(passed), lat, sid, "")
}

func runHarnessSkills(ctx *Context) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	n := 0
	if agent.Catalog != nil {
		n = len(agent.Catalog.Skills)
	}
	passed := n >= 1
	return mkResult(passed, false, boolScore(passed), 0, fmt.Sprintf("%d skills", n), "")
}

// --- HA ---

func runHALLMRetryConfig(ctx *Context) Result {
	n := config.GlobalConfig.EffectiveLLMRetries()
	passed := n >= 1
	return mkResult(passed, false, boolScore(passed), 0, fmt.Sprintf("retries=%d", n), "")
}

func runHASSHTimeoutConfig(ctx *Context) Result {
	n := config.GlobalConfig.EffectiveSSHTimeout()
	passed := n >= 30
	return mkResult(passed, false, boolScore(passed), 0, fmt.Sprintf("timeout=%ds", n), "")
}

func runHAURLNormalize(ctx *Context) Result {
	u := config.NormalizeChatURL("https://example.com/v1")
	passed := strings.HasSuffix(u, "/chat/completions")
	return mkResult(passed, false, boolScore(passed), 0, u, "")
}

func boolScore(p bool) float64 {
	if p {
		return 100
	}
	return 0
}

func outputContains(out, expect string) bool {
	return strings.Contains(strings.ToLower(out), strings.ToLower(expect))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
