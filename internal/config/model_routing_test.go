package config

import "testing"

func TestEffectiveModelsKeepsLegacyAndOrdersPrimaryFirst(t *testing.T) {
	legacy := Config{Provider: "custom", APIProtocol: ProtocolOpenAIChat, ApiURL: "http://localhost:1/v1", ModelName: "legacy", LLMRetries: 2}
	models := legacy.EffectiveModels()
	if len(models) != 1 || models[0].Role != "primary" || models[0].ModelName != "legacy" || models[0].MaxRetries != 2 {
		t.Fatalf("legacy migration=%#v", models)
	}
	legacy.Models = []ModelConfig{
		{ID: "fallback", Role: "fallback", ModelName: "secondary", APIURL: "http://localhost:2/v1"},
		{ID: "primary", Role: "primary", ModelName: "primary", APIURL: "http://localhost:1/v1"},
	}
	models = legacy.EffectiveModels()
	if len(models) != 2 || models[0].ID != "primary" || models[1].ID != "fallback" || models[1].Provider != "custom" {
		t.Fatalf("model ordering/inheritance=%#v", models)
	}
}

func TestValidateModelRoutingRejectsAmbiguousPrimaryAndUnknownFailure(t *testing.T) {
	base := Config{
		AgentRuntime: "v3", Provider: "custom", APIProtocol: ProtocolOpenAIChat,
		ApiURL: "http://localhost:1/v1", ModelName: "primary",
		SSHHostKeyPolicy: "accept-new", SSHKnownHostsPath: "/tmp/known_hosts",
		Models: []ModelConfig{{ID: "a", Role: "primary"}, {ID: "b", Role: "primary"}},
	}
	if err := ValidateRuntimeConfig(base); err == nil {
		t.Fatal("multiple primary models accepted")
	}
	base.Models = []ModelConfig{{ID: "a", Role: "primary"}, {ID: "b", Role: "fallback"}}
	base.ModelRouting.FailoverOn = []string{"bad_error"}
	if err := ValidateRuntimeConfig(base); err == nil {
		t.Fatal("unknown failover class accepted")
	}
}
