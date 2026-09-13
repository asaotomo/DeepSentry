package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/ui"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ServerStatus struct {
	Name         string
	Transport    string
	State        string
	Error        string
	Protocol     string
	Instructions string
	Tools        int
	Resources    int
	Templates    int
	Prompts      int
	Auth         string
}

type ExternalResource struct {
	Server      string
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	Size        int64
}

type ExternalPrompt struct {
	Server      string
	Name        string
	Title       string
	Description string
	Arguments   []*sdkmcp.PromptArgument
}

type ExternalResourceTemplate struct {
	Server      string
	URITemplate string
	Name        string
	Title       string
	Description string
	MIMEType    string
}

// ImageArtifact is a local, path-backed copy of MCP image content. The marker
// embedded in tool text is consumed by the harness before the next LLM turn,
// keeping base64 out of checkpoints while preserving vision input.
type ImageArtifact struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

const (
	imageArtifactMarker = "[DEEPSENTRY_MCP_IMAGE] "
	maxMCPImageBytes    = 20 << 20
)

type sdkConnection struct {
	mu          sync.RWMutex
	refreshMu   sync.Mutex
	serverName  string
	transport   string
	fingerprint string
	session     *sdkmcp.ClientSession
	ctx         context.Context
	cancel      context.CancelFunc
	toolTimeout time.Duration
	// toolTimeoutExplicit preserves an operator's configured deadline. When it
	// is false, known HawkEye long-running tools receive their documented
	// per-tool deadline instead of the generic 60-second MCP default.
	toolTimeoutExplicit   bool
	hawkEye               bool
	fofaMap               bool
	hawkEyeInterceptArmed bool
	hawkEyeCaptureOwned   bool
	hawkEyeResizeChanged  bool
	status                ServerStatus
	resources             []ExternalResource
	templates             []ExternalResourceTemplate
	prompts               []ExternalPrompt
	config                ServerConfig
	ownedCmd              *exec.Cmd
}

var sdkConnections = struct {
	sync.RWMutex
	byName map[string]*sdkConnection
}{byName: make(map[string]*sdkConnection)}

// Connect uses the official Tier-1 MCP Go SDK for stdio and Streamable HTTP.
// It supports protocol negotiation, pagination and list_changed notifications.
func Connect(cfg ServerConfig) error {
	return connectWithOAuthHandler(cfg, nil)
}

// ConnectOAuth performs an interactive MCP Authorization Code + PKCE flow.
// It is intentionally separate from Connect so startup never opens a browser
// without a user's explicit `/mcp login` request. Tokens remain in memory.
func ConnectOAuth(cfg ServerConfig) error {
	if err := validateRemoteMCPURL(cfg.URL); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("启动 MCP OAuth 回调失败: %w", err)
	}
	defer listener.Close()
	redirectURL := "http://" + listener.Addr().String() + "/callback"
	authCh := make(chan *sdkauth.AuthorizationResult, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, req *http.Request) {
		if oauthErr := strings.TrimSpace(req.URL.Query().Get("error")); oauthErr != "" {
			select {
			case errCh <- fmt.Errorf("OAuth 授权失败: %s", oauthErr):
			default:
			}
			http.Error(w, "Authorization failed. You can close this window.", http.StatusBadRequest)
			return
		}
		result := &sdkauth.AuthorizationResult{Code: req.URL.Query().Get("code"), State: req.URL.Query().Get("state")}
		select {
		case authCh <- result:
		default:
		}
		_, _ = fmt.Fprint(w, "DeepSentry MCP authentication successful. You can close this window.")
	})
	callbackServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if serveErr := callbackServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			select {
			case errCh <- serveErr:
			default:
			}
		}
	}()
	defer callbackServer.Close()

	handler, err := sdkauth.NewAuthorizationCodeHandler(&sdkauth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirectURL,
		AuthorizationCodeFetcher: func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
			if err := openOAuthBrowser(args.URL); err != nil {
				return nil, fmt.Errorf("无法打开浏览器，请手动访问 %s: %w", args.URL, err)
			}
			select {
			case result := <-authCh:
				return result, nil
			case authErr := <-errCh:
				return nil, authErr
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	if err != nil {
		return fmt.Errorf("初始化 MCP OAuth 失败: %w", err)
	}
	if cfg.StartupTimeoutSec <= 0 {
		cfg.StartupTimeoutSec = 180
	}
	return connectWithOAuthHandler(cfg, handler)
}

func connectWithOAuthHandler(cfg ServerConfig, oauthHandler sdkauth.OAuthHandler) error {
	if cfg.Disabled {
		return nil
	}
	serverName := strings.TrimSpace(cfg.Name)
	if serverName == "" {
		serverName = strings.TrimSpace(cfg.Command)
	}
	if serverName == "" {
		return fmt.Errorf("MCP server name 不能为空")
	}
	transportName := strings.ToLower(strings.TrimSpace(cfg.Type))
	if transportName == "" {
		transportName = "stdio"
	}
	if transportName == "http" {
		transportName = "streamable_http"
	}
	stdioCfg := cfg
	if transportName == "stdio" {
		if reused, ok := shouldReuseHawkEyeHTTP(cfg); ok {
			cfg = reused
			transportName = "streamable_http"
		}
	}
	fingerprintRaw, _ := json.Marshal(cfg)
	fingerprint := string(fingerprintRaw)
	if oauthHandler != nil {
		fingerprint += "|oauth"
	}

	sdkConnections.RLock()
	existing := sdkConnections.byName[serverName]
	sdkConnections.RUnlock()
	if existing != nil && existing.fingerprint == fingerprint {
		existing.mu.RLock()
		connected := existing.session != nil && existing.status.State == "connected"
		existing.mu.RUnlock()
		if connected {
			return nil
		}
	}
	if existing != nil {
		closeSDKConnection(existing)
	}

	startupTimeout := time.Duration(cfg.StartupTimeoutSec) * time.Second
	if startupTimeout <= 0 {
		startupTimeout = 15 * time.Second
	}
	toolTimeout := time.Duration(cfg.ToolTimeoutSec) * time.Second
	if toolTimeout <= 0 {
		toolTimeout = 60 * time.Second
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	connectCtx, connectCancel := context.WithTimeout(baseCtx, startupTimeout)
	defer connectCancel()

	var conn *sdkConnection
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "deepsentry", Version: ui.Version}, &sdkmcp.ClientOptions{
		Capabilities: &sdkmcp.ClientCapabilities{},
		KeepAlive:    30 * time.Second,
		ToolListChangedHandler: func(context.Context, *sdkmcp.ToolListChangedRequest) {
			if conn != nil {
				// #nosec G118 -- capability refresh must outlive the notification
				// callback and uses conn.ctx, which Disconnect cancels.
				go conn.refreshCapabilities()
			}
		},
		PromptListChangedHandler: func(context.Context, *sdkmcp.PromptListChangedRequest) {
			if conn != nil {
				// #nosec G118 -- see ToolListChangedHandler above.
				go conn.refreshCapabilities()
			}
		},
		ResourceListChangedHandler: func(context.Context, *sdkmcp.ResourceListChangedRequest) {
			if conn != nil {
				// #nosec G118 -- see ToolListChangedHandler above.
				go conn.refreshCapabilities()
			}
		},
	})

	var transport sdkmcp.Transport
	var ownedCmd *exec.Cmd
	switch transportName {
	case "stdio":
		if strings.TrimSpace(cfg.Command) == "" {
			cancel()
			return fmt.Errorf("MCP stdio command 不能为空")
		}
		ownedCmd = newOwnedStdioCommand(cfg)
		transport = &sdkmcp.CommandTransport{Command: ownedCmd, TerminateDuration: 200 * time.Millisecond}
	case "streamable_http":
		if err := validateRemoteMCPURL(cfg.URL); err != nil {
			cancel()
			return err
		}
		transport = &sdkmcp.StreamableClientTransport{
			Endpoint:     cfg.URL,
			HTTPClient:   mcpHTTPClient(cfg),
			MaxRetries:   5,
			OAuthHandler: oauthHandler,
		}
	default:
		cancel()
		return fmt.Errorf("MCP transport %q 暂不支持；可用 stdio/http/streamable_http", cfg.Type)
	}

	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		forgetOwnedProcess(cfg, ownedCmd)
		if retried, retryCfg, retryErr := retryHawkEyeHTTPAfterStdioFail(connectCtx, client, stdioCfg, transportName, oauthHandler, err); retryErr == nil {
			cfg = retryCfg
			transportName = "streamable_http"
			ownedCmd = nil
			session = retried
			fingerprintRaw, _ = json.Marshal(cfg)
			fingerprint = string(fingerprintRaw)
			err = nil
		} else {
			cancel()
			setFailedServerStatus(serverName, transportName, fingerprint, cfg, oauthHandler != nil, retryErr)
			return fmt.Errorf("MCP server %s 连接失败: %w", serverName, retryErr)
		}
	}
	init := session.InitializeResult()
	conn = &sdkConnection{
		serverName:          serverName,
		transport:           transportName,
		fingerprint:         fingerprint,
		session:             session,
		ctx:                 baseCtx,
		cancel:              cancel,
		toolTimeout:         toolTimeout,
		toolTimeoutExplicit: cfg.ToolTimeoutSec > 0,
		config:              cfg,
		ownedCmd:            ownedCmd,
		status: ServerStatus{
			Name:      serverName,
			Transport: transportName,
			State:     "connected",
			Auth:      mcpAuthMode(cfg, oauthHandler != nil),
		},
	}
	if init != nil {
		conn.status.Protocol = init.ProtocolVersion
		conn.status.Instructions = strings.TrimSpace(init.Instructions)
	}
	if err := conn.refreshCapabilities(); err != nil {
		_ = session.Close()
		forgetOwnedProcess(cfg, ownedCmd)
		cancel()
		setFailedServerStatus(serverName, transportName, fingerprint, cfg, oauthHandler != nil, err)
		return fmt.Errorf("MCP server %s 能力发现失败: %w", serverName, err)
	}
	rememberOwnedProcess(cfg, ownedCmd)
	sdkConnections.Lock()
	sdkConnections.byName[serverName] = conn
	sdkConnections.Unlock()
	go monitorSDKConnection(conn)
	return nil
}

func retryHawkEyeHTTPAfterStdioFail(ctx context.Context, client *sdkmcp.Client, stdioCfg ServerConfig, transportName string, oauthHandler sdkauth.OAuthHandler, stdioErr error) (*sdkmcp.ClientSession, ServerConfig, error) {
	if stdioErr == nil || transportName != "stdio" || !isHawkEyeStdioConfig(stdioCfg) {
		return nil, stdioCfg, stdioErr
	}
	port := hawkEyePortFromConfig(stdioCfg)
	if !hawkEyePortOccupied(port) && !hawkEyeHTTPReady(hawkEyeMCPURL(stdioCfg)) {
		return nil, stdioCfg, stdioErr
	}
	reused := hawkEyeStreamableHTTPConfig(stdioCfg)
	if err := validateRemoteMCPURL(reused.URL); err != nil {
		return nil, stdioCfg, stdioErr
	}
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:     reused.URL,
		HTTPClient:   mcpHTTPClient(reused),
		MaxRetries:   5,
		OAuthHandler: oauthHandler,
	}, nil)
	if err != nil {
		return nil, reused, fmt.Errorf("端口 %d 已占用，改连 Streamable HTTP %s 失败: %w", port, reused.URL, err)
	}
	return session, reused, nil
}

func openOAuthBrowser(target string) error {
	if parsed, err := url.Parse(target); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("OAuth URL 无效")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (conn *sdkConnection) refreshCapabilities() error {
	conn.refreshMu.Lock()
	defer conn.refreshMu.Unlock()
	baseCtx := conn.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 30*time.Second)
	defer cancel()

	toolCount := 0
	discovered := make([]serverToolHandler, 0)
	for tool, err := range conn.session.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		if tool == nil || !conn.toolEnabled(tool.Name) {
			continue
		}
		schema, _ := tool.InputSchema.(map[string]interface{})
		external := ExternalTool{
			Name: tool.Name, OriginalName: tool.Name, Description: tool.Description,
			Server: conn.serverName, InputSchema: schema, Annotations: convertSDKToolAnnotations(tool.Annotations),
		}
		originalName := tool.Name
		handler := func(args map[string]string) (string, error) {
			return conn.callTool(originalName, schema, args)
		}
		discovered = append(discovered, serverToolHandler{tool: external, handler: handler})
		toolCount++
	}
	discoveredTools := make([]ExternalTool, 0, len(discovered))
	for _, item := range discovered {
		discoveredTools = append(discoveredTools, item.tool)
	}
	hawkEye := false
	fofaMap := false
	for i := range discoveredTools {
		if IsHawkEyeTool(&discoveredTools[i], discoveredTools) {
			hawkEye = true
		}
		if IsFofaMapTool(&discoveredTools[i], discoveredTools) {
			fofaMap = true
		}
	}
	conn.mu.Lock()
	conn.hawkEye = hawkEye
	conn.fofaMap = fofaMap
	conn.mu.Unlock()
	globalRegistry.replaceServerHandlers(conn.serverName, discovered)

	resources := make([]ExternalResource, 0)
	for resource, err := range conn.session.Resources(ctx, nil) {
		if err != nil {
			break
		}
		if resource != nil {
			resources = append(resources, ExternalResource{Server: conn.serverName, URI: resource.URI, Name: resource.Name, Title: resource.Title, Description: resource.Description, MIMEType: resource.MIMEType, Size: resource.Size})
		}
	}
	templates := make([]ExternalResourceTemplate, 0)
	for template, err := range conn.session.ResourceTemplates(ctx, nil) {
		if err != nil {
			break
		}
		if template != nil {
			templates = append(templates, ExternalResourceTemplate{Server: conn.serverName, URITemplate: template.URITemplate, Name: template.Name, Title: template.Title, Description: template.Description, MIMEType: template.MIMEType})
		}
	}
	prompts := make([]ExternalPrompt, 0)
	for prompt, err := range conn.session.Prompts(ctx, nil) {
		if err != nil {
			break
		}
		if prompt != nil {
			prompts = append(prompts, ExternalPrompt{Server: conn.serverName, Name: prompt.Name, Title: prompt.Title, Description: prompt.Description, Arguments: prompt.Arguments})
		}
	}
	conn.mu.Lock()
	conn.resources = resources
	conn.templates = templates
	conn.prompts = prompts
	conn.status.State = "connected"
	conn.status.Error = ""
	conn.status.Tools = toolCount
	conn.status.Resources = len(resources)
	conn.status.Templates = len(templates)
	conn.status.Prompts = len(prompts)
	conn.mu.Unlock()
	return nil
}

func (conn *sdkConnection) toolEnabled(name string) bool {
	if len(conn.config.EnabledTools) > 0 && !containsFold(conn.config.EnabledTools, name) {
		return false
	}
	return !containsFold(conn.config.DisabledTools, name)
}

func (conn *sdkConnection) callTool(name string, schema map[string]interface{}, args map[string]string) (string, error) {
	conn.mu.RLock()
	hawkEye := conn.hawkEye
	fofaMap := conn.fofaMap
	deadline := conn.toolTimeout
	toolTimeoutExplicit := conn.toolTimeoutExplicit
	conn.mu.RUnlock()
	if hawkEye {
		args = applyHawkEyeCallDefaults(name, args)
	}
	if fofaMap {
		args = applyFofaMapCallDefaults(name, args)
	}
	coerced, err := validateAndCoerceMCPArgs(schema, args)
	if err != nil {
		return "", fmt.Errorf("MCP 工具 %s 参数无效: %w", name, err)
	}
	baseCtx := conn.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	if hawkEye {
		deadline = HawkEyeToolTimeout(name, deadline, toolTimeoutExplicit)
	}
	if fofaMap {
		deadline = FofaMapToolTimeout(name, deadline, toolTimeoutExplicit)
	}
	ctx, cancel := context.WithTimeout(baseCtx, deadline)
	defer cancel()
	result, err := conn.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: coerced})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("MCP 工具 %s 未返回结果", name)
	}
	out := formatMCPContent(result.Content, result.StructuredContent)
	if hawkEye {
		out = formatHawkEyeMCPContent(name, result.Content, result.StructuredContent, args)
	}
	if result.IsError {
		if hawkEye && hawkEyeStaleNavigateError(name, out) {
			urlValue := strings.TrimSpace(args["url"])
			if urlValue != "" {
				recoveryCtx, recoveryCancel := context.WithTimeout(baseCtx, minMCPDuration(deadline, 35*time.Second))
				recovered, recoveryErr := conn.session.CallTool(recoveryCtx, &sdkmcp.CallToolParams{
					Name: "browser_tabs",
					Arguments: map[string]any{
						"action": "new", "url": urlValue, "active": true, "bind": true,
					},
				})
				recoveryCancel()
				if recoveryErr == nil && recovered != nil && !recovered.IsError {
					recoveryOut := formatMCPContent(recovered.Content, recovered.StructuredContent)
					return "DeepSentry 已自动修复 HawkEye 陈旧标签绑定：新建目标页、置为活动并绑定到 MCP。\n" + recoveryOut + "\n下一步直接用 browser_find 定位文本，或 browser_snapshot 取得交互 ref。", nil
				}
				recoveryWhy := "未返回可用结果"
				if recoveryErr != nil {
					recoveryWhy = recoveryErr.Error()
				}
				return "MCP 工具返回可修正错误:\n" + out + "\nDeepSentry 尝试自动重建并绑定标签页失败: " + recoveryWhy, nil
			}
		}
		return "MCP 工具返回可修正错误:\n" + out, nil
	}
	conn.recordHawkEyeState(name, args)
	return compactHawkEyeMCPOutput(name, out), nil
}

func minMCPDuration(left, right time.Duration) time.Duration {
	if left > 0 && left < right {
		return left
	}
	return right
}

func applyHawkEyeCallDefaults(name string, args map[string]string) map[string]string {
	if token := hawkEyeContinuationToken(args); token != "" {
		// HawkEye 1.0.7 rejects numeric cursors and extra fields on continuation.
		return map[string]string{"page_token": token}
	}
	copyArgs := make(map[string]string, len(args)+4)
	for key, value := range args {
		if strings.EqualFold(strings.ReplaceAll(strings.TrimSpace(key), "_", ""), "cursor") && strings.TrimSpace(value) != "" && strings.TrimSpace(value) != "0" {
			continue
		}
		copyArgs[key] = value
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "browser_snapshot":
		if strings.TrimSpace(copyArgs["context_budget_chars"]) == "" {
			copyArgs["context_budget_chars"] = "30000"
		}
		if strings.TrimSpace(copyArgs["max_elements"]) == "" {
			copyArgs["max_elements"] = "160"
		}
		if strings.TrimSpace(copyArgs["compact"]) == "" {
			copyArgs["compact"] = "true"
		}
		if strings.TrimSpace(copyArgs["viewport_only"]) == "" {
			copyArgs["viewport_only"] = "true"
		}
	case "browser_search", "browser_fetch", "browser_read_text", "browser_find":
		if strings.TrimSpace(copyArgs["context_budget_chars"]) == "" {
			copyArgs["context_budget_chars"] = "30000"
		}
	case "browser_research":
		if strings.TrimSpace(copyArgs["context_budget_chars"]) == "" {
			copyArgs["context_budget_chars"] = "50000"
		}
	case "browser_press_key":
		if strings.TrimSpace(copyArgs["inputMode"]) == "" && strings.EqualFold(strings.TrimSpace(copyArgs["key"]), "f") {
			copyArgs["inputMode"] = "trusted"
		}
	case "browser_click":
		blob := strings.ToLower(copyArgs["text"] + " " + copyArgs["element"])
		if strings.TrimSpace(copyArgs["clickMode"]) == "" && (strings.Contains(blob, "全屏") || strings.Contains(blob, "fullscreen")) {
			copyArgs["clickMode"] = "trusted"
		}
	case "browser_captcha_assist":
		if hawkEyeArg(copyArgs, "action") == "solve" && strings.TrimSpace(copyArgs["authorized"]) == "" {
			copyArgs["authorized"] = "true"
		}
	}
	return copyArgs
}

func hawkEyeStaleNavigateError(name, output string) bool {
	if !strings.EqualFold(strings.TrimSpace(name), "browser_navigate") {
		return false
	}
	lower := strings.ToLower(output)
	return strings.Contains(lower, "test tab no longer exists") ||
		strings.Contains(lower, "bound tab no longer exists") ||
		strings.Contains(lower, "target tab no longer exists")
}

func compactHawkEyeMCPOutput(name, output string) string {
	limit := 0
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "browser_navigate":
		limit = 20000
	case "browser_snapshot", "browser_click", "browser_hover", "browser_type",
		"browser_press_key", "browser_select_option", "browser_reload",
		"browser_go_back", "browser_go_forward", "browser_scroll", "browser_wait_for",
		"browser_fill_form", "browser_find", "browser_read_text":
		limit = 24000
	}
	if limit == 0 || len(output) <= limit {
		return output
	}
	return output[:limit] + fmt.Sprintf("\n\n[DeepSentry HawkEye 上下文优化：%s 结果共 %d 字符，已保留前 %d；请用 browser_find 定位目标，或把 context.next_page_token 原样作为 page_token 单独继续。不要对 artifact 执行本地 grep/read_file]\n", name, len(output), limit)
}

func formatHawkEyeMCPContent(name string, contents []sdkmcp.Content, structured any, args map[string]string) string {
	dumpStructured := structured
	if hawkEyeDropsStructuredDump(name) {
		dumpStructured = nil
	}
	out := formatMCPContent(contents, dumpStructured)
	if summary := hawkEyeStructuredSummary(name, structured); summary != "" {
		out += "\n" + summary
	}
	if hint := hawkEyeClickMissingTextHint(name, args); hint != "" {
		out += "\n" + hint
	}
	if hint := hawkEyeInteractionHint(name); hint != "" {
		out += "\n" + hint
	}
	return compactHawkEyeMCPOutput(name, out)
}

func (conn *sdkConnection) recordHawkEyeState(name string, args map[string]string) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if !conn.hawkEye {
		return
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "hawkeye_intercept":
		switch hawkEyeArg(args, "action") {
		case "enable":
			conn.hawkEyeInterceptArmed = true
		case "disable":
			conn.hawkEyeInterceptArmed = false
		}
	case "hawkeye_capture_start":
		conn.hawkEyeCaptureOwned = true
	case "hawkeye_capture_stop":
		conn.hawkEyeCaptureOwned = false
	case "browser_resize":
		if hawkEyeArg(args, "action") == "reset" {
			conn.hawkEyeResizeChanged = false
		} else {
			conn.hawkEyeResizeChanged = true
		}
	}
}

func convertSDKToolAnnotations(in *sdkmcp.ToolAnnotations) ToolAnnotations {
	if in == nil {
		return ToolAnnotations{}
	}
	out := ToolAnnotations{
		Present:        true,
		ReadOnlyHint:   in.ReadOnlyHint,
		IdempotentHint: in.IdempotentHint,
	}
	if in.DestructiveHint != nil {
		out.DestructiveKnown = true
		out.DestructiveHint = *in.DestructiveHint
	}
	if in.OpenWorldHint != nil {
		out.OpenWorldKnown = true
		out.OpenWorldHint = *in.OpenWorldHint
	}
	return out
}

func formatMCPContent(contents []sdkmcp.Content, structured any) string {
	parts := make([]string, 0, len(contents)+1)
	// Transfer markers are kept separately and appended after visible output is
	// truncated. Otherwise a server that returns a very large text block before
	// an image could silently cut off the image evidence before the harness can
	// attach it to the next vision-model turn.
	markers := make([]string, 0)
	for _, content := range contents {
		switch item := content.(type) {
		case *sdkmcp.TextContent:
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		case *sdkmcp.ResourceLink:
			parts = append(parts, fmt.Sprintf("[资源] %s (%s)", valueOrString(item.Title, item.Name), item.URI))
		case *sdkmcp.EmbeddedResource:
			if item.Resource != nil {
				if item.Resource.Text != "" {
					parts = append(parts, item.Resource.Text)
				} else {
					parts = append(parts, fmt.Sprintf("[嵌入资源] %s (%s, %d bytes)", item.Resource.URI, item.Resource.MIMEType, len(item.Resource.Blob)))
				}
			}
		case *sdkmcp.ImageContent:
			artifact, err := persistMCPImage(item.Data, item.MIMEType)
			if err != nil {
				parts = append(parts, fmt.Sprintf("[图片内容] %s · %d bytes · 保存失败: %v", item.MIMEType, len(item.Data), err))
			} else {
				raw, _ := json.Marshal(artifact)
				parts = append(parts, fmt.Sprintf("[图片内容] %s · %d bytes · 已保存到 %s", artifact.MediaType, artifact.Size, artifact.Path))
				markers = append(markers, imageArtifactMarker+string(raw))
			}
		case *sdkmcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[音频内容] %s · %d bytes", item.MIMEType, len(item.Data)))
		default:
			if raw, err := json.Marshal(item); err == nil {
				parts = append(parts, string(raw))
			}
		}
	}
	if structured != nil {
		if raw, err := json.MarshalIndent(structured, "", "  "); err == nil && string(raw) != "null" {
			parts = append(parts, "[结构化结果]\n"+string(raw))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "(MCP 无可显示输出)")
	}
	visible := truncateMCPText(strings.Join(parts, "\n"), 1<<20)
	if len(markers) == 0 {
		return visible
	}
	return visible + "\n" + strings.Join(markers, "\n")
}

// ExtractImageArtifacts removes internal transfer markers from model-visible
// text and returns validated metadata for harness-side vision attachments.
func ExtractImageArtifacts(text string) (string, []ImageArtifact) {
	lines := strings.Split(text, "\n")
	clean := make([]string, 0, len(lines))
	artifacts := make([]ImageArtifact, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, imageArtifactMarker) {
			var artifact ImageArtifact
			if json.Unmarshal([]byte(strings.TrimPrefix(trimmed, imageArtifactMarker)), &artifact) == nil && artifact.Path != "" {
				artifacts = append(artifacts, artifact)
				continue
			}
		}
		clean = append(clean, line)
	}
	return strings.TrimSpace(strings.Join(clean, "\n")), artifacts
}

func persistMCPImage(data []byte, declaredMIME string) (ImageArtifact, error) {
	if len(data) == 0 {
		return ImageArtifact{}, errors.New("图片数据为空")
	}
	if len(data) > maxMCPImageBytes {
		return ImageArtifact{}, fmt.Errorf("图片超过 %d MiB 限制", maxMCPImageBytes>>20)
	}
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	ext := ""
	switch detected {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	default:
		return ImageArtifact{}, fmt.Errorf("不支持的图片格式 %q（声明为 %q）", detected, declaredMIME)
	}
	dir := strings.TrimSpace(os.Getenv("DEEPSENTRY_MCP_ARTIFACT_DIR"))
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ImageArtifact{}, err
		}
		dir = filepath.Join(cwd, "reports", "mcp-artifacts")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return ImageArtifact{}, err
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return ImageArtifact{}, err
	}
	sum := sha256.Sum256(data)
	digest := fmt.Sprintf("%x", sum[:])
	name := fmt.Sprintf("mcp_%s_%s%s", time.Now().Format("20060102_150405.000000000"), digest[:12], ext)
	path := filepath.Join(absDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ImageArtifact{}, err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return ImageArtifact{}, writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return ImageArtifact{}, closeErr
	}
	return ImageArtifact{Path: path, Name: name, MediaType: detected, Size: int64(len(data)), SHA256: digest}, nil
}

func ListServerStatuses() []ServerStatus {
	sdkConnections.RLock()
	connections := make([]*sdkConnection, 0, len(sdkConnections.byName))
	for _, conn := range sdkConnections.byName {
		connections = append(connections, conn)
	}
	sdkConnections.RUnlock()
	out := make([]ServerStatus, 0, len(connections))
	for _, conn := range connections {
		conn.mu.RLock()
		out = append(out, conn.status)
		conn.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ServerInventory is a compact per-connection tool count for banners and startup logs.
type ServerInventory struct {
	Name      string
	ToolCount int
}

// ConnectedInventory lists currently connected MCP servers and how many tools
// each exposed. SDK connection status is preferred; the tool registry is the
// fallback when only the legacy stdio adapter is populated.
func ConnectedInventory() []ServerInventory {
	if items := inventoryFromStatuses(ListServerStatuses()); len(items) > 0 {
		return items
	}
	return inventoryFromTools(Global().ListTools())
}

func inventoryFromStatuses(statuses []ServerStatus) []ServerInventory {
	out := make([]ServerInventory, 0, len(statuses))
	for _, status := range statuses {
		if !strings.EqualFold(strings.TrimSpace(status.State), "connected") {
			continue
		}
		name := strings.TrimSpace(status.Name)
		if name == "" {
			continue
		}
		out = append(out, ServerInventory{Name: name, ToolCount: status.Tools})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func inventoryFromTools(tools []ExternalTool) []ServerInventory {
	counts := map[string]int{}
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Server)
		if name == "" {
			continue
		}
		counts[name]++
	}
	out := make([]ServerInventory, 0, len(counts))
	for name, count := range counts {
		out = append(out, ServerInventory{Name: name, ToolCount: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FormatConnectedInventory returns a one-line summary such as
// "2 个已连接 · fofamap 15 · hx0-hawkeye 51". Empty when nothing is connected.
func FormatConnectedInventory() string {
	return formatConnectedInventory(ConnectedInventory())
}

func formatConnectedInventory(items []ServerInventory) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s %d", item.Name, item.ToolCount))
	}
	return fmt.Sprintf("%d 个已连接 · %s", len(items), strings.Join(parts, " · "))
}

func FormatServerStatus() string {
	statuses := ListServerStatuses()
	if len(statuses) == 0 {
		return "当前没有已连接的 MCP Server。配置后请开始新会话或重启。"
	}
	var b strings.Builder
	for _, status := range statuses {
		line := fmt.Sprintf("- %s [%s] %s · tools=%d resources=%d templates=%d prompts=%d", status.Name, status.Transport, status.State, status.Tools, status.Resources, status.Templates, status.Prompts)
		if status.Protocol != "" {
			line += " · protocol=" + status.Protocol
		}
		if status.Auth != "" {
			line += " · auth=" + status.Auth
		}
		if status.Error != "" {
			line += " · error=" + status.Error
		}
		if status.State != "connected" {
			if status.Auth == "oauth" {
				line += " · retry=/mcp login " + status.Name
			} else {
				line += " · retry=/mcp reconnect " + status.Name
			}
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

func mcpAuthMode(cfg ServerConfig, oauth bool) string {
	if oauth {
		return "oauth"
	}
	if strings.TrimSpace(cfg.BearerTokenEnvVar) != "" {
		return "bearer-env"
	}
	if len(cfg.Headers) > 0 {
		return "headers"
	}
	return "none"
}

func ListResources(server string) []ExternalResource {
	var out []ExternalResource
	for _, conn := range selectedConnections(server) {
		conn.mu.RLock()
		out = append(out, conn.resources...)
		conn.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].URI < out[j].URI
	})
	return out
}

func ListResourceTemplates(server string) []ExternalResourceTemplate {
	var out []ExternalResourceTemplate
	for _, conn := range selectedConnections(server) {
		conn.mu.RLock()
		out = append(out, conn.templates...)
		conn.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].URITemplate < out[j].URITemplate
	})
	return out
}

func ReadResource(server, uri string) (string, error) {
	conn, err := getSDKConnection(server)
	if err != nil {
		return "", err
	}
	baseCtx := conn.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, conn.toolTimeout)
	defer cancel()
	result, err := conn.session.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(result.Contents))
	for _, content := range result.Contents {
		if content == nil {
			continue
		}
		if content.Text != "" {
			parts = append(parts, content.Text)
		} else {
			parts = append(parts, fmt.Sprintf("[二进制资源] %s · %s · %d bytes", content.URI, content.MIMEType, len(content.Blob)))
		}
	}
	if len(parts) == 0 {
		return "(MCP 资源为空)", nil
	}
	return truncateMCPText(strings.Join(parts, "\n"), 1<<20), nil
}

func ListPrompts(server string) []ExternalPrompt {
	var out []ExternalPrompt
	for _, conn := range selectedConnections(server) {
		conn.mu.RLock()
		out = append(out, conn.prompts...)
		conn.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func GetPrompt(server, name string, args map[string]string) (string, error) {
	conn, err := getSDKConnection(server)
	if err != nil {
		return "", err
	}
	baseCtx := conn.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, conn.toolTimeout)
	defer cancel()
	result, err := conn.session.GetPrompt(ctx, &sdkmcp.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(result.Messages)+1)
	if result.Description != "" {
		parts = append(parts, result.Description)
	}
	for _, message := range result.Messages {
		if message != nil {
			parts = append(parts, fmt.Sprintf("[%s]\n%s", message.Role, formatMCPContent([]sdkmcp.Content{message.Content}, nil)))
		}
	}
	return truncateMCPText(strings.Join(parts, "\n\n"), 1<<20), nil
}

func FormatServerInstructions() string {
	statuses := ListServerStatuses()
	var b strings.Builder
	const totalBudget = 8192
	for _, status := range statuses {
		if status.Instructions == "" {
			continue
		}
		instructions := []rune(status.Instructions)
		if len(instructions) > 2048 {
			instructions = append(instructions[:2047], '…')
		}
		section := fmt.Sprintf("\nMCP Server %s 指令:\n%s\n", status.Name, string(instructions))
		if b.Len()+len(section) > totalBudget {
			break
		}
		b.WriteString(section)
	}
	return b.String()
}

func selectedConnections(server string) []*sdkConnection {
	server = strings.TrimSpace(server)
	sdkConnections.RLock()
	defer sdkConnections.RUnlock()
	if server != "" {
		if conn := sdkConnections.byName[server]; conn != nil {
			return []*sdkConnection{conn}
		}
		return nil
	}
	out := make([]*sdkConnection, 0, len(sdkConnections.byName))
	for _, conn := range sdkConnections.byName {
		out = append(out, conn)
	}
	return out
}

func getSDKConnection(server string) (*sdkConnection, error) {
	sdkConnections.RLock()
	conn := sdkConnections.byName[strings.TrimSpace(server)]
	sdkConnections.RUnlock()
	if conn == nil {
		return nil, fmt.Errorf("未连接 MCP Server: %s", server)
	}
	return conn, nil
}

func monitorSDKConnection(conn *sdkConnection) {
	err := conn.session.Wait()
	// A replaced connection may finish after its successor has registered the
	// same server name. Only the currently installed connection may mutate
	// status or unregister tools.
	sdkConnections.RLock()
	current := sdkConnections.byName[conn.serverName]
	sdkConnections.RUnlock()
	if current != conn {
		return
	}
	conn.mu.Lock()
	conn.status.State = "disconnected"
	if err != nil {
		conn.status.Error = err.Error()
	}
	conn.mu.Unlock()
	globalRegistry.unregisterServer(conn.serverName)
}

func closeSDKConnection(conn *sdkConnection) {
	if conn == nil {
		return
	}
	cleanupHawkEyeConnectionState(conn)
	if conn.cancel != nil {
		conn.cancel()
	}
	forgetOwnedProcess(conn.config, conn.ownedCmd)
	conn.ownedCmd = nil
	if conn.session != nil {
		_ = conn.session.Close()
	}
	sdkConnections.Lock()
	if sdkConnections.byName[conn.serverName] == conn {
		delete(sdkConnections.byName, conn.serverName)
	}
	sdkConnections.Unlock()
	globalRegistry.unregisterServer(conn.serverName)
}

// cleanupHawkEyeConnectionState is a best-effort safety net for process exit,
// server replacement and explicit /mcp remove. It only reverts state that this
// DeepSentry connection successfully changed. Intercept disable is first and
// HawkEye defines it as "stop and release all", preventing a browser tab from
// remaining hung even if the outer CLI cleanup has a short deadline.
func cleanupHawkEyeConnectionState(conn *sdkConnection) {
	if conn == nil || conn.session == nil {
		return
	}
	conn.mu.RLock()
	intercept, capture, resized := conn.hawkEyeInterceptArmed, conn.hawkEyeCaptureOwned, conn.hawkEyeResizeChanged
	conn.mu.RUnlock()
	if !intercept && !capture && !resized {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	call := func(name string, args map[string]any) {
		if ctx.Err() == nil {
			_, _ = conn.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		}
	}
	if intercept {
		call("hawkeye_intercept", map[string]any{"action": "disable"})
	}
	if capture {
		call("hawkeye_capture_stop", map[string]any{})
	}
	if resized {
		call("browser_resize", map[string]any{"action": "reset"})
	}
}

func closeSDKConnections() {
	sdkConnections.RLock()
	connections := make([]*sdkConnection, 0, len(sdkConnections.byName))
	for _, conn := range sdkConnections.byName {
		connections = append(connections, conn)
	}
	sdkConnections.RUnlock()
	for _, conn := range connections {
		closeSDKConnection(conn)
	}
}

// Reconnect replaces a failed/disconnected session without changing config.
// Tool calls are never replayed automatically because a transport failure may
// happen after a modifying server operation has already taken effect.
func Reconnect(server string) error {
	server = strings.TrimSpace(server)
	if server == "" {
		return fmt.Errorf("MCP Server 名称不能为空")
	}
	sdkConnections.RLock()
	existing := sdkConnections.byName[server]
	sdkConnections.RUnlock()
	if existing == nil {
		return fmt.Errorf("未找到 MCP Server 连接记录: %s", server)
	}
	existing.mu.RLock()
	cfg := existing.config
	auth := existing.status.Auth
	existing.mu.RUnlock()
	if auth == "oauth" {
		return fmt.Errorf("MCP Server %s 使用 OAuth；请执行 /mcp login %s 重新授权", server, server)
	}
	closeSDKConnection(existing)
	return Connect(cfg)
}

// Disconnect closes one live MCP connection and removes all tools, resources
// and prompts discovered from it. It is safe to call for an offline server.
func Disconnect(server string) {
	sdkConnections.RLock()
	conn := sdkConnections.byName[strings.TrimSpace(server)]
	sdkConnections.RUnlock()
	if conn != nil {
		closeSDKConnection(conn)
	}
}

func setFailedServerStatus(name, transport, fingerprint string, cfg ServerConfig, oauth bool, err error) {
	conn := &sdkConnection{
		serverName: name, transport: transport, fingerprint: fingerprint, config: cfg,
		status: ServerStatus{Name: name, Transport: transport, State: "failed", Error: err.Error(), Auth: mcpAuthMode(cfg, oauth)},
	}
	sdkConnections.Lock()
	sdkConnections.byName[name] = conn
	sdkConnections.Unlock()
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
	token   string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for key, value := range t.headers {
		if strings.TrimSpace(key) != "" && !strings.ContainsAny(key, "\r\n") && !strings.ContainsAny(value, "\r\n") {
			clone.Header.Set(key, value)
		}
	}
	if t.token != "" {
		clone.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(clone)
}

func mcpHTTPClient(cfg ServerConfig) *http.Client {
	base := http.RoundTripper(config.HTTPTransport())
	token := ""
	if name := strings.TrimSpace(cfg.BearerTokenEnvVar); name != "" {
		token = os.Getenv(name)
	}
	origin, _ := url.Parse(strings.TrimSpace(cfg.URL))
	return &http.Client{
		Transport: headerTransport{base: base, headers: cfg.Headers, token: token},
		Timeout:   0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("MCP HTTP 重定向次数过多")
			}
			// headerTransport applies credentials on every request, so redirects
			// must remain on the configured origin to prevent credential leaks.
			if origin == nil || !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
				return fmt.Errorf("MCP HTTP 拒绝携带凭据跨域重定向")
			}
			return nil
		},
	}
}

func truncateMCPText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + fmt.Sprintf("\n… [MCP 输出已限制为 %d 字符]", maxRunes)
}

func validateRemoteMCPURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("MCP HTTP URL 无效")
	}
	host := strings.ToLower(parsed.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return fmt.Errorf("MCP HTTP URL 必须使用 HTTPS；仅 localhost/回环地址允许 HTTP")
	}
	return nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func valueOrString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
