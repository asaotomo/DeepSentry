package config

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ModelDisplayInfo returns a compact TUI label for the model and the context
// window DeepSentry is currently using. Only an explicit config value is shown
// as exact; inferred/provider/safe defaults deliberately use ≈ to avoid
// presenting an assumption as the model vendor's confirmed capability.
func (c Config) ModelDisplayInfo() string {
	capabilities := c.EffectiveModelCapabilities()
	provider := strings.TrimSpace(c.Provider)
	if provider == "" {
		provider = "custom"
	}
	model := strings.TrimSpace(c.ModelName)
	if model == "" {
		model = "unknown-model"
	}
	operator := "≈"
	source := "安全默认"
	switch capabilities.DetectionSource {
	case "config":
		operator = "="
		source = "配置"
	case "model-name":
		source = "名称推断"
	case "provider-default":
		source = "厂商预设"
	case "model-catalog":
		source = "官方模型目录"
	}
	return fmt.Sprintf("%s / %s · ctx%s%s[%s]", provider, model, operator, formatModelTokenCapacity(capabilities.ContextWindowTokens), source)
}

func formatModelTokenCapacity(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	default:
		return strconv.Itoa(tokens)
	}
}

const (
	ModelProfileCompact  = "compact"
	ModelProfileBalanced = "balanced"
	ModelProfileFull     = "full"
)

// ModelCapabilities separates context capacity from reasoning capability.
// Parameter count influences prompt/tool density, never the context window;
// context_window_tokens remains the authoritative runtime limit.
type ModelCapabilities struct {
	Local                bool
	ParameterBillions    float64
	ContextWindowTokens  int
	ContextUtilization   float64
	ReservedOutputTokens int
	PromptProfile        string
	NativeToolLimit      int
	KeepRecentMessages   int
	SummaryChunkTokens   int
	SystemPromptTokens   int
	DetectionSource      string
	SupportsVision       bool
	VisionSource         string
}

func (c Config) EffectiveModelCapabilities() ModelCapabilities {
	local := c.IsLocalModelEndpoint()
	parameterB := c.ModelParameterB
	if parameterB <= 0 {
		parameterB = inferParameterBillions(c.ModelName)
	}
	window, source := c.ContextWindowTokens, "config"
	if window <= 0 {
		if preset, ok := FindModelPreset(c.Provider, c.ModelName); ok && preset.ContextWindowTokens > 0 {
			window, source = preset.ContextWindowTokens, "model-catalog"
		} else if hinted := inferContextWindow(c.ModelName); hinted > 0 {
			window, source = hinted, "model-name"
		} else if strings.EqualFold(c.Provider, string(ProviderGoogle)) || strings.Contains(strings.ToLower(c.ModelName), "gemini") {
			// Current Gemini text families expose 1M context. Explicit config
			// always wins if a specific variant differs.
			window, source = 1_048_576, "provider-default"
		} else if strings.EqualFold(c.Provider, string(ProviderAnthropic)) {
			window, source = 1_000_000, "provider-default"
		} else if local {
			// Local servers often lower the model's advertised context via
			// num_ctx/max_model_len. Stay conservative unless configured.
			window, source = 32_768, "local-safe-default"
		} else {
			window, source = 131_072, "remote-safe-default"
		}
	}
	window = clampInt(window, 4_096, 4_194_304)

	profile := strings.ToLower(strings.TrimSpace(c.ModelProfile))
	if profile != ModelProfileCompact && profile != ModelProfileBalanced && profile != ModelProfileFull {
		profile = inferPromptProfile(local, parameterB, window)
	}

	utilization := c.ContextUtilization
	if utilization <= 0 || utilization >= 0.95 {
		switch profile {
		case ModelProfileCompact:
			utilization = 0.62
		case ModelProfileBalanced:
			utilization = 0.72
		default:
			utilization = 0.82
		}
	}
	utilization = math.Max(0.40, math.Min(0.90, utilization))

	reserved := c.ReservedOutputTokens
	if reserved <= 0 {
		switch {
		case window <= 16_384:
			reserved = 2_048
		case window <= 65_536:
			reserved = 4_096
		case window <= 262_144:
			reserved = 8_192
		default:
			reserved = 16_384
		}
	}
	reserved = clampInt(reserved, 512, maxInt(1_024, window/4))

	toolLimit := c.NativeToolLimit
	keepRecent := 24
	var summaryChunk int
	switch profile {
	case ModelProfileCompact:
		if toolLimit <= 0 {
			toolLimit = 8
		}
		keepRecent = 8
		summaryChunk = minInt(8_192, int(float64(window)*0.35))
	case ModelProfileBalanced:
		if toolLimit <= 0 {
			toolLimit = 20
		}
		keepRecent = 12
		summaryChunk = minInt(32_768, int(float64(window)*0.45))
	default:
		// 0 means expose all enabled native tools.
		keepRecent = 24
		summaryChunk = minInt(600_000, int(float64(window)*0.65))
	}
	summaryChunk = maxInt(2_048, summaryChunk)

	vision, visionSource := c.effectiveVisionCapability()
	return ModelCapabilities{
		Local:                local,
		ParameterBillions:    parameterB,
		ContextWindowTokens:  window,
		ContextUtilization:   utilization,
		ReservedOutputTokens: reserved,
		PromptProfile:        profile,
		NativeToolLimit:      toolLimit,
		KeepRecentMessages:   keepRecent,
		SummaryChunkTokens:   summaryChunk,
		DetectionSource:      source,
		SupportsVision:       vision,
		VisionSource:         visionSource,
	}
}

func (c Config) effectiveVisionCapability() (bool, string) {
	mode := strings.ToLower(strings.TrimSpace(c.VisionMode))
	switch mode {
	case "enabled", "enable", "true", "required", "on":
		return true, "config"
	case "disabled", "disable", "false", "off":
		return false, "config"
	}
	model := strings.ToLower(strings.TrimSpace(c.ModelName))
	if preset, ok := FindModelPreset(c.Provider, c.ModelName); ok {
		return preset.SupportsVision, "model-catalog"
	}
	hints := []string{
		"vision", "multimodal", "gpt-4o", "gpt-4.1", "gpt-5", "gpt-6", "chatgpt-6", "chatgpt6", "claude-", "gemini-",
		"qwen-vl", "qwen2-vl", "qwen3-vl", "deepseek-vl", "llava", "pixtral", "internvl", "minicpm-v",
	}
	for _, hint := range hints {
		if strings.Contains(model, hint) {
			return true, "model-name"
		}
	}
	if strings.HasSuffix(model, "-vl") || strings.Contains(model, "-vl-") {
		return true, "model-name"
	}
	return false, "safe-default"
}

func (m ModelCapabilities) HistoryBudgetTokens(systemPromptTokens int) int {
	target := int(float64(m.ContextWindowTokens) * m.ContextUtilization)
	budget := target - m.ReservedOutputTokens - maxInt(0, systemPromptTokens)
	return maxInt(1_024, budget)
}

func (m ModelCapabilities) SystemPromptBudgetTokens() int {
	switch m.PromptProfile {
	case ModelProfileCompact:
		return minInt(6_000, maxInt(2_000, m.ContextWindowTokens/4))
	case ModelProfileBalanced:
		return minInt(16_000, maxInt(6_000, m.ContextWindowTokens/4))
	default:
		return minInt(64_000, maxInt(16_000, m.ContextWindowTokens/5))
	}
}

func (c Config) IsLocalModelEndpoint() bool {
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	if p == string(ProviderOllama) || p == string(ProviderLMStudio) {
		return true
	}
	u, err := url.Parse(c.ApiURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" || host == "host.docker.internal"
}

var parameterBPattern = regexp.MustCompile(`(?i)(?:^|[-_:/])([0-9]+(?:\.[0-9]+)?)b(?:$|[-_:/])`)
var contextHintPattern = regexp.MustCompile(`(?i)(?:^|[-_:/])([0-9]+(?:\.[0-9]+)?)(k|m)(?:$|[-_:/])`)

func inferParameterBillions(model string) float64 {
	match := parameterBPattern.FindStringSubmatch(strings.ToLower(model))
	if len(match) != 2 {
		return 0
	}
	value, _ := strconv.ParseFloat(match[1], 64)
	return value
}

func inferContextWindow(model string) int {
	matches := contextHintPattern.FindAllStringSubmatch(strings.ToLower(model), -1)
	best := 0
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		multiplier := 1_000.0
		if match[2] == "m" {
			multiplier = 1_000_000.0
		}
		candidate := int(value * multiplier)
		if candidate > best {
			best = candidate
		}
	}
	return best
}

func inferPromptProfile(local bool, parameterB float64, window int) string {
	if local {
		switch {
		case parameterB > 32:
			return ModelProfileBalanced
		default:
			return ModelProfileCompact
		}
	}
	if window <= 16_384 {
		return ModelProfileCompact
	}
	if window <= 65_536 {
		return ModelProfileBalanced
	}
	return ModelProfileFull
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
