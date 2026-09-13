package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ManageConfig performs controlled config.yaml maintenance for Agent/tool use.
// Mutating actions always create a timestamped backup before writing.
func ManageConfig(args map[string]string) (string, error) {
	if args == nil {
		args = map[string]string{}
	}
	action := strings.ToLower(firstConfigArg(args, "action", "op"))
	if action == "" {
		action = "status"
	}
	path := resolveConfigPath(firstConfigArg(args, "config_path", "config_file"))

	switch action {
	case "status", "show", "show_config", "view", "list", "overview":
		return configStatus(path)
	case "get", "read", "get_config", "read_config", "export":
		return configGet(path, args)
	case "validate":
		doc, _, err := loadConfigNode(path)
		if err != nil {
			return "", err
		}
		if err := validateConfigNode(doc); err != nil {
			return "", fmt.Errorf("配置校验失败: %w", err)
		}
		return fmt.Sprintf("[OK] 配置 YAML 与运行时语义校验通过: %s", path), nil
	case "backup":
		backupPath, err := backupConfig(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("[OK] 已备份配置: %s", backupPath), nil
	case "replace_yaml", "apply_yaml", "repair":
		return manageReplaceYAML(path, args)
	}

	doc, existed, err := loadConfigNode(path)
	if err != nil {
		return "", err
	}
	root := ensureConfigRoot(doc)

	var changed string
	switch action {
	case "add_skill_source", "add_skill", "skill_source":
		changed, err = manageAddSkillSource(root, args)
	case "add_mcp_server", "add_mcp", "mcp_server":
		changed, err = manageAddMCPServer(root, args)
	case "import_claude_mcp", "import_mcp_json":
		changed, err = manageImportClaudeMCP(root, args)
	case "disable_mcp_server", "disable_mcp", "mcp_off":
		changed, err = manageToggleMCPServer(root, args, true)
	case "enable_mcp_server", "enable_mcp", "mcp_on":
		changed, err = manageToggleMCPServer(root, args, false)
	case "remove_mcp_server", "remove_mcp":
		changed, err = manageRemoveMCPServer(root, args)
	case "disable_skill", "skill_off":
		changed, err = manageToggleSkill(root, args, true)
	case "enable_skill", "skill_on":
		changed, err = manageToggleSkill(root, args, false)
	case "disable_skills", "skills_off":
		changed, err = manageToggleSkillSystem(root, true)
	case "enable_skills", "skills_on":
		changed, err = manageToggleSkillSystem(root, false)
	case "enable_only_skill", "skill_only":
		changed, err = manageEnableOnlySkill(root, args)
	case "disable_skill_source", "skill_source_off":
		changed, err = manageToggleSkillSource(root, args, true)
	case "enable_skill_source", "skill_source_on":
		changed, err = manageToggleSkillSource(root, args, false)
	case "remove_skill_source", "skill_remove":
		changed, err = manageRemoveSkillSource(root, args)
	case "add_target", "upsert_target":
		changed, err = manageAddTarget(root, args)
	case "enable_fleet", "make_fleet", "fleet_mode":
		changed, err = manageEnableFleet(root, args)
	case "set_ssh", "set_ssh_target":
		changed, err = manageSetSSH(root, args)
	case "set", "set_value":
		changed, err = manageSetScalar(root, args)
	default:
		return "", fmt.Errorf("未知配置管理动作: %s", action)
	}
	if err != nil {
		return "", err
	}
	if err := validateConfigNode(doc); err != nil {
		return "", fmt.Errorf("变更后的配置无效，未写入: %w", err)
	}

	backupPath := "(新建配置，无旧文件)"
	if existed {
		if backupPath, err = backupConfig(path); err != nil {
			return "", err
		}
	}
	if err := writeConfigNode(path, doc); err != nil {
		return "", err
	}
	if err := reloadManagedConfig(path); err != nil {
		return "", err
	}

	return fmt.Sprintf("[OK] 已更新配置: %s\n[OK] 备份: %s\n[OK] 变更: %s", path, backupPath, changed), nil
}

func configStatus(path string) (string, error) {
	doc, existed, err := loadConfigNode(path)
	if err != nil {
		return "", err
	}
	if !existed {
		return fmt.Sprintf("配置文件不存在，将在首次写入时创建: %s", path), nil
	}
	root := ensureConfigRoot(doc)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("配置文件: %s\n", path))
	b.WriteString(fmt.Sprintf("skill_sources: %s\n", strings.Join(readScalarSeq(root, "skill_sources"), ", ")))
	b.WriteString(fmt.Sprintf("disabled_skill_sources: %s\n", strings.Join(readScalarSeq(root, "disabled_skill_sources"), ", ")))
	b.WriteString(fmt.Sprintf("disabled_skills: %s\n", strings.Join(readScalarSeq(root, "disabled_skills"), ", ")))
	b.WriteString(fmt.Sprintf("skills_disabled: %v\n", readBoolScalar(root, "skills_disabled")))
	b.WriteString(fmt.Sprintf("mcp_servers: %s\n", strings.Join(readScalarSeq(root, "mcp_servers"), ", ")))
	b.WriteString(fmt.Sprintf("mcp_server_configs: %s\n", strings.Join(readMCPServerConfigSummaries(root), ", ")))
	b.WriteString(fmt.Sprintf("targets: %d 个\n", len(readTargets(root))))
	configured := GlobalConfig
	if provider := readScalar(root, "provider"); provider != "" {
		configured.Provider = provider
	}
	if apiURL := readScalar(root, "api_url"); apiURL != "" {
		configured.ApiURL = apiURL
	}
	if model := readScalar(root, "model_name"); model != "" {
		configured.ModelName = model
	}
	if profile := readScalar(root, "model_profile"); profile != "" {
		configured.ModelProfile = profile
	}
	if raw := readScalar(root, "context_window_tokens"); raw != "" {
		configured.ContextWindowTokens, _ = strconv.Atoi(raw)
	}
	if raw := readScalar(root, "model_parameter_b"); raw != "" {
		configured.ModelParameterB, _ = strconv.ParseFloat(raw, 64)
	}
	if raw := readScalar(root, "context_utilization"); raw != "" {
		configured.ContextUtilization, _ = strconv.ParseFloat(raw, 64)
	}
	if raw := readScalar(root, "reserved_output_tokens"); raw != "" {
		configured.ReservedOutputTokens, _ = strconv.Atoi(raw)
	}
	if raw := readScalar(root, "native_tool_limit"); raw != "" {
		configured.NativeToolLimit, _ = strconv.Atoi(raw)
	}
	capabilities := configured.EffectiveModelCapabilities()
	b.WriteString(fmt.Sprintf("model_adaptation: profile=%s, context=%d, output_reserve=%d, native_tool_limit=%d, source=%s\n",
		capabilities.PromptProfile, capabilities.ContextWindowTokens, capabilities.ReservedOutputTokens,
		capabilities.NativeToolLimit, capabilities.DetectionSource))
	if host := readScalar(root, "ssh_host"); host != "" && readScalar(root, "target_protocol") != "local" {
		b.WriteString(fmt.Sprintf("single ssh: %s@%s\n", valueOr(readScalar(root, "ssh_user"), "root"), host))
	}
	return strings.TrimSpace(b.String()), nil
}

func configGet(path string, args map[string]string) (string, error) {
	key := firstConfigArg(args, "key", "name")
	if key == "" {
		return configStatus(path)
	}
	doc, existed, err := loadConfigNode(path)
	if err != nil {
		return "", err
	}
	if !existed {
		return "", fmt.Errorf("配置文件不存在: %s", path)
	}
	root := ensureConfigRoot(doc)
	switch key {
	case "skill_sources", "disabled_skill_sources", "disabled_skills", "mcp_servers":
		return fmt.Sprintf("%s: %s", key, strings.Join(readScalarSeq(root, key), ", ")), nil
	case "mcp_server_configs":
		return fmt.Sprintf("%s: %s", key, strings.Join(readMCPServerConfigSummaries(root), ", ")), nil
	case "targets":
		return fmt.Sprintf("targets: %d 个", len(readTargets(root))), nil
	}
	node := findMapValue(root, key)
	if node == nil {
		return fmt.Sprintf("%s: (未配置)", key), nil
	}
	if node.Kind == yaml.ScalarNode {
		return fmt.Sprintf("%s: %s", key, redactConfigValue(key, node.Value)), nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return "", err
	}
	_ = enc.Close()
	return fmt.Sprintf("%s:\n%s", key, redactSensitiveText(buf.String())), nil
}

func manageAddSkillSource(root *yaml.Node, args map[string]string) (string, error) {
	source := firstConfigArg(args, "source", "skill_source", "path", "dir")
	if source == "" {
		return "", fmt.Errorf("source/skill_source/path 不能为空")
	}
	values := readScalarSeq(root, "skill_sources")
	if len(values) == 0 {
		values = []string{"skills", "~/.deepsentry/skills"}
	}
	if !containsString(values, source) {
		values = append(values, source)
	}
	setScalarSeq(root, "skill_sources", values)
	return fmt.Sprintf("已加入 skill_sources: %s", source), nil
}

func manageAddMCPServer(root *yaml.Node, args map[string]string) (string, error) {
	spec := firstConfigArg(args, "spec", "server")
	name := firstConfigArg(args, "name")
	command := firstConfigArg(args, "command", "cmd")
	rawArgs := firstConfigArg(args, "args", "arguments")
	cwd := firstConfigArg(args, "cwd", "dir")
	env := parseEnvArg(firstConfigArg(args, "env"))
	serverType := strings.ToLower(valueOr(firstConfigArg(args, "type"), "stdio"))
	if serverType == "http" {
		serverType = "streamable_http"
	}

	structured := cwd != "" || len(env) > 0 || firstConfigArg(args, "structured") != "" || serverType != "stdio" ||
		firstConfigArg(args, "url", "headers", "bearer_token_env_var", "token_env", "enabled_tools", "disabled_tools", "startup_timeout_sec", "tool_timeout_sec", "required") != ""
	if structured {
		if name == "" || (command == "" && serverType == "stdio") {
			return "", fmt.Errorf("spec 为空时 name 和 command 必填")
		}
		startupTimeout, err := parseOptionalIntArg(args, "startup_timeout_sec", 0, 300)
		if err != nil {
			return "", err
		}
		toolTimeout, err := parseOptionalIntArg(args, "tool_timeout_sec", 0, 3600)
		if err != nil {
			return "", err
		}
		cfg := MCPServerConfig{
			Name:              name,
			Type:              serverType,
			Command:           command,
			Args:              splitCSVArgs(rawArgs),
			Env:               env,
			CWD:               cwd,
			URL:               firstConfigArg(args, "url"),
			Headers:           parseEnvArg(firstConfigArg(args, "headers")),
			BearerTokenEnvVar: firstConfigArg(args, "bearer_token_env_var", "token_env"),
			EnabledTools:      splitCSVArgs(firstConfigArg(args, "enabled_tools")),
			DisabledTools:     splitCSVArgs(firstConfigArg(args, "disabled_tools")),
			StartupTimeoutSec: startupTimeout,
			ToolTimeoutSec:    toolTimeout,
			Required:          parseBoolArg(firstConfigArg(args, "required")),
		}
		upsertMCPServerConfig(root, cfg)
		return fmt.Sprintf("已加入 mcp_server_configs: %s", name), nil
	}

	if spec == "" {
		if name == "" || command == "" {
			return "", fmt.Errorf("spec 为空时 name 和 command 必填")
		}
		spec = fmt.Sprintf("%s:%s", name, command)
		if rawArgs != "" {
			spec += ":" + rawArgs
		}
	}
	if strings.Count(spec, ":") < 1 {
		return "", fmt.Errorf("mcp spec 格式应为 name:command:arg1,arg2")
	}
	values := readScalarSeq(root, "mcp_servers")
	if !containsString(values, spec) {
		values = append(values, spec)
	}
	setScalarSeq(root, "mcp_servers", values)
	return fmt.Sprintf("已加入 mcp_servers: %s", spec), nil
}

func manageImportClaudeMCP(root *yaml.Node, args map[string]string) (string, error) {
	content := firstConfigArg(args, "content", "json", "data")
	if content == "" {
		path := firstConfigArg(args, "import_path", "source", "path")
		if path == "" {
			return "", fmt.Errorf("content/json 或 import_path/source 必填")
		}
		data, err := os.ReadFile(resolveConfigPath(path))
		if err != nil {
			return "", fmt.Errorf("读取 Claude MCP JSON 失败: %w", err)
		}
		content = string(data)
	}
	var raw struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			CWD     string            `json:"cwd"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return "", fmt.Errorf("解析 Claude MCP JSON 失败: %w", err)
	}
	if len(raw.MCPServers) == 0 {
		return "", fmt.Errorf("JSON 中未找到 mcpServers")
	}
	names := make([]string, 0, len(raw.MCPServers))
	for name, server := range raw.MCPServers {
		cfg := MCPServerConfig{
			Name:    name,
			Type:    valueOr(strings.ToLower(server.Type), "stdio"),
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
			CWD:     server.CWD,
			URL:     server.URL,
			Headers: server.Headers,
		}
		if cfg.Type == "http" {
			cfg.Type = "streamable_http"
		}
		upsertMCPServerConfig(root, cfg)
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("已导入 Claude MCP Server: %s", strings.Join(names, ", ")), nil
}

func manageToggleMCPServer(root *yaml.Node, args map[string]string, disabled bool) (string, error) {
	name := firstConfigArg(args, "name", "server")
	if name == "" {
		return "", fmt.Errorf("name/server 不能为空")
	}
	if toggleMCPServerConfig(root, name, disabled) {
		return fmt.Sprintf("已%s mcp_server_configs: %s", mapBool(disabled, "禁用", "启用"), name), nil
	}
	if disabled && removeMCPServerSpec(root, name) {
		return fmt.Sprintf("已从旧 mcp_servers 移除: %s", name), nil
	}
	return "", fmt.Errorf("未找到 MCP Server: %s", name)
}

func manageRemoveMCPServer(root *yaml.Node, args map[string]string) (string, error) {
	name := firstConfigArg(args, "name", "server")
	if name == "" {
		return "", fmt.Errorf("name/server 不能为空")
	}
	removedStruct := removeMCPServerConfig(root, name)
	removedLegacy := removeMCPServerSpec(root, name)
	if !removedStruct && !removedLegacy {
		return "", fmt.Errorf("未找到 MCP Server: %s", name)
	}
	return fmt.Sprintf("已移除 MCP Server: %s", name), nil
}

func manageToggleSkillSource(root *yaml.Node, args map[string]string, disabled bool) (string, error) {
	source := firstConfigArg(args, "source", "path", "dir")
	if source == "" {
		return "", fmt.Errorf("source/path/dir 不能为空")
	}
	values := readScalarSeq(root, "disabled_skill_sources")
	if disabled {
		if !containsString(values, source) {
			values = append(values, source)
		}
		setScalarSeq(root, "disabled_skill_sources", values)
		return fmt.Sprintf("已禁用 Skill 来源: %s", source), nil
	}
	values = removeString(values, source)
	setScalarSeq(root, "disabled_skill_sources", values)
	return fmt.Sprintf("已启用 Skill 来源: %s", source), nil
}

func manageToggleSkill(root *yaml.Node, args map[string]string, disabled bool) (string, error) {
	name := firstConfigArg(args, "name", "skill", "skill_name")
	if name == "" {
		return "", fmt.Errorf("name/skill/skill_name 不能为空")
	}
	if name == "*" || len([]rune(name)) > 128 || strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("Skill 名称无效")
	}
	values := readScalarSeq(root, "disabled_skills")
	if disabled {
		if !containsStringFold(values, name) {
			values = append(values, name)
		}
		setScalarSeq(root, "disabled_skills", values)
		return fmt.Sprintf("已禁用 Skill: %s", name), nil
	}
	values = removeStringFold(values, name)
	setScalarSeq(root, "disabled_skills", values)
	return fmt.Sprintf("已启用 Skill: %s", name), nil
}

func manageToggleSkillSystem(root *yaml.Node, disabled bool) (string, error) {
	setBoolScalar(root, "skills_disabled", disabled)
	if disabled {
		return "已全局禁用 Skill 功能", nil
	}
	return "已全局启用 Skill 功能", nil
}

func manageEnableOnlySkill(root *yaml.Node, args map[string]string) (string, error) {
	name := strings.TrimSpace(firstConfigArg(args, "name", "skill", "skill_name"))
	if name == "" || name == "*" || len([]rune(name)) > 128 || strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("Skill 名称无效")
	}

	// Preserve existing disabled names so a temporarily missing Skill cannot
	// become enabled unexpectedly when its source returns. Remove the selected
	// name and add every other currently discovered name to the denylist.
	disabled := removeStringFold(readScalarSeq(root, "disabled_skills"), name)
	for _, candidate := range strings.Split(firstConfigArg(args, "available_skills", "skills"), "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.EqualFold(candidate, name) {
			continue
		}
		if candidate == "*" || len([]rune(candidate)) > 128 || strings.ContainsAny(candidate, "\r\n\x00") {
			return "", fmt.Errorf("Skill 名称无效: %s", candidate)
		}
		if !containsStringFold(disabled, candidate) {
			disabled = append(disabled, candidate)
		}
	}
	setScalarSeq(root, "disabled_skills", disabled)
	setBoolScalar(root, "skills_disabled", false)
	return fmt.Sprintf("仅启用 Skill: %s（其余已发现 Skill 均已禁用）", name), nil
}

func manageRemoveSkillSource(root *yaml.Node, args map[string]string) (string, error) {
	source := firstConfigArg(args, "source", "path", "dir")
	if source == "" {
		return "", fmt.Errorf("source/path/dir 不能为空")
	}
	values := removeString(readScalarSeq(root, "skill_sources"), source)
	setScalarSeq(root, "skill_sources", values)
	disabled := removeString(readScalarSeq(root, "disabled_skill_sources"), source)
	setScalarSeq(root, "disabled_skill_sources", disabled)
	return fmt.Sprintf("已移除 Skill 来源: %s", source), nil
}

func manageAddTarget(root *yaml.Node, args map[string]string) (string, error) {
	host := firstConfigArg(args, "host", "ssh_host", "addr", "address")
	if host == "" {
		return "", fmt.Errorf("host 不能为空")
	}
	protocol := strings.ToLower(valueOr(firstConfigArg(args, "protocol"), "ssh"))
	var err error
	host, err = normalizeManagedEndpoint(protocol, host, firstConfigArg(args, "port"))
	if err != nil {
		return "", err
	}
	name := firstConfigArg(args, "name")
	if name == "" {
		name = defaultTargetName(protocol, host)
	}
	replaced := upsertTarget(root, configTargetFromArgs(name, protocol, host, args))
	return fmt.Sprintf("已%s targets: %s (%s %s)", mapBool(replaced, "更新", "添加"), name, protocol, host), nil
}

func manageEnableFleet(root *yaml.Node, args map[string]string) (string, error) {
	changes := []string{}
	if t, ok := currentSingleTarget(root); ok {
		replaced := upsertTarget(root, t)
		changes = append(changes, fmt.Sprintf("%s当前单目标: %s (%s %s)", mapBool(replaced, "更新", "加入"), t.Name, t.Protocol, t.Host))
	}
	if host := firstConfigArg(args, "host", "ssh_host", "addr", "address"); host != "" {
		protocol := strings.ToLower(valueOr(firstConfigArg(args, "protocol"), "ssh"))
		normalizedHost, err := normalizeManagedEndpoint(protocol, host, firstConfigArg(args, "port"))
		if err != nil {
			return "", err
		}
		host = normalizedHost
		name := firstConfigArg(args, "name")
		if name == "" {
			name = defaultTargetName(protocol, host)
		}
		t := configTargetFromArgs(name, protocol, host, args)
		replaced := upsertTarget(root, t)
		changes = append(changes, fmt.Sprintf("%s新目标: %s (%s %s)", mapBool(replaced, "更新", "加入"), t.Name, t.Protocol, t.Host))
	}
	if len(readTargets(root)) == 0 {
		return "", fmt.Errorf("没有可用 targets；请提供 host 或先配置单台 SSH/Telnet/FTP")
	}
	setScalar(root, "target_protocol", "local")
	if parseBoolArg(firstConfigArg(args, "clear_single", "clear_single_target")) {
		clearSingleTargetScalars(root)
		changes = append(changes, "已清理单目标连接字段")
	}
	if len(changes) == 0 {
		changes = append(changes, "已启用 Fleet 控制端模式")
	}
	return strings.Join(changes, "; "), nil
}

func manageSetSSH(root *yaml.Node, args map[string]string) (string, error) {
	host := firstConfigArg(args, "host", "ssh_host", "addr", "address")
	if host == "" {
		return "", fmt.Errorf("host/ssh_host 不能为空")
	}
	var err error
	host, err = normalizeManagedEndpoint("ssh", host, firstConfigArg(args, "port"))
	if err != nil {
		return "", err
	}
	user := valueOr(firstConfigArg(args, "user", "ssh_user", "username"), "root")
	password := firstConfigArg(args, "password", "ssh_password", "pass")
	keyPath := firstConfigArg(args, "key_path", "ssh_key_path", "key")
	deviceType := valueOr(firstConfigArg(args, "device_type", "ssh_device_type"), "auto")
	setScalar(root, "target_protocol", "ssh")
	setScalar(root, "ssh_host", host)
	setScalar(root, "ssh_user", user)
	setScalar(root, "ssh_password", password)
	setScalar(root, "ssh_key_path", keyPath)
	if passphrase := firstConfigArg(args, "key_passphrase", "ssh_key_passphrase", "passphrase"); passphrase != "" {
		setScalar(root, "ssh_key_passphrase", passphrase)
	}
	setScalar(root, "ssh_device_type", deviceType)
	setScalar(root, "ssh_prompt", firstConfigArg(args, "prompt", "ssh_prompt"))
	setScalar(root, "ssh_enable_password", firstConfigArg(args, "enable_password", "ssh_enable_password"))
	return fmt.Sprintf("已设置单目标 SSH: %s@%s", user, host), nil
}

func manageSetScalar(root *yaml.Node, args map[string]string) (string, error) {
	key := firstConfigArg(args, "key", "name")
	value := firstConfigArg(args, "value")
	if key == "" {
		return "", fmt.Errorf("key 不能为空")
	}
	if !allowedScalarConfigKey(key) {
		return "", fmt.Errorf("不允许通过 set 修改该字段: %s", key)
	}
	if err := validateManagedScalar(key, value); err != nil {
		return "", err
	}
	setScalar(root, key, value)
	return fmt.Sprintf("已设置 %s = %s", key, redactConfigValue(key, value)), nil
}

func validateManagedScalar(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "agent_runtime":
		switch strings.ToLower(value) {
		case "legacy", "v3":
			return nil
		default:
			return fmt.Errorf("agent_runtime 必须是 legacy|v3，收到 %q", value)
		}
	case "terminal_theme":
		switch strings.ToLower(value) {
		case "auto", "dark", "light":
			return nil
		default:
			return fmt.Errorf("terminal_theme 必须是 auto|dark|light，收到 %q", value)
		}
	case "model_profile":
		switch strings.ToLower(value) {
		case "auto", ModelProfileCompact, ModelProfileBalanced, ModelProfileFull:
			return nil
		default:
			return fmt.Errorf("model_profile 必须是 auto|compact|balanced|full，收到 %q", value)
		}
	case "model_parameter_b":
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n < 0 || n > 1000 {
			return fmt.Errorf("model_parameter_b 必须是 0-1000 的数字，收到 %q", value)
		}
	case "context_window_tokens":
		n, err := strconv.Atoi(value)
		if err != nil || (n != 0 && (n < 4096 || n > 4_194_304)) {
			return fmt.Errorf("context_window_tokens 必须是 0 或 4096-4194304 的整数，收到 %q", value)
		}
	case "context_utilization":
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || (n != 0 && (n < 0.40 || n > 0.90)) {
			return fmt.Errorf("context_utilization 必须是 0 或 0.40-0.90，收到 %q", value)
		}
	case "reserved_output_tokens":
		n, err := strconv.Atoi(value)
		if err != nil || (n != 0 && n < 512) || n > 1_048_576 {
			return fmt.Errorf("reserved_output_tokens 必须是 0 或 512-1048576 的整数，收到 %q", value)
		}
	case "native_tool_limit":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 1000 {
			return fmt.Errorf("native_tool_limit 必须是 0-1000 的整数，收到 %q", value)
		}
	case "ssh_host_key_policy":
		switch strings.ToLower(value) {
		case "strict", "accept-new", "insecure":
			return nil
		default:
			return fmt.Errorf("ssh_host_key_policy 必须是 strict|accept-new|insecure，收到 %q", value)
		}
	case "ssh_legacy_compat":
		switch strings.ToLower(value) {
		case "true", "1", "yes", "on", "false", "0", "no", "off":
			return nil
		default:
			return fmt.Errorf("ssh_legacy_compat 必须是 true|false，收到 %q", value)
		}
	case "ssh_connect_timeout_sec":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 120 {
			return fmt.Errorf("ssh_connect_timeout_sec 必须是 0-120 的整数，收到 %q", value)
		}
	case "archive_max_entries":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1_000_000 {
			return fmt.Errorf("archive_max_entries 必须是 1-1000000 的整数，收到 %q", value)
		}
	case "archive_max_file_bytes", "archive_max_total_bytes":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1_048_576 || n > 1<<50 {
			return fmt.Errorf("%s 必须是 1048576-%d 的整数字节，收到 %q", key, int64(1<<50), value)
		}
	}
	return nil
}

func manageReplaceYAML(path string, args map[string]string) (string, error) {
	content := firstConfigArg(args, "content", "yaml", "data")
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content/yaml 不能为空")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return "", fmt.Errorf("新配置 YAML 解析失败，未写入: %w", err)
	}
	if len(doc.Content) > 0 && doc.Content[0] != nil && doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("新配置根节点必须是 YAML mapping")
	}
	ensureConfigRoot(&doc)
	if err := validateConfigNode(&doc); err != nil {
		return "", fmt.Errorf("新配置无效，未写入: %w", err)
	}

	backupPath := "(新建配置，无旧文件)"
	if _, err := os.Stat(path); err == nil {
		var backupErr error
		backupPath, backupErr = backupConfig(path)
		if backupErr != nil {
			return "", backupErr
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("检查配置文件失败: %w", err)
	}
	if err := writeConfigNode(path, &doc); err != nil {
		return "", err
	}
	if err := reloadManagedConfig(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("[OK] 已替换并重载配置: %s\n[OK] 备份: %s", path, backupPath), nil
}

func loadConfigNode(path string) (*yaml.Node, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newConfigDoc(), false, nil
		}
		return nil, false, fmt.Errorf("读取配置失败: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return newConfigDoc(), true, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, true, fmt.Errorf("配置 YAML 解析失败: %w", err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	return &doc, true, nil
}

func newConfigDoc() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{{
			Kind: yaml.MappingNode,
		}},
	}
}

func ensureConfigRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 || doc.Content[0] == nil {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		root.Kind = yaml.MappingNode
		root.Content = nil
	}
	return root
}

func writeConfigNode(path string, doc *yaml.Node) error {
	data, err := encodeConfigNode(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

func encodeConfigNode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("编码配置失败: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("编码配置失败: %w", err)
	}
	return buf.Bytes(), nil
}

func validateConfigNode(doc *yaml.Node) error {
	data, err := encodeConfigNode(doc)
	if err != nil {
		return err
	}
	reader := viper.New()
	reader.SetConfigType("yaml")
	reader.SetDefault("provider", "deepseek")
	reader.SetDefault("api_protocol", "auto")
	reader.SetDefault("api_url", "https://api.deepseek.com")
	reader.SetDefault("model_name", "deepseek-flash")
	reader.SetDefault("model_profile", "auto")
	reader.SetDefault("ssh_host_key_policy", "accept-new")
	reader.SetDefault("ssh_known_hosts_path", "~/.deepsentry/known_hosts")
	reader.SetDefault("ssh_legacy_compat", "true")
	reader.SetDefault("ssh_connect_timeout_sec", 25)
	reader.SetDefault("scheduler_timezone", "Local")
	if err := reader.ReadConfig(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("解析配置失败: %w", err)
	}
	var candidate Config
	if err := reader.Unmarshal(&candidate); err != nil {
		return fmt.Errorf("映射配置失败: %w", err)
	}
	ApplyProviderDefaults(&candidate)
	if err := applyRawCaseSensitiveData(data, &candidate); err != nil {
		return err
	}
	return ValidateRuntimeConfig(candidate)
}

func backupConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取待备份配置失败: %w", err)
	}
	dir := filepath.Join(filepath.Dir(path), ".deepsentry_backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	name := fmt.Sprintf("config_%s.yaml", time.Now().Format("20060102_150405_000000000"))
	backupPath := filepath.Join(dir, name)
	// #nosec G703 -- path 是当前运行时已加载的本机配置，备份文件名由程序固定生成且权限为 0600。
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", fmt.Errorf("写入备份失败: %w", err)
	}
	return backupPath, nil
}

func reloadManagedConfig(path string) error {
	setViperDefaults()
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("重新读取配置失败: %w", err)
	}
	var loaded Config
	if err := viper.Unmarshal(&loaded); err != nil {
		return fmt.Errorf("重新解析配置失败: %w", err)
	}
	ApplyProviderDefaults(&loaded)
	if err := applyRawCaseSensitiveConfig(path, &loaded); err != nil {
		return fmt.Errorf("读取大小写敏感配置失败: %w", err)
	}
	if err := ValidateRuntimeConfig(loaded); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	GlobalConfig = loaded
	return nil
}

func resolveConfigPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		path = strings.TrimSpace(viper.ConfigFileUsed())
	}
	if path == "" {
		path = "config.yaml"
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func firstConfigArg(args map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(args[key]); v != "" {
			return v
		}
	}
	return ""
}

func containsStringFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func removeStringFold(values []string, target string) []string {
	target = strings.TrimSpace(target)
	out := values[:0]
	for _, value := range values {
		if !strings.EqualFold(strings.TrimSpace(value), target) {
			out = append(out, value)
		}
	}
	return out
}

func ensureSeq(root *yaml.Node, key string) *yaml.Node {
	if node := findMapValue(root, key); node != nil {
		if node.Kind != yaml.SequenceNode {
			node.Kind = yaml.SequenceNode
			node.Content = nil
		}
		return node
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = append(root.Content, scalarNode(key), seq)
	return seq
}

func setScalarSeq(root *yaml.Node, key string, values []string) {
	seq := ensureSeq(root, key)
	seq.Content = nil
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		seq.Content = append(seq.Content, scalarNode(value))
	}
}

func readScalarSeq(root *yaml.Node, key string) []string {
	node := findMapValue(root, key)
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind == yaml.ScalarNode && strings.TrimSpace(item.Value) != "" {
			values = append(values, item.Value)
		}
	}
	return values
}

func setScalar(root *yaml.Node, key, value string) {
	if node := findMapValue(root, key); node != nil {
		node.Kind = yaml.ScalarNode
		node.Tag = "!!str"
		node.Value = value
		node.Content = nil
		return
	}
	root.Content = append(root.Content, scalarNode(key), scalarNode(value))
}

func readScalar(root *yaml.Node, key string) string {
	node := findMapValue(root, key)
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func findMapValue(root *yaml.Node, key string) *yaml.Node {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i+1]
		}
	}
	return nil
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func boolNode(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: mapBool(value, "true", "false")}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func stringSeqNode(values []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			seq.Content = append(seq.Content, scalarNode(value))
		}
	}
	return seq
}

func stringMapNode(values map[string]string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		node.Content = append(node.Content, scalarNode(key), scalarNode(values[key]))
	}
	return node
}

func mcpServerConfigNode(cfg MCPServerConfig) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	node.Content = append(node.Content, scalarNode("name"), scalarNode(cfg.Name))
	node.Content = append(node.Content, scalarNode("type"), scalarNode(valueOr(cfg.Type, "stdio")))
	if cfg.Command != "" {
		node.Content = append(node.Content, scalarNode("command"), scalarNode(cfg.Command))
	}
	if len(cfg.Args) > 0 {
		node.Content = append(node.Content, scalarNode("args"), stringSeqNode(cfg.Args))
	}
	if len(cfg.Env) > 0 {
		node.Content = append(node.Content, scalarNode("env"), stringMapNode(cfg.Env))
	}
	if cfg.CWD != "" {
		node.Content = append(node.Content, scalarNode("cwd"), scalarNode(cfg.CWD))
	}
	if cfg.URL != "" {
		node.Content = append(node.Content, scalarNode("url"), scalarNode(cfg.URL))
	}
	if len(cfg.Headers) > 0 {
		node.Content = append(node.Content, scalarNode("headers"), stringMapNode(cfg.Headers))
	}
	if cfg.BearerTokenEnvVar != "" {
		node.Content = append(node.Content, scalarNode("bearer_token_env_var"), scalarNode(cfg.BearerTokenEnvVar))
	}
	if len(cfg.EnabledTools) > 0 {
		node.Content = append(node.Content, scalarNode("enabled_tools"), stringSeqNode(cfg.EnabledTools))
	}
	if len(cfg.DisabledTools) > 0 {
		node.Content = append(node.Content, scalarNode("disabled_tools"), stringSeqNode(cfg.DisabledTools))
	}
	if cfg.StartupTimeoutSec > 0 {
		node.Content = append(node.Content, scalarNode("startup_timeout_sec"), intNode(cfg.StartupTimeoutSec))
	}
	if cfg.ToolTimeoutSec > 0 {
		node.Content = append(node.Content, scalarNode("tool_timeout_sec"), intNode(cfg.ToolTimeoutSec))
	}
	if cfg.Required {
		node.Content = append(node.Content, scalarNode("required"), boolNode(true))
	}
	if cfg.Disabled {
		node.Content = append(node.Content, scalarNode("disabled"), boolNode(true))
	}
	return node
}

func upsertMCPServerConfig(root *yaml.Node, cfg MCPServerConfig) {
	seq := ensureSeq(root, "mcp_server_configs")
	node := mcpServerConfigNode(cfg)
	for i, item := range seq.Content {
		if readScalar(item, "name") == cfg.Name {
			seq.Content[i] = node
			return
		}
	}
	seq.Content = append(seq.Content, node)
}

func toggleMCPServerConfig(root *yaml.Node, name string, disabled bool) bool {
	seq := findMapValue(root, "mcp_server_configs")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range seq.Content {
		if readScalar(item, "name") == name {
			setBoolScalar(item, "disabled", disabled)
			return true
		}
	}
	return false
}

func removeMCPServerConfig(root *yaml.Node, name string) bool {
	seq := findMapValue(root, "mcp_server_configs")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	out := seq.Content[:0]
	removed := false
	for _, item := range seq.Content {
		if readScalar(item, "name") == name {
			removed = true
			continue
		}
		out = append(out, item)
	}
	seq.Content = out
	return removed
}

func removeMCPServerSpec(root *yaml.Node, name string) bool {
	values := readScalarSeq(root, "mcp_servers")
	out := values[:0]
	removed := false
	for _, value := range values {
		if value == name || strings.HasPrefix(value, name+":") {
			removed = true
			continue
		}
		out = append(out, value)
	}
	if removed {
		setScalarSeq(root, "mcp_servers", out)
	}
	return removed
}

func readMCPServerConfigSummaries(root *yaml.Node) []string {
	seq := findMapValue(root, "mcp_server_configs")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(seq.Content))
	for _, item := range seq.Content {
		name := readScalar(item, "name")
		if name == "" {
			continue
		}
		label := name
		if readBoolScalar(item, "disabled") {
			label += "(disabled)"
		}
		if typ := readScalar(item, "type"); typ != "" && typ != "stdio" {
			label += "[" + typ + "]"
		}
		out = append(out, label)
	}
	return out
}

func setBoolScalar(root *yaml.Node, key string, value bool) {
	if node := findMapValue(root, key); node != nil {
		node.Kind = yaml.ScalarNode
		node.Tag = "!!bool"
		node.Value = mapBool(value, "true", "false")
		node.Content = nil
		return
	}
	root.Content = append(root.Content, scalarNode(key), boolNode(value))
}

func readBoolScalar(root *yaml.Node, key string) bool {
	node := findMapValue(root, key)
	if node == nil || node.Kind != yaml.ScalarNode {
		return false
	}
	return strings.EqualFold(node.Value, "true")
}

func targetNode(t TargetConfig) *yaml.Node {
	fields := []struct {
		key   string
		value string
	}{
		{"name", t.Name},
		{"protocol", t.Protocol},
		{"host", t.Host},
		{"user", t.User},
		{"password", t.Password},
		{"key_path", t.KeyPath},
		{"key_passphrase", t.KeyPassphrase},
		{"prompt", t.Prompt},
		{"auth_prompt_regex", t.AuthPromptRegex},
		{"device_type", t.DeviceType},
		{"enable_password", t.EnablePassword},
		{"ftp_tls_mode", t.FTPTLSMode},
		{"ftp_tls_server_name", t.FTPTLSServerName},
		{"ftp_tls_ca_file", t.FTPTLSCAFile},
		{"ftp_data_mode", t.FTPDataMode},
		{"ftp_active_address", t.FTPActiveAddress},
	}
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, f := range fields {
		if f.value != "" || f.key == "password" || f.key == "key_path" {
			node.Content = append(node.Content, scalarNode(f.key), scalarNode(f.value))
		}
	}
	if t.FTPTLSInsecureSkipVerify {
		node.Content = append(node.Content, scalarNode("ftp_tls_insecure_skip_verify"), boolNode(true))
	}
	if len(t.Tags) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, tag := range t.Tags {
			seq.Content = append(seq.Content, scalarNode(tag))
		}
		node.Content = append(node.Content, scalarNode("tags"), seq)
	}
	return node
}

func upsertTarget(root *yaml.Node, t TargetConfig) bool {
	target := targetNode(t)
	seq := ensureSeq(root, "targets")
	out := make([]*yaml.Node, 0, len(seq.Content))
	replaced := false
	for _, item := range seq.Content {
		if readScalar(item, "name") == t.Name || sameManagedEndpoint(readScalar(item, "protocol"), readScalar(item, "host"), t.Protocol, t.Host) {
			if !replaced {
				out = append(out, target)
				replaced = true
			}
			// Drop later legacy/duplicate matches while preserving unrelated
			// targets, including the same host on a different explicit port.
			continue
		}
		out = append(out, item)
	}
	if replaced {
		seq.Content = out
		return true
	}
	seq.Content = append(out, target)
	return false
}

func normalizeManagedEndpoint(protocol, host, rawPort string) (string, error) {
	host = strings.TrimSpace(host)
	rawPort = strings.TrimSpace(rawPort)
	if host == "" || rawPort == "" {
		return host, nil
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("port 必须是 1-65535 的整数，收到 %q", rawPort)
	}
	if existingHost, existingPort, err := net.SplitHostPort(host); err == nil {
		if existingPort != rawPort {
			return "", fmt.Errorf("host 已包含端口 %s，但 port=%s；请只保留一个一致端口", existingPort, rawPort)
		}
		return net.JoinHostPort(strings.Trim(existingHost, "[]"), rawPort), nil
	}
	trimmedHost := strings.Trim(host, "[]")
	if strings.Contains(trimmedHost, ":") {
		return net.JoinHostPort(trimmedHost, rawPort), nil
	}
	return net.JoinHostPort(trimmedHost, rawPort), nil
}

func sameManagedEndpoint(protocolA, hostA, protocolB, hostB string) bool {
	if !strings.EqualFold(strings.TrimSpace(protocolA), strings.TrimSpace(protocolB)) {
		return false
	}
	hostA = strings.TrimSpace(hostA)
	hostB = strings.TrimSpace(hostB)
	if hostA == hostB {
		return true
	}
	baseA, portA := splitManagedEndpoint(hostA)
	baseB, portB := splitManagedEndpoint(hostB)
	// A legacy host without an explicit port is the same logical target when
	// the corrected call adds its port. Two explicit different ports remain
	// distinct and can coexist intentionally.
	return strings.EqualFold(baseA, baseB) && (portA == "" || portB == "" || portA == portB)
}

func splitManagedEndpoint(endpoint string) (string, string) {
	if host, port, err := net.SplitHostPort(endpoint); err == nil {
		return strings.Trim(host, "[]"), port
	}
	if strings.Count(endpoint, ":") == 1 {
		parts := strings.SplitN(endpoint, ":", 2)
		if _, err := strconv.Atoi(parts[1]); err == nil {
			return parts[0], parts[1]
		}
	}
	return strings.Trim(endpoint, "[]"), ""
}

func configTargetFromArgs(name, protocol, host string, args map[string]string) TargetConfig {
	return TargetConfig{
		Name:                     name,
		Protocol:                 protocol,
		Host:                     host,
		User:                     valueOr(firstConfigArg(args, "user", "ssh_user", "username"), defaultUser(protocol)),
		Password:                 firstConfigArg(args, "password", "ssh_password", "pass"),
		KeyPath:                  firstConfigArg(args, "key_path", "ssh_key_path", "key"),
		KeyPassphrase:            firstConfigArg(args, "key_passphrase", "ssh_key_passphrase", "passphrase"),
		Prompt:                   firstConfigArg(args, "prompt"),
		AuthPromptRegex:          firstConfigArg(args, "auth_prompt_regex", "telnet_auth_prompt_regex"),
		DeviceType:               valueOr(firstConfigArg(args, "device_type", "ssh_device_type", "telnet_device_type"), "auto"),
		EnablePassword:           firstConfigArg(args, "enable_password", "ssh_enable_password", "telnet_enable_password"),
		FTPTLSMode:               firstConfigArg(args, "ftp_tls_mode", "tls_mode"),
		FTPTLSServerName:         firstConfigArg(args, "ftp_tls_server_name", "tls_server_name"),
		FTPTLSCAFile:             firstConfigArg(args, "ftp_tls_ca_file", "tls_ca_file"),
		FTPTLSInsecureSkipVerify: parseBoolArg(firstConfigArg(args, "ftp_tls_insecure_skip_verify", "tls_insecure_skip_verify")),
		FTPDataMode:              firstConfigArg(args, "ftp_data_mode", "data_mode"),
		FTPActiveAddress:         firstConfigArg(args, "ftp_active_address", "active_address"),
		Tags:                     splitTags(firstConfigArg(args, "tags", "tag")),
	}
}

func currentSingleTarget(root *yaml.Node) (TargetConfig, bool) {
	protocol := strings.ToLower(readScalar(root, "target_protocol"))
	switch protocol {
	case "", "ssh":
		host := readScalar(root, "ssh_host")
		if host == "" {
			return TargetConfig{}, false
		}
		return TargetConfig{
			Name:           defaultTargetName("ssh", host),
			Protocol:       "ssh",
			Host:           host,
			User:           valueOr(readScalar(root, "ssh_user"), "root"),
			Password:       readScalar(root, "ssh_password"),
			KeyPath:        readScalar(root, "ssh_key_path"),
			KeyPassphrase:  readScalar(root, "ssh_key_passphrase"),
			Prompt:         readScalar(root, "ssh_prompt"),
			DeviceType:     valueOr(readScalar(root, "ssh_device_type"), "auto"),
			EnablePassword: readScalar(root, "ssh_enable_password"),
		}, true
	case "telnet":
		host := readScalar(root, "telnet_host")
		if host == "" {
			return TargetConfig{}, false
		}
		return TargetConfig{
			Name:            defaultTargetName("telnet", host),
			Protocol:        "telnet",
			Host:            host,
			User:            valueOr(readScalar(root, "telnet_user"), "root"),
			Password:        readScalar(root, "telnet_password"),
			Prompt:          readScalar(root, "telnet_prompt"),
			AuthPromptRegex: readScalar(root, "telnet_auth_prompt_regex"),
			DeviceType:      valueOr(readScalar(root, "telnet_device_type"), "auto"),
			EnablePassword:  readScalar(root, "telnet_enable_password"),
		}, true
	case "ftp":
		host := readScalar(root, "ftp_host")
		if host == "" {
			return TargetConfig{}, false
		}
		return TargetConfig{
			Name:                     defaultTargetName("ftp", host),
			Protocol:                 "ftp",
			Host:                     host,
			User:                     valueOr(readScalar(root, "ftp_user"), "anonymous"),
			Password:                 readScalar(root, "ftp_password"),
			FTPTLSMode:               readScalar(root, "ftp_tls_mode"),
			FTPTLSServerName:         readScalar(root, "ftp_tls_server_name"),
			FTPTLSCAFile:             readScalar(root, "ftp_tls_ca_file"),
			FTPTLSInsecureSkipVerify: parseBoolArg(readScalar(root, "ftp_tls_insecure_skip_verify")),
			FTPDataMode:              readScalar(root, "ftp_data_mode"),
			FTPActiveAddress:         readScalar(root, "ftp_active_address"),
		}, true
	default:
		return TargetConfig{}, false
	}
}

func clearSingleTargetScalars(root *yaml.Node) {
	for _, key := range []string{
		"ssh_host", "ssh_user", "ssh_password", "ssh_key_path", "ssh_key_passphrase", "ssh_device_type", "ssh_prompt", "ssh_enable_password",
		"telnet_host", "telnet_user", "telnet_password", "telnet_prompt", "telnet_auth_prompt_regex", "telnet_device_type", "telnet_enable_password",
		"ftp_host", "ftp_user", "ftp_password", "ftp_tls_mode", "ftp_tls_server_name", "ftp_tls_ca_file", "ftp_data_mode", "ftp_active_address",
	} {
		setScalar(root, key, "")
	}
	setBoolScalar(root, "ftp_tls_insecure_skip_verify", false)
}

func readTargets(root *yaml.Node) []TargetConfig {
	seq := findMapValue(root, "targets")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	targets := make([]TargetConfig, 0, len(seq.Content))
	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		targets = append(targets, TargetConfig{
			Name:                     readScalar(item, "name"),
			Protocol:                 readScalar(item, "protocol"),
			Host:                     readScalar(item, "host"),
			User:                     readScalar(item, "user"),
			Password:                 readScalar(item, "password"),
			KeyPath:                  readScalar(item, "key_path"),
			KeyPassphrase:            readScalar(item, "key_passphrase"),
			Prompt:                   readScalar(item, "prompt"),
			AuthPromptRegex:          readScalar(item, "auth_prompt_regex"),
			DeviceType:               readScalar(item, "device_type"),
			EnablePassword:           readScalar(item, "enable_password"),
			FTPTLSMode:               readScalar(item, "ftp_tls_mode"),
			FTPTLSServerName:         readScalar(item, "ftp_tls_server_name"),
			FTPTLSCAFile:             readScalar(item, "ftp_tls_ca_file"),
			FTPTLSInsecureSkipVerify: parseBoolArg(readScalar(item, "ftp_tls_insecure_skip_verify")),
			FTPDataMode:              readScalar(item, "ftp_data_mode"),
			FTPActiveAddress:         readScalar(item, "ftp_active_address"),
			Tags:                     readScalarSeq(item, "tags"),
		})
	}
	return targets
}

func parseBoolArg(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parseOptionalIntArg(args map[string]string, key string, min, max int) (int, error) {
	raw := strings.TrimSpace(args[key])
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("%s 必须是 %d~%d 的整数", key, min, max)
	}
	return value, nil
}

func splitTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ' ' || r == '\n' || r == '\t'
	})
	var tags []string
	seen := map[string]bool{}
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

func splitCSVArgs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			args = append(args, strings.TrimSpace(part))
		}
	}
	return args
}

func parseEnvArg(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	env := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) == 2 && strings.TrimSpace(pair[0]) != "" {
			env[strings.TrimSpace(pair[0])] = pair[1]
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func allowedScalarConfigKey(key string) bool {
	switch key {
	case "provider", "api_protocol", "api_url", "model_name", "api_key", "temperature",
		"model_profile", "model_parameter_b", "context_window_tokens", "context_utilization",
		"reserved_output_tokens", "native_tool_limit", "agent_runtime", "terminal_theme",
		"llm_timeout_sec", "llm_retries", "ssh_command_timeout_sec", "ssh_max_output_bytes",
		"max_steps", "subagent_max_steps", "target_protocol", "ssh_host", "ssh_user",
		"ssh_password", "ssh_key_path", "ssh_key_passphrase", "ssh_host_key_policy", "ssh_known_hosts_path", "ssh_legacy_compat", "ssh_connect_timeout_sec", "ssh_device_type", "ssh_prompt", "ssh_enable_password", "telnet_host", "telnet_user", "telnet_password",
		"telnet_prompt", "telnet_auth_prompt_regex", "telnet_device_type", "telnet_enable_password", "ftp_host", "ftp_user", "ftp_password", "ftp_tls_mode", "ftp_tls_server_name", "ftp_tls_ca_file", "ftp_tls_insecure_skip_verify", "ftp_data_mode", "ftp_active_address", "ftp_connect_timeout_sec", "ftp_command_timeout_sec", "ftp_transfer_timeout_sec", "use_native_tools",
		"controller_proxy", "browser_binary", "browser_timeout_sec", "browser_artifact_dir",
		"archive_max_entries", "archive_max_file_bytes", "archive_max_total_bytes",
		"scheduler_enabled", "scheduler_store", "scheduler_interval_sec", "scheduler_timezone",
		"dingtalk_webhook", "dingtalk_secret", "feishu_webhook", "feishu_secret",
		"email_gateway_url", "email_gateway_token", "email_gateway_header", "email_to", "email_from",
		"benchmark_base_url", "benchmark_token":
		return true
	default:
		return false
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func removeString(values []string, needle string) []string {
	out := values[:0]
	for _, value := range values {
		if value != needle {
			out = append(out, value)
		}
	}
	return out
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultUser(protocol string) string {
	switch strings.ToLower(protocol) {
	case "ftp":
		return "anonymous"
	default:
		return "root"
	}
}

func defaultTargetName(protocol, host string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	name := strings.Trim(re.ReplaceAllString(host, "-"), "-")
	if name == "" {
		name = "target"
	}
	return protocol + "-" + name
}

func mapBool(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func redactConfigValue(key, value string) string {
	lower := strings.ToLower(key)
	if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "api_key") {
		if value == "" {
			return ""
		}
		return "***"
	}
	return value
}

func redactSensitiveText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if redacted := redactConfigValue(key, strings.TrimSpace(parts[1])); redacted == "***" {
			lines[i] = parts[0] + ": ***"
		}
	}
	return strings.Join(lines, "\n")
}
