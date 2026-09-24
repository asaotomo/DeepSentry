package benchmark

import (
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/harness"
	"ai-edr/internal/harness/subagent"
	"ai-edr/internal/tools"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExtendedScenarios 扩展场景：取证、应急、安全边界
func ExtendedScenarios() []Scenario {
	return []Scenario{
		// --- 控制端扩展 ---
		{
			ID: "LOC-03", Category: CatLocalTool, Name: "HTTP 探测 (控制端)",
			Description: "http_probe 从控制端发起", RequiresLLM: false,
			Run: runLocalHTTPProbe,
		},
		{
			ID: "LOC-04", Category: CatLocalTool, Name: "TCP 端口探活 (控制端)",
			Description: "netcat_probe 从控制端探测", RequiresRemote: true, RequiresLLM: false,
			Run: runLocalNetcatProbe,
		},

		// --- 目标机扩展 ---
		{
			ID: "REM-07", Category: CatRemoteTool, Name: "arp_table (/proc)",
			Description: "ARP 缓存表", RequiresRemote: true,
			Run: runRemoteARP,
		},
		{
			ID: "REM-08", Category: CatRemoteTool, Name: "firewall_status (/proc)",
			Description: "内核防火墙/网络状态", RequiresRemote: true,
			Run: runRemoteFirewall,
		},
		{
			ID: "REM-09", Category: CatRemoteTool, Name: "flow_snapshot 连接快照",
			Description: "连接流快照对比", RequiresRemote: true,
			Run: runRemoteFlowSnapshot,
		},

		// --- 文件系统扩展 ---
		{
			ID: "FS-05", Category: CatFilesystem, Name: "workspace write + read",
			Description: "控制端 workspace 写入后读回", RequiresLLM: false,
			Run: runFSWorkspaceWriteRead,
		},
		{
			ID: "FS-06", Category: CatFilesystem, Name: "edit_file workspace",
			Description: "控制端 workspace 文件编辑", RequiresLLM: false,
			Run: runFSWorkspaceEdit,
		},
		{
			ID: "FS-07", Category: CatFilesystem, Name: "glob 远程 /etc",
			Description: "glob 匹配目标机 /etc/*release*", RequiresRemote: true,
			Run: runFSGlobRemote,
		},

		// --- 联动扩展 ---
		{
			ID: "LINK-04", Category: CatLinkage, Name: "应急响应工具链",
			Description: "mem_info + process_list + port_listen 串联", RequiresRemote: true,
			Run: runLinkageIncidentChain,
		},
		{
			ID: "LINK-05", Category: CatLinkage, Name: "视角标签验证",
			Description: "远程输出含目标机标签、本地含控制端标签", RequiresRemote: true,
			Run: runLinkagePerspectiveTags,
		},
		{
			ID: "LINK-06", Category: CatLinkage, Name: "控制端扫描目标端口",
			Description: "nmap_scan quick 从控制端扫目标 SSH 端口", RequiresRemote: true,
			Run: runLinkagePortScan,
		},

		// --- 取证分析 ---
		{
			ID: "FOR-01", Category: CatForensics, Name: "file_ident 魔数识别",
			Description: "识别 /etc/passwd 文件类型", RequiresRemote: true,
			Run: runForensicFileIdent,
		},
		{
			ID: "FOR-02", Category: CatForensics, Name: "file_hash SHA256",
			Description: "计算 /etc/passwd 哈希", RequiresRemote: true,
			Run: runForensicFileHash,
		},
		{
			ID: "FOR-03", Category: CatForensics, Name: "file_strings 提取",
			Description: "从 /etc/passwd 提取 root 字符串", RequiresRemote: true,
			Run: runForensicFileStrings,
		},
		{
			ID: "FOR-04", Category: CatForensics, Name: "read_log 文本日志",
			Description: "read_log 读取 /etc/os-release", RequiresRemote: true,
			Run: runForensicReadLog,
		},
		{
			ID: "FOR-05", Category: CatForensics, Name: "grep 取证模式",
			Description: "grep /etc/passwd 中 UID=0 账户", RequiresRemote: true,
			Run: runForensicGrepPriv,
		},

		// --- 安全应急场景 ---
		{
			ID: "INC-01", Category: CatIncident, Name: "内存告警排查",
			Description: "mem_info 获取 MemTotal/MemFree", RequiresRemote: true,
			Run: runIncidentMemAlert,
		},
		{
			ID: "INC-02", Category: CatIncident, Name: "网络暴露面审计",
			Description: "port_listen + net_connections listen", RequiresRemote: true,
			Run: runIncidentExposure,
		},
		{
			ID: "INC-03", Category: CatIncident, Name: "异常连接排查",
			Description: "net_connections established + arp_table", RequiresRemote: true,
			Run: runIncidentConnections,
		},
		{
			ID: "INC-04", Category: CatIncident, Name: "webshell-hunt Skill",
			Description: "加载 webshell-hunt skill 并注入 prompt", RequiresLLM: false,
			Run: runIncidentSkillWebshell,
		},
		{
			ID: "INC-05", Category: CatIncident, Name: "log-analysis Skill",
			Description: "加载 log-analysis skill", RequiresLLM: false,
			Run: runIncidentSkillLog,
		},
		{
			ID: "INC-06", Category: CatIncident, Name: "network-recon Skill",
			Description: "加载 network-recon skill", RequiresLLM: false,
			Run: runIncidentSkillNetwork,
		},
		{
			ID: "INC-07", Category: CatIncident, Name: "forensics Skill E2E",
			Description: "load forensics skill + mem_info 工具链", RequiresRemote: true,
			Run: runIncidentForensicsChain,
		},
		{
			ID: "INC-08", Category: CatIncident, Name: "vuln-scan Skill",
			Description: "加载 vuln-scan skill", RequiresLLM: false,
			Run: runIncidentSkillVuln,
		},

		// --- Agent 扩展 ---
		{
			ID: "AGT-05", Category: CatAgent, Name: "子 Agent 注册表",
			Description: "prompt 含当前子 Agent 目录", RequiresLLM: false,
			Run: runAgentSubAgentRegistry,
		},
		{
			ID: "AGT-06", Category: CatAgent, Name: "子 Agent 未知拦截",
			Description: "未知 task_name 返回友好错误", RequiresLLM: false,
			Run: runAgentSubAgentUnknown,
		},
		{
			ID: "AGT-07", Category: CatAgent, Name: "应急 E2E (LLM)",
			Description: "LLM 驱动 mem_info + port_listen 后 finish", RequiresRemote: true, RequiresLLM: true,
			Run: runAgentIncidentE2E,
		},

		// --- Harness 扩展 ---
		{
			ID: "HAR-04", Category: CatHarness, Name: "子 Agent 规格数量",
			Description: "至少 5 个预置子 Agent", RequiresLLM: false,
			Run: runHarnessSubAgents,
		},
		{
			ID: "HAR-05", Category: CatHarness, Name: "Memory forget",
			Description: "remember 后 forget 清除", RequiresLLM: false,
			Run: runHarnessMemoryForget,
		},

		// --- 安全边界 ---
		{
			ID: "SEC-01", Category: CatSecurity, Name: "受保护路径拦截",
			Description: "禁止读取 config.yaml", RequiresLLM: false,
			Run: runSecurityProtectedPath,
		},
		{
			ID: "SEC-02", Category: CatSecurity, Name: "workspace 白名单",
			Description: "workspace 可读、config 不可读", RequiresLLM: false,
			Run: runSecurityWorkspaceWhitelist,
		},
		{
			ID: "SEC-03", Category: CatSecurity, Name: "远程视角隔离",
			Description: "ping 不走目标机 execute", RequiresRemote: true,
			Run: runSecurityPerspectiveIsolation,
		},
		{
			ID: "SEC-04", Category: CatSecurity, Name: "工具风险等级标注",
			Description: "nmap_scan 为 high 风险", RequiresLLM: false,
			Run: runSecurityToolRiskLevel,
		},
	}
}

// --- Local extended ---

func runLocalHTTPProbe(ctx *Context) Result {
	start := time.Now()
	out, _, err := tools.Run("http_probe", map[string]string{"url": "http://example.com", "method": "HEAD"}, false)
	lat := time.Since(start)
	passed := err == nil && (outputContains(out, "HTTP") || outputContains(out, "200") || outputContains(out, "Status"))
	score := 0.0
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	return mkResult(passed, err == nil && !passed, score, lat, "http_probe example.com", truncate(out, 80))
}

func runLocalNetcatProbe(ctx *Context) Result {
	host := config.GlobalConfig.SSHHost
	if host == "" {
		return mkResult(false, false, 0, 0, "无 SSH 主机", "")
	}
	host = strings.Split(host, ":")[0]
	port := "22"
	if strings.Contains(config.GlobalConfig.SSHHost, ":2222") {
		port = "2222"
	}
	start := time.Now()
	out, _, err := tools.Run("netcat_probe", map[string]string{"host": host, "port": port}, false)
	lat := time.Since(start)
	passed := err == nil && (outputContains(out, "开放") || outputContains(out, "可达") || outputContains(out, "open"))
	score := 0.0
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	return mkResult(passed, false, score, lat, fmt.Sprintf("netcat %s:%s", host, port), truncate(out, 80))
}

// --- Remote extended ---

func runRemoteARP(ctx *Context) Result {
	return runToolScenario("arp_table", nil, "ARP", 8*time.Second)
}

func runRemoteFirewall(ctx *Context) Result {
	start := time.Now()
	out, _, err := tools.Run("firewall_status", nil, false)
	lat := time.Since(start)
	passed := err == nil && (outputContains(out, "防火墙") || outputContains(out, "/proc") || outputContains(out, "port_listen"))
	score := 0.0
	if passed {
		score = scorePass(lat, 8*time.Second)
	}
	return mkResult(passed, false, score, lat, "firewall_status", truncate(out, 80))
}

func runRemoteFlowSnapshot(ctx *Context) Result {
	start := time.Now()
	out, _, err := tools.Run("flow_snapshot", map[string]string{"interval": "1"}, false)
	lat := time.Since(start)
	passed := err == nil && (outputContains(out, "连接") || outputContains(out, "snapshot") || outputContains(out, "PROTO"))
	score := 0.0
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	return mkResult(passed, false, score, lat, "flow_snapshot", truncate(out, 80))
}

// --- Filesystem extended ---

func workspaceBenchPath(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".deepsentry", "workspace", name)
}

func runFSWorkspaceWriteRead(ctx *Context) Result {
	start := time.Now()
	ws := workspaceBenchPath("bench_write.txt")
	_ = os.MkdirAll(filepath.Dir(ws), 0755)
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true, WorkspaceDir: filepath.Dir(ws)})
	if err != nil {
		return mkResult(false, false, 0, time.Since(start), err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}

	writeAction := &harness.AgentAction{Type: harness.ActionWriteFile, Path: ws, Content: "bench_write_ok"}
	writeRes, err := agent.HandleAction(stepCtx, writeAction)
	if err != nil || !outputContains(writeRes.Output, "已写入") {
		_ = os.Remove(ws)
		return mkResult(false, false, 0, time.Since(start), "write failed", truncate(writeRes.Output, 60))
	}

	readAction := &harness.AgentAction{Type: harness.ActionReadFile, Path: ws}
	readRes, err := agent.HandleAction(stepCtx, readAction)
	lat := time.Since(start)
	passed := err == nil && outputContains(readRes.Output, "bench_write_ok")
	score := boolScore(passed)
	_ = os.Remove(ws)
	return mkResult(passed, false, score, lat, "write+read workspace", truncate(readRes.Output, 60))
}

func runFSWorkspaceEdit(ctx *Context) Result {
	start := time.Now()
	ws := workspaceBenchPath("bench_edit.txt")
	_ = os.MkdirAll(filepath.Dir(ws), 0755)
	_ = os.WriteFile(ws, []byte("hello world"), 0644)
	defer os.Remove(ws)

	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true, WorkspaceDir: filepath.Dir(ws)})
	if err != nil {
		return mkResult(false, false, 0, time.Since(start), err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	action := &harness.AgentAction{Type: harness.ActionEditFile, Path: ws, OldString: "world", NewString: "benchmark"}
	res, err := agent.HandleAction(stepCtx, action)
	lat := time.Since(start)
	passed := err == nil && outputContains(res.Output, "已编辑")
	if passed {
		data, _ := os.ReadFile(ws)
		passed = strings.Contains(string(data), "benchmark")
	}
	return mkResult(passed, false, boolScore(passed), lat, "edit_file workspace", truncate(res.Output, 60))
}

func runFSGlobRemote(ctx *Context) Result {
	return runAgentDirectAction(harness.ActionGlob, map[string]string{"path": "/etc", "glob_pattern": "os-release"}, "os-release", 10*time.Second)
}

// --- Linkage extended ---

func runLinkageIncidentChain(ctx *Context) Result {
	start := time.Now()
	chain := []struct {
		name   string
		args   map[string]string
		expect string
	}{
		{"mem_info", nil, "MemTotal"},
		{"process_list", map[string]string{"limit": "3"}, "PID"},
		{"port_listen", nil, "LISTEN"},
	}
	var ev strings.Builder
	allPass := true
	for _, step := range chain {
		out, _, err := tools.Run(step.name, step.args, false)
		if err != nil || !outputContains(out, step.expect) {
			allPass = false
			ev.WriteString(step.name + ":FAIL|")
		} else {
			ev.WriteString(step.name + ":OK|")
		}
	}
	lat := time.Since(start)
	score := 0.0
	if allPass {
		score = scorePass(lat, 20*time.Second)
	} else {
		score = 40
	}
	return mkResult(allPass, !allPass, score, lat, "应急工具链", ev.String())
}

func runLinkagePerspectiveTags(ctx *Context) Result {
	start := time.Now()
	remoteOut, _, rerr := tools.Run("mem_info", nil, false)
	localOut, _, lerr := tools.Run("ping", map[string]string{"host": "127.0.0.1", "count": "1"}, false)
	lat := time.Since(start)
	remoteTag := outputContains(remoteOut, "远程") || outputContains(remoteOut, "目标")
	localTag := outputContains(localOut, "控制") || outputContains(localOut, "Go内置")
	passed := rerr == nil && lerr == nil && remoteTag && localTag
	score := boolScore(passed)
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	return mkResult(passed, rerr == nil || lerr == nil, score, lat, "视角标签", truncate(remoteOut, 30)+"|"+truncate(localOut, 30))
}

func runLinkagePortScan(ctx *Context) Result {
	host := config.GlobalConfig.SSHHost
	if host == "" {
		return mkResult(false, false, 0, 0, "无 SSH 主机", "")
	}
	host = strings.Split(host, ":")[0]
	port := "22"
	if strings.Contains(config.GlobalConfig.SSHHost, ":2222") {
		port = "2222"
	}
	start := time.Now()
	out, _, err := tools.Run("nmap_scan", map[string]string{"host": host, "ports": port, "mode": "quick"}, false)
	lat := time.Since(start)
	passed := err == nil && (outputContains(out, "开放") || outputContains(out, port))
	score := 0.0
	if passed {
		score = scorePass(lat, 30*time.Second)
	}
	return mkResult(passed, false, score, lat, "nmap_scan target", truncate(out, 80))
}

// --- Forensics ---

func runForensicFileIdent(ctx *Context) Result {
	return runToolScenario("file_ident", map[string]string{"path": "/etc/passwd"}, "文本", 10*time.Second)
}

func runForensicFileHash(ctx *Context) Result {
	return runToolScenario("file_hash", map[string]string{"path": "/etc/passwd"}, "SHA256", 15*time.Second)
}

func runForensicFileStrings(ctx *Context) Result {
	return runToolScenario("file_strings", map[string]string{"path": "/etc/passwd", "pattern": "root", "limit": "5"}, "root", 15*time.Second)
}

func runForensicReadLog(ctx *Context) Result {
	return runToolScenario("read_log", map[string]string{"path": "/etc/os-release", "lines": "5"}, "NAME", 10*time.Second)
}

func runForensicGrepPriv(ctx *Context) Result {
	return runAgentDirectAction(harness.ActionGrep, map[string]string{"path": "/etc/passwd", "pattern": "root:"}, "root:", 10*time.Second)
}

// --- Incident ---

func runIncidentMemAlert(ctx *Context) Result {
	start := time.Now()
	out, _, err := tools.Run("mem_info", nil, false)
	lat := time.Since(start)
	passed := err == nil && outputContains(out, "MemTotal") && outputContains(out, "MemFree")
	score := 0.0
	if passed {
		score = scorePass(lat, 8*time.Second)
	}
	return mkResult(passed, false, score, lat, "内存告警", truncate(out, 60))
}

func runIncidentExposure(ctx *Context) Result {
	start := time.Now()
	pout, _, perr := tools.Run("port_listen", nil, false)
	nout, _, nerr := tools.Run("net_connections", map[string]string{"filter": "listen"}, false)
	lat := time.Since(start)
	passed := perr == nil && nerr == nil && outputContains(pout, "LISTEN") && outputContains(nout, "tcp")
	score := boolScore(passed)
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	return mkResult(passed, false, score, lat, "暴露面审计", "port_listen+net_connections")
}

func runIncidentConnections(ctx *Context) Result {
	start := time.Now()
	nout, _, nerr := tools.Run("net_connections", map[string]string{"filter": "established"}, false)
	aout, _, aerr := tools.Run("arp_table", nil, false)
	lat := time.Since(start)
	passed := nerr == nil && aerr == nil && len(nout) > 50 && len(aout) > 20
	score := boolScore(passed)
	if passed {
		score = scorePass(lat, 15*time.Second)
	}
	return mkResult(passed, nerr == nil || aerr == nil, score, lat, "异常连接", truncate(nout, 40))
}

func runIncidentSkillLoad(skillName string) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	action := &harness.AgentAction{Type: harness.ActionLoadSkill, SkillName: skillName}
	res, err := agent.HandleAction(stepCtx, action)
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), truncate(res.Output, 60))
	}
	loaded := len(agent.State.LoadedSkills[skillName]) > 50
	prompt := agent.BuildSystemPrompt("")
	inPrompt := strings.Contains(prompt, skillName)
	passed := loaded && inPrompt
	return mkResult(passed, loaded, boolScore(passed), 0, skillName, truncate(res.Output, 60))
}

func runIncidentSkillWebshell(ctx *Context) Result {
	return runIncidentSkillLoad("webshell-hunt")
}

func runIncidentSkillLog(ctx *Context) Result {
	return runIncidentSkillLoad("log-analysis")
}

func runIncidentSkillNetwork(ctx *Context) Result {
	return runIncidentSkillLoad("network-recon")
}

func runIncidentSkillVuln(ctx *Context) Result {
	return runIncidentSkillLoad("vuln-scan")
}

func runIncidentForensicsChain(ctx *Context) Result {
	start := time.Now()
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}

	skillAction := &harness.AgentAction{Type: harness.ActionLoadSkill, SkillName: "forensics"}
	if _, err := agent.HandleAction(stepCtx, skillAction); err != nil {
		return mkResult(false, false, 0, time.Since(start), "load forensics failed", "")
	}

	out, _, err := tools.Run("mem_info", nil, false)
	lat := time.Since(start)
	loaded := len(agent.State.LoadedSkills["forensics"]) > 50
	passed := loaded && err == nil && outputContains(out, "MemTotal")
	score := boolScore(passed)
	if passed {
		score = scorePass(lat, 12*time.Second)
	}
	return mkResult(passed, loaded, score, lat, "forensics+mem_info", truncate(out, 60))
}

// --- Agent extended ---

func runAgentSubAgentRegistry(ctx *Context) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	prompt := agent.BuildSystemPrompt("")
	required := make([]string, 0, len(subagent.Registry))
	for _, spec := range subagent.Registry {
		required = append(required, spec.Name)
	}
	missing := 0
	for _, name := range required {
		if !strings.Contains(prompt, name) {
			missing++
		}
	}
	passed := missing == 0
	score := float64(len(required)-missing) / float64(len(required)) * 100
	return mkResult(passed, missing > 0 && missing < len(required), score, 0, fmt.Sprintf("%d/%d subagents", len(required)-missing, len(required)), "")
}

func runAgentSubAgentUnknown(ctx *Context) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	action := &harness.AgentAction{Type: harness.ActionTask, TaskName: "nonexistent-agent", TaskPrompt: "test"}
	res, err := agent.HandleAction(stepCtx, action)
	passed := err == nil && outputContains(res.Output, "未知子 Agent")
	return mkResult(passed, false, boolScore(passed), 0, "unknown subagent blocked", truncate(res.Output, 80))
}

func runAgentIncidentE2E(ctx *Context) Result {
	steps, finished, ev := runAgentTask("Benchmark 固定流程：第 1 步必须 action=tool/tool_name=mem_info；第 2 步必须 action=tool/tool_name=port_listen；收到两个 Output 后第 3 步必须 action=finish 给出简要结论。不要使用 execute，不要跳过任一工具。", 6)
	passed := finished && outputContains(ev, "mem_info") && outputContains(ev, "port_listen")
	score := 0.0
	if passed {
		score = 100
	} else if outputContains(ev, "mem_info") || outputContains(ev, "port_listen") {
		score = 65
	} else if isExternalLLMCapacityEvidence(ev) {
		score = 60
	}
	return mkResult(passed, !passed && steps > 0, score, 0, fmt.Sprintf("%d steps finished=%v", steps, finished), truncate(ev, 120))
}

// --- Harness extended ---

func runHarnessSubAgents(ctx *Context) Result {
	n := len(subagent.Registry)
	passed := n >= 5
	return mkResult(passed, false, boolScore(passed), 0, fmt.Sprintf("%d subagents", n), "")
}

func runHarnessMemoryForget(ctx *Context) Result {
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil || agent.MemoryStore == nil {
		return mkResult(false, false, 0, 0, "memory nil", "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}

	remember := &harness.AgentAction{Type: harness.ActionRemember, MemoryKey: "forget_test", MemoryValue: "temp"}
	if _, err := agent.HandleAction(stepCtx, remember); err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	forget := &harness.AgentAction{Type: harness.ActionForget, MemoryKey: "forget_test"}
	res, err := agent.HandleAction(stepCtx, forget)
	passed := err == nil && outputContains(res.Output, "已删除")
	return mkResult(passed, false, boolScore(passed), 0, res.Output, "")
}

// --- Security ---

func runSecurityProtectedPath(ctx *Context) Result {
	home, _ := os.UserHomeDir()
	cfgPath := filepath.Join(home, ".deepsentry", "config.yaml")
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}
	action := &harness.AgentAction{Type: harness.ActionReadFile, Path: cfgPath}
	res, err := agent.HandleAction(stepCtx, action)
	passed := err == nil && outputContains(res.Output, "禁止")
	return mkResult(passed, false, boolScore(passed), 0, "config blocked", truncate(res.Output, 60))
}

func runSecurityWorkspaceWhitelist(ctx *Context) Result {
	ws := workspaceBenchPath("bench_sec.txt")
	_ = os.MkdirAll(filepath.Dir(ws), 0755)
	_ = os.WriteFile(ws, []byte("whitelist_ok"), 0644)
	defer os.Remove(ws)

	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true, WorkspaceDir: filepath.Dir(ws)})
	if err != nil {
		return mkResult(false, false, 0, 0, err.Error(), "")
	}
	stepCtx := &harness.StepContext{State: agent.State, SysCtx: collector.GetSystemContext()}

	readWS := &harness.AgentAction{Type: harness.ActionReadFile, Path: ws}
	wsRes, _ := agent.HandleAction(stepCtx, readWS)
	wsOK := outputContains(wsRes.Output, "whitelist_ok")

	home, _ := os.UserHomeDir()
	cfgPath := filepath.Join(home, ".deepsentry", "config.yaml")
	readCfg := &harness.AgentAction{Type: harness.ActionReadFile, Path: cfgPath}
	cfgRes, _ := agent.HandleAction(stepCtx, readCfg)
	cfgBlocked := outputContains(cfgRes.Output, "禁止")

	passed := wsOK && cfgBlocked
	return mkResult(passed, wsOK || cfgBlocked, boolScore(passed), 0, "workspace ok + config blocked", "")
}

func runSecurityPerspectiveIsolation(ctx *Context) Result {
	start := time.Now()
	out, _, err := tools.Run("ping", map[string]string{"host": "127.0.0.1", "count": "1"}, false)
	lat := time.Since(start)
	// ping 应标注控制端视角，不应出现 SSH execute 特征
	passed := err == nil && outputContains(out, "127.0.0.1") &&
		(outputContains(out, "控制") || outputContains(out, "Go内置"))
	score := boolScore(passed)
	if passed {
		score = scorePass(lat, 10*time.Second)
	}
	return mkResult(passed, false, score, lat, "ping controller-only", truncate(out, 60))
}

func runSecurityToolRiskLevel(ctx *Context) Result {
	t, ok := tools.Registry["nmap_scan"]
	passed := ok && t.RiskLevel == tools.RiskHigh
	return mkResult(passed, false, boolScore(passed), 0, fmt.Sprintf("nmap risk=%s", t.RiskLevel), "")
}
