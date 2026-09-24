package config

import "testing"

func TestEffectiveModelCapabilitiesUsesLargeGoogleWindow(t *testing.T) {
	capabilities := (Config{Provider: "google", ModelName: "gemini-3.1-pro"}).EffectiveModelCapabilities()
	if capabilities.ContextWindowTokens != 1_048_576 || capabilities.PromptProfile != ModelProfileFull {
		t.Fatalf("unexpected Google capabilities: %#v", capabilities)
	}
	if got := capabilities.HistoryBudgetTokens(20_000); got < 800_000 {
		t.Fatalf("1M model history budget was artificially capped: %d", got)
	}
}

func TestEffectiveModelCapabilitiesAdaptsLocalModelSizeAndWindow(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantB     float64
		wantCtx   int
		profile   string
		toolLimit int
	}{
		{name: "14B compact", model: "qwen2.5-14b-instruct-32k", wantB: 14, wantCtx: 32_000, profile: ModelProfileCompact, toolLimit: 8},
		{name: "20B compact", model: "local-20b-64k", wantB: 20, wantCtx: 64_000, profile: ModelProfileCompact, toolLimit: 8},
		{name: "30B compact", model: "qwen-30b-128k", wantB: 30, wantCtx: 128_000, profile: ModelProfileCompact, toolLimit: 8},
		{name: "70B balanced", model: "llama-70b-128k", wantB: 70, wantCtx: 128_000, profile: ModelProfileBalanced, toolLimit: 20},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capabilities := (Config{Provider: "ollama", ModelName: test.model}).EffectiveModelCapabilities()
			if !capabilities.Local || capabilities.ParameterBillions != test.wantB ||
				capabilities.ContextWindowTokens != test.wantCtx || capabilities.PromptProfile != test.profile ||
				capabilities.NativeToolLimit != test.toolLimit {
				t.Fatalf("unexpected capabilities: %#v", capabilities)
			}
		})
	}
}

func TestEffectiveModelCapabilitiesExplicitOverridesWin(t *testing.T) {
	capabilities := (Config{
		Provider:             "ollama",
		ModelName:            "model-14b-32k",
		ModelProfile:         "full",
		ModelParameterB:      20,
		ContextWindowTokens:  98_304,
		ContextUtilization:   0.77,
		ReservedOutputTokens: 3_000,
		NativeToolLimit:      13,
	}).EffectiveModelCapabilities()
	if capabilities.PromptProfile != ModelProfileFull || capabilities.ParameterBillions != 20 ||
		capabilities.ContextWindowTokens != 98_304 || capabilities.ContextUtilization != 0.77 ||
		capabilities.ReservedOutputTokens != 3_000 || capabilities.NativeToolLimit != 13 ||
		capabilities.DetectionSource != "config" {
		t.Fatalf("explicit values did not win: %#v", capabilities)
	}
}

func TestEffectiveModelCapabilitiesUsesConservativeLocalDefault(t *testing.T) {
	capabilities := (Config{ApiURL: "http://127.0.0.1:1234/v1", ModelName: "custom-local"}).EffectiveModelCapabilities()
	if !capabilities.Local || capabilities.ContextWindowTokens != 32_768 ||
		capabilities.PromptProfile != ModelProfileCompact || capabilities.DetectionSource != "local-safe-default" {
		t.Fatalf("unexpected local default: %#v", capabilities)
	}
}

func TestModelDisplayInfoDistinguishesConfiguredFromAssumedContext(t *testing.T) {
	assumed := (Config{Provider: "custom", ModelName: "qianfan-code-latest", ApiURL: "https://example.com/v1"}).ModelDisplayInfo()
	if assumed != "custom / qianfan-code-latest · ctx≈131.1K[安全默认]" {
		t.Fatalf("unexpected assumed label: %q", assumed)
	}
	configured := (Config{Provider: "custom", ModelName: "qianfan-code-latest", ContextWindowTokens: 1_048_576}).ModelDisplayInfo()
	if configured != "custom / qianfan-code-latest · ctx=1.05M[配置]" {
		t.Fatalf("unexpected configured label: %q", configured)
	}
}

func TestEffectiveVisionCapabilityRecognizesDeepSeekVisionAndExplicitOverrides(t *testing.T) {
	auto := (Config{ModelName: "deepseek-v4-flash-vision-exp", VisionMode: "auto"}).EffectiveModelCapabilities()
	if !auto.SupportsVision || auto.VisionSource != "model-catalog" || auto.ContextWindowTokens != 1_000_000 || auto.DetectionSource != "model-catalog" {
		t.Fatalf("DeepSeek vision model was not recognized: %#v", auto)
	}
	disabled := (Config{ModelName: "deepseek-v4-flash-vision-exp", VisionMode: "disabled"}).EffectiveModelCapabilities()
	if disabled.SupportsVision || disabled.VisionSource != "config" {
		t.Fatalf("explicit vision disable did not win: %#v", disabled)
	}
	enabled := (Config{ModelName: "private-model", VisionMode: "enabled"}).EffectiveModelCapabilities()
	if !enabled.SupportsVision || enabled.VisionSource != "config" {
		t.Fatalf("explicit vision enable did not win: %#v", enabled)
	}
}

func TestOfficialChineseMultimodalModelsAreAutoDetected(t *testing.T) {
	for _, test := range []struct {
		provider string
		model    string
		context  int
	}{
		{"deepseek", "deepseek-flash", 1_000_000},
		{"deepseek", "deepseek-v4.1-flash", 1_000_000},
		{"deepseek", "deepseek-v4-flash-vision-exp", 1_000_000},
		{"glm", "glm-5.3-flash", 1_000_000},
		{"minimax", "MiniMax-M3", 1_000_000},
		{"mimo", "mimo-v2.6-pro", 1_000_000},
		{"mimo", "mimo-v2.6-flash", 1_000_000},
		{"mimo", "mimo-v2.5", 1_000_000},
		{"xai", "grok-4.7", 500_000},
		{"openai", "gpt-6-astra", 1_050_000},
		{"openai", "gpt-6-sol", 1_050_000},
		{"openai", "gpt-6-luna", 1_050_000},
		{"openai", "gpt-5.6", 1_050_000},
		{"anthropic", "claude-opus-5-5", 1_000_000},
		{"anthropic", "claude-opus-5", 1_000_000},
		{"anthropic", "claude-sonnet-5", 1_000_000},
		{"anthropic", "claude-haiku-4-5", 200_000},
		{"google", "gemini-3.8-flash", 1_048_576},
		{"qwen", "qwen3.8-max", 1_000_000},
		{"qwen", "qwen3.7-plus", 1_000_000},
	} {
		capabilities := (Config{Provider: test.provider, ModelName: test.model, VisionMode: "auto"}).EffectiveModelCapabilities()
		if !capabilities.SupportsVision || capabilities.VisionSource != "model-catalog" || capabilities.ContextWindowTokens != test.context {
			t.Fatalf("%s/%s capabilities=%#v", test.provider, test.model, capabilities)
		}
	}
}
