package mcp

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/ui"
)

// ExternalTool MCP 发现的外部工具
type ExternalTool struct {
	Name         string
	OriginalName string
	Description  string
	Server       string
	InputSchema  map[string]interface{}
	Annotations  ToolAnnotations
}

// ToolAnnotations keeps the safety hints advertised by an MCP server. These
// are hints rather than trust decisions; DeepSentry only uses them together
// with a locally known server profile (for example HawkEye's versioned tool
// contract) and otherwise keeps the conservative external-tool policy.
type ToolAnnotations struct {
	Present          bool
	ReadOnlyHint     bool
	DestructiveHint  bool
	DestructiveKnown bool
	IdempotentHint   bool
	OpenWorldHint    bool
	OpenWorldKnown   bool
}

// ToolHandler 外部工具执行回调
type ToolHandler func(args map[string]string) (string, error)

// Registry MCP 工具注册表（对标 deepagents MCP 扩展）
type Registry struct {
	mu        sync.RWMutex
	tools     map[string]*ExternalTool
	handlers  map[string]ToolHandler
	aliases   map[string]string
	ambiguous map[string]bool
}

var globalRegistry = NewRegistry()

// NewRegistry creates an isolated MCP tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:     make(map[string]*ExternalTool),
		handlers:  make(map[string]ToolHandler),
		aliases:   make(map[string]string),
		ambiguous: make(map[string]bool),
	}
}

// stdioConnection is retained as a protocol-compatibility adapter for older
// MCP servers and its focused compatibility tests. Production connections use
// the official SDK path in sdk_client.go.
type stdioConnection struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	reader      *bufio.Reader
	serverName  string
	fingerprint string
	nextID      int
}

var stdioConnections = struct {
	sync.Mutex
	byName map[string]*stdioConnection
}{byName: make(map[string]*stdioConnection)}

// Global 返回全局 MCP 注册表
func Global() *Registry {
	return globalRegistry
}

// RegisterHandler 注册 MCP 工具处理器
func (r *Registry) RegisterHandler(name string, tool ExternalTool, handler ToolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = &tool
	r.handlers[name] = handler
}

func (r *Registry) registerServerHandler(server string, tool ExternalTool, handler ToolHandler) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registerServerHandlerLocked(server, tool, handler)
}

func (r *Registry) registerServerHandlerLocked(server string, tool ExternalTool, handler ToolHandler) string {
	original := strings.TrimSpace(tool.OriginalName)
	if original == "" {
		original = strings.TrimSpace(tool.Name)
	}
	canonical := canonicalToolName(server, original)
	tool.Name = canonical
	tool.OriginalName = original
	r.tools[canonical] = &tool
	r.handlers[canonical] = handler
	if previous, exists := r.aliases[original]; !exists && !r.ambiguous[original] {
		r.aliases[original] = canonical
	} else if exists && previous != canonical {
		delete(r.aliases, original)
		r.ambiguous[original] = true
	}
	return canonical
}

type serverToolHandler struct {
	tool    ExternalTool
	handler ToolHandler
}

// replaceServerHandlers swaps one server's complete tool set under one lock so
// list_changed refreshes never expose a half-empty registry to concurrent calls.
func (r *Registry) replaceServerHandlers(server string, discovered []serverToolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, tool := range r.tools {
		if tool != nil && tool.Server == server {
			delete(r.tools, name)
			delete(r.handlers, name)
		}
	}
	for _, item := range discovered {
		original := strings.TrimSpace(item.tool.OriginalName)
		if original == "" {
			original = strings.TrimSpace(item.tool.Name)
		}
		canonical := canonicalToolName(server, original)
		item.tool.Name = canonical
		item.tool.OriginalName = original
		r.tools[canonical] = &item.tool
		r.handlers[canonical] = item.handler
	}
	r.rebuildAliasesLocked()
}

func (r *Registry) unregisterServer(server string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, tool := range r.tools {
		if tool != nil && tool.Server == server {
			delete(r.tools, name)
			delete(r.handlers, name)
		}
	}
	r.rebuildAliasesLocked()
}

func (r *Registry) rebuildAliasesLocked() {
	r.aliases = make(map[string]string)
	r.ambiguous = make(map[string]bool)
	for canonical, tool := range r.tools {
		if tool == nil || tool.Server == "" {
			continue
		}
		original := tool.OriginalName
		if strings.TrimSpace(original) == "" {
			original = tool.Name
		}
		if previous, exists := r.aliases[original]; !exists && !r.ambiguous[original] {
			r.aliases[original] = canonical
		} else if exists && previous != canonical {
			delete(r.aliases, original)
			r.ambiguous[original] = true
		}
	}
}

// Get 获取 MCP 工具
func (r *Registry) Get(name string) (*ExternalTool, ToolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if canonical, ok := r.aliases[name]; ok {
		name = canonical
	}
	t, ok := r.tools[name]
	h := r.handlers[name]
	return t, h, ok && h != nil
}

func canonicalToolName(server, tool string) string {
	clean := func(value string) string {
		original := strings.TrimSpace(value)
		var b strings.Builder
		changed := false
		for _, r := range original {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
				changed = true
			}
		}
		value = strings.Trim(b.String(), "_")
		if value == "" {
			value = "tool"
			changed = true
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(original)))[:8]
		if changed || len(value) > 30 {
			if len(value) > 21 {
				value = value[:21]
			}
			value = strings.TrimRight(value, "_-") + "_" + hash
		}
		return value
	}
	server, tool = clean(server), clean(tool)
	if server == "" {
		return tool
	}
	return server + "__" + tool
}

// ListTools returns immutable copies for native tool-schema generation.
func (r *Registry) ListTools() []ExternalTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ExternalTool, 0, len(r.tools))
	for _, tool := range r.tools {
		if tool != nil {
			out = append(out, *tool)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListNames 列出已注册 MCP 工具名
func (r *Registry) ListNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// FormatToolDetail returns the live schema for a discovered MCP tool. The
// canonical name is shown even when the caller used an unambiguous alias.
func (r *Registry) FormatToolDetail(name string) (string, bool) {
	tool, _, ok := r.Get(strings.TrimPrefix(strings.TrimSpace(name), "mcp:"))
	if !ok || tool == nil {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【MCP 工具 %s】\n服务: %s\n说明: %s\n", tool.Name, tool.Server, tool.Description)
	fmt.Fprintf(&b, "调用: {\"action\":\"tool\",\"tool_name\":\"mcp:%s\",\"tool_args\":{...}}\n", tool.Name)
	if len(tool.InputSchema) > 0 {
		schema, err := json.MarshalIndent(tool.InputSchema, "", "  ")
		if err == nil {
			b.WriteString("参数 JSON Schema:\n")
			b.Write(schema)
			b.WriteByte('\n')
		}
	}
	b.WriteString("tool_args 的值用字符串表达；object/array 参数传 JSON 字符串。服务端参数仍会按上述 schema 校验。\n")
	return b.String(), true
}

// SearchToolSummaries exposes MCP tools that were omitted from the bounded
// system prompt, without injecting every server schema on each model turn.
func (r *Registry) SearchToolSummaries(query string, limit int) string {
	if limit <= 0 {
		limit = 12
	}
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	type hit struct {
		tool  ExternalTool
		score int
	}
	var hits []hit
	for _, tool := range r.ListTools() {
		name := strings.ToLower(tool.Name + " " + tool.OriginalName)
		other := strings.ToLower(tool.Server + " " + tool.Description)
		score := 0
		matched := true
		for _, term := range terms {
			switch {
			case strings.Contains(name, term):
				score += 2
			case strings.Contains(other, term):
				score++
			default:
				matched = false
			}
		}
		if matched {
			hits = append(hits, hit{tool: tool, score: score})
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].tool.Name < hits[j].tool.Name
		}
		return hits[i].score > hits[j].score
	})
	var b strings.Builder
	b.WriteString("【MCP 工具候选】精确参数请再用 tool_catalog(name=工具名) 查询。\n")
	for i, item := range hits {
		if i >= limit {
			fmt.Fprintf(&b, "…另有 %d 个匹配工具。\n", len(hits)-limit)
			break
		}
		fmt.Fprintf(&b, "- mcp:%s (%s): %s\n", item.tool.Name, item.tool.Server, truncateMCPText(item.tool.Description, 200))
	}
	return b.String()
}

// FormatPrompt 生成 MCP 工具 prompt 片段
func (r *Registry) FormatPrompt() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n【MCP 扩展工具】\n")
	b.WriteString("格式: {\"action\":\"tool\",\"tool_name\":\"mcp:<name>\",\"tool_args\":{...}}\n\n")
	b.WriteString("需要完整参数时调用 tool_catalog(name=精确 MCP 名称)；工具未列出时用 tool_catalog(category=mcp, query=关键词) 搜索。\n")
	hasHawkEye := false
	hasFofaMap := false
	names := make([]string, 0, len(r.tools))
	for name, tool := range r.tools {
		names = append(names, name)
		if isHawkEyeExternalToolInSet(tool, r.tools) {
			hasHawkEye = true
		}
		if isFofaMapExternalToolInSet(tool, r.tools) {
			hasFofaMap = true
		}
	}
	if hasHawkEye {
		b.WriteString(hawkEyeWorkflowPrompt())
	}
	if hasFofaMap {
		b.WriteString(fofaMapWorkflowPrompt())
	}
	sort.Slice(names, func(i, j int) bool {
		left := maxMCPProfilePriority(hawkEyeToolPromptPriorityInSet(r.tools[names[i]], r.tools), fofaMapToolPromptPriorityInSet(r.tools[names[i]], r.tools))
		right := maxMCPProfilePriority(hawkEyeToolPromptPriorityInSet(r.tools[names[j]], r.tools), fofaMapToolPromptPriorityInSet(r.tools[names[j]], r.tools))
		if left == right {
			return names[i] < names[j]
		}
		return left > right
	})
	const promptBudget = 12000
	omitted := 0
	for _, name := range names {
		t := r.tools[name]
		line := fmt.Sprintf("- **mcp:%s** (%s): %s\n", name, t.Server, truncateMCPText(t.Description, 600))
		if b.Len()+len(line) > promptBudget {
			omitted++
			continue
		}
		b.WriteString(line)
	}
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("… 另有 %d 个 MCP 工具因 prompt 预算未列出。\n", omitted))
	}
	b.WriteString(FormatServerInstructions())
	return b.String()
}

func maxMCPProfilePriority(values ...int) int {
	best := 0
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
}

func isHawkEyeExternalTool(tool *ExternalTool) bool {
	return IsHawkEyeTool(tool, nil)
}

func hawkEyeWorkflowPrompt() string {
	return `【HawkEye MCP 1.0.12 深度适配工作流】
HawkEye 已出现在本会话工具列表中，就等于已经连通。禁止再用 execute/ls/md5/lsof/config_manage 做安装、端口或文件自检；直接完成用户任务。扩展弹窗需打开 “HawkEye Browser Automation MCP”，VIP/Pro 才能桥接。
覆盖：本会话禁止用内置 browser_browse/browser_interact 打开、播放或全屏网页。真实浏览器一律走 HawkEye。
- B站/哔哩哔哩/播放/倍速/全屏：第一步必须 load_skill("bilibili-play")，然后严格按该 skill 执行。合集第 N 集用 https://www.bilibili.com/video/BV...?p=N，不要靠连点播放器碰运气。
- 打开页面：已知 URL 用 browser_navigate。内网/实验环境自签证书会自动绕过拦截页，公网证书告警用 browser_security 诊断。搜索结果里出现 BV/番剧链接后，再 navigate 到完整 https URL。整次任务最多 browser_tabs action=new 一次；list 不绑定，已打开的页用 select。
- 交互顺序：snapshot（默认视口 compact 树，禁止 read_file artifact）→ 高层工具 → snapshot(diff=true)。精确点击必须同时传 text=无障碍名称 和 ref，点最小可交互节点；element 只是说明。可用 stableId。清晰度等下拉才用 browser_select_option，value 传可见项（如 1080P）；不要点装饰箭头后满页盲点。
- 播放/倍速/全屏（对标成功会话）：hawkeye_evaluate 读 video 的 paused/currentTime/playbackRate。倍速主路径是一次 evaluate 设置 video.playbackRate=2，不要点「倍速」菜单。全屏主路径是聚焦播放器后 browser_press_key key=f inputMode=trusted；用 document.fullscreenElement 验证（B站常见 bpx-player-container）。trusted 失败时不得用 JS dispatchEvent 伪装成功，也不得连点「全屏」按钮。
- 黑屏截图不是没在播：B站全屏走 GPU 层，browser_screenshot 常黑。currentTime 递增才算在播。需要画面时用 hawkeye_evaluate 对 video 做 canvas.drawImage，不要反复截图。
- 真实用户手势：clickMode=trusted / inputMode=trusted。Chrome 走 CDP trusted input，Firefox 走 native-input relay。
- 大页面：优先 browser_find/read_text。complete=false 时把 context.next_page_token 原样作为 page_token 单独再调同一工具；禁止传数字 cursor，禁止带着 url/query/max_elements 一起翻页。2 分钟内有效。不要对 HawkEye snapshot artifact 做 execute/grep。
- 公开检索：browser_search/research 必须用搜索运算符（"精确短语"、site:、filetype:、intitle:、-排除、2020..2025），不要只丢关键词。首页嘈杂就改写再搜。
- 抓包：capture_state/start → 触发请求 → history → inspect/request_get。GET 无 body 不是漏抓。browser_fetch 默认也会写入抓包历史。
- 主动验证：先 scope get，仅授权 host 上 mutate → replay/compare；fuzz 需 fuzzingEnabled。
- 拦截必须收尾：enable → queue → release/drop → disable。
- 验证码：captcha_assist 先 analyze 看放大图，只认大号深色字；禁止 evaluate/下载/点击验证码图。solve 带 authorized=true。第三方反机器人挑战请用户手动完成。
- 不要并行修改同一标签、抓包或拦截状态。evaluate 用于校验播放状态、设 playbackRate、canvas 抓帧；不能代替 trusted 全屏手势，也不能用来解验证码。

`
}

func fofaMapWorkflowPrompt() string {
	return `【FofaMap MCP v2.0.1 深度适配工作流】
FofaMap 已出现在本会话工具列表中，就等于已经连通。第一步 load_skill("fofamap")，然后调用 fofa_account / fofa_search 等 MCP 工具。禁止 execute scripts/fofa_recon.py（那是 ClawHub Skill 包装，不是 MCP 2.0.1），禁止 curl/wget 打 FOFA API 或自检密钥。
- 查询前置：先 fofa_account + fofa_fields 确认账户、vip_level、字段权限和额度。注册用户没有 Host API；个人/教育账户没有 stats API，不要对这类账户调用 fofa_host_profile/fofa_stats。
- 产品/OA/VPN/中间件/摄像头/CMS：检索前先 fofa_rules（空 keyword 可列出内置目录，不耗额度），原样使用返回的 query/app=；不要凭模型记忆编造 FOFA 产品名。
- 安全查询：每个消耗额度的查询先 fofa_validate_query；验证通过后再 fofa_search。继续翻页时把 next_cursor 原样交给 fofa_search_next 的 cursor 参数（DeepSentry 也会把 next_cursor 别名成 cursor），不得解析或自行生成 cursor。
- 嵌套参数：fofa_export / nuclei_plan 的 schema 需要 request 对象。可以直接传 request JSON，也可以扁平传 query/targets，DeepSentry 会自动包进 request。fields 可传 JSON 数组或逗号分隔。
- 结果处理：小批量聚合用 fofa_stats/fofa_host_profile/icon_search；大结果集使用 fofa_export，记录返回的本地绝对路径和条数，用 fofa_job_status 或 Resource fofamap://jobs/{id} 轮询。宽泛自然语言资产测绘用 fofa_agent_run，组织官网结果保留 website_candidates 的 corroborated/observed/candidate，不要拍板成已确认归属。
- 主动扫描必须显式授权：仅当用户明确要求扫描其有权测试的目标时，先 nuclei_plan 展示范围、模板和预计影响；获得用户确认后，才把计划返回的一次性令牌原样传给 nuclei_execute。不得跳过 plan，不得复用或伪造令牌。
- 查询结果仅代表 FOFA 数据快照，不能把网络空间测绘命中直接表述为已验证漏洞；需要区分暴露面线索、被动证据和主动验证结论。认证/额度/超时错误不能当成空结果。

`
}

func firstNonEmptyMCP(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// Run 执行 MCP 工具
func (r *Registry) Run(name string, args map[string]string) (string, error) {
	_, handler, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("未注册 MCP 工具: %s", name)
	}
	return handler(args)
}

// ServerConfig MCP 服务器启动配置
type ServerConfig struct {
	Name              string            `json:"name" yaml:"name"`
	Type              string            `json:"type" yaml:"type"`
	Command           string            `json:"command" yaml:"command"`
	Args              []string          `json:"args" yaml:"args"`
	Env               map[string]string `json:"env" yaml:"env"`
	CWD               string            `json:"cwd" yaml:"cwd"`
	URL               string            `json:"url" yaml:"url"`
	Headers           map[string]string `json:"headers" yaml:"headers"`
	BearerTokenEnvVar string            `json:"bearer_token_env_var" yaml:"bearer_token_env_var"`
	EnabledTools      []string          `json:"enabled_tools" yaml:"enabled_tools"`
	DisabledTools     []string          `json:"disabled_tools" yaml:"disabled_tools"`
	StartupTimeoutSec int               `json:"startup_timeout_sec" yaml:"startup_timeout_sec"`
	ToolTimeoutSec    int               `json:"tool_timeout_sec" yaml:"tool_timeout_sec"`
	Required          bool              `json:"required" yaml:"required"`
	Disabled          bool              `json:"disabled" yaml:"disabled"`
}

// ConnectStdio 连接 stdio MCP 服务器并发现 tools（简化 JSON-RPC 2.0）
func ConnectStdio(cfg ServerConfig) error {
	if cfg.Disabled {
		return nil
	}
	if cfg.Type != "" && cfg.Type != "stdio" {
		return fmt.Errorf("MCP server %s 类型 %s 暂不支持运行，当前仅支持 stdio", cfg.Name, cfg.Type)
	}
	if cfg.Command == "" {
		return fmt.Errorf("MCP server command 不能为空")
	}
	serverName := strings.TrimSpace(cfg.Name)
	if serverName == "" {
		serverName = cfg.Command
	}
	fingerprintRaw, _ := json.Marshal(cfg)
	fingerprint := string(fingerprintRaw)

	stdioConnections.Lock()
	defer stdioConnections.Unlock()
	if existing := stdioConnections.byName[serverName]; existing != nil {
		if existing.fingerprint == fingerprint {
			return nil
		}
		existing.close()
		delete(stdioConnections.byName, serverName)
		globalRegistry.unregisterServer(serverName)
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = mcpProcessEnvironment(os.Environ(), cfg.Env)
	if strings.TrimSpace(cfg.CWD) != "" {
		cmd.Dir = cfg.CWD
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 MCP 服务器 %s 失败: %w", cfg.Name, err)
	}
	conn := &stdioConnection{
		cmd:         cmd,
		stdin:       stdin,
		reader:      bufio.NewReader(stdout),
		serverName:  serverName,
		fingerprint: fingerprint,
		nextID:      2,
	}
	connected := false
	defer func() {
		if !connected {
			conn.close()
		}
	}()

	// initialize
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]string{"name": "deepsentry", "version": ui.Version},
		},
	}
	if err := writeJSONRPC(stdin, initReq); err != nil {
		return err
	}
	initRaw, err := readJSONRPCResponse(conn.reader, 1, 15*time.Second)
	if err != nil {
		return fmt.Errorf("MCP server %s initialize 失败: %w", serverName, err)
	}
	if err := jsonRPCResponseError(initRaw); err != nil {
		return fmt.Errorf("MCP server %s initialize 失败: %w", serverName, err)
	}
	if err := writeJSONRPC(stdin, map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]interface{}{},
	}); err != nil {
		return err
	}

	// tools/list
	listReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]interface{}{},
	}
	if err := writeJSONRPC(stdin, listReq); err != nil {
		return err
	}
	respRaw, err := readJSONRPCResponse(conn.reader, 2, 15*time.Second)
	if err != nil {
		return err
	}
	if err := jsonRPCResponseError(respRaw); err != nil {
		return fmt.Errorf("MCP server %s tools/list 失败: %w", serverName, err)
	}

	var listResp struct {
		Result struct {
			Tools []struct {
				Name        string                 `json:"name"`
				Description string                 `json:"description"`
				InputSchema map[string]interface{} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respRaw, &listResp); err != nil {
		return fmt.Errorf("解析 MCP tools/list 失败: %w", err)
	}

	for _, t := range listResp.Result.Tools {
		toolName := t.Name
		desc := t.Description
		schema := t.InputSchema
		globalRegistry.RegisterHandler(toolName, ExternalTool{
			Name:        toolName,
			Description: desc,
			Server:      serverName,
			InputSchema: schema,
		}, makeStdioHandler(conn, toolName, schema))
	}

	stdioConnections.byName[serverName] = conn
	connected = true
	go monitorStdioConnection(conn, cmd)
	return nil
}

// mcpProcessEnvironment applies least privilege to third-party MCP servers.
// Credentials are never inherited implicitly; a server receives secrets only
// when the user explicitly places them in that server's env configuration.
func mcpProcessEnvironment(parent []string, explicit map[string]string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"TMPDIR": true, "TMP": true, "TEMP": true, "LANG": true, "TZ": true,
		"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
		"APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true,
	}
	values := make(map[string]string, len(allowed)+len(explicit))
	for _, item := range parent {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if allowed[upper] || strings.HasPrefix(upper, "LC_") || strings.HasPrefix(upper, "XDG_") {
			values[key] = value
		}
	}
	for key, value := range explicit {
		if strings.TrimSpace(key) != "" && !strings.ContainsAny(key, "=\x00") && !strings.ContainsRune(value, '\x00') {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func makeStdioHandler(conn *stdioConnection, toolName string, schema map[string]interface{}) ToolHandler {
	return func(args map[string]string) (string, error) {
		coerced, err := validateAndCoerceMCPArgs(schema, args)
		if err != nil {
			return "", fmt.Errorf("MCP 工具 %s 参数无效: %w", toolName, err)
		}
		conn.mu.Lock()
		defer conn.mu.Unlock()
		if conn.cmd == nil || conn.cmd.Process == nil {
			return "", fmt.Errorf("MCP server %s 已断开", conn.serverName)
		}
		conn.nextID++
		callReq := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      conn.nextID,
			"method":  "tools/call",
			"params": map[string]interface{}{
				"name":      toolName,
				"arguments": coerced,
			},
		}
		if err := writeJSONRPC(conn.stdin, callReq); err != nil {
			conn.closeLocked()
			return "", err
		}
		raw, err := readJSONRPCResponse(conn.reader, conn.nextID, 10*time.Minute)
		if err != nil {
			conn.closeLocked()
			return "", err
		}
		var callResp struct {
			Result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &callResp); err != nil {
			return string(raw), nil
		}
		if callResp.Error != nil {
			return "", fmt.Errorf("MCP 错误: %s", callResp.Error.Message)
		}
		var parts []string
		for _, c := range callResp.Result.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) == 0 {
			return "(MCP 无文本输出)", nil
		}
		return strings.Join(parts, "\n"), nil
	}
}

func (c *stdioConnection) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
}

func (c *stdioConnection) closeLocked() {
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	c.cmd = nil
}

func monitorStdioConnection(conn *stdioConnection, cmd *exec.Cmd) {
	_ = cmd.Wait()
	stdioConnections.Lock()
	defer stdioConnections.Unlock()
	if stdioConnections.byName[conn.serverName] == conn {
		delete(stdioConnections.byName, conn.serverName)
		globalRegistry.unregisterServer(conn.serverName)
	}
}

// CloseAll stops every MCP session and any stdio child this process started.
// It is safe to call repeatedly.
func CloseAll() {
	closeSDKConnections()
	stdioConnections.Lock()
	connections := make([]*stdioConnection, 0, len(stdioConnections.byName))
	for name, conn := range stdioConnections.byName {
		connections = append(connections, conn)
		delete(stdioConnections.byName, name)
		globalRegistry.unregisterServer(name)
	}
	stdioConnections.Unlock()
	for _, conn := range connections {
		conn.close()
	}
}

func jsonRPCResponseError(raw []byte) error {
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("无效 JSON-RPC 响应: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("JSON-RPC %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return nil
}

func writeJSONRPC(w interface{ Write([]byte) (int, error) }, payload map[string]interface{}) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	line := append(raw, '\n')
	_, err = w.Write(line)
	return err
}

func readJSONRPCLine(r *bufio.Reader) ([]byte, error) {
	const maxMCPMessageBytes = 16 << 20
	var out bytes.Buffer
	for {
		part, err := r.ReadSlice('\n')
		if out.Len()+len(part) > maxMCPMessageBytes {
			return nil, fmt.Errorf("MCP JSON-RPC 消息超过上限 %d 字节", maxMCPMessageBytes)
		}
		_, _ = out.Write(part)
		if err == nil {
			return out.Bytes(), nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}

func validateAndCoerceMCPArgs(schema map[string]interface{}, args map[string]string) (map[string]interface{}, error) {
	if args == nil {
		args = map[string]string{}
	}
	properties, _ := schema["properties"].(map[string]interface{})
	required, _ := schema["required"].([]interface{})
	for _, item := range required {
		name, _ := item.(string)
		if name == "" {
			continue
		}
		if _, ok := args[name]; !ok {
			return nil, fmt.Errorf("缺少必填参数 %s", name)
		}
	}
	if allow, ok := schema["additionalProperties"].(bool); ok && !allow {
		for name := range args {
			if isMCPEnvelopeArg(name) {
				delete(args, name)
				continue
			}
			if _, exists := properties[name]; !exists {
				return nil, fmt.Errorf("未知参数 %s", name)
			}
		}
	}

	out := make(map[string]interface{}, len(args))
	for name, raw := range args {
		if isMCPEnvelopeArg(name) {
			continue
		}
		spec, _ := properties[name].(map[string]interface{})
		value, err := coerceMCPValue(raw, spec, schema)
		if err != nil {
			return nil, fmt.Errorf("参数 %s: %w", name, err)
		}
		spec = resolveMCPSchemaRef(spec, schema)
		if enum, ok := spec["enum"].([]interface{}); ok && len(enum) > 0 {
			matched := false
			for _, allowed := range enum {
				if fmt.Sprint(value) == fmt.Sprint(allowed) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("参数 %s=%q 不在 enum 可选值中", name, raw)
			}
		}
		out[name] = value
	}
	return out, nil
}

func isMCPEnvelopeArg(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "thought", "reasoning", "reasoning_content", "is_finished", "final_report", "risk_level", "tool_name", "tool_args", "skill_name":
		return true
	default:
		return false
	}
}

func resolveMCPSchemaRef(spec, root map[string]interface{}) map[string]interface{} {
	if spec == nil {
		return map[string]interface{}{}
	}
	ref, _ := spec["$ref"].(string)
	if ref == "" || root == nil {
		return spec
	}
	name := ""
	switch {
	case strings.HasPrefix(ref, "#/$defs/"):
		name = strings.TrimPrefix(ref, "#/$defs/")
	case strings.HasPrefix(ref, "#/definitions/"):
		name = strings.TrimPrefix(ref, "#/definitions/")
	default:
		return spec
	}
	defs, _ := root["$defs"].(map[string]interface{})
	if defs == nil {
		defs, _ = root["definitions"].(map[string]interface{})
	}
	def, _ := defs[name].(map[string]interface{})
	if def == nil {
		return spec
	}
	merged := make(map[string]interface{}, len(def)+len(spec))
	for key, value := range def {
		merged[key] = value
	}
	for key, value := range spec {
		if key == "$ref" {
			continue
		}
		merged[key] = value
	}
	return merged
}

func mcpSchemaType(spec map[string]interface{}) string {
	if spec == nil {
		return ""
	}
	if typeName, ok := spec["type"].(string); ok {
		return typeName
	}
	types, _ := spec["type"].([]interface{})
	for _, item := range types {
		typeName, _ := item.(string)
		if typeName != "" && typeName != "null" {
			return typeName
		}
	}
	return ""
}

func coerceMCPValue(raw string, spec, root map[string]interface{}) (interface{}, error) {
	spec = resolveMCPSchemaRef(spec, root)
	if anyOf, ok := spec["anyOf"].([]interface{}); ok && len(anyOf) > 0 {
		var lastErr error
		for _, item := range anyOf {
			itemSpec, _ := item.(map[string]interface{})
			if mcpSchemaType(resolveMCPSchemaRef(itemSpec, root)) == "null" && strings.TrimSpace(raw) == "" {
				return nil, nil
			}
			value, err := coerceMCPValueWithoutAnyOf(raw, itemSpec, root)
			if err == nil {
				return value, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
	}
	return coerceMCPValueWithoutAnyOf(raw, spec, root)
}

func coerceMCPValueWithoutAnyOf(raw string, spec, root map[string]interface{}) (interface{}, error) {
	spec = resolveMCPSchemaRef(spec, root)
	switch mcpSchemaType(spec) {
	case "integer":
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("需要 integer，收到 %q", raw)
		}
		return value, nil
	case "number":
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, fmt.Errorf("需要 number，收到 %q", raw)
		}
		return value, nil
	case "boolean":
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("需要 boolean，收到 %q", raw)
		}
		return value, nil
	case "array":
		trimmed := strings.TrimSpace(raw)
		var value []interface{}
		if err := json.Unmarshal([]byte(trimmed), &value); err == nil {
			return value, nil
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "[") {
			parts := strings.Split(trimmed, ",")
			out := make([]interface{}, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				out = append(out, part)
			}
			if len(out) > 0 {
				return out, nil
			}
		}
		return nil, fmt.Errorf("需要 JSON array，收到 %q", raw)
	case "object":
		var value map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("需要 JSON object，收到 %q", raw)
		}
		return value, nil
	default:
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "{") {
			var value map[string]interface{}
			if err := json.Unmarshal([]byte(trimmed), &value); err == nil {
				return value, nil
			}
		}
		if strings.HasPrefix(trimmed, "[") {
			var value []interface{}
			if err := json.Unmarshal([]byte(trimmed), &value); err == nil {
				return value, nil
			}
		}
		return raw, nil
	}
}

func readJSONRPCLineWithTimeout(r *bufio.Reader, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		return readJSONRPCLine(r)
	}
	type result struct {
		raw []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		raw, err := readJSONRPCLine(r)
		ch <- result{raw: raw, err: err}
	}()
	select {
	case res := <-ch:
		return res.raw, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("等待 JSON-RPC 响应超时 (%s)", timeout)
	}
}

func readJSONRPCResponse(r *bufio.Reader, wantID int, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("等待 JSON-RPC id=%d 响应超时 (%s)", wantID, timeout)
		}
		raw, err := readJSONRPCLineWithTimeout(r, remaining)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(raw, &envelope) != nil || len(envelope.ID) == 0 || string(envelope.ID) == "null" {
			// Servers may emit notifications/log messages between responses.
			continue
		}
		var numericID int
		if json.Unmarshal(envelope.ID, &numericID) == nil && numericID == wantID {
			return raw, nil
		}
	}
}

// LoadServersFromConfig 从配置加载 MCP 服务器
func LoadServersFromConfig(servers []ServerConfig) error {
	for _, s := range servers {
		if err := Connect(s); err != nil {
			return fmt.Errorf("MCP [%s]: %w", s.Name, err)
		}
	}
	return nil
}
