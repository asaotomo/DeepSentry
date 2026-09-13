package tools

import (
	"strings"
	"testing"
)

func TestGetUnknownTool(t *testing.T) {
	_, _, err := Run("nonexistent", nil, false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFormatCatalog(t *testing.T) {
	prompt := FormatCatalogPrompt()
	if !strings.Contains(prompt, "tool_catalog") || !strings.Contains(prompt, "按需发现") {
		t.Fatal("catalog prompt should expose discovery entry")
	}
	if strings.Contains(prompt, "host(必填), count") {
		t.Fatal("catalog prompt should not dump full tool args every turn")
	}
}

func TestRegistryCount(t *testing.T) {
	ConfigureEnabled(nil, nil)
	const releaseToolCount = 73
	if got := CountEnabled(); got != releaseToolCount {
		t.Fatalf("enabled tool count = %d, want release count %d; update the release docs and this gate together", got, releaseToolCount)
	}
}

func TestFormatCatalogDetail(t *testing.T) {
	detail := FormatCatalogDetail("网络连通", "ping")
	if !strings.Contains(detail, "ping") || !strings.Contains(detail, "host (string, 必填)") {
		t.Fatal("catalog detail should include matching tool args")
	}
	db := FormatCatalogDetail("数据库探测", "redis")
	if !strings.Contains(db, "redis_probe") {
		t.Fatal("catalog detail should include redis_probe")
	}
	web := FormatCatalogDetail("Web探测", "snapshot")
	if !strings.Contains(web, "web_snapshot") {
		t.Fatal("catalog detail should include web_snapshot")
	}
	headless := FormatCatalogDetail("Web探测", "headless")
	if !strings.Contains(headless, "headless_browser") {
		t.Fatal("catalog detail should include headless_browser")
	}
	browser := FormatCatalogDetail("浏览器控制", "")
	if !strings.Contains(browser, "browser_browse") || !strings.Contains(browser, "browser_interact") {
		t.Fatal("catalog detail should include persistent browser control tools")
	}
	pcap := FormatCatalogDetail("抓包分析", "pcap")
	if !strings.Contains(pcap, "pcap_analyze") || !strings.Contains(pcap, "gopacket") {
		t.Fatal("catalog detail should include pcap_analyze")
	}
	script := FormatCatalogDetail("脚本执行", "script")
	if !strings.Contains(script, "script_run") || !strings.Contains(script, "高风险") && !strings.Contains(script, "🔴") {
		t.Fatal("catalog detail should include high-risk script_run")
	}
	transfer := FormatCatalogDetail("文件传输", "archive")
	if !strings.Contains(transfer, "archive_pack") || !strings.Contains(transfer, "archive_extract") {
		t.Fatal("catalog detail should include archive tools")
	}
	proxy := FormatCatalogDetail("代理转发", "")
	if !strings.Contains(proxy, "tcp_forward") || !strings.Contains(proxy, "socks5_proxy") {
		t.Fatal("catalog detail should include proxy forwarding tools")
	}
	fleet := FormatCatalogDetail("批量运维", "fleet")
	if !strings.Contains(fleet, "fleet_exec") || !strings.Contains(fleet, "fleet_inventory") {
		t.Fatal("catalog detail should include fleet tools")
	}
	competition := FormatCatalogDetail("比赛辅助", "")
	if !strings.Contains(competition, "flag_scan") || !strings.Contains(competition, "awd_service_check") {
		t.Fatal("catalog detail should include competition tools")
	}
	doc := FormatCatalogDetail("文档解析", "")
	if !strings.Contains(doc, "document_parse") || !strings.Contains(doc, "PDF") {
		t.Fatal("catalog detail should include document_parse")
	}
	configSearch := FormatCatalogDetail("配置管理", "add ssh target config_manage")
	if !strings.Contains(configSearch, "config_manage") || !strings.Contains(configSearch, "port (integer") {
		t.Fatalf("multi-keyword search should find config_manage with structured help:\n%s", configSearch)
	}
}

func TestFormatCatalogDetailUsesChineseSemanticAliases(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"识别伪装二进制", []string{"file_ident", "file_hash"}},
		{"提取网页表单与脚本", []string{"web_snapshot"}},
		{"跟进分页安全公告", []string{"browser_browse"}},
		{"检查 AWD 服务可用性", []string{"awd_service_check"}},
		{"核查异常自启动", []string{"service_unit_audit"}},
	}
	for _, test := range tests {
		detail := FormatCatalogDetail("all", test.query)
		for _, name := range test.want {
			if !strings.Contains(detail, "**"+name+"**") {
				t.Errorf("query %q catalog did not retrieve %s:\n%s", test.query, name, detail)
			}
		}
	}
	awd := FormatCatalogDetail("all", "检查 AWD 服务可用性")
	awdIndex := strings.Index(awd, "**awd_service_check**")
	configIndex := strings.Index(awd, "**config_manage**")
	if awdIndex < 0 || (configIndex >= 0 && awdIndex > configIndex) {
		t.Fatalf("semantic catalog did not rank AWD tool first:\n%s", awd)
	}
	if !strings.Contains(awd, "不得再次调用 tool_catalog") {
		t.Fatalf("semantic catalog missing one-shot discovery guidance:\n%s", awd)
	}
}

func TestToolContractsValidateCanonicalCalls(t *testing.T) {
	valid := map[string]string{
		"action":   "add_target",
		"protocol": "ssh",
		"host":     "10.0.0.8",
		"port":     "2222",
		"user":     "root",
		"password": "secret",
	}
	if err := ValidateCall("config_manage", valid); err != nil {
		t.Fatalf("canonical config call rejected: %v", err)
	}
	invalid := make(map[string]string, len(valid)+1)
	for k, v := range valid {
		invalid[k] = v
	}
	invalid["force"] = "true"
	if err := ValidateCall("config_manage", invalid); err == nil || !strings.Contains(err.Error(), "未知参数: force") || !strings.Contains(err.Error(), "示例") {
		t.Fatalf("unknown parameter should fail with executable help: %v", err)
	}
	invalidAction := map[string]string{"action": "list_targets", "force": "true"}
	if err := ValidateCall("config_manage", invalidAction); err == nil || !strings.Contains(err.Error(), "fleet_inventory") {
		t.Fatalf("hallucinated action should point to fleet_inventory: %v", err)
	}
}

func TestValidateCallDropsShadowActionKeys(t *testing.T) {
	args := map[string]string{
		"action":  "auto",
		"source":  "/tmp/test02.zip",
		"extract": "true",
		"crc32":   "true",
	}
	if err := ValidateCall("zip_password_recover", args); err != nil {
		t.Fatalf("shadow crc32 key should be ignored: %v", err)
	}
	if args["crc32"] != "" || args["action"] != "auto" {
		t.Fatalf("shadow key not dropped: %#v", args)
	}
	crcOnly := map[string]string{"source": "/tmp/test03.zip", "crc32": "true"}
	if err := ValidateCall("zip_password_recover", crcOnly); err != nil {
		t.Fatalf("crc32 shadow should become action: %v", err)
	}
	if crcOnly["action"] != "crc32" || crcOnly["crc32"] != "" {
		t.Fatalf("crc32 shadow was not promoted: %#v", crcOnly)
	}
}

func TestWorkflowContractsRejectIncompleteOrWrongTypedCalls(t *testing.T) {
	tests := []struct {
		name string
		args map[string]string
		want string
	}{
		{"file_download", map[string]string{"remote_path": "/var/log/auth.log"}, "local_path"},
		{"archive_extract", map[string]string{"source": "/tmp/a.zip"}, "dest"},
		{"script_run", map[string]string{"language": "python"}, "content | path"},
		{"fleet_file", map[string]string{"action": "download", "remote_path": "/tmp/a"}, "local_path"},
		{"tcp_forward", map[string]string{"action": "start", "listen_port": "abc", "target_host": "127.0.0.1", "target_port": "80"}, "必须是整数"},
		{"schedule_task", map[string]string{"action": "remove"}, "id"},
		{"tsecbench", map[string]string{"action": "submit", "unique_code": "demo"}, "flag"},
		{"browser_browse", map[string]string{"action": "open"}, "url"},
		{"browser_browse", map[string]string{"action": "follow"}, "selector"},
		{"browser_interact", map[string]string{"action": "click"}, "selector"},
		{"browser_interact", map[string]string{"action": "type", "selector": "@e1"}, "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCall(tt.name, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}

	if err := ValidateCall("script_run", map[string]string{"language": "shell", "content": "echo ok", "timeout": "30"}); err != nil {
		t.Fatalf("valid script_run rejected: %v", err)
	}
	schema := JSONSchema("script_run")
	if _, ok := schema["anyOf"]; !ok {
		t.Fatalf("script_run native schema must express content/path requirement: %#v", schema)
	}
}

func TestBrowserToolsSplitReadAndMutationRisk(t *testing.T) {
	browse, ok := Get("browser_browse")
	if !ok || browse.RiskLevel != RiskLow {
		t.Fatalf("browser_browse should be auto-runnable read-only browsing: %#v", browse)
	}
	interact, ok := Get("browser_interact")
	if !ok || interact.RiskLevel != RiskHigh {
		t.Fatalf("browser_interact should require high-risk confirmation: %#v", interact)
	}
}

func TestBrowserBrowseDispatchStatus(t *testing.T) {
	out, risk, err := Run("browser_browse", map[string]string{"action": "status"}, false)
	if err != nil {
		t.Fatalf("browser_browse status dispatch failed: %v", err)
	}
	if risk != RiskLow || !strings.Contains(out, "Browser control status") {
		t.Fatalf("unexpected browser status result risk=%q out=%q", risk, out)
	}
}

func TestEveryEnabledToolHasNativeJSONSchema(t *testing.T) {
	for _, name := range ListNames() {
		schema := JSONSchema(name)
		if schema["type"] != "object" {
			t.Fatalf("tool %s missing object schema: %#v", name, schema)
		}
		if _, ok := schema["additionalProperties"]; !ok {
			t.Fatalf("tool %s schema does not declare unknown-arg behavior", name)
		}
		tool, _ := Get(name)
		props, _ := schema["properties"].(map[string]interface{})
		if tool.ArgsHint != "" && tool.ArgsHint != "无参数" && len(props) == 0 {
			t.Fatalf("tool %s has documented args but empty native schema: %q", name, tool.ArgsHint)
		}
		help := FormatToolHelp(name)
		if !strings.Contains(help, "示例:") {
			t.Fatalf("tool %s should have an executable catalog example: %s", name, help)
		}
	}
}

func TestConfigureEnabled(t *testing.T) {
	ConfigureEnabled(nil, nil)
	defer ConfigureEnabled(nil, nil)

	ConfigureEnabled([]string{"ping"}, nil)
	if _, ok := Get("ping"); !ok {
		t.Fatal("ping should be enabled")
	}
	if _, ok := Get("nmap_scan"); ok {
		t.Fatal("nmap_scan should be disabled by whitelist")
	}

	ConfigureEnabled(nil, []string{"ping"})
	if _, ok := Get("ping"); ok {
		t.Fatal("ping should be disabled by blacklist")
	}
}
