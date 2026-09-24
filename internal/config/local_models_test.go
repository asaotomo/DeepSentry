package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverLocalModelVisionOllamaUsesSelectedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/api/show" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected Ollama request: %s %s", r.Method, r.URL.Path)
		}
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "gemma4:latest" {
			t.Errorf("wrong model in Ollama show request: %+v, %v", input, err)
		}
		fmt.Fprint(w, `{"capabilities":["completion","vision"]}`)
	}))
	defer server.Close()
	got, err := DiscoverLocalModelVision(context.Background(), "ollama", server.URL+"/proxy/v1", "none", "gemma4:latest")
	if err != nil || !got.Known || !got.Supported {
		t.Fatalf("Ollama vision=%+v err=%v", got, err)
	}
}

func TestDiscoverLocalModelVisionLMStudioMatchesLoadedInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected LM Studio request: %s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"models":[{"key":"publisher/vision-model","loaded_instances":[{"id":"loaded-vision"}],"capabilities":{"vision":true}},{"key":"text-only","capabilities":{"vision":false}}]}`)
	}))
	defer server.Close()
	for _, tt := range []struct {
		model string
		want  LocalModelVision
	}{
		{"loaded-vision", LocalModelVision{Supported: true, Known: true}},
		{"text-only", LocalModelVision{Known: true}},
		{"missing", LocalModelVision{}},
	} {
		got, err := DiscoverLocalModelVision(context.Background(), "lmstudio", server.URL+"/v1", "secret", tt.model)
		if err != nil || got != tt.want {
			t.Fatalf("LM Studio model=%q vision=%+v err=%v", tt.model, got, err)
		}
	}
}

func TestDiscoverLocalModelVisionLocalAIModalitiesAndUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/v1/models/capabilities" {
			t.Errorf("unexpected LocalAI path: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"visual","capabilities":["chat"],"input_modalities":["text","image"]},{"id":"text","capabilities":["chat"],"input_modalities":["text"]},{"id":"image-generator","capabilities":["image"],"input_modalities":["text"]},{"id":"old"}]}`)
	}))
	defer server.Close()
	for _, tt := range []struct {
		model string
		want  LocalModelVision
	}{
		{"visual", LocalModelVision{Supported: true, Known: true}},
		{"text", LocalModelVision{Known: true}},
		{"image-generator", LocalModelVision{Known: true}},
		{"old", LocalModelVision{}},
	} {
		got, err := DiscoverLocalModelVision(context.Background(), "localai", server.URL+"/proxy/v1", "none", tt.model)
		if err != nil || got != tt.want {
			t.Fatalf("LocalAI model=%q vision=%+v err=%v", tt.model, got, err)
		}
	}
}

func TestDiscoverLocalModelVisionGenericRequiresExplicitMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"visual","vision":true},{"id":"ambiguous","multimodal":true},{"id":"opaque","capabilities":["chat"]}]}`)
	}))
	defer server.Close()
	visual, err := DiscoverLocalModelVision(context.Background(), "vllm", server.URL+"/v1", "none", "visual")
	if err != nil || visual != (LocalModelVision{Supported: true, Known: true}) {
		t.Fatalf("generic explicit visual=%+v err=%v", visual, err)
	}
	opaque, err := DiscoverLocalModelVision(context.Background(), "vllm", server.URL+"/v1", "none", "opaque")
	if err != nil || opaque.Known {
		t.Fatalf("generic uninformative metadata must stay unknown: %+v err=%v", opaque, err)
	}
	ambiguous, err := DiscoverLocalModelVision(context.Background(), "llamacpp", server.URL+"/v1", "none", "ambiguous")
	if err != nil || ambiguous.Known {
		t.Fatalf("multimodal alone does not prove image support: %+v err=%v", ambiguous, err)
	}
}

func TestApplyLocalRuntimeContextUsesLoadedLMStudioInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected LM Studio path: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"models":[{"key":"openai/gpt-oss-20b","max_context_length":131072,"loaded_instances":[{"id":"openai/gpt-oss-20b","config":{"context_length":15687}}],"capabilities":{"trained_for_tool_use":true}}]}`)
	}))
	defer server.Close()
	cfg := Config{Provider: "lmstudio", ApiURL: server.URL + "/v1", ModelName: "openai/gpt-oss-20b"}
	info, err := ApplyLocalRuntimeContext(context.Background(), &cfg)
	if err != nil || info.ContextWindowTokens != 15687 || !info.NativeToolsKnown || !info.NativeToolsSupported {
		t.Fatalf("runtime info=%+v err=%v", info, err)
	}
	if cfg.ContextWindowTokens != 15687 || cfg.EffectiveModelCapabilities().DetectionSource != "local-runtime" || !cfg.LocalNativeToolsAvailable {
		t.Fatalf("loaded context not applied: %+v", cfg)
	}
	if got := cfg.ModelDisplayInfo(); !strings.Contains(got, "ctx=15.7K[运行时]") {
		t.Fatalf("runtime context source not shown: %q", got)
	}
	cfg.ContextWindowTokens = 64_000
	if _, err := ApplyLocalRuntimeContext(context.Background(), &cfg); err != nil || cfg.ContextWindowTokens != 64_000 {
		t.Fatalf("explicit context was overwritten: %+v err=%v", cfg, err)
	}
}

func TestDiscoverLocalModelRuntimeOllamaUsesRunningContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/api/ps":
			fmt.Fprint(w, `{"models":[{"model":"qwen3:14b","context_length":8192}]}`)
		case "/proxy/api/show":
			fmt.Fprint(w, `{"capabilities":["completion","tools"]}`)
		default:
			t.Errorf("unexpected Ollama path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	info, err := DiscoverLocalModelRuntime(context.Background(), "ollama", server.URL+"/proxy/v1", "none", "qwen3:14b")
	if err != nil || info.ContextWindowTokens != 8192 || !info.NativeToolsKnown || !info.NativeToolsSupported {
		t.Fatalf("Ollama runtime=%+v err=%v", info, err)
	}
}

func TestDiscoverLocalModelRuntimeLocalAITools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/capabilities" {
			t.Errorf("unexpected LocalAI path: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"local-vision","capabilities":["chat","vision","tools"]}]}`)
	}))
	defer server.Close()
	info, err := DiscoverLocalModelRuntime(context.Background(), "localai", server.URL+"/v1", "none", "local-vision")
	if err != nil || !info.NativeToolsKnown || !info.NativeToolsSupported || info.ContextWindowTokens != 0 {
		t.Fatalf("LocalAI runtime=%+v err=%v", info, err)
	}
}

func TestLocalModelsURLPreservesProxyPrefix(t *testing.T) {
	for input, want := range map[string]string{
		"http://localhost:11434":                          "http://localhost:11434/v1/models",
		"http://localhost:8000/v1":                        "http://localhost:8000/v1/models",
		"http://localhost:8000/proxy/v1/chat/completions": "http://localhost:8000/proxy/v1/models",
		"http://localhost:1234/v1/models":                 "http://localhost:1234/v1/models",
	} {
		got, err := LocalModelsURL(input)
		if err != nil || got != want {
			t.Fatalf("LocalModelsURL(%q)=%q,%v want %q", input, got, err, want)
		}
	}
	if _, err := LocalModelsURL("localhost:11434/v1"); err == nil {
		t.Fatal("relative local model URL accepted")
	}
}

func TestDiscoverLocalModelsUsesAuthenticationAndExactIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected models request: path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"data":[{"id":"qwen3:14b"},{"id":"qwen3:14b"},{"id":"Qwen/Qwen3-4B"},{"id":"bad\u001bmodel"}]}`)
	}))
	defer server.Close()
	ids, err := DiscoverLocalModels(context.Background(), server.URL+"/proxy/v1/chat/completions", "secret")
	if err != nil || !reflect.DeepEqual(ids, []string{"qwen3:14b", "Qwen/Qwen3-4B"}) {
		t.Fatalf("models=%v err=%v", ids, err)
	}
}

func TestDiscoverLocalModelsDoesNotInventOfflineModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request: path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	ids, err := DiscoverLocalModels(context.Background(), server.URL+"/v1", "none")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || len(ids) != 0 {
		t.Fatalf("unavailable model list should fail without a fake default: models=%v err=%v", ids, err)
	}
}
