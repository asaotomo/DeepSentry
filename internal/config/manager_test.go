package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

func TestManageConfigAddSkillSourceCreatesConfig(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	out, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "add_skill_source",
		"source":      "/opt/deepsentry-skills",
	})
	if err != nil {
		t.Fatalf("ManageConfig add_skill_source: %v", err)
	}
	if !strings.Contains(out, "新建配置") {
		t.Fatalf("expected new config message, got %q", out)
	}

	values := readYAMLStringSeq(t, path, "skill_sources")
	want := []string{"skills", "~/.deepsentry/skills", "/opt/deepsentry-skills"}
	if strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected skill_sources: %#v", values)
	}
	if len(GlobalConfig.SkillSources) != 3 {
		t.Fatalf("GlobalConfig not reloaded with skill_sources: %#v", GlobalConfig.SkillSources)
	}
}

func TestValidateManagedModelAdaptationScalars(t *testing.T) {
	valid := map[string]string{
		"model_profile":          "compact",
		"model_parameter_b":      "14",
		"context_window_tokens":  "1048576",
		"context_utilization":    "0.82",
		"reserved_output_tokens": "16384",
		"native_tool_limit":      "8",
	}
	for key, value := range valid {
		if err := validateManagedScalar(key, value); err != nil {
			t.Fatalf("valid %s=%s rejected: %v", key, value, err)
		}
	}
	invalid := map[string]string{
		"model_profile":          "giant",
		"model_parameter_b":      "fourteen",
		"context_window_tokens":  "2048",
		"context_utilization":    "1.0",
		"reserved_output_tokens": "10",
		"native_tool_limit":      "-1",
	}
	for key, value := range invalid {
		if err := validateManagedScalar(key, value); err == nil {
			t.Fatalf("invalid %s=%s accepted", key, value)
		}
	}
}

func TestManagedRuntimeRollbackScalar(t *testing.T) {
	for _, value := range []string{"v3", "legacy", " V3 "} {
		if err := validateManagedScalar("agent_runtime", value); err != nil {
			t.Fatalf("valid agent_runtime=%q rejected: %v", value, err)
		}
	}
	if err := validateManagedScalar("agent_runtime", "v2"); err == nil {
		t.Fatal("unsupported runtime accepted")
	}
	if !allowedScalarConfigKey("agent_runtime") {
		t.Fatal("config_manage set cannot perform documented runtime rollback")
	}
}

func TestManagedTerminalThemeScalar(t *testing.T) {
	for _, value := range []string{"auto", "dark", "light", " LIGHT "} {
		if err := validateManagedScalar("terminal_theme", value); err != nil {
			t.Fatalf("valid terminal_theme=%q rejected: %v", value, err)
		}
	}
	if err := validateManagedScalar("terminal_theme", "sepia"); err == nil {
		t.Fatal("unsupported terminal theme accepted")
	}
	if !allowedScalarConfigKey("terminal_theme") {
		t.Fatal("config_manage cannot update terminal_theme")
	}
}

func TestManageConfigPathArgDoesNotOverrideConfigPath(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("skill_sources:\n  - skills\n"), 0o600); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read seed config: %v", err)
	}

	source := filepath.Join(dir, "external-skills")
	out, err := ManageConfig(map[string]string{
		"action": "add_skill_source",
		"path":   source,
	})
	if err != nil {
		t.Fatalf("ManageConfig add_skill_source with path source: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Fatalf("expected current config path in output, got %q", out)
	}
	values := readYAMLStringSeq(t, path, "skill_sources")
	if !containsString(values, source) {
		t.Fatalf("skill source not added to current config: %#v", values)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("path arg should be treated as skill source, not config file; stat err=%v", err)
	}
}

func TestManageConfigGetAction(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("ssh_password: SuperSecret123!\nskill_sources:\n  - skills\n"), 0o600); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	out, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "get",
		"key":         "skill_sources",
	})
	if err != nil {
		t.Fatalf("ManageConfig get skill_sources: %v", err)
	}
	if !strings.Contains(out, "skills") {
		t.Fatalf("unexpected get output: %q", out)
	}
	secretOut, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "get",
		"key":         "ssh_password",
	})
	if err != nil {
		t.Fatalf("ManageConfig get ssh_password: %v", err)
	}
	if strings.Contains(secretOut, "SuperSecret123!") || !strings.Contains(secretOut, "***") {
		t.Fatalf("secret should be redacted, got %q", secretOut)
	}
	statusOut, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "get_config",
	})
	if err != nil {
		t.Fatalf("ManageConfig get_config alias: %v", err)
	}
	if !strings.Contains(statusOut, "配置文件:") {
		t.Fatalf("get_config should return config status, got %q", statusOut)
	}
}

func TestManageConfigViewAndListAliases(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("skill_sources:\n  - skills\n"), 0o600); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	for _, action := range []string{"view", "show", "list", "overview"} {
		out, err := ManageConfig(map[string]string{"config_path": path, "action": action})
		if err != nil {
			t.Fatalf("ManageConfig alias %q: %v", action, err)
		}
		if !strings.Contains(out, "配置文件:") || !strings.Contains(out, "skills") {
			t.Fatalf("alias %q returned unexpected output: %q", action, out)
		}
	}
}

func TestManageConfigSetSSHBacksUpAndRedactsOutput(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("provider: deepseek\nmodel_name: test-model\n"), 0o600); err != nil {
		t.Fatalf("write seed config: %v", err)
	}

	out, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "set_ssh",
		"host":        "10.0.0.8:22",
		"user":        "root",
		"password":    "SuperSecret123!",
	})
	if err != nil {
		t.Fatalf("ManageConfig set_ssh: %v", err)
	}
	if strings.Contains(out, "SuperSecret123!") {
		t.Fatalf("tool output leaked password: %q", out)
	}
	if GlobalConfig.SSHHost != "10.0.0.8:22" || GlobalConfig.SSHPassword != "SuperSecret123!" {
		t.Fatalf("GlobalConfig not reloaded with ssh values: %#v", GlobalConfig)
	}

	backups, err := filepath.Glob(filepath.Join(dir, ".deepsentry_backups", "config_*.yaml"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one backup, got %#v err=%v", backups, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "SuperSecret123!") {
		t.Fatalf("updated config missing password:\n%s", data)
	}
}

func TestManageConfigAddTargetUpsertsByHost(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, user := range []string{"ubuntu", "admin"} {
		if _, err := ManageConfig(map[string]string{
			"config_path": path,
			"action":      "add_target",
			"protocol":    "ssh",
			"host":        "10.0.0.9:2222",
			"user":        user,
			"tags":        "prod,web",
		}); err != nil {
			t.Fatalf("ManageConfig add_target user=%s: %v", user, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	targets, ok := root["targets"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("expected one upserted target, got %#v", root["targets"])
	}
	target := targets[0].(map[string]any)
	if target["user"] != "admin" {
		t.Fatalf("expected target user updated to admin, got %#v", target)
	}
}

func TestManageConfigAddFTPTargetPreservesTLSAndDataMode(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()
	path := filepath.Join(t.TempDir(), "config.yaml")
	_, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "add_target", "protocol": "ftp", "host": "backup.example:990",
		"user": "backup", "password": "secret", "ftp_tls_mode": "implicit",
		"ftp_tls_server_name": "backup.example", "ftp_tls_ca_file": "/etc/deepsentry/backup-ca.pem",
		"ftp_data_mode": "active", "ftp_active_address": "192.0.2.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(GlobalConfig.Targets) != 1 {
		t.Fatalf("targets=%#v", GlobalConfig.Targets)
	}
	target := GlobalConfig.Targets[0]
	if target.FTPTLSMode != "implicit" || target.FTPTLSServerName != "backup.example" ||
		target.FTPTLSCAFile != "/etc/deepsentry/backup-ca.pem" || target.FTPDataMode != "active" ||
		target.FTPActiveAddress != "192.0.2.10" {
		t.Fatalf("FTP TLS/data fields lost: %#v", target)
	}
}

func TestManageConfigAddTargetAcceptsSeparatePortAndRepairsLegacyHost(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := `target_protocol: local
targets:
  - name: ssh-10.0.0.9
    protocol: ssh
    host: 10.0.0.9
    user: root
    password: old-secret
  - name: ssh-10.0.0.9-2222
    protocol: ssh
    host: 10.0.0.9:2222
    user: root
    password: duplicate-secret
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "add_target",
		"protocol":    "ssh",
		"host":        "10.0.0.9",
		"port":        "2222",
		"user":        "admin",
		"password":    "new-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ssh 10.0.0.9:2222") {
		t.Fatalf("success output must echo canonical endpoint: %q", out)
	}
	if len(GlobalConfig.Targets) != 1 {
		t.Fatalf("legacy bare host should be replaced, not duplicated: %#v", GlobalConfig.Targets)
	}
	target := GlobalConfig.Targets[0]
	if target.Host != "10.0.0.9:2222" || target.User != "admin" || target.Password != "new-secret" {
		t.Fatalf("unexpected normalized target: %#v", target)
	}
}

func TestNormalizeManagedEndpointRejectsConflictingOrInvalidPort(t *testing.T) {
	for _, tc := range []struct {
		host, port string
	}{{"10.0.0.8:22", "2222"}, {"10.0.0.8", "0"}, {"10.0.0.8", "abc"}} {
		if _, err := normalizeManagedEndpoint("ssh", tc.host, tc.port); err == nil {
			t.Fatalf("normalizeManagedEndpoint(%q,%q) should fail", tc.host, tc.port)
		}
	}
	if got, err := normalizeManagedEndpoint("ssh", "2001:db8::1", "2222"); err != nil || got != "[2001:db8::1]:2222" {
		t.Fatalf("IPv6 normalization got=%q err=%v", got, err)
	}
}

func TestManageConfigEnableFleetPromotesSingleTarget(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := `target_protocol: ssh
ssh_host: 198.51.100.42:2222
ssh_user: root
ssh_password: single-secret
ssh_device_type: huawei
ssh_prompt: <Core-SSH>
ssh_enable_password: enable-secret
targets:
  - name: ssh-192.0.2.49
    protocol: ssh
    host: 192.0.2.49:2222
    user: root
    password: target-secret
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	out, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "enable_fleet",
	})
	if err != nil {
		t.Fatalf("ManageConfig enable_fleet: %v", err)
	}
	if !strings.Contains(out, "当前单目标") {
		t.Fatalf("unexpected output: %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if root["target_protocol"] != "local" {
		t.Fatalf("target_protocol = %#v, want local", root["target_protocol"])
	}
	targets, ok := root["targets"].([]any)
	if !ok || len(targets) != 2 {
		t.Fatalf("expected two fleet targets, got %#v", root["targets"])
	}
	foundNetworkSSH := false
	for _, raw := range targets {
		target, _ := raw.(map[string]any)
		if target["host"] == "198.51.100.42:2222" {
			foundNetworkSSH = target["device_type"] == "huawei" && target["prompt"] == "<Core-SSH>" && target["enable_password"] == "enable-secret"
		}
	}
	if !foundNetworkSSH {
		t.Fatalf("promoted SSH network-device fields missing: %#v", targets)
	}
	if len(GlobalConfig.Targets) != 2 || GlobalConfig.TargetProtocol != "local" {
		t.Fatalf("GlobalConfig not reloaded as fleet: %#v", GlobalConfig)
	}
}

func TestManageConfigReplaceYAMLBacksUpInvalidConfig(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("provider: [\n"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	out, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "replace_yaml",
		"content":     "provider: deepseek\nmodel_name: repaired-model\n",
	})
	if err != nil {
		t.Fatalf("ManageConfig replace_yaml: %v", err)
	}
	if !strings.Contains(out, "已替换并重载配置") {
		t.Fatalf("unexpected output: %q", out)
	}
	if GlobalConfig.ModelName != "repaired-model" {
		t.Fatalf("GlobalConfig not reloaded after repair: %#v", GlobalConfig)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".deepsentry_backups", "config_*.yaml"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one invalid-config backup, got %#v err=%v", backups, err)
	}
	backupData, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupData) != "provider: [\n" {
		t.Fatalf("backup should preserve invalid original, got %q", backupData)
	}
}

func TestManageConfigImportClaudeMCPAndToggle(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	claudeJSON := `{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {"TOKEN": "abc"},
      "cwd": "/tmp"
    }
  }
}`
	if _, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "import_claude_mcp",
		"content":     claudeJSON,
	}); err != nil {
		t.Fatalf("import_claude_mcp: %v", err)
	}
	if len(GlobalConfig.MCPServerConfigs) != 1 {
		t.Fatalf("expected one mcp server config, got %#v", GlobalConfig.MCPServerConfigs)
	}
	got := GlobalConfig.MCPServerConfigs[0]
	if got.Name != "fs" || got.Command != "npx" || got.CWD != "/tmp" || got.Env["TOKEN"] != "abc" {
		t.Fatalf("unexpected imported config: %#v", got)
	}

	if _, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "disable_mcp_server",
		"name":        "fs",
	}); err != nil {
		t.Fatalf("disable_mcp_server: %v", err)
	}
	if !GlobalConfig.MCPServerConfigs[0].Disabled {
		t.Fatalf("expected fs disabled: %#v", GlobalConfig.MCPServerConfigs[0])
	}
}

func TestManageConfigAddsStructuredHTTPMCPFields(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := ManageConfig(map[string]string{
		"config_path":         path,
		"action":              "add_mcp_server",
		"name":                "remote-docs",
		"type":                "http",
		"url":                 "https://mcp.example.test/mcp",
		"headers":             "X-Workspace=security",
		"token_env":           "MCP_DOCS_TOKEN",
		"enabled_tools":       "search,read",
		"disabled_tools":      "delete",
		"startup_timeout_sec": "30",
		"tool_timeout_sec":    "120",
		"required":            "true",
	}); err != nil {
		t.Fatalf("add HTTP MCP: %v", err)
	}
	if len(GlobalConfig.MCPServerConfigs) != 1 {
		t.Fatalf("expected one MCP config: %#v", GlobalConfig.MCPServerConfigs)
	}
	got := GlobalConfig.MCPServerConfigs[0]
	if got.Type != "streamable_http" || got.URL != "https://mcp.example.test/mcp" || got.Headers["X-Workspace"] != "security" || got.BearerTokenEnvVar != "MCP_DOCS_TOKEN" || !got.Required {
		t.Fatalf("structured HTTP fields were not preserved: %#v", got)
	}
	if len(got.EnabledTools) != 2 || got.ToolTimeoutSec != 120 || got.StartupTimeoutSec != 30 {
		t.Fatalf("tool filters/timeouts were not preserved: %#v", got)
	}
}

func TestManageConfigImportsClaudeHTTPMCP(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `{"mcpServers":{"docs":{"type":"http","url":"https://mcp.example.test/mcp","headers":{"X-Team":"blue"}}}}`
	if _, err := ManageConfig(map[string]string{"config_path": path, "action": "import_claude_mcp", "content": content}); err != nil {
		t.Fatal(err)
	}
	got := GlobalConfig.MCPServerConfigs[0]
	if got.Type != "streamable_http" || got.Headers["X-Team"] != "blue" {
		t.Fatalf("unexpected imported HTTP MCP: %#v", got)
	}
}

func TestManageConfigToggleSkillSource(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "add_skill_source",
		"source":      "/opt/deepsentry-skills",
	}); err != nil {
		t.Fatalf("add_skill_source: %v", err)
	}
	if _, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "disable_skill_source",
		"source":      "/opt/deepsentry-skills",
	}); err != nil {
		t.Fatalf("disable_skill_source: %v", err)
	}
	if len(GlobalConfig.DisabledSkillSources) != 1 || GlobalConfig.DisabledSkillSources[0] != "/opt/deepsentry-skills" {
		t.Fatalf("unexpected disabled skill sources: %#v", GlobalConfig.DisabledSkillSources)
	}
}

func TestManageConfigToggleSkillByNameCaseInsensitively(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, name := range []string{"fun-brainstorming", "FUN-BRAINSTORMING"} {
		if _, err := ManageConfig(map[string]string{
			"config_path": path,
			"action":      "disable_skill",
			"name":        name,
		}); err != nil {
			t.Fatalf("disable_skill %q: %v", name, err)
		}
	}
	if got := readYAMLStringSeq(t, path, "disabled_skills"); len(got) != 1 || got[0] != "fun-brainstorming" {
		t.Fatalf("disabled_skills should be case-insensitive and idempotent: %#v", got)
	}
	if len(GlobalConfig.DisabledSkills) != 1 {
		t.Fatalf("runtime disabled skills not reloaded: %#v", GlobalConfig.DisabledSkills)
	}
	if _, err := ManageConfig(map[string]string{
		"config_path": path,
		"action":      "enable_skill",
		"name":        "Fun-Brainstorming",
	}); err != nil {
		t.Fatalf("enable_skill: %v", err)
	}
	if got := readYAMLStringSeq(t, path, "disabled_skills"); len(got) != 0 {
		t.Fatalf("enable should remove the name regardless of case: %#v", got)
	}
}

func TestManageConfigTogglesAllSkillsWithoutPath(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := ManageConfig(map[string]string{"config_path": path, "action": "disable_skills"}); err != nil {
		t.Fatalf("disable_skills: %v", err)
	}
	if !GlobalConfig.SkillsDisabled {
		t.Fatal("global Skill switch was not disabled")
	}
	if _, err := ManageConfig(map[string]string{"config_path": path, "action": "enable_skills"}); err != nil {
		t.Fatalf("enable_skills: %v", err)
	}
	if GlobalConfig.SkillsDisabled {
		t.Fatal("global Skill switch was not enabled")
	}
}

func TestManageConfigEnablesOnlyOneSkillAtomically(t *testing.T) {
	old := GlobalConfig
	defer func() { GlobalConfig = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	initial := "skills_disabled: true\ndisabled_skills:\n  - stale-skill\n  - fofamap\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ManageConfig(map[string]string{
		"config_path":      path,
		"action":           "enable_only_skill",
		"name":             "FOFAMAP",
		"available_skills": "fofamap\nfun-brainstorming\nlog-audit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "仅启用 Skill: FOFAMAP") {
		t.Fatalf("unexpected result: %s", out)
	}
	if GlobalConfig.SkillsDisabled {
		t.Fatal("only must also turn on the global Skill switch")
	}
	doc, _, err := loadConfigNode(path)
	if err != nil {
		t.Fatal(err)
	}
	got := readScalarSeq(ensureConfigRoot(doc), "disabled_skills")
	for _, want := range []string{"stale-skill", "fun-brainstorming", "log-audit"} {
		if !containsStringFold(got, want) {
			t.Fatalf("disabled_skills=%#v missing %q", got, want)
		}
	}
	if containsStringFold(got, "fofamap") {
		t.Fatalf("selected Skill remained disabled: %#v", got)
	}
}

func readYAMLStringSeq(t *testing.T, path, key string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var root map[string][]string
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return root[key]
}
