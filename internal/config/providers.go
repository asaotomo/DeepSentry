package config

import "strings"

// Provider 预设 LLM 厂商
type Provider string

const (
	ProviderOpenAI     Provider = "openai"
	ProviderAnthropic  Provider = "anthropic"
	ProviderGoogle     Provider = "google"
	ProviderDeepSeek   Provider = "deepseek"
	ProviderQwen       Provider = "qwen"
	ProviderQianfan    Provider = "qianfan"
	ProviderVolcengine Provider = "volcengine"
	ProviderMiniMax    Provider = "minimax"
	ProviderMimo       Provider = "mimo"
	ProviderGLM        Provider = "glm"
	ProviderHunyuan    Provider = "hunyuan"
	ProviderTencentHY  Provider = "tencent_hy"
	ProviderTeleAI     Provider = "teleai"
	ProviderCTYun      Provider = "ctyun"
	ProviderOllama     Provider = "ollama"
	ProviderLMStudio   Provider = "lmstudio"
	ProviderXAI        Provider = "xai"
	ProviderGrok       Provider = "grok"
	ProviderCustom     Provider = "custom"
)

const (
	ProtocolAuto              = "auto"
	ProtocolOpenAIChat        = "openai_chat"
	ProtocolAnthropicMessages = "anthropic_messages"
	ProtocolOpenAIResponses   = "openai_responses"
)

// ProviderPreset 厂商默认 endpoint 与模型
type ProviderPreset struct {
	ID          Provider
	DisplayName string
	APIURL      string
	Model       string
	AuthStyle   string // bearer | x-api-key
	Protocol    string
	NativeTools bool
}

// ModelPreset is a deliberately small capability catalog for vendor model IDs
// whose context and image support are documented. Exact IDs are preferred over
// fuzzy name matching so MCP screenshots are never sent to a known text-only
// model by accident. Unknown/custom models still use vision_mode and the legacy
// conservative heuristics.
type ModelPreset struct {
	Provider            Provider
	ID                  string
	ContextWindowTokens int
	SupportsVision      bool
}

// AllProviders contains convenience API/model presets. They are compatibility
// defaults rather than a promise that a provider still labels them "latest";
// operators can always override endpoint and model in config.
var AllProviders = []ProviderPreset{
	{
		ID: ProviderOpenAI, DisplayName: "OpenAI",
		APIURL: "https://api.openai.com/v1", Model: "gpt-6-astra",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIResponses, NativeTools: false,
	},
	{
		ID: ProviderAnthropic, DisplayName: "Anthropic Claude",
		APIURL: "https://api.anthropic.com/v1", Model: "claude-fable-5-1",
		AuthStyle: "x-api-key", Protocol: ProtocolAnthropicMessages, NativeTools: false,
	},
	{
		ID: ProviderGoogle, DisplayName: "Google Gemini",
		APIURL: "https://generativelanguage.googleapis.com/v1beta/openai", Model: "gemini-3.8-flash",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderDeepSeek, DisplayName: "DeepSeek",
		APIURL: "https://api.deepseek.com", Model: "deepseek-flash",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderQwen, DisplayName: "Alibaba Qwen / DashScope",
		APIURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen3.8-max",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderQianfan, DisplayName: "百度千帆 Coding Plan",
		APIURL: "https://qianfan.baidubce.com/v2/coding", Model: "qianfan-code-latest",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderVolcengine, DisplayName: "火山方舟 Coding Plan",
		APIURL: "https://ark.cn-beijing.volces.com/api/coding/v3", Model: "ark-code-latest",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderMiniMax, DisplayName: "MiniMax",
		APIURL: "https://api.minimax.cn/v1", Model: "MiniMax-M3",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderMimo, DisplayName: "Xiaomi MiMo Token Plan / MiMo Claw",
		APIURL: "https://token-plan-cn.xiaomimimo.com/v1", Model: "mimo-v2.5",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderGLM, DisplayName: "智谱 GLM",
		APIURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-5.3-flash",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderHunyuan, DisplayName: "腾讯混元 Hunyuan / TokenHub",
		APIURL: "https://tokenhub.tencentmaas.com/v1", Model: "hy4-preview",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderTencentHY, DisplayName: "Tencent HY (alias)",
		APIURL: "https://tokenhub.tencentmaas.com/v1", Model: "hy4-preview",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderTeleAI, DisplayName: "中国电信星辰 / TeleAI",
		APIURL: "https://wishub-x6.ctyun.cn/coding/v1", Model: "GLM-5-Pro",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderCTYun, DisplayName: "天翼云息壤 TokenHub (alias)",
		APIURL: "https://wishub-x6.ctyun.cn/coding/v1", Model: "GLM-5-Pro",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderOllama, DisplayName: "Ollama (本地)",
		APIURL: "http://localhost:11434/v1", Model: "llama3.3",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: false,
	},
	{
		ID: ProviderLMStudio, DisplayName: "LM Studio (本地)",
		APIURL: "http://localhost:1234/v1", Model: "local-model",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: false,
	},
	{
		ID: ProviderXAI, DisplayName: "xAI Grok",
		APIURL: "https://api.x.ai/v1", Model: "grok-4.6",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
	{
		ID: ProviderGrok, DisplayName: "Grok (alias)",
		APIURL: "https://api.x.ai/v1", Model: "grok-4.6",
		AuthStyle: "bearer", Protocol: ProtocolOpenAIChat, NativeTools: true,
	},
}

// AllModelPresets tracks the current documented IDs used by the built-in
// Chinese providers. Historical IDs that are still served remain available so
// existing configs get correct routing without becoming the new default.
var AllModelPresets = []ModelPreset{
	{Provider: ProviderDeepSeek, ID: "deepseek-flash", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderDeepSeek, ID: "deepseek-v4.1-flash", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderDeepSeek, ID: "deepseek-v4.1-flash-expires-on-0910", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderDeepSeek, ID: "deepseek-v4-flash-vision-exp", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderDeepSeek, ID: "deepseek-v4-flash", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderDeepSeek, ID: "deepseek-v4-pro", ContextWindowTokens: 1_000_000},
	{Provider: ProviderGLM, ID: "glm-5.3-flash", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderGLM, ID: "glm-5.3", ContextWindowTokens: 1_000_000},
	{Provider: ProviderMiniMax, ID: "MiniMax-M3", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderMiniMax, ID: "MiniMax-M2.7", ContextWindowTokens: 204_800},
	{Provider: ProviderMiniMax, ID: "MiniMax-M2.7-highspeed", ContextWindowTokens: 204_800},
	{Provider: ProviderMiniMax, ID: "MiniMax-M2.5", ContextWindowTokens: 204_800},
	{Provider: ProviderMiniMax, ID: "MiniMax-M2.5-highspeed", ContextWindowTokens: 204_800},
	{Provider: ProviderMimo, ID: "mimo-v2.5", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderMimo, ID: "mimo-v2.5-pro", ContextWindowTokens: 1_000_000},
	{Provider: ProviderMimo, ID: "mimo-v2.5-pro-ultraspeed", ContextWindowTokens: 1_000_000},
	{Provider: ProviderOpenAI, ID: "gpt-6-astra", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-6", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "chatgpt-6", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "chatgpt6", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-6-sol", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-6-terra", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-6-luna", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-sol", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-terra", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-luna", ContextWindowTokens: 1_050_000, SupportsVision: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.5", ContextWindowTokens: 400_000, SupportsVision: true},
	{Provider: ProviderAnthropic, ID: "claude-opus-5", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-5", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderAnthropic, ID: "claude-fable-5-1", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderAnthropic, ID: "claude-fable-5", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderAnthropic, ID: "claude-haiku-4-5", ContextWindowTokens: 200_000, SupportsVision: true},
	{Provider: ProviderAnthropic, ID: "claude-haiku-4-5-20251001", ContextWindowTokens: 200_000, SupportsVision: true},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-8", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderGoogle, ID: "gemini-3.8-flash", ContextWindowTokens: 1_048_576, SupportsVision: true},
	{Provider: ProviderGoogle, ID: "gemini-3.7-flash", ContextWindowTokens: 1_048_576, SupportsVision: true},
	{Provider: ProviderGoogle, ID: "gemini-3.6-flash", ContextWindowTokens: 1_048_576, SupportsVision: true},
	{Provider: ProviderGoogle, ID: "gemini-3.5-flash", ContextWindowTokens: 1_048_576, SupportsVision: true},
	{Provider: ProviderGoogle, ID: "gemini-3.5-flash-lite", ContextWindowTokens: 1_048_576, SupportsVision: true},
	{Provider: ProviderQwen, ID: "qwen3.8-max", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderQwen, ID: "qwen3.8-flash", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderQwen, ID: "qwen3.7-plus", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderHunyuan, ID: "hy4-preview", ContextWindowTokens: 1_000_000},
	{Provider: ProviderHunyuan, ID: "hy3", ContextWindowTokens: 256_000},
	{Provider: ProviderXAI, ID: "grok-4.6", ContextWindowTokens: 500_000, SupportsVision: true},
	{Provider: ProviderXAI, ID: "grok-4.5", ContextWindowTokens: 500_000, SupportsVision: true},
	{Provider: ProviderXAI, ID: "grok-4.3", ContextWindowTokens: 1_000_000, SupportsVision: true},
	{Provider: ProviderXAI, ID: "grok-4", ContextWindowTokens: 256_000, SupportsVision: true},
}

// FindModelPreset resolves exact official IDs case-insensitively. Gateways often
// expose an official model under provider=custom, so a unique exact ID remains
// useful even when the configured provider is a proxy rather than the vendor.
func FindModelPreset(provider, model string) (ModelPreset, bool) {
	provider = canonicalProviderID(provider)
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ModelPreset{}, false
	}
	for _, preset := range AllModelPresets {
		if strings.ToLower(preset.ID) != model {
			continue
		}
		if provider == "" || provider == string(ProviderCustom) || provider == string(preset.Provider) {
			return preset, true
		}
	}
	return ModelPreset{}, false
}

func canonicalProviderID(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case string(ProviderTencentHY):
		return string(ProviderHunyuan)
	case string(ProviderGrok):
		return string(ProviderXAI)
	case string(ProviderCTYun):
		return string(ProviderTeleAI)
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// FindProvider 按 ID 查找预设
func FindProvider(id string) (ProviderPreset, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range AllProviders {
		if string(p.ID) == id {
			return p, true
		}
	}
	return ProviderPreset{}, false
}

// officialModelAliases maps marketing or retired IDs to the names the vendor
// currently accepts. DeepSeek's product name is "V4.1 Flash", but the official
// API only accepts deepseek-flash and deepseek-v4-pro.
var officialModelAliases = map[string]string{
	"deepseek-v4.1-flash":                 "deepseek-flash",
	"deepseek-v4.1-flash-expires-on-0910": "deepseek-flash",
}

// CanonicalModelID rewrites known retired or marketing IDs to the official
// request name. Unknown IDs are returned unchanged.
func CanonicalModelID(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if alias, ok := officialModelAliases[key]; ok {
		return alias
	}
	return strings.TrimSpace(model)
}

// ApplyProviderDefaults 根据 provider 填充空的 api_url / model_name
func ApplyProviderDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	p := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if p == "" || p == string(ProviderCustom) {
		if strings.TrimSpace(cfg.APIProtocol) == "" || strings.EqualFold(cfg.APIProtocol, ProtocolAuto) {
			if strings.Contains(cfg.ApiURL, "anthropic.com") {
				cfg.APIProtocol = ProtocolAnthropicMessages
			} else if strings.Contains(cfg.ApiURL, "/responses") {
				cfg.APIProtocol = ProtocolOpenAIResponses
			} else {
				cfg.APIProtocol = ProtocolOpenAIChat
			}
		}
		cfg.ModelName = CanonicalModelID(cfg.ModelName)
		cfg.ApiURL = NormalizeAPIURL(cfg.ApiURL, cfg.APIProtocol)
		return
	}
	preset, ok := FindProvider(p)
	if !ok {
		cfg.ModelName = CanonicalModelID(cfg.ModelName)
		cfg.ApiURL = NormalizeAPIURL(cfg.ApiURL, cfg.APIProtocol)
		return
	}
	if strings.TrimSpace(cfg.ApiURL) == "" {
		cfg.ApiURL = preset.APIURL
	}
	if strings.TrimSpace(cfg.ModelName) == "" {
		cfg.ModelName = preset.Model
	}
	if strings.TrimSpace(cfg.APIProtocol) == "" || strings.EqualFold(cfg.APIProtocol, ProtocolAuto) {
		cfg.APIProtocol = preset.Protocol
	}
	cfg.ModelName = CanonicalModelID(cfg.ModelName)
	cfg.ApiURL = NormalizeAPIURL(cfg.ApiURL, cfg.APIProtocol)
}

// NormalizeChatURL 将 base URL 规范为 chat/completions 端点
func NormalizeChatURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	raw = strings.TrimRight(raw, "/")
	if strings.HasSuffix(raw, "/chat/completions") {
		return raw
	}
	if strings.HasSuffix(raw, "/responses") {
		return raw
	}
	// Anthropic 使用 /v1/messages
	if strings.Contains(raw, "anthropic.com") {
		if strings.HasSuffix(raw, "/v1") {
			return raw + "/messages"
		}
		return raw
	}
	if strings.HasSuffix(raw, "/v1") {
		return raw + "/chat/completions"
	}
	if strings.HasSuffix(raw, "/v4") {
		// 智谱 v4
		return raw + "/chat/completions"
	}
	if !strings.Contains(raw, "chat/completions") && !strings.Contains(raw, "/messages") && !strings.Contains(raw, "/responses") {
		return raw + "/chat/completions"
	}
	return raw
}

// IsAnthropic 是否 Anthropic API
func (c *Config) IsAnthropic() bool {
	if strings.EqualFold(c.APIProtocol, ProtocolAnthropicMessages) {
		return true
	}
	p := strings.ToLower(c.Provider)
	return p == string(ProviderAnthropic) || strings.Contains(c.ApiURL, "anthropic.com")
}

// IsOpenAICompatible 是否 OpenAI 兼容 Chat Completions
func (c *Config) IsOpenAICompatible() bool {
	return !c.IsAnthropic() && !strings.EqualFold(c.APIProtocol, ProtocolOpenAIResponses)
}

func (c *Config) IsOpenAIResponses() bool {
	return strings.EqualFold(c.APIProtocol, ProtocolOpenAIResponses) || strings.Contains(c.ApiURL, "/responses")
}

func (c *Config) EffectiveAPIProtocol() string {
	if strings.TrimSpace(c.APIProtocol) == "" {
		if c.IsAnthropic() {
			return ProtocolAnthropicMessages
		}
		if c.IsOpenAIResponses() {
			return ProtocolOpenAIResponses
		}
		return ProtocolOpenAIChat
	}
	return strings.ToLower(strings.TrimSpace(c.APIProtocol))
}

// EffectiveLLMTimeout 单步 LLM 超时秒
func (c *Config) EffectiveLLMTimeout() int {
	if c.LLMTimeoutSec > 0 {
		return c.LLMTimeoutSec
	}
	return 120
}

// EffectiveLLMRetries 重试次数
func (c *Config) EffectiveLLMRetries() int {
	if c.LLMRetries > 0 {
		return c.LLMRetries
	}
	return 3
}

// EffectiveSSHTimeout SSH 单命令超时秒
func (c *Config) EffectiveSSHTimeout() int {
	if c.SSHCommandTimeoutSec > 0 {
		return c.SSHCommandTimeoutSec
	}
	return 90
}

// EffectiveSSHMaxOutputBytes SSH 单命令回传上限（字节）；超出后截断并继续排空管道，不报错
func (c *Config) EffectiveSSHMaxOutputBytes() int {
	if c.SSHMaxOutputBytes > 0 {
		return c.SSHMaxOutputBytes
	}
	return 512 * 1024
}

// NormalizeAPIURL selects the request endpoint without duplicating API versions.
// Explicit protocol wins; custom gateway prefixes are preserved.
func NormalizeAPIURL(raw, protocol string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return raw
	}
	suffix := ""
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case ProtocolOpenAIResponses:
		suffix = "/responses"
	case ProtocolAnthropicMessages:
		suffix = "/messages"
	default:
		return NormalizeChatURL(raw)
	}
	for _, old := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(raw, old) {
			raw = strings.TrimSuffix(raw, old)
			break
		}
	}
	return raw + suffix
}

// ProviderModelSuggestions contains currently selectable model IDs. Historical
// capability entries are intentionally not offered as new configuration choices.
func ProviderModelSuggestions(provider string) []string {
	p, ok := FindProvider(canonicalProviderID(provider))
	if !ok {
		return nil
	}
	ids := []string{p.Model}
	switch p.ID {
	case ProviderOpenAI:
		ids = append(ids, "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna")
	case ProviderAnthropic:
		ids = append(ids, "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5-20251001")
	case ProviderGoogle:
		ids = append(ids, "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash-lite")
	case ProviderDeepSeek:
		ids = append(ids, "deepseek-v4-pro")
	case ProviderQwen:
		ids = append(ids, "qwen3.8-flash", "qwen3.7-plus")
	case ProviderMiniMax:
		ids = append(ids, "MiniMax-M2.7", "MiniMax-M2.7-highspeed")
	case ProviderMimo:
		ids = append(ids, "mimo-v2.5-pro", "mimo-v2.5-pro-ultraspeed")
	case ProviderGLM:
		ids = append(ids, "glm-5.3")
	case ProviderOllama, ProviderLMStudio:
		return nil // installed model IDs are user-specific
	}
	return ids
}
