package analyzer

import (
	"ai-edr/internal/harness/subagent"
	"ai-edr/internal/mcp"
	deepsentrytools "ai-edr/internal/tools"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ToolDefinition OpenAI 兼容 tools schema
type ToolDefinition struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// AgentToolDefinitions 返回 Deep Agent 原生 tool calling schema
func AgentToolDefinitions() []ToolDefinition {
	return AgentToolDefinitionsForContext(0, "")
}

// AgentToolDefinitionsForContext limits schema fan-out for smaller models and
// ranks task-relevant tools first. agent_action and tool_catalog are always
// present, so omitted tools remain discoverable/invokable via the compatibility
// path after tool_catalog describes them.
func AgentToolDefinitionsForContext(limit int, contextText string) []ToolDefinition {
	return agentToolDefinitionsForContext(limit, contextText, nil)
}

// AgentToolDefinitionsForContextWithPinned implements the Runtime v3 deferred
// tool contract. Tools already discovered or successfully invoked remain
// visible even when the per-turn candidate set changes. This cumulative
// behavior prevents a verified schema from falling out of the next request
// after context compaction.
func AgentToolDefinitionsForContextWithPinned(limit int, contextText string, pinned []string) []ToolDefinition {
	return agentToolDefinitionsForContext(limit, contextText, pinned)
}

func agentToolDefinitionsForContext(limit int, contextText string, pinned []string) []ToolDefinition {
	definitions := []ToolDefinition{
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "agent_action",
				Description: "Execute a DeepSentry action that is not already exposed as a direct native function. Never wrap a visible built-in in agent_action; call that native function directly. Prefer action=execute for ordinary shell work. When a Skill catalog entry matches the task, call skill(name) first instead of burying load_skill here.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"thought": map[string]string{"type": "string", "description": "Reasoning"},
						"action": map[string]interface{}{
							"type":        "string",
							"description": "execute|task|load_skill|todo|ask_user|read_file|write_file|edit_file|glob|grep|ls|remember|forget|tool|finish. Do not use upload/download as action; use action=execute with command upload/download if file transfer is needed.",
							"enum":        []string{"execute", "task", "load_skill", "todo", "ask_user", "read_file", "write_file", "edit_file", "glob", "grep", "ls", "remember", "forget", "tool", "finish"},
						},
						"command":        map[string]string{"type": "string", "description": "Native shell command to run. This is the default path for system status, logs, scripts, chmod, crontab/systemd, curl notifications, etc."},
						"task_name":      map[string]string{"type": "string", "description": "Required when action=task and parallel_tasks is not used. Must be one of: " + strings.Join(subagent.Names(), ", ") + ". Never leave empty."},
						"task_prompt":    map[string]string{"type": "string", "description": "Required when action=task and parallel_tasks is not used. Give the sub-agent a concrete standalone task. Never leave empty."},
						"task_max_steps": map[string]string{"type": "integer", "description": "AI-estimated max steps for this sub-agent task. The runtime caps it by user-configured subagent_max_steps."},
						"parallel_tasks": map[string]interface{}{
							"type":        "array",
							"description": "Run multiple independent sub-agents concurrently and return a combined collaboration report. Use this for multiple targets or directions. Each item must include non-empty task_name and task_prompt.",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"task_name":       map[string]string{"type": "string", "description": "Required. Must be a registered sub-agent name; never leave empty."},
									"task_prompt":     map[string]string{"type": "string", "description": "Required. Concrete standalone task for this sub-agent; never leave empty."},
									"target_selector": map[string]string{"type": "string"},
									"task_max_steps":  map[string]string{"type": "integer"},
								},
								"required": []string{"task_name", "task_prompt"},
							},
						},
						"target_selector": map[string]string{"type": "string", "description": "Optional targets selector for multi-server task delegation, e.g. all, prod, ssh, web-01"},
						"target_name":     map[string]string{"type": "string"},
						"target_protocol": map[string]string{"type": "string"},
						"target_host":     map[string]string{"type": "string"},
						"skill_name":      map[string]string{"type": "string"},
						"path":            map[string]string{"type": "string"},
						"content":         map[string]string{"type": "string"},
						"pattern":         map[string]string{"type": "string"},
						"old_string":      map[string]string{"type": "string", "description": "edit_file 要替换的原文。必须包含足够上下文，使匹配唯一；多处相同且都要改时才配合 replace_all。"},
						"new_string":      map[string]string{"type": "string", "description": "edit_file 的替换结果。"},
						"replace_all":     map[string]string{"type": "boolean", "description": "仅当 old_string 的每一处都应替换时设为 true。默认拒绝非唯一匹配。"},
						"offset":          map[string]string{"type": "integer", "description": "read_file 起始行，从 1 开始。大文件返回 next offset，不要整文件重读。"},
						"limit":           map[string]string{"type": "integer", "description": "read_file 最多读取的行数，最大 400。"},
						"glob_pattern":    map[string]string{"type": "string"},
						"memory_key":      map[string]string{"type": "string"},
						"memory_value":    map[string]string{"type": "string"},
						"memory_scope":    map[string]string{"type": "string"},
						"tool_name":       map[string]string{"type": "string", "description": "MCP tool name, or a built-in name only when direct native functions are unavailable. Prefer the separately exposed built-in native functions because their parameters are validated."},
						"tool_args":       map[string]interface{}{"type": "object", "description": "MCP arguments, or documented built-in arguments on compatibility paths.", "additionalProperties": map[string]string{"type": "string"}},
						"question":        map[string]string{"type": "string", "description": "Question to ask the user when required information is missing. Use with action=ask_user."},
						"options":         map[string]interface{}{"type": "array", "description": "Optional short answer choices for action=ask_user.", "items": map[string]string{"type": "string"}},
						"final_report":    map[string]string{"type": "string"},
						"is_finished":     map[string]string{"type": "boolean"},
						"todos": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"id":      map[string]string{"type": "string", "description": "String id, e.g. \"1\". Do not use a JSON number."},
									"content": map[string]string{"type": "string", "description": "Task content. Do not use title/detail instead."},
									"status":  map[string]string{"type": "string", "description": "pending|in_progress|completed"},
								},
							},
						},
					},
					"required": []string{"thought", "action"},
				},
			},
		},
	}

	// Expose every enabled built-in as a real native function. This gives the
	// provider the exact argument names/types/enums instead of asking the model
	// to guess inside agent_action.tool_args.
	definitions = append(definitions, ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        "tool_catalog",
			Description: "Discover a DeepSentry built-in only when no currently exposed specialized function matches. Never repeat the same catalog search; after discovery invoke the returned exact tool instead of searching again.",
			Parameters:  deepsentrytools.JSONSchema("tool_catalog"),
		},
	})
	definitions = append(definitions, ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        "skill",
			Description: "Load a Skill playbook before acting when the task matches a catalog name/description (B站/播放, ZIP/伪加密, FOFA/测绘, etc.). Call this first. If 【已加载 Skills】 already contains it, follow that playbook; do not probe pwd/ls or open extra tabs.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]string{"type": "string", "description": "Exact Skill name from the catalog, e.g. bilibili-play, zipcracker, fofamap"},
				},
				"required": []string{"name"},
			},
		},
	})
	mcpTools := selectMCPToolsForContext(mcp.Global().ListTools(), limit, contextText, pinned)
	builtInLimit := limit
	if limit > 0 && len(mcpTools) > 0 {
		builtInLimit = limit - len(mcpTools)
		if builtInLimit < 1 {
			builtInLimit = 1
			mcpTools = mcpTools[:maxAnalyzerInt(0, limit-builtInLimit)]
		}
	}
	names := selectNativeToolNamesWithPinned(deepsentrytools.ListNames(), builtInLimit, contextText, pinned)
	for _, name := range names {
		tool, ok := deepsentrytools.Get(name)
		if !ok {
			continue
		}
		description := fmt.Sprintf("DeepSentry built-in [%s, %s]. %s", tool.Category, tool.Perspective, tool.Description)
		if help := deepsentrytools.FormatToolHelp(name); help != "" {
			description += "\n" + truncateToolDescription(help, 800)
		}
		if aliases := deepsentrytools.SearchAliases(name); len(aliases) > 0 {
			description += "\nTypical intents / 常见意图: " + strings.Join(aliases, ", ")
		}
		description = truncateToolDescription(description, 1020)
		definitions = append(definitions, ToolDefinition{
			Type: "function",
			Function: FunctionDef{
				Name:        name,
				Description: description,
				Parameters:  deepsentrytools.JSONSchema(name),
			},
		})
	}
	// MCP tools use the server-provided schema in every profile. Limited models
	// receive a context-ranked subset (especially the current HawkEye workflow)
	// instead of being forced to guess string arguments through agent_action.
	for _, tool := range mcpTools {
		parameters := tool.InputSchema
		if parameters == nil {
			parameters = map[string]interface{}{"type": "object", "additionalProperties": true}
		}
		description := truncateToolDescription(tool.Description, 800)
		if description == "" {
			description = "MCP tool exposed by server " + tool.Server
		}
		if mcp.IsHawkEyeTool(&tool, mcpTools) {
			if overlay := mcp.HawkEyeToolDescriptionOverlay(tool.OriginalName); overlay != "" {
				description = truncateToolDescription(tool.Description, 420) + " " + overlay
			}
		}
		if mcp.IsFofaMapTool(&tool, mcpTools) {
			if overlay := mcp.FofaMapToolDescriptionOverlay(tool.OriginalName); overlay != "" {
				description = truncateToolDescription(tool.Description, 420) + " " + overlay
			}
		}
		definitions = append(definitions, ToolDefinition{
			Type: "function",
			Function: FunctionDef{
				Name:        tool.Name,
				Description: description,
				Parameters:  parameters,
			},
		})
	}
	return definitions
}

// alwaysVisibleNativeSchemaCount is agent_action + tool_catalog + skill.
const alwaysVisibleNativeSchemaCount = 3

func alwaysVisibleNativeTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "agent_action", "tool_catalog", "skill":
		return true
	default:
		return false
	}
}

func selectMCPToolsForContext(tools []mcp.ExternalTool, limit int, contextText string, pinned []string) []mcp.ExternalTool {
	if limit <= 0 {
		return tools
	}
	maxMCP := limit / 2
	query := strings.ToLower(contextText)
	if hawkEyeTaskIntent(query) || fofaMapTaskIntent(query) {
		for i := range tools {
			if mcp.IsHawkEyeTool(&tools[i], tools) || mcp.IsFofaMapTool(&tools[i], tools) {
				// HawkEye and FofaMap workflows commonly need a 4-6 tool chain.
				// Give their exact MCP schemas more room while retaining at least
				// two DeepSentry built-ins.
				maxMCP = (limit * 2) / 3
				if maxMCP > limit-2 {
					maxMCP = limit - 2
				}
				break
			}
		}
	}
	if maxMCP < 1 {
		return nil
	}
	pinnedSet := make(map[string]bool, len(pinned))
	for _, name := range pinned {
		pinnedSet[strings.TrimSpace(name)] = true
	}
	type scoredTool struct {
		tool  mcp.ExternalTool
		score int
	}
	scored := make([]scoredTool, 0, len(tools))
	for _, tool := range tools {
		score := 0
		if pinnedSet[tool.Name] {
			score += 10000
		}
		original := strings.ToLower(strings.TrimSpace(tool.OriginalName))
		if original == "" {
			original = strings.ToLower(tool.Name)
		}
		if strings.Contains(query, strings.ToLower(tool.Name)) || strings.Contains(query, original) {
			score += 2000
		}
		if mcp.IsHawkEyeTool(&tool, tools) {
			score += hawkEyeMCPContextScore(original, query)
		} else if mcp.IsFofaMapTool(&tool, tools) {
			score += fofaMapMCPContextScore(original, query)
		} else {
			for _, token := range strings.FieldsFunc(strings.ReplaceAll(original, "_", " "), func(r rune) bool { return r == '-' || r == ' ' }) {
				if len(token) >= 4 && strings.Contains(query, token) {
					score += 40
				}
			}
		}
		if score > 0 {
			scored = append(scored, scoredTool{tool: tool, score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].tool.Name < scored[j].tool.Name
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > maxMCP {
		scored = scored[:maxMCP]
	}
	out := make([]mcp.ExternalTool, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.tool)
	}
	return out
}

func fofaMapTaskIntent(query string) bool {
	for _, value := range []string{
		"fofamap", "fofa", "资产测绘", "网络空间测绘", "公网资产", "互联网资产", "暴露面",
		"app=", "icon_hash", "host profile", "host_profile", "nuclei", "指纹规则", "fofa 查询",
		"测绘", "空间搜索", "致远", "vpn", "favicon", "网站发现", "icon hash", "资产发现",
	} {
		if strings.Contains(query, value) {
			return true
		}
	}
	return false
}

func fofaMapMCPContextScore(name, query string) int {
	if !fofaMapTaskIntent(query) {
		return 0
	}
	containsAny := func(values ...string) bool {
		for _, value := range values {
			if strings.Contains(query, value) {
				return true
			}
		}
		return false
	}
	score := 0
	add := func(points int, names ...string) {
		for _, candidate := range names {
			if name == candidate {
				score += points
				return
			}
		}
	}
	// Account, fields and validation are the safe preflight for every search.
	add(760, "fofa_account", "fofa_fields", "fofa_validate_query")
	if containsAny("产品", "oa", "vpn", "规则", "指纹", "app=", "rule", "fingerprint") {
		add(1000, "fofa_rules")
		add(620, "fofa_syntax")
	}
	if containsAny("搜索", "查询", "资产", "测绘", "暴露面", "search", "query", "fofa") {
		add(920, "fofa_search")
		add(700, "fofa_search_next")
	}
	if containsAny("下一页", "翻页", "cursor", "继续查询", "next") {
		add(1100, "fofa_search_next")
	}
	if containsAny("图标", "icon", "favicon") {
		add(1050, "fofa_icon_search")
	}
	if containsAny("主机画像", "host profile", "host_profile", "聚合", "统计", "stats") {
		add(980, "fofa_host_profile", "fofa_stats")
	}
	if containsAny("导出", "保存结果", "大批量", "export", "csv", "jsonl") {
		add(1100, "fofa_export")
	}
	if containsAny("自然语言", "自动分析", "agent", "广泛调研") {
		add(900, "fofa_agent_run")
	}
	// Active tools only enter the preferred set on explicit scan intent.
	if containsAny("主动扫描", "漏洞扫描", "nuclei", "执行扫描", "scan") {
		add(1000, "nuclei_plan")
		add(820, "nuclei_execute")
	}
	if containsAny("任务状态", "job", "进度") {
		add(820, "fofa_job_status")
	}
	return score
}

func hawkEyeTaskIntent(query string) bool {
	for _, value := range []string{
		"hawkeye", "鹰眼", "hx0", "浏览器", "页面", "网页", "browser", "bilibili", "b站",
		"抓包", "流量", "请求头", "请求体", "capture", "traffic", "request", "header", "body",
		"拦截", "改包", "intercept", "release", "drop", "fuzz", "重放", "replay", "截图", "captcha",
		"播放", "倍速", "全屏", "视频", "watch", "play", "fullscreen",
		"证书", "tls", "验证码", "site:", "filetype:",
	} {
		if strings.Contains(query, value) {
			return true
		}
	}
	return false
}

func hawkEyeMCPContextScore(name, query string) int {
	containsAny := func(values ...string) bool {
		for _, value := range values {
			if strings.Contains(query, value) {
				return true
			}
		}
		return false
	}
	browserIntent := containsAny("浏览器", "页面", "网页", "打开", "访问", "点击", "输入", "browser", "website", "navigate", "click", "b站", "bilibili", "播放", "视频", "watch", "play")
	captureIntent := containsAny("抓包", "流量", "请求头", "请求体", "响应", "capture", "traffic", "request", "header", "body")
	interceptIntent := containsAny("拦截", "改包", "放行", "丢弃", "intercept", "release", "drop")
	securityIntent := containsAny("安全", "渗透", "漏洞", "审计", "ctf", "fuzz", "重放", "敏感", "暗链", "security", "pentest", "replay")
	visualIntent := containsAny("截图", "图片", "视觉", "screenshot", "image", "visual")
	captchaIntent := containsAny("验证码", "captcha", "滑块", "checkcode")
	researchIntent := containsAny("搜索", "调研", "资料", "research", "search", "site:", "filetype:")
	tlsIntent := containsAny("证书", "tls", "不安全", "certificate", "hostname mismatch")
	activationIntent := containsAny("全屏", "真实手势", "真鼠标", "剪贴板", "useractivation", "fullscreen", "trusted")
	playbackIntent := containsAny("播放", "倍速", "2倍", "2x", "playback", "speed", "视频", "b站", "bilibili")
	fileIntent := containsAny("上传", "下载", "文件", "upload", "download", "file")
	scriptIntent := containsAny("脚本", "注入", "编解码", "javascript", "script", "evaluate", "codec")
	findingIntent := containsAny("报告", "发现", "敏感", "暗链", "finding", "sensitive", "darklink")
	if !(browserIntent || captureIntent || interceptIntent || securityIntent || visualIntent || captchaIntent || researchIntent || tlsIntent || activationIntent || playbackIntent || fileIntent || scriptIntent || findingIntent || containsAny("hawkeye", "鹰眼", "hx0")) {
		return 0
	}
	score := 0
	add := func(points int, names ...string) {
		for _, candidate := range names {
			if name == candidate {
				score += points
				return
			}
		}
	}
	if browserIntent {
		add(650, "browser_tabs", "browser_navigate", "browser_snapshot", "browser_find", "browser_read_text")
		add(470, "browser_click", "browser_type", "browser_press_key", "browser_wait_for", "browser_scroll")
		add(260, "browser_hover", "browser_select_option", "browser_fill_form", "browser_handle_dialog", "browser_reload")
	}
	if captureIntent {
		add(760, "hawkeye_capture_state", "hawkeye_capture_start", "hawkeye_capture_history", "hawkeye_capture_inspect", "hawkeye_request_get")
		add(420, "hawkeye_capture_stop")
		add(350, "browser_tabs", "browser_snapshot")
	}
	if interceptIntent {
		add(800, "hawkeye_scope", "hawkeye_intercept")
		add(450, "hawkeye_capture_state", "hawkeye_capture_history", "hawkeye_request_get")
	}
	if securityIntent {
		add(700, "hawkeye_scope", "hawkeye_request_get", "hawkeye_request_mutate", "hawkeye_request_replay", "hawkeye_response_compare")
		add(550, "hawkeye_capture_history", "hawkeye_capture_inspect", "hawkeye_sensitive_scan", "hawkeye_darklink_scan", "hawkeye_findings", "hawkeye_fuzz_run", "browser_security")
	}
	if visualIntent {
		add(900, "browser_screenshot", "browser_snapshot")
	}
	if captchaIntent {
		add(1100, "browser_captcha_assist", "browser_snapshot")
		add(400, "browser_type")
	}
	if tlsIntent {
		add(1100, "browser_security", "browser_snapshot")
	}
	if researchIntent {
		add(800, "browser_research", "browser_search", "browser_fetch", "browser_read_text")
	}
	if activationIntent {
		add(1000, "browser_tabs", "browser_snapshot", "browser_click", "browser_press_key")
		add(380, "hawkeye_evaluate") // verification only; workflow prompt forbids synthetic-event fallback
	}
	if playbackIntent {
		add(1100, "browser_navigate", "browser_snapshot", "browser_press_key", "hawkeye_evaluate")
		add(700, "browser_click", "browser_find")
		add(400, "browser_tabs", "browser_select_option")
	}
	if fileIntent {
		add(850, "browser_file_upload", "browser_download_file", "browser_screenshot")
	}
	if scriptIntent {
		add(820, "hawkeye_codec", "hawkeye_script_list", "hawkeye_script_run", "hawkeye_script_upsert", "hawkeye_evaluate")
	}
	if findingIntent {
		add(820, "hawkeye_findings", "hawkeye_sensitive_scan", "hawkeye_darklink_scan", "hawkeye_request_get", "hawkeye_capture_inspect")
	}
	if containsAny("hawkeye", "鹰眼", "hx0") && !(browserIntent || captureIntent || interceptIntent || securityIntent || visualIntent || researchIntent) {
		add(300, "browser_tabs", "browser_snapshot", "hawkeye_capture_state")
	}
	return score
}

func selectNativeToolNamesWithPinned(names []string, limit int, contextText string, pinned []string) []string {
	sort.Strings(names)
	if limit <= 0 || len(names) <= limit {
		return names
	}
	query := strings.ToLower(contextText)
	searchQuery := deepsentrytools.NewSearchQuery(query)
	selected := make([]string, 0, limit)
	seen := map[string]bool{}
	// Previously selected tools have the strongest claim on the bounded native
	// schema budget. Ignore stale/disabled names and preserve deterministic
	// order so provider prompt caching remains as stable as possible.
	pinned = append([]string(nil), pinned...)
	sort.Strings(pinned)
	for _, name := range pinned {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || !containsToolName(names, name) || len(selected) >= limit {
			continue
		}
		selected = append(selected, name)
		seen[name] = true
	}
	type scoredTool struct {
		name  string
		score int
	}
	var scored []scoredTool
	for _, name := range names {
		if seen[name] {
			continue
		}
		if name == "config_manage" && !explicitDeepSentryConfigIntent(query) {
			continue
		}
		tool, ok := deepsentrytools.Get(name)
		if !ok {
			continue
		}
		score := deepsentrytools.SearchRelevanceForQuery(tool, searchQuery)
		scored = append(scored, scoredTool{name: name, score: score})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].name < scored[j].name
		}
		return scored[i].score > scored[j].score
	})
	for _, item := range scored {
		if len(selected) >= limit {
			break
		}
		// Runtime v3 uses true deferred exposure: zero-relevance tools remain
		// hidden and are available through tool_catalog. Alphabetically filling
		// the remaining budget made config_manage and other unrelated tools look
		// like recommendations, which measurably reduced long-tail selection.
		if item.score <= 0 {
			continue
		}
		selected = append(selected, item.name)
	}
	return selected
}

func explicitDeepSentryConfigIntent(query string) bool {
	query = strings.ToLower(query)
	for _, marker := range []string{
		"deepsentry", "config.yaml", "agent_runtime", "添加目标", "新增目标", "修改目标",
		"fleet目标", "mcp server", "mcp_server", "skill来源", "启用skill", "禁用skill",
		"enable skill", "disable skill", "show config", "config status", "manage config", "配置状态",
	} {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return false
}

func containsToolName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func truncateToolDescription(text string, maxRunes int) string {
	runes := []rune(text)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

// ParseToolCallResponse 从 native tool_calls 解析 AgentResponse
func ParseToolCallResponse(toolCallArgs string) (AgentResponse, error) {
	var compat CompatibilityResponse
	if err := json.Unmarshal([]byte(repairModelJSONEscapes(toolCallArgs)), &compat); err != nil {
		return AgentResponse{}, err
	}
	resp := AgentResponse{
		Thought:        compat.Thought,
		Command:        compat.Command,
		RiskLevel:      compat.RiskLevel,
		IsFinished:     compat.IsFinished,
		Question:       compat.Question,
		Options:        compat.Options,
		Action:         compat.Action,
		TaskName:       compat.TaskName,
		TaskPrompt:     compat.TaskPrompt,
		TaskMaxSteps:   compat.TaskMaxSteps,
		ParallelTasks:  compat.ParallelTasks,
		TargetSelector: compat.TargetSelector,
		TargetName:     compat.TargetName,
		TargetProtocol: compat.TargetProtocol,
		TargetHost:     compat.TargetHost,
		SkillName:      compat.SkillName,
		Path:           compat.Path,
		Content:        compat.Content,
		Pattern:        compat.Pattern,
		Todos:          compat.Todos,
		MemoryKey:      compat.MemoryKey,
		MemoryValue:    compat.MemoryValue,
		MemoryScope:    compat.MemoryScope,
		ToolName:       compat.ToolName,
		ToolArgs:       parseToolArgs(compat.ToolArgs),
		OldString:      compat.OldString,
		NewString:      compat.NewString,
		ReplaceAll:     compat.ReplaceAll,
		GlobPattern:    compat.GlobPattern,
		Offset:         compat.Offset,
		Limit:          compat.Limit,
	}
	if v, ok := compat.FinalReport.(string); ok {
		resp.FinalReport = v
	}
	// edit_file / glob fields via json unmarshal into compat — copy manually from raw if needed
	return resp, nil
}

// ParseNamedToolCall maps a directly exposed built-in native function back to
// the existing harness action protocol so risk review and approvals remain in
// one place.
func ParseNamedToolCall(name, toolCallArgs string) (AgentResponse, error) {
	if name == "" || name == "agent_action" {
		return ParseToolCallResponse(toolCallArgs)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(repairModelJSONEscapes(toolCallArgs)), &raw); err != nil {
		return AgentResponse{}, err
	}
	if name == "skill" || name == "load_skill" {
		args := parseToolArgs(raw)
		skillName := strings.TrimSpace(args["name"])
		if skillName == "" {
			skillName = strings.TrimSpace(args["skill_name"])
		}
		return AgentResponse{
			Thought:   "加载 Skill " + skillName,
			Action:    "load_skill",
			SkillName: skillName,
		}, nil
	}
	if name != "tool_catalog" {
		if _, ok := deepsentrytools.Get(name); !ok {
			if _, _, mcpOK := mcp.Global().Get(name); !mcpOK {
				return AgentResponse{}, fmt.Errorf("unknown native tool: %s", name)
			}
			return AgentResponse{
				Thought:  "调用已连接的 MCP 工具 " + name,
				Action:   "tool",
				ToolName: name,
				ToolArgs: parseToolArgs(raw),
			}, nil
		}
	}
	return AgentResponse{
		Thought:  "调用已校验的内置工具 " + name,
		Action:   "tool",
		ToolName: name,
		ToolArgs: parseToolArgs(raw),
	}, nil
}
