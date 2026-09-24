package config

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/net/proxy"
	"gopkg.in/yaml.v3"
)

// GlobalConfig 全局配置实例，供其他模块读取
var GlobalConfig Config

func setViperDefaults() {
	viper.SetDefault("provider", "deepseek")
	viper.SetDefault("api_protocol", "auto")
	viper.SetDefault("api_url", "https://api.deepseek.com")
	viper.SetDefault("model_name", "deepseek-flash")
	viper.SetDefault("temperature", 0.0)
	viper.SetDefault("model_profile", "auto")
	viper.SetDefault("model_parameter_b", 0.0)
	viper.SetDefault("context_window_tokens", 0)
	viper.SetDefault("context_utilization", 0.0)
	viper.SetDefault("reserved_output_tokens", 0)
	viper.SetDefault("native_tool_limit", 0)
	viper.SetDefault("vision_mode", "auto")
	viper.SetDefault("ssh_user", "root")
	viper.SetDefault("ssh_host_key_policy", "accept-new")
	viper.SetDefault("ssh_known_hosts_path", "~/.deepsentry/known_hosts")
	viper.SetDefault("ssh_device_type", "auto")
	viper.SetDefault("ssh_legacy_compat", "true")
	viper.SetDefault("ssh_connect_timeout_sec", 25)
	viper.SetDefault("telnet_user", "root")
	viper.SetDefault("telnet_device_type", "auto")
	viper.SetDefault("telnet_connect_timeout_sec", 10)
	viper.SetDefault("telnet_login_timeout_sec", 20)
	viper.SetDefault("telnet_command_timeout_sec", 90)
	viper.SetDefault("ftp_user", "anonymous")
	viper.SetDefault("ftp_tls_mode", "plain")
	viper.SetDefault("ftp_data_mode", "passive")
	viper.SetDefault("ftp_connect_timeout_sec", 10)
	viper.SetDefault("ftp_command_timeout_sec", 30)
	viper.SetDefault("ftp_transfer_timeout_sec", 90)
	viper.SetDefault("use_native_tools", true)
	viper.SetDefault("agent_runtime", "v3")
	viper.SetDefault("trace_enabled", true)
	viper.SetDefault("trace_dir", "reports/traces")
	viper.SetDefault("terminal_theme", "auto")
	viper.SetDefault("llm_timeout_sec", 120)
	viper.SetDefault("llm_retries", 3)
	viper.SetDefault("ssh_command_timeout_sec", 90)
	viper.SetDefault("ssh_max_output_bytes", 512*1024)
	viper.SetDefault("execution_budget.enabled", true)
	viper.SetDefault("execution_budget.max_steps", 180)
	viper.SetDefault("execution_budget.subagent_max_steps", 90)
	viper.SetDefault("execution_budget.total_steps", 300)
	viper.SetDefault("execution_budget.extension_steps", 15)
	viper.SetDefault("execution_budget.no_progress_steps", 8)
	viper.SetDefault("max_steps", 30)
	viper.SetDefault("subagent_max_steps", 15)
	viper.SetDefault("controller_proxy", "")
	viper.SetDefault("browser_timeout_sec", 20)
	viper.SetDefault("browser_artifact_dir", "reports/browser")
	viper.SetDefault("archive_max_entries", 10_000)
	viper.SetDefault("archive_max_file_bytes", int64(512*1024*1024))
	viper.SetDefault("archive_max_total_bytes", int64(2*1024*1024*1024))
	viper.SetDefault("scheduler_enabled", true)
	viper.SetDefault("scheduler_store", "reports/schedules/tasks.json")
	viper.SetDefault("scheduler_interval_sec", 30)
	viper.SetDefault("scheduler_timezone", "Local")
	viper.SetDefault("dingtalk_webhook", "")
	viper.SetDefault("dingtalk_secret", "")
	viper.SetDefault("feishu_webhook", "")
	viper.SetDefault("feishu_secret", "")
	viper.SetDefault("email_gateway_url", "")
	viper.SetDefault("email_gateway_token", "")
	viper.SetDefault("email_gateway_header", "Authorization")
	viper.SetDefault("email_to", "")
	viper.SetDefault("email_from", "")
	viper.SetDefault("benchmark_base_url", "")
	viper.SetDefault("benchmark_token", "")
}

// Config 结构体定义
type Config struct {
	Chat ChatConfig `mapstructure:"chat"`

	// --- LLM 配置 ---
	Provider    string  `mapstructure:"provider"`     // openai|anthropic|google|deepseek|qwen|hunyuan|teleai|minimax|mimo|glm|custom
	APIProtocol string  `mapstructure:"api_protocol"` // openai_chat|anthropic_messages|openai_responses|auto
	ApiURL      string  `mapstructure:"api_url"`
	ModelName   string  `mapstructure:"model_name"`
	ApiKey      string  `mapstructure:"api_key"`
	Temperature float64 `mapstructure:"temperature"`

	// --- Model capability / context adaptation ---
	// Zero values use conservative auto-detection. Local runtimes should set
	// context_window_tokens to their actual num_ctx/max_model_len for best use.
	ModelProfile              string  `mapstructure:"model_profile"` // auto|compact|balanced|full
	ModelParameterB           float64 `mapstructure:"model_parameter_b"`
	ContextWindowTokens       int     `mapstructure:"context_window_tokens"`
	LocalContextDiscovered    bool    `mapstructure:"-" json:"-" yaml:"-"`
	LocalNativeToolsAvailable bool    `mapstructure:"-" json:"-" yaml:"-"`
	ContextUtilization        float64 `mapstructure:"context_utilization"`
	ReservedOutputTokens      int     `mapstructure:"reserved_output_tokens"`
	NativeToolLimit           int     `mapstructure:"native_tool_limit"`
	VisionMode                string  `mapstructure:"vision_mode"` // auto|enabled|disabled

	// Runtime v3 is additive and can be rolled back to legacy without changing
	// model or target configuration.
	AgentRuntime  string             `mapstructure:"agent_runtime"`
	Models        []ModelConfig      `mapstructure:"models"`
	ModelRouting  ModelRoutingConfig `mapstructure:"model_routing"`
	TraceEnabled  bool               `mapstructure:"trace_enabled"`
	TraceDir      string             `mapstructure:"trace_dir"`
	TerminalTheme string             `mapstructure:"terminal_theme"` // auto|dark|light

	LLMTimeoutSec        int                   `mapstructure:"llm_timeout_sec"`
	LLMRetries           int                   `mapstructure:"llm_retries"`
	SSHCommandTimeoutSec int                   `mapstructure:"ssh_command_timeout_sec"`
	SSHMaxOutputBytes    int                   `mapstructure:"ssh_max_output_bytes"`
	ExecutionBudget      ExecutionBudgetConfig `mapstructure:"execution_budget"`
	MaxSteps             int                   `mapstructure:"max_steps"`
	SubAgentMaxSteps     int                   `mapstructure:"subagent_max_steps"`

	// --- SSH 配置 ---
	TargetProtocol           string         `mapstructure:"target_protocol"` // local|ssh|telnet|ftp，空值兼容旧 ssh_host
	SSHHost                  string         `mapstructure:"ssh_host"`
	SSHUser                  string         `mapstructure:"ssh_user"`
	SSHPassword              string         `mapstructure:"ssh_password"`
	SSHKeyPath               string         `mapstructure:"ssh_key_path"`
	SSHKeyPassphrase         string         `mapstructure:"ssh_key_passphrase"`
	SSHHostKeyPolicy         string         `mapstructure:"ssh_host_key_policy"` // strict|accept-new|insecure
	SSHKnownHostsPath        string         `mapstructure:"ssh_known_hosts_path"`
	SSHLegacyCompat          string         `mapstructure:"ssh_legacy_compat"` // 默认 true：兼容 ssh-rsa / DH-SHA1 / CBC
	SSHConnectTimeoutSec     int            `mapstructure:"ssh_connect_timeout_sec"`
	SSHDeviceType            string         `mapstructure:"ssh_device_type"` // auto|huawei|h3c|ruijie|cisco|juniper|fortinet|...
	SSHPrompt                string         `mapstructure:"ssh_prompt"`      // 网络设备交互 CLI prompt；可空自动探测
	SSHEnablePassword        string         `mapstructure:"ssh_enable_password"`
	TelnetHost               string         `mapstructure:"telnet_host"`
	TelnetUser               string         `mapstructure:"telnet_user"`
	TelnetPassword           string         `mapstructure:"telnet_password"`
	TelnetPrompt             string         `mapstructure:"telnet_prompt"` // 登录后的命令提示符；不参与用户名/密码阶段
	TelnetAuthPromptRegex    string         `mapstructure:"telnet_auth_prompt_regex"`
	TelnetDeviceType         string         `mapstructure:"telnet_device_type"` // auto|huawei|h3c|ruijie|cisco|linux
	TelnetEnablePassword     string         `mapstructure:"telnet_enable_password"`
	TelnetConnectTimeoutSec  int            `mapstructure:"telnet_connect_timeout_sec"`
	TelnetLoginTimeoutSec    int            `mapstructure:"telnet_login_timeout_sec"`
	TelnetCommandTimeoutSec  int            `mapstructure:"telnet_command_timeout_sec"`
	FTPHost                  string         `mapstructure:"ftp_host"`
	FTPUser                  string         `mapstructure:"ftp_user"`
	FTPPassword              string         `mapstructure:"ftp_password"`
	FTPTLSMode               string         `mapstructure:"ftp_tls_mode"` // plain|explicit|implicit
	FTPTLSServerName         string         `mapstructure:"ftp_tls_server_name"`
	FTPTLSCAFile             string         `mapstructure:"ftp_tls_ca_file"`
	FTPTLSInsecureSkipVerify bool           `mapstructure:"ftp_tls_insecure_skip_verify"`
	FTPDataMode              string         `mapstructure:"ftp_data_mode"` // passive|active|auto
	FTPActiveAddress         string         `mapstructure:"ftp_active_address"`
	FTPConnectTimeoutSec     int            `mapstructure:"ftp_connect_timeout_sec"`
	FTPCommandTimeoutSec     int            `mapstructure:"ftp_command_timeout_sec"`
	FTPTransferTimeoutSec    int            `mapstructure:"ftp_transfer_timeout_sec"`
	Targets                  []TargetConfig `mapstructure:"targets"`

	// --- Deep Agent Harness ---
	UseNativeTools       bool              `mapstructure:"use_native_tools"`
	EnabledTools         []string          `mapstructure:"enabled_tools"`
	DisabledTools        []string          `mapstructure:"disabled_tools"`
	SkillSources         []string          `mapstructure:"skill_sources"`
	DisabledSkillSources []string          `mapstructure:"disabled_skill_sources"`
	DisabledSkills       []string          `mapstructure:"disabled_skills"`
	SkillsDisabled       bool              `mapstructure:"skills_disabled"`
	MCPServers           []string          `mapstructure:"mcp_servers"`
	MCPServerConfigs     []MCPServerConfig `mapstructure:"mcp_server_configs"`

	// --- Controller browser runtime ---
	ControllerProxy      string `mapstructure:"controller_proxy"`
	BrowserBinary        string `mapstructure:"browser_binary"`
	BrowserTimeoutSec    int    `mapstructure:"browser_timeout_sec"`
	BrowserArtifactDir   string `mapstructure:"browser_artifact_dir"`
	ArchiveMaxEntries    int    `mapstructure:"archive_max_entries"`
	ArchiveMaxFileBytes  int64  `mapstructure:"archive_max_file_bytes"`
	ArchiveMaxTotalBytes int64  `mapstructure:"archive_max_total_bytes"`

	// --- Controller scheduler / notifications ---
	Inspection           InspectionConfig `mapstructure:"inspection"`
	SchedulerEnabled     bool             `mapstructure:"scheduler_enabled"`
	SchedulerStore       string           `mapstructure:"scheduler_store"`
	SchedulerIntervalSec int              `mapstructure:"scheduler_interval_sec"`
	SchedulerTimezone    string           `mapstructure:"scheduler_timezone"`
	DingTalkWebhook      string           `mapstructure:"dingtalk_webhook"`
	DingTalkSecret       string           `mapstructure:"dingtalk_secret"`
	FeishuWebhook        string           `mapstructure:"feishu_webhook"`
	FeishuSecret         string           `mapstructure:"feishu_secret"`
	EmailGatewayURL      string           `mapstructure:"email_gateway_url"`
	EmailGatewayToken    string           `mapstructure:"email_gateway_token"`
	EmailGatewayHeader   string           `mapstructure:"email_gateway_header"`
	EmailTo              string           `mapstructure:"email_to"`
	EmailFrom            string           `mapstructure:"email_from"`

	// --- Benchmark platform integrations ---
	BenchmarkBaseURL string `mapstructure:"benchmark_base_url"`
	BenchmarkToken   string `mapstructure:"benchmark_token"`
}

// EffectiveAgentRuntime returns the runtime used when callers construct a
// Config directly instead of loading it through Viper. Runtime v3 is the
// default; legacy remains an explicit rollback mode for one release cycle.
func (c Config) EffectiveAgentRuntime() string {
	mode := strings.ToLower(strings.TrimSpace(c.AgentRuntime))
	if mode == "" {
		return "v3"
	}
	return mode
}

// ModelConfig describes one primary or fallback model. Empty tuning fields
// inherit the legacy top-level configuration so existing config.yaml files
// remain valid.
type ModelConfig struct {
	ID                   string  `mapstructure:"id" json:"id" yaml:"id"`
	Role                 string  `mapstructure:"role" json:"role" yaml:"role"` // primary | fallback
	Provider             string  `mapstructure:"provider" json:"provider" yaml:"provider"`
	APIProtocol          string  `mapstructure:"api_protocol" json:"api_protocol" yaml:"api_protocol"`
	APIURL               string  `mapstructure:"api_url" json:"api_url" yaml:"api_url"`
	APIKey               string  `mapstructure:"api_key" json:"api_key" yaml:"api_key"`
	ModelName            string  `mapstructure:"model_name" json:"model_name" yaml:"model_name"`
	Temperature          float64 `mapstructure:"temperature" json:"temperature" yaml:"temperature"`
	ModelProfile         string  `mapstructure:"model_profile" json:"model_profile" yaml:"model_profile"`
	ContextWindowTokens  int     `mapstructure:"context_window_tokens" json:"context_window_tokens" yaml:"context_window_tokens"`
	ReservedOutputTokens int     `mapstructure:"reserved_output_tokens" json:"reserved_output_tokens" yaml:"reserved_output_tokens"`
	NativeToolLimit      int     `mapstructure:"native_tool_limit" json:"native_tool_limit" yaml:"native_tool_limit"`
	VisionMode           string  `mapstructure:"vision_mode" json:"vision_mode" yaml:"vision_mode"`
	MaxRetries           int     `mapstructure:"max_retries" json:"max_retries" yaml:"max_retries"`
}

type ModelRoutingConfig struct {
	FailoverOn []string `mapstructure:"failover_on" json:"failover_on" yaml:"failover_on"`
}

// EffectiveModels returns a deterministic primary-first model chain. When the
// additive models list is absent, the legacy top-level model becomes primary.
func (c Config) EffectiveModels() []ModelConfig {
	if len(c.Models) == 0 {
		return []ModelConfig{{
			ID:                   firstNonEmptyConfig(c.ModelName, "primary"),
			Role:                 "primary",
			Provider:             c.Provider,
			APIProtocol:          c.APIProtocol,
			APIURL:               c.ApiURL,
			APIKey:               c.ApiKey,
			ModelName:            c.ModelName,
			Temperature:          c.Temperature,
			ModelProfile:         c.ModelProfile,
			ContextWindowTokens:  c.ContextWindowTokens,
			ReservedOutputTokens: c.ReservedOutputTokens,
			NativeToolLimit:      c.NativeToolLimit,
			VisionMode:           c.VisionMode,
			MaxRetries:           c.EffectiveLLMRetries(),
		}}
	}
	primary := make([]ModelConfig, 0, len(c.Models))
	fallback := make([]ModelConfig, 0, len(c.Models))
	for index, model := range c.Models {
		model = mergeModelConfig(c, model, index)
		if strings.EqualFold(model.Role, "primary") || (strings.TrimSpace(model.Role) == "" && len(primary) == 0) {
			model.Role = "primary"
			primary = append(primary, model)
		} else {
			model.Role = "fallback"
			fallback = append(fallback, model)
		}
	}
	return append(primary, fallback...)
}

func mergeModelConfig(base Config, model ModelConfig, index int) ModelConfig {
	model.Provider = firstNonEmptyConfig(model.Provider, base.Provider)
	model.APIProtocol = firstNonEmptyConfig(model.APIProtocol, base.APIProtocol)
	model.APIURL = firstNonEmptyConfig(model.APIURL, base.ApiURL)
	model.APIKey = firstNonEmptyConfig(model.APIKey, base.ApiKey)
	model.ModelName = firstNonEmptyConfig(model.ModelName, base.ModelName)
	model.ModelProfile = firstNonEmptyConfig(model.ModelProfile, base.ModelProfile)
	model.VisionMode = firstNonEmptyConfig(model.VisionMode, base.VisionMode, "auto")
	if model.Temperature == 0 {
		model.Temperature = base.Temperature
	}
	if model.ContextWindowTokens == 0 {
		model.ContextWindowTokens = base.ContextWindowTokens
	}
	if model.ReservedOutputTokens == 0 {
		model.ReservedOutputTokens = base.ReservedOutputTokens
	}
	if model.NativeToolLimit == 0 {
		model.NativeToolLimit = base.NativeToolLimit
	}
	if model.MaxRetries == 0 {
		model.MaxRetries = base.EffectiveLLMRetries()
	}
	if strings.TrimSpace(model.ID) == "" {
		model.ID = fmt.Sprintf("model_%d_%s", index+1, model.ModelName)
	}
	return model
}

// ConfigForModel materializes a model endpoint into the existing provider
// configuration consumed by legacy adapters.
func (c Config) ConfigForModel(model ModelConfig) Config {
	out := c
	out.Provider = model.Provider
	out.APIProtocol = model.APIProtocol
	out.ApiURL = model.APIURL
	out.ApiKey = model.APIKey
	out.ModelName = model.ModelName
	out.Temperature = model.Temperature
	out.ModelProfile = model.ModelProfile
	out.ContextWindowTokens = model.ContextWindowTokens
	out.ReservedOutputTokens = model.ReservedOutputTokens
	out.NativeToolLimit = model.NativeToolLimit
	out.VisionMode = model.VisionMode
	out.LLMRetries = model.MaxRetries
	out.Models = nil
	ApplyProviderDefaults(&out)
	return out
}

func firstNonEmptyConfig(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// EffectiveDisabledSkills returns the name denylist consumed by the live
// catalog. "*" is an internal sentinel for the global /skill off switch.
func (c Config) EffectiveDisabledSkills() []string {
	out := append([]string(nil), c.DisabledSkills...)
	if c.SkillsDisabled {
		out = append(out, "*")
	}
	return out
}

type TargetConfig struct {
	Name                     string   `mapstructure:"name" json:"name"`
	Protocol                 string   `mapstructure:"protocol" json:"protocol"` // ssh|telnet|ftp
	Host                     string   `mapstructure:"host" json:"host"`
	User                     string   `mapstructure:"user" json:"user"`
	Password                 string   `mapstructure:"password" json:"password"`
	KeyPath                  string   `mapstructure:"key_path" json:"key_path"`
	KeyPassphrase            string   `mapstructure:"key_passphrase" json:"key_passphrase"`
	Prompt                   string   `mapstructure:"prompt" json:"prompt"`
	AuthPromptRegex          string   `mapstructure:"auth_prompt_regex" json:"auth_prompt_regex"`
	DeviceType               string   `mapstructure:"device_type" json:"device_type"`
	EnablePassword           string   `mapstructure:"enable_password" json:"enable_password"`
	FTPTLSMode               string   `mapstructure:"ftp_tls_mode" json:"ftp_tls_mode"`
	FTPTLSServerName         string   `mapstructure:"ftp_tls_server_name" json:"ftp_tls_server_name"`
	FTPTLSCAFile             string   `mapstructure:"ftp_tls_ca_file" json:"ftp_tls_ca_file"`
	FTPTLSInsecureSkipVerify bool     `mapstructure:"ftp_tls_insecure_skip_verify" json:"ftp_tls_insecure_skip_verify"`
	FTPDataMode              string   `mapstructure:"ftp_data_mode" json:"ftp_data_mode"`
	FTPActiveAddress         string   `mapstructure:"ftp_active_address" json:"ftp_active_address"`
	Tags                     []string `mapstructure:"tags" json:"tags"`
}

type MCPServerConfig struct {
	Name              string            `mapstructure:"name" json:"name" yaml:"name"`
	Type              string            `mapstructure:"type" json:"type" yaml:"type"`
	Command           string            `mapstructure:"command" json:"command" yaml:"command"`
	Args              []string          `mapstructure:"args" json:"args" yaml:"args"`
	Env               map[string]string `mapstructure:"env" json:"env" yaml:"env"`
	CWD               string            `mapstructure:"cwd" json:"cwd" yaml:"cwd"`
	URL               string            `mapstructure:"url" json:"url" yaml:"url"`
	Headers           map[string]string `mapstructure:"headers" json:"headers" yaml:"headers"`
	BearerTokenEnvVar string            `mapstructure:"bearer_token_env_var" json:"bearer_token_env_var" yaml:"bearer_token_env_var"`
	EnabledTools      []string          `mapstructure:"enabled_tools" json:"enabled_tools" yaml:"enabled_tools"`
	DisabledTools     []string          `mapstructure:"disabled_tools" json:"disabled_tools" yaml:"disabled_tools"`
	StartupTimeoutSec int               `mapstructure:"startup_timeout_sec" json:"startup_timeout_sec" yaml:"startup_timeout_sec"`
	ToolTimeoutSec    int               `mapstructure:"tool_timeout_sec" json:"tool_timeout_sec" yaml:"tool_timeout_sec"`
	Required          bool              `mapstructure:"required" json:"required" yaml:"required"`
	Disabled          bool              `mapstructure:"disabled" json:"disabled" yaml:"disabled"`
}

// InitConfig 初始化配置 (核心加载逻辑)
func InitConfig(cfgFile string) error {
	setViperDefaults()
	if cfgFile != "" {
		// 1. 如果用户通过命令行指定了文件，直接使用
		viper.SetConfigFile(cfgFile)
	} else {
		// 2. 否则按顺序搜索默认路径
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		// 搜索路径优先级：
		// 1. 当前目录 (.)
		viper.AddConfigPath(".")
		// 2. 可执行文件所在目录（从其他 cwd 启动已打包二进制时仍能找到随包配置）
		if executable, execErr := os.Executable(); execErr == nil {
			viper.AddConfigPath(filepath.Dir(executable))
		}
		// 3. 用户主目录下的 .deepsentry 文件夹
		viper.AddConfigPath(filepath.Join(home, ".deepsentry"))
		// 4. 系统级配置 /etc/deepsentry
		viper.AddConfigPath("/etc/deepsentry")

		viper.SetConfigName("config") // 查找 config.yaml, config.json 等
		viper.SetConfigType("yaml")   // 默认以 yaml 格式解析
	}

	// 3. 开启环境变量自动覆盖
	// 例如: export DEEPSENTRY_API_KEY="xxx" 会自动覆盖配置文件中的 api_key
	viper.SetEnvPrefix("DEEPSENTRY")
	viper.AutomaticEnv()

	// 4. 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		// 如果只是没找到文件，返回特定错误类型，以便 main.go 决定是否进入向导
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			return err
		}
		// 其他错误（如 YAML 格式错误）直接返回
		return fmt.Errorf("配置文件读取错误: %w", err)
	}

	// 5. 将读取到的配置映射到全新结构体，校验后原子替换
	var loaded Config
	if err := viper.Unmarshal(&loaded); err != nil {
		return fmt.Errorf("配置解析失败: %w", err)
	}
	ApplyProviderDefaults(&loaded)
	loaded.AgentRuntime = loaded.EffectiveAgentRuntime()
	if err := applyRawCaseSensitiveConfig(viper.ConfigFileUsed(), &loaded); err != nil {
		return fmt.Errorf("读取大小写敏感配置失败: %w", err)
	}
	if err := ValidateRuntimeConfig(loaded); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	GlobalConfig = loaded
	return nil
}

func (c Config) EffectiveArchiveLimits() (entries int, fileBytes, totalBytes int64) {
	entries = c.ArchiveMaxEntries
	if entries <= 0 {
		entries = 10_000
	}
	fileBytes = c.ArchiveMaxFileBytes
	if fileBytes <= 0 {
		fileBytes = 512 * 1024 * 1024
	}
	totalBytes = c.ArchiveMaxTotalBytes
	if totalBytes <= 0 {
		totalBytes = 2 * 1024 * 1024 * 1024
	}
	if totalBytes < fileBytes {
		fileBytes = totalBytes
	}
	return entries, fileBytes, totalBytes
}

func applyRawCaseSensitiveConfig(path string, cfg *Config) error {
	if strings.TrimSpace(path) == "" || cfg == nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return applyRawCaseSensitiveData(data, cfg)
}

func applyRawCaseSensitiveData(data []byte, cfg *Config) error {
	if cfg == nil {
		return nil
	}
	var raw struct {
		MCPServerConfigs []MCPServerConfig `yaml:"mcp_server_configs"`
		BenchmarkBaseURL string            `yaml:"BENCHMARK_BASE_URL"`
		BenchmarkToken   string            `yaml:"BENCHMARK_TOKEN"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.MCPServerConfigs != nil {
		cfg.MCPServerConfigs = raw.MCPServerConfigs
	}
	if strings.TrimSpace(raw.BenchmarkBaseURL) != "" {
		cfg.BenchmarkBaseURL = strings.TrimSpace(raw.BenchmarkBaseURL)
	}
	if strings.TrimSpace(raw.BenchmarkToken) != "" {
		cfg.BenchmarkToken = strings.TrimSpace(raw.BenchmarkToken)
	}
	return nil
}

// ResolveStartupProxy validates fscan-style startup proxy flags. The two flags
// are intentionally mutually exclusive because DeepSentry has one controller
// egress route per process.
func ResolveStartupProxy(httpProxy, socks5Proxy string) (string, error) {
	httpProxy = strings.TrimSpace(httpProxy)
	socks5Proxy = strings.TrimSpace(socks5Proxy)
	if httpProxy != "" && socks5Proxy != "" {
		return "", fmt.Errorf("-proxy 与 -socks5 不能同时使用")
	}
	raw := httpProxy
	want := "http"
	if socks5Proxy != "" {
		raw = socks5Proxy
		want = "socks5"
	}
	if raw == "" {
		return "", nil
	}
	u, err := ParseControllerProxy(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	if want == "http" && scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("-proxy 仅接受 http:// 或 https://，SOCKS5 请使用 -socks5")
	}
	if want == "socks5" && scheme != "socks5" && scheme != "socks5h" {
		return "", fmt.Errorf("-socks5 仅接受 socks5:// 或 socks5h://")
	}
	return u.String(), nil
}

// ParseControllerProxy validates a controller egress proxy URL.
func ParseControllerProxy(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("代理 URL 无效，格式示例: http://127.0.0.1:8080 或 socks5://127.0.0.1:1080")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("代理仅支持 http|https|socks5|socks5h")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("代理端口无效: %q", u.Port())
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("代理 URL 不能包含路径、查询参数或 fragment")
	}
	return u, nil
}

// ControllerProxySummary returns a credential-free value suitable for UI and
// logs. Proxy credentials must never be copied into the model context.
func ControllerProxySummary(raw string) string {
	u, err := ParseControllerProxy(raw)
	if err != nil {
		return "invalid"
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host
}

// ControllerDialContext opens a controller-side TCP connection through the
// configured HTTP CONNECT or SOCKS5 proxy. With no explicit proxy it dials
// directly; environment HTTP_PROXY variables remain HTTP-only by design.
func ControllerDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	raw := strings.TrimSpace(GlobalConfig.ControllerProxy)
	if raw == "" {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	u, err := ParseControllerProxy(raw)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{})
		if err != nil {
			return nil, fmt.Errorf("创建 SOCKS5 dialer 失败: %w", err)
		}
		if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
			return contextDialer.DialContext(ctx, network, addr)
		}
		return dialWithContextFallback(ctx, dialer, network, addr)
	case "http", "https":
		return dialHTTPConnectProxy(ctx, u, addr)
	default:
		return nil, fmt.Errorf("不支持的代理协议: %s", u.Scheme)
	}
}

func dialWithContextFallback(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := dialer.Dial(network, addr)
		ch <- result{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case got := <-ch:
		return got.conn, got.err
	}
}

type bufferedProxyConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedProxyConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func dialHTTPConnectProxy(ctx context.Context, proxyURL *url.URL, target string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, fmt.Errorf("连接代理 %s 失败: %w", ControllerProxySummary(proxyURL.String()), err)
	}
	if strings.EqualFold(proxyURL.Scheme, "https") {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: proxyURL.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("HTTPS 代理 TLS 握手失败: %w", err)
		}
		conn = tlsConn
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	req.Header.Set("User-Agent", "DeepSentry")
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("发送 HTTP CONNECT 失败: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取 HTTP CONNECT 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("HTTP CONNECT 被代理拒绝: %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedProxyConn{Conn: conn, reader: reader}, nil
}

// ControllerDialTimeout is the timeout-aware convenience used by native TCP
// tools and SSH/Telnet/FTP executors.
func ControllerDialTimeout(network, addr string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return ControllerDialContext(ctx, network, addr)
}

// HTTPTransport returns a fresh controller-side transport honoring the same
// startup/config proxy route as native TCP tools.
func HTTPTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	raw := strings.TrimSpace(GlobalConfig.ControllerProxy)
	if raw == "" {
		return tr
	}
	u, err := ParseControllerProxy(raw)
	if err != nil {
		return tr
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		tr.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		tr.Proxy = nil
		tr.DialContext = ControllerDialContext
	}
	return tr
}

// HTTPClient returns a controller-side HTTP client honoring controller_proxy.
// Supported explicit proxy schemes: http, https, socks5, socks5h.
// Empty controller_proxy falls back to the standard environment proxy behavior.
func HTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: HTTPTransport()}
}

// SaveConfig 将当前 Viper 中的配置保存到文件 (默认保存到当前目录)
func SaveConfig() error {
	// 确保默认保存为 yaml 格式
	viper.SetConfigType("yaml")
	// 保存到当前目录下的 config.yaml
	return WriteConfigAsPrivate("config.yaml")
}

func WriteConfigAsPrivate(path string) error {
	if err := viper.WriteConfigAs(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
