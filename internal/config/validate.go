package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ValidateRuntimeConfig rejects ambiguous or unsupported runtime state before
// it can reach an executor. Zero-valued tuning fields retain their documented
// auto/default semantics.
func ValidateRuntimeConfig(cfg Config) error {
	runtimeMode := cfg.EffectiveAgentRuntime()
	if runtimeMode != "legacy" && runtimeMode != "v3" {
		return fmt.Errorf("agent_runtime=%q 无效；可选 legacy|v3", cfg.AgentRuntime)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.TerminalTheme)) {
	case "", "auto", "dark", "light":
	default:
		return fmt.Errorf("terminal_theme=%q 无效；可选 auto|dark|light", cfg.TerminalTheme)
	}
	if strings.TrimSpace(cfg.TraceDir) == "." || strings.TrimSpace(cfg.TraceDir) == "/" {
		return fmt.Errorf("trace_dir 不能是当前目录或根目录")
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		return fmt.Errorf("provider 不能为空")
	}
	if provider != string(ProviderCustom) {
		if _, ok := FindProvider(provider); !ok {
			return fmt.Errorf("未知 provider %q；自定义兼容接口请使用 provider=custom", cfg.Provider)
		}
	}
	protocol := strings.ToLower(strings.TrimSpace(cfg.EffectiveAPIProtocol()))
	switch protocol {
	case ProtocolOpenAIChat, ProtocolAnthropicMessages, ProtocolOpenAIResponses:
	default:
		return fmt.Errorf("api_protocol=%q 无效；可选 auto|%s|%s|%s", cfg.APIProtocol, ProtocolOpenAIChat, ProtocolAnthropicMessages, ProtocolOpenAIResponses)
	}
	if strings.TrimSpace(cfg.ModelName) == "" {
		return fmt.Errorf("model_name 不能为空")
	}
	if cfg.Temperature < 0 || cfg.Temperature > 2 {
		return fmt.Errorf("temperature 必须在 0~2 之间")
	}
	if err := validateHTTPURL("api_url", cfg.ApiURL); err != nil {
		return err
	}
	profile := strings.ToLower(strings.TrimSpace(cfg.ModelProfile))
	if profile != "" && profile != "auto" && profile != ModelProfileCompact && profile != ModelProfileBalanced && profile != ModelProfileFull {
		return fmt.Errorf("model_profile=%q 无效；可选 auto|compact|balanced|full", cfg.ModelProfile)
	}
	if cfg.ModelParameterB < 0 {
		return fmt.Errorf("model_parameter_b 不能为负数")
	}
	if cfg.ContextWindowTokens != 0 && (cfg.ContextWindowTokens < 4_096 || cfg.ContextWindowTokens > 4_194_304) {
		return fmt.Errorf("context_window_tokens 必须为 0(auto) 或 4096~4194304")
	}
	if cfg.ContextUtilization != 0 && (cfg.ContextUtilization < 0.40 || cfg.ContextUtilization > 0.90) {
		return fmt.Errorf("context_utilization 必须为 0(auto) 或 0.40~0.90")
	}
	if cfg.ReservedOutputTokens < 0 || cfg.NativeToolLimit < 0 {
		return fmt.Errorf("reserved_output_tokens/native_tool_limit 不能为负数")
	}
	if cfg.ContextWindowTokens > 0 && cfg.ReservedOutputTokens >= cfg.ContextWindowTokens {
		return fmt.Errorf("reserved_output_tokens 必须小于 context_window_tokens")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.VisionMode)) {
	case "", "auto", "enabled", "enable", "true", "required", "on", "disabled", "disable", "false", "off":
	default:
		return fmt.Errorf("vision_mode=%q 无效；可选 auto|enabled|disabled", cfg.VisionMode)
	}
	if cfg.LLMTimeoutSec < 0 || cfg.LLMRetries < 0 || cfg.SSHCommandTimeoutSec < 0 || cfg.SSHMaxOutputBytes < 0 || cfg.MaxSteps < 0 || cfg.SubAgentMaxSteps < 0 {
		return fmt.Errorf("timeout/retries/output/max_steps 配置不能为负数")
	}
	if cfg.SSHConnectTimeoutSec < 0 || cfg.SSHConnectTimeoutSec > 120 {
		return fmt.Errorf("ssh_connect_timeout_sec 必须在 0-120 之间")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.SSHLegacyCompat)) {
	case "", "true", "1", "yes", "on", "false", "0", "no", "off":
	default:
		return fmt.Errorf("ssh_legacy_compat=%q 无效；可选 true|false", cfg.SSHLegacyCompat)
	}
	if err := validateDeviceType("telnet_device_type", cfg.TelnetDeviceType); err != nil {
		return err
	}
	if err := validateDeviceType("ssh_device_type", cfg.SSHDeviceType); err != nil {
		return err
	}
	if raw := strings.TrimSpace(cfg.SSHPrompt); strings.HasPrefix(strings.ToLower(raw), "regex:") {
		if _, err := regexp.Compile(strings.TrimSpace(raw[len("regex:"):])); err != nil {
			return fmt.Errorf("ssh_prompt 正则无效: %w", err)
		}
	}
	if cfg.TelnetConnectTimeoutSec < 0 || cfg.TelnetLoginTimeoutSec < 0 || cfg.TelnetCommandTimeoutSec < 0 {
		return fmt.Errorf("telnet 超时配置不能为负数")
	}
	if cfg.TelnetConnectTimeoutSec > 300 || cfg.TelnetLoginTimeoutSec > 600 || cfg.TelnetCommandTimeoutSec > 3600 {
		return fmt.Errorf("telnet 超时配置超过安全上限")
	}
	if cfg.FTPConnectTimeoutSec < 0 || cfg.FTPCommandTimeoutSec < 0 || cfg.FTPTransferTimeoutSec < 0 {
		return fmt.Errorf("ftp 超时配置不能为负数")
	}
	if cfg.FTPConnectTimeoutSec > 300 || cfg.FTPCommandTimeoutSec > 600 || cfg.FTPTransferTimeoutSec > 86400 {
		return fmt.Errorf("ftp 超时配置超过安全上限")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.FTPTLSMode)) {
	case "", "plain", "explicit", "implicit":
	default:
		return fmt.Errorf("ftp_tls_mode=%q 无效；可选 plain|explicit|implicit", cfg.FTPTLSMode)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.FTPDataMode)) {
	case "", "passive", "active", "auto":
	default:
		return fmt.Errorf("ftp_data_mode=%q 无效；可选 passive|active|auto", cfg.FTPDataMode)
	}
	if raw := strings.TrimSpace(cfg.FTPActiveAddress); raw != "" && net.ParseIP(raw) == nil {
		return fmt.Errorf("ftp_active_address=%q 必须是 IP 地址", cfg.FTPActiveAddress)
	}
	if raw := strings.TrimSpace(cfg.TelnetAuthPromptRegex); raw != "" {
		if _, err := regexp.Compile(raw); err != nil {
			return fmt.Errorf("telnet_auth_prompt_regex 无效: %w", err)
		}
	}
	if cfg.LLMRetries > 10 {
		return fmt.Errorf("llm_retries 最大为 10")
	}
	if err := validateModelChain(cfg); err != nil {
		return err
	}
	if cfg.MaxSteps > 1_000 || cfg.SubAgentMaxSteps > 200 {
		return fmt.Errorf("max_steps 最大 1000，subagent_max_steps 最大 200")
	}

	targetProtocol := strings.ToLower(strings.TrimSpace(cfg.TargetProtocol))
	switch targetProtocol {
	case "", "local", "ssh", "telnet", "ftp":
	default:
		return fmt.Errorf("target_protocol=%q 无效；可选 local|ssh|telnet|ftp", cfg.TargetProtocol)
	}
	policy := strings.ToLower(strings.TrimSpace(cfg.SSHHostKeyPolicy))
	switch policy {
	case "strict", "accept-new", "insecure":
	default:
		return fmt.Errorf("ssh_host_key_policy=%q 无效；可选 strict|accept-new|insecure", cfg.SSHHostKeyPolicy)
	}
	if policy != "insecure" && strings.TrimSpace(cfg.SSHKnownHostsPath) == "" {
		return fmt.Errorf("ssh_known_hosts_path 不能为空")
	}
	if cfg.ArchiveMaxEntries < 0 || cfg.ArchiveMaxFileBytes < 0 || cfg.ArchiveMaxTotalBytes < 0 {
		return fmt.Errorf("archive 安全上限不能为负数")
	}
	if cfg.ArchiveMaxFileBytes > 0 && cfg.ArchiveMaxTotalBytes > 0 && cfg.ArchiveMaxFileBytes > cfg.ArchiveMaxTotalBytes {
		return fmt.Errorf("archive_max_file_bytes 不能大于 archive_max_total_bytes")
	}
	if raw := strings.TrimSpace(cfg.ControllerProxy); raw != "" {
		if _, err := ParseControllerProxy(raw); err != nil {
			return fmt.Errorf("controller_proxy 无效: %w", err)
		}
	}
	if tz := strings.TrimSpace(cfg.SchedulerTimezone); tz != "" && !strings.EqualFold(tz, "local") {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("scheduler_timezone=%q 无效: %w", tz, err)
		}
	}
	if err := validateTargets(cfg.Targets); err != nil {
		return err
	}
	if err := validateMCPServers(cfg.MCPServerConfigs); err != nil {
		return err
	}
	return nil
}

func validateModelChain(cfg Config) error {
	models := cfg.EffectiveModels()
	if len(models) == 0 {
		return fmt.Errorf("至少需要一个模型")
	}
	seen := make(map[string]bool, len(models))
	primary := 0
	for index, model := range models {
		if seen[model.ID] {
			return fmt.Errorf("models[%d].id=%q 重复", index, model.ID)
		}
		seen[model.ID] = true
		if strings.EqualFold(model.Role, "primary") {
			primary++
		}
		endpoint := cfg.ConfigForModel(model)
		provider := strings.ToLower(strings.TrimSpace(endpoint.Provider))
		if provider != string(ProviderCustom) {
			if _, ok := FindProvider(provider); !ok {
				return fmt.Errorf("models[%d].provider=%q 无效", index, endpoint.Provider)
			}
		}
		if strings.TrimSpace(endpoint.ModelName) == "" {
			return fmt.Errorf("models[%d].model_name 不能为空", index)
		}
		if err := validateHTTPURL(fmt.Sprintf("models[%d].api_url", index), endpoint.ApiURL); err != nil {
			return err
		}
		if model.MaxRetries < 0 || model.MaxRetries > 10 {
			return fmt.Errorf("models[%d].max_retries 必须为 0~10", index)
		}
	}
	if primary != 1 {
		return fmt.Errorf("models 必须且只能包含一个 primary，当前为 %d", primary)
	}
	allowed := map[string]bool{"rate_limit": true, "timeout": true, "server_error": true, "connection": true, "invalid_output": true}
	for _, kind := range cfg.ModelRouting.FailoverOn {
		if !allowed[strings.ToLower(strings.TrimSpace(kind))] {
			return fmt.Errorf("model_routing.failover_on 包含未知错误类型 %q", kind)
		}
	}
	return nil
}

func validateHTTPURL(name, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s 必须是有效的 HTTP(S) URL", name)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s 仅支持 http/https", name)
	}
	return nil
}

func validateTargets(targets []TargetConfig) error {
	seen := make(map[string]bool, len(targets))
	for i, target := range targets {
		protocol := strings.ToLower(strings.TrimSpace(target.Protocol))
		switch protocol {
		case "ssh", "telnet", "ftp":
		default:
			return fmt.Errorf("targets[%d].protocol=%q 无效", i, target.Protocol)
		}
		if strings.TrimSpace(target.Host) == "" {
			return fmt.Errorf("targets[%d].host 不能为空", i)
		}
		if protocol == "telnet" || protocol == "ssh" {
			if err := validateDeviceType(fmt.Sprintf("targets[%d].device_type", i), target.DeviceType); err != nil {
				return err
			}
			if protocol == "ssh" {
				if raw := strings.TrimSpace(target.Prompt); strings.HasPrefix(strings.ToLower(raw), "regex:") {
					if _, err := regexp.Compile(strings.TrimSpace(raw[len("regex:"):])); err != nil {
						return fmt.Errorf("targets[%d].prompt 正则无效: %w", i, err)
					}
				}
			}
			if protocol == "telnet" {
				if raw := strings.TrimSpace(target.AuthPromptRegex); raw != "" {
					if _, err := regexp.Compile(raw); err != nil {
						return fmt.Errorf("targets[%d].auth_prompt_regex 无效: %w", i, err)
					}
				}
			}
		}
		if protocol == "ftp" {
			switch strings.ToLower(strings.TrimSpace(target.FTPTLSMode)) {
			case "", "plain", "explicit", "implicit":
			default:
				return fmt.Errorf("targets[%d].ftp_tls_mode=%q 无效", i, target.FTPTLSMode)
			}
			switch strings.ToLower(strings.TrimSpace(target.FTPDataMode)) {
			case "", "passive", "active", "auto":
			default:
				return fmt.Errorf("targets[%d].ftp_data_mode=%q 无效", i, target.FTPDataMode)
			}
			if raw := strings.TrimSpace(target.FTPActiveAddress); raw != "" && net.ParseIP(raw) == nil {
				return fmt.Errorf("targets[%d].ftp_active_address=%q 必须是 IP 地址", i, target.FTPActiveAddress)
			}
		}
		identity := strings.ToLower(strings.TrimSpace(target.Name))
		if identity == "" {
			identity = protocol + ":" + strings.ToLower(strings.TrimSpace(target.Host))
		}
		if seen[identity] {
			return fmt.Errorf("targets 存在重复目标 %q", identity)
		}
		seen[identity] = true
	}
	return nil
}

func validateMCPServers(servers []MCPServerConfig) error {
	seen := make(map[string]bool, len(servers))
	for i, server := range servers {
		if server.Disabled {
			continue
		}
		name := strings.TrimSpace(server.Name)
		if name == "" {
			return fmt.Errorf("mcp_server_configs[%d].name 不能为空", i)
		}
		if seen[name] {
			return fmt.Errorf("mcp_server_configs 存在重复 name %q", name)
		}
		seen[name] = true
		serverType := strings.ToLower(strings.TrimSpace(server.Type))
		if serverType == "" {
			serverType = "stdio"
		}
		switch serverType {
		case "stdio":
			if strings.TrimSpace(server.Command) == "" {
				return fmt.Errorf("mcp_server_configs[%d].command 不能为空", i)
			}
		case "http", "streamable_http":
			parsed, err := url.Parse(strings.TrimSpace(server.URL))
			if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
				return fmt.Errorf("mcp_server_configs[%d].url 必须使用 HTTPS；仅 localhost/回环地址允许 HTTP", i)
			}
		default:
			return fmt.Errorf("mcp_server_configs[%d].type=%q 暂不支持；可用 stdio/http/streamable_http", i, server.Type)
		}
		if server.StartupTimeoutSec < 0 || server.StartupTimeoutSec > 300 || server.ToolTimeoutSec < 0 || server.ToolTimeoutSec > 3600 {
			return fmt.Errorf("mcp_server_configs[%d] timeout 超出允许范围", i)
		}
		if tokenEnv := strings.TrimSpace(server.BearerTokenEnvVar); tokenEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(tokenEnv) {
			return fmt.Errorf("mcp_server_configs[%d].bearer_token_env_var 不是合法环境变量名", i)
		}
		for key, value := range server.Headers {
			if strings.TrimSpace(key) == "" || strings.ContainsAny(key+value, "\r\n") {
				return fmt.Errorf("mcp_server_configs[%d].headers 包含无效请求头", i)
			}
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func validateDeviceType(field, value string) error {
	if allowedDeviceType(value) {
		return nil
	}
	return fmt.Errorf("%s=%q 无效；可选 auto|huawei|h3c|ruijie|cisco|asa|juniper|fortinet|paloalto|hillstone|sangfor|checkpoint|linux|generic 及常见别名", field, value)
}

func allowedDeviceType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "huawei", "h3c", "ruijie", "cisco", "linux", "generic",
		"juniper", "junos", "screenos", "netscreen",
		"fortinet", "fortios", "fortigate",
		"paloalto", "panos", "palo-alto",
		"hillstone", "stoneos",
		"sangfor",
		"checkpoint", "gaia",
		"asa", "cisco-asa", "pix",
		"vrp", "usg", "comware", "secpath", "rgos", "red-giant", "redgiant",
		"ios", "ios-xe", "nxos", "nx-os", "ios-xr",
		"dptech", "venustech", "topsec", "leadsec":
		return true
	default:
		return false
	}
}
