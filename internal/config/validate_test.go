package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func validRuntimeConfig() Config {
	return Config{
		Provider:            "custom",
		APIProtocol:         ProtocolOpenAIChat,
		ApiURL:              "http://127.0.0.1:11434/v1/chat/completions",
		ModelName:           "test-model",
		ModelProfile:        "auto",
		SSHHostKeyPolicy:    "accept-new",
		SSHKnownHostsPath:   "~/.deepsentry/known_hosts",
		SchedulerTimezone:   "Local",
		ContextWindowTokens: 131_072,
	}
}

func TestValidateRuntimeConfigRejectsUnsafeOrAmbiguousState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"unknown provider", func(c *Config) { c.Provider = "mystery" }, "provider"},
		{"bad terminal theme", func(c *Config) { c.TerminalTheme = "sepia" }, "terminal_theme"},
		{"bad url", func(c *Config) { c.ApiURL = "file:///tmp/model" }, "api_url"},
		{"bad context", func(c *Config) { c.ContextWindowTokens = 2048 }, "context_window_tokens"},
		{"bad vision mode", func(c *Config) { c.VisionMode = "maybe" }, "vision_mode"},
		{"negative temperature", func(c *Config) { c.Temperature = -0.1 }, "temperature"},
		{"excessive temperature", func(c *Config) { c.Temperature = 2.1 }, "temperature"},
		{"bad host key policy", func(c *Config) { c.SSHHostKeyPolicy = "ignore" }, "ssh_host_key_policy"},
		{"bad ssh device", func(c *Config) { c.SSHDeviceType = "toaster" }, "ssh_device_type"},
		{"bad ssh prompt regex", func(c *Config) { c.SSHPrompt = "regex:[" }, "ssh_prompt"},
		{"negative ftp timeout", func(c *Config) { c.FTPTransferTimeoutSec = -1 }, "ftp 超时"},
		{"excessive ftp timeout", func(c *Config) { c.FTPCommandTimeoutSec = 601 }, "ftp 超时"},
		{"bad ftp tls mode", func(c *Config) { c.FTPTLSMode = "ssl3" }, "ftp_tls_mode"},
		{"bad ftp data mode", func(c *Config) { c.FTPDataMode = "bounce" }, "ftp_data_mode"},
		{"bad ftp active address", func(c *Config) { c.FTPActiveAddress = "host.example" }, "ftp_active_address"},
		{"duplicate target", func(c *Config) {
			c.Targets = []TargetConfig{{Name: "prod", Protocol: "ssh", Host: "a"}, {Name: "prod", Protocol: "ssh", Host: "b"}}
		}, "重复目标"},
		{"insecure remote mcp", func(c *Config) {
			c.MCPServerConfigs = []MCPServerConfig{{Name: "remote", Type: "http", URL: "http://example.test/mcp"}}
		}, "必须使用 HTTPS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validRuntimeConfig()
			tt.mutate(&cfg)
			err := ValidateRuntimeConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestInitConfigReplacesRatherThanMergesOldGlobalState(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte("provider: custom\napi_protocol: openai_chat\napi_url: http://127.0.0.1:11434/v1\nmodel_name: local\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	GlobalConfig = validRuntimeConfig()
	GlobalConfig.ApiKey = "stale-secret"
	GlobalConfig.Targets = []TargetConfig{{Name: "stale", Protocol: "ssh", Host: "old"}}
	if err := InitConfig(path); err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.ApiKey != "" || len(GlobalConfig.Targets) != 0 {
		t.Fatalf("removed fields survived reload: %#v", GlobalConfig)
	}
	if GlobalConfig.AgentRuntime != "v3" {
		t.Fatalf("missing agent_runtime should default to v3, got %q", GlobalConfig.AgentRuntime)
	}
	if GlobalConfig.TerminalTheme != "auto" {
		t.Fatalf("missing terminal_theme should default to auto, got %q", GlobalConfig.TerminalTheme)
	}
}

func TestInitConfigKeepsExplicitLegacyRuntimeRollback(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("provider: custom\napi_protocol: openai_chat\napi_url: http://127.0.0.1:11434/v1\nmodel_name: local\nagent_runtime: legacy\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitConfig(path); err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.AgentRuntime != "legacy" || GlobalConfig.EffectiveAgentRuntime() != "legacy" {
		t.Fatalf("explicit legacy rollback was not preserved: %#v", GlobalConfig.AgentRuntime)
	}
}

func TestEffectiveAgentRuntimeDefaultsToV3(t *testing.T) {
	if got := (Config{}).EffectiveAgentRuntime(); got != "v3" {
		t.Fatalf("empty runtime=%q want v3", got)
	}
	if got := (Config{AgentRuntime: " LEGACY "}).EffectiveAgentRuntime(); got != "legacy" {
		t.Fatalf("normalized runtime=%q want legacy", got)
	}
}

func TestManageReplaceYAMLRejectsRuntimeInvalidConfigBeforeWrite(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := []byte("provider: custom\napi_protocol: openai_chat\napi_url: http://127.0.0.1:11434/v1\nmodel_name: local\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ManageConfig(map[string]string{
		"action":      "replace_yaml",
		"config_path": path,
		"content":     "provider: mystery\napi_url: file:///tmp/model\nmodel_name: broken\n",
	})
	if err == nil || !strings.Contains(err.Error(), "未写入") {
		t.Fatalf("expected pre-write validation failure, got %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("invalid replacement changed config:\n%s", got)
	}
}
