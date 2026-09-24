package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestNormalizeChatURL(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":                          "https://api.deepseek.com/chat/completions",
		"https://token-plan-cn.xiaomimimo.com/v1":           "https://token-plan-cn.xiaomimimo.com/v1/chat/completions",
		"https://qianfan.baidubce.com/v2/coding":            "https://qianfan.baidubce.com/v2/coding/chat/completions",
		"https://ark.cn-beijing.volces.com/api/coding/v3":   "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions",
		"https://api.deepseek.com/v1/chat/completions":      "https://api.deepseek.com/v1/chat/completions",
		"https://api.anthropic.com/v1":                      "https://api.anthropic.com/v1/messages",
		"https://dashscope.aliyuncs.com/compatible-mode/v1": "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		"https://api.hunyuan.cloud.tencent.com/v1":          "https://api.hunyuan.cloud.tencent.com/v1/chat/completions",
		"https://tokenhub.tencentmaas.com/v1":               "https://tokenhub.tencentmaas.com/v1/chat/completions",
	}
	for in, want := range cases {
		got := NormalizeChatURL(in)
		if got != want {
			t.Fatalf("%s => %s, want %s", in, got, want)
		}
	}
}

func TestCanonicalModelIDRewritesRetiredDeepSeekIDs(t *testing.T) {
	cases := map[string]string{
		"deepseek-v4.1-flash":                 "deepseek-flash",
		"DeepSeek-V4.1-Flash":                 "deepseek-flash",
		"deepseek-v4.1-flash-expires-on-0910": "deepseek-flash",
		"deepseek-flash":                      "deepseek-flash",
		"deepseek-v4-pro":                     "deepseek-v4-pro",
	}
	for in, want := range cases {
		if got := CanonicalModelID(in); got != want {
			t.Fatalf("CanonicalModelID(%q)=%q, want %q", in, got, want)
		}
	}

	cfg := &Config{Provider: "deepseek", ModelName: "deepseek-v4.1-flash"}
	ApplyProviderDefaults(cfg)
	if cfg.ModelName != "deepseek-flash" {
		t.Fatalf("existing config was not rewritten: %q", cfg.ModelName)
	}
	capabilities := cfg.EffectiveModelCapabilities()
	if !capabilities.SupportsVision || capabilities.ContextWindowTokens != 1_000_000 {
		t.Fatalf("rewritten model lost vision/context: %+v", capabilities)
	}
}

func TestApplyProviderDefaultsLatestChineseVisionModels(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		url      string
	}{
		{"deepseek", "deepseek-flash", "https://api.deepseek.com/chat/completions"},
		{"glm", "glm-5.3-flash", "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{"minimax", "MiniMax-M3", "https://api.minimax.cn/v1/chat/completions"},
		{"mimo", "mimo-v2.6-pro", "https://token-plan-cn.xiaomimimo.com/v1/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			cfg := &Config{Provider: test.provider}
			ApplyProviderDefaults(cfg)
			if cfg.ModelName != test.model || cfg.ApiURL != test.url || cfg.APIProtocol != ProtocolOpenAIChat {
				t.Fatalf("unexpected defaults: %+v", cfg)
			}
			capabilities := cfg.EffectiveModelCapabilities()
			if !capabilities.SupportsVision || capabilities.ContextWindowTokens != 1_000_000 {
				t.Fatalf("unexpected capabilities: %+v", capabilities)
			}
		})
	}
}

func TestProviderDefaultsChineseOpenAICompatible(t *testing.T) {
	cases := []struct {
		provider string
		urlPart  string
		model    string
	}{
		{"qwen", "dashscope.aliyuncs.com", "qwen3.8-max"},
		{"qianfan", "qianfan.baidubce.com", "qianfan-code-latest"},
		{"volcengine", "ark.cn-beijing.volces.com", "ark-code-latest"},
		{"hunyuan", "tokenhub.tencentmaas.com", "hy4-preview"},
		{"tencent_hy", "tokenhub.tencentmaas.com", "hy4-preview"},
		{"teleai", "ctyun.cn", "GLM-5-Pro"},
		{"ctyun", "ctyun.cn", "GLM-5-Pro"},
	}
	for _, tc := range cases {
		cfg := &Config{Provider: tc.provider}
		ApplyProviderDefaults(cfg)
		if !contains(cfg.ApiURL, tc.urlPart) || !contains(cfg.ApiURL, "chat/completions") {
			t.Fatalf("%s unexpected url: %s", tc.provider, cfg.ApiURL)
		}
		if cfg.ModelName != tc.model || cfg.APIProtocol != ProtocolOpenAIChat {
			t.Fatalf("%s unexpected defaults: %+v", tc.provider, cfg)
		}
	}
}

func TestCodingPlanProviderPresets(t *testing.T) {
	tests := []struct {
		id, displayName string
	}{
		{"qianfan", "百度千帆 Coding Plan"},
		{"volcengine", "火山方舟 Coding Plan"},
		{"mimo", "Xiaomi MiMo Token Plan / MiMo Claw"},
	}
	for _, test := range tests {
		preset, ok := FindProvider(test.id)
		if !ok {
			t.Fatalf("provider %s not found", test.id)
		}
		if preset.DisplayName != test.displayName || preset.Protocol != ProtocolOpenAIChat || !preset.NativeTools {
			t.Fatalf("unexpected %s preset: %+v", test.id, preset)
		}
	}
}

func TestFindModelPresetPreservesTextOnlyVariants(t *testing.T) {
	for _, test := range []struct {
		provider string
		model    string
		vision   bool
		context  int
	}{
		{"deepseek", "deepseek-v4-pro", false, 1_000_000},
		{"minimax", "MiniMax-M2.7-highspeed", false, 204_800},
		{"mimo", "mimo-v2.5-pro", false, 1_000_000},
		{"custom", "GLM-5.3-FLASH", true, 1_000_000},
		{"openai", "gpt-6-astra", true, 1_050_000},
		{"openai", "gpt-6-sol", true, 1_050_000},
		{"openai", "gpt-6-luna", true, 1_050_000},
		{"openai", "gpt-6", true, 1_050_000},
		{"custom", "chatgpt6", true, 1_050_000},
		{"deepseek", "deepseek-flash", true, 1_000_000},
		{"deepseek", "deepseek-v4.1-flash", true, 1_000_000},
		{"openai", "gpt-5.6", true, 1_050_000},
		{"anthropic", "claude-opus-5-5", true, 1_000_000},
		{"anthropic", "claude-opus-5", true, 1_000_000},
		{"anthropic", "claude-sonnet-5", true, 1_000_000},
		{"anthropic", "claude-haiku-4-5", true, 200_000},
		{"google", "gemini-3.8-flash", true, 1_048_576},
		{"qwen", "qwen3.8-max", true, 1_000_000},
		{"qwen", "qwen3.7-plus", true, 1_000_000},
		{"tencent_hy", "hy4-preview", false, 1_000_000},
		{"grok", "grok-4.7", true, 500_000},
		{"grok", "grok-4.6", true, 500_000},
		{"xai", "grok-4.3", true, 1_000_000},
	} {
		preset, ok := FindModelPreset(test.provider, test.model)
		if !ok || preset.SupportsVision != test.vision || preset.ContextWindowTokens != test.context {
			t.Fatalf("FindModelPreset(%q,%q)=%+v,%v", test.provider, test.model, preset, ok)
		}
	}
}

func TestProviderDefaultsOfficialLatestModels(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		urlPart  string
		protocol string
	}{
		{"openai", "gpt-6-astra", "api.openai.com/v1", ProtocolOpenAIResponses},
		{"anthropic", "claude-opus-5-5", "api.anthropic.com/v1", ProtocolAnthropicMessages},
		{"google", "gemini-3.8-flash", "generativelanguage.googleapis.com/v1beta/openai", ProtocolOpenAIChat},
		{"qwen", "qwen3.8-max", "dashscope.aliyuncs.com/compatible-mode/v1", ProtocolOpenAIChat},
		{"hunyuan", "hy4-preview", "tokenhub.tencentmaas.com/v1", ProtocolOpenAIChat},
		{"xai", "grok-4.7", "api.x.ai/v1", ProtocolOpenAIChat},
	}
	for _, test := range tests {
		cfg := &Config{Provider: test.provider}
		ApplyProviderDefaults(cfg)
		if cfg.ModelName != test.model || !contains(cfg.ApiURL, test.urlPart) || cfg.APIProtocol != test.protocol {
			t.Fatalf("%s unexpected defaults: %+v", test.provider, cfg)
		}
	}
}

func TestProviderDefaultsXAIAndLMStudio(t *testing.T) {
	xai := &Config{Provider: "xai"}
	ApplyProviderDefaults(xai)
	if xai.ModelName != "grok-4.7" || !contains(xai.ApiURL, "api.x.ai") || xai.APIProtocol != ProtocolOpenAIChat {
		t.Fatalf("unexpected xai defaults: %+v", xai)
	}

	lm := &Config{Provider: "lmstudio"}
	ApplyProviderDefaults(lm)
	if !contains(lm.ApiURL, "localhost:1234") || lm.APIProtocol != ProtocolOpenAIChat {
		t.Fatalf("unexpected lmstudio defaults: %+v", lm)
	}
}

func TestLocalProviderPresetsAndExplicitModelIDs(t *testing.T) {
	for provider, base := range map[string]string{
		"ollama": "localhost:11434", "lmstudio": "localhost:1234",
		"vllm": "localhost:8000", "llamacpp": "localhost:8080",
		"sglang": "localhost:30000", "localai": "localhost:8080",
	} {
		cfg := &Config{Provider: provider, ModelName: "my-served-model"}
		ApplyProviderDefaults(cfg)
		if !IsLocalProvider(provider) || !contains(cfg.ApiURL, base) ||
			cfg.APIProtocol != ProtocolOpenAIChat || cfg.ModelName != "my-served-model" {
			t.Fatalf("%s unexpected local defaults: %+v", provider, cfg)
		}
		if !cfg.IsLocalModelEndpoint() || cfg.EffectiveModelCapabilities().ContextWindowTokens != 32_768 {
			t.Fatalf("%s lost conservative local model adaptation", provider)
		}
		if len(ProviderModelSuggestions(provider)) != 0 {
			t.Fatalf("%s should discover the actual served model IDs", provider)
		}
	}
}

func TestLocalProviderBareHostGetsOpenAIV1Path(t *testing.T) {
	for _, provider := range []string{"ollama", "lmstudio", "vllm", "llamacpp", "sglang", "localai"} {
		cfg := &Config{Provider: provider, ApiURL: "http://127.0.0.1:12345", ModelName: "served-model"}
		ApplyProviderDefaults(cfg)
		if cfg.ApiURL != "http://127.0.0.1:12345/v1/chat/completions" {
			t.Fatalf("%s bare-host API URL=%q", provider, cfg.ApiURL)
		}
	}
}

func TestResponsesURLPreserved(t *testing.T) {
	in := "https://api.openai.com/v1/responses"
	if got := NormalizeChatURL(in); got != in {
		t.Fatalf("responses url changed: %s", got)
	}
}

func TestInitConfigAcceptsUppercaseBenchmarkKeys(t *testing.T) {
	old := GlobalConfig
	defer func() {
		GlobalConfig = old
		viper.Reset()
	}()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("provider: custom\napi_url: http://example.test/v1\napi_key: test\nmodel_name: test\nBENCHMARK_BASE_URL: https://tsecbench.example\nBENCHMARK_TOKEN: token-123\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.Reset()
	if err := InitConfig(path); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}
	if GlobalConfig.BenchmarkBaseURL != "https://tsecbench.example" || GlobalConfig.BenchmarkToken != "token-123" {
		t.Fatalf("benchmark config not loaded: %#v", GlobalConfig)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestProtocolEndpointRouting(t *testing.T) {
	for _, tc := range []struct{ protocol, input, want string }{
		{ProtocolOpenAIResponses, "https://api.openai.com/v1", "https://api.openai.com/v1/responses"},
		{ProtocolOpenAIResponses, "https://proxy.test/prefix/v1/chat/completions", "https://proxy.test/prefix/v1/responses"},
		{ProtocolOpenAIResponses, "https://api.deepseek.com", "https://api.deepseek.com/responses"},
		{ProtocolAnthropicMessages, "https://proxy.test/anthropic/v1", "https://proxy.test/anthropic/v1/messages"},
	} {
		c := Config{Provider: "custom", APIProtocol: tc.protocol, ApiURL: tc.input, ModelName: "pinned-model"}
		ApplyProviderDefaults(&c)
		if c.ApiURL != tc.want || c.ModelName != "pinned-model" {
			t.Fatalf("routing: %+v", c)
		}
		ApplyProviderDefaults(&c)
		if c.ApiURL != tc.want {
			t.Fatalf("non-idempotent: %s", c.ApiURL)
		}
	}
	c := Config{Provider: "openai"}
	ApplyProviderDefaults(&c)
	if c.ApiURL != "https://api.openai.com/v1/responses" {
		t.Fatal(c.ApiURL)
	}
}
func TestModelSuggestionsExcludeRetiredAndUnverifiedIDs(t *testing.T) {
	openai := ProviderModelSuggestions("openai")
	if len(openai) < 3 || openai[0] != "gpt-6-astra" || openai[1] != "gpt-6-sol" || openai[2] != "gpt-6-luna" {
		t.Fatalf("openai suggestions=%v", openai)
	}
	for _, id := range openai {
		if id == "chatgpt6" || id == "gpt-6-terra" {
			t.Fatal(id)
		}
	}
	mimo := ProviderModelSuggestions("mimo")
	if len(mimo) == 0 || mimo[0] != "mimo-v2.6-pro" {
		t.Fatalf("mimo suggestions=%v", mimo)
	}
	xai := ProviderModelSuggestions("xai")
	if len(xai) == 0 || xai[0] != "grok-4.7" {
		t.Fatalf("xai suggestions=%v", xai)
	}
	ids := ProviderModelSuggestions("anthropic")
	if len(ids) < 4 || ids[0] != "claude-opus-5-5" || ids[1] != "claude-fable-5-1" {
		t.Fatal(ids)
	}
	for _, id := range ProviderModelSuggestions("deepseek") {
		if id == "deepseek-v4-flash" {
			t.Fatal("retired default offered")
		}
	}
	if len(ProviderModelSuggestions("ollama")) != 0 {
		t.Fatal("invented installed local models")
	}
}
