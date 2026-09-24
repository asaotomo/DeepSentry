package builtin

import (
	"ai-edr/internal/config"
	"ai-edr/internal/mcp"
	"ai-edr/internal/skills"
	"fmt"
	"strconv"
	"strings"
)

func arg(args map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(args[k]); v != "" {
			return v
		}
	}
	return ""
}

func argInt(args map[string]string, key string, def, max int) int {
	s := arg(args, key)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func argBool(args map[string]string, key string) bool {
	v := strings.ToLower(strings.TrimSpace(args[key]))
	return v == "1" || v == "true" || v == "yes" || v == "y" || v == "on"
}

func argBoolDefault(args map[string]string, key string, def bool) bool {
	if _, ok := args[key]; !ok {
		return def
	}
	return argBool(args, key)
}

// Run 统一调度 Go 原生内置工具（BusyBox 模式，不依赖目标系统 CLI）
func Run(name string, args map[string]string, rt Runtime) (string, error) {
	if args == nil {
		args = map[string]string{}
	}
	unlock := lockRuntimeExecutor(rt.Exec)
	defer unlock()

	switch name {
	case "computer_use":
		return "", fmt.Errorf("computer_use 必须在带会话 ID 和中止信号的本地 Agent 中调用；请使用 Agent 工具入口")
	case "ping":
		return Ping(rt, arg(args, "host", "target", "ip"), argInt(args, "count", 4, 10))
	case "dns_lookup":
		return DNSLookup(rt, arg(args, "host", "domain"), arg(args, "type", "record"))
	case "http_probe":
		return HTTPProbe(rt, arg(args, "url"), arg(args, "method"))
	case "netcat_probe":
		return TCPProbe(rt, arg(args, "host", "target"), arg(args, "port"), argInt(args, "timeout", 3, 10))
	case "nmap_scan":
		return PortScan(rt, arg(args, "host", "target"), arg(args, "ports", "port"), strings.ToLower(arg(args, "mode")))
	case "cidr_scan":
		return CIDRScan(rt, arg(args, "cidr", "range"), arg(args, "ports", "port"), argInt(args, "timeout", 1, 5), argInt(args, "limit", 256, 1024))
	case "net_connections":
		return NetConnections(rt, arg(args, "filter"))
	case "port_listen":
		return PortListen(rt)
	case "route_table":
		return RouteTable(rt)
	case "arp_table":
		return ARPTable(rt)
	case "mem_info":
		return MemInfo(rt)
	case "process_list":
		return ProcessList(rt, argInt(args, "limit", 50, 200))
	case "target_health_summary":
		return TargetHealthSummary(rt)
	case "network_device_baseline":
		return NetworkDeviceBaseline(rt, arg(args, "profile", "device_type", "vendor"))
	case "network_device_diagnose":
		return NetworkDeviceDiagnose(rt, arg(args, "profile", "device_type", "vendor"), arg(args, "focus"))
	case "competition_answer_check":
		return CompetitionAnswerCheck(arg(args, "task", "question", "requirement"), arg(args, "answer", "draft", "report"))
	case "host_incident_baseline":
		return HostIncidentBaseline(rt, args)
	case "webshell_hunt":
		return WebShellHunt(rt, args)
	case "disk_usage":
		return DiskUsage(rt, arg(args, "path", "root"))
	case "file_tail":
		return FileTail(rt, arg(args, "path", "file"), argInt(args, "lines", 100, 1000))
	case "login_audit":
		return LoginAudit(rt, argInt(args, "lines", 200, 2000))
	case "service_units":
		return ServiceUnits(rt, arg(args, "query", "pattern"), argInt(args, "limit", 80, 300))
	case "file_hash":
		return FileHash(rt, arg(args, "path", "file"))
	case "flow_snapshot":
		return FlowSnapshot(rt, argInt(args, "interval", 2, 10))
	case "pcap_analyze":
		return PcapAnalyze(rt, arg(args, "path", "file", "pcap"), arg(args, "mode"), argInt(args, "limit", 5000, 50000))
	case "packet_capture":
		out, err := FlowSnapshot(rt, argInt(args, "interval", 3, 10))
		if err != nil {
			return "", err
		}
		return "⚠️ 目标系统无 tcpdump，已自动降级为 Go 原生 flow_snapshot:\n\n" + out, nil
	case "bandwidth_test":
		host := arg(args, "host")
		if host == "" {
			host = "8.8.8.8"
		}
		return Ping(rt, host, 5)
	case "http_fetch":
		return HTTPFetch(rt, arg(args, "url", "target"), arg(args, "method"), argInt(args, "max_bytes", 65536, 524288))
	case "web_snapshot":
		return WebSnapshot(rt, arg(args, "url", "target"), argInt(args, "max_bytes", 131072, 524288))
	case "headless_browser":
		return HeadlessBrowser(rt, arg(args, "url", "target"), arg(args, "mode"), argInt(args, "wait_ms", 1500, 10000), argInt(args, "max_text", 20000, 100000), arg(args, "selector"), argBool(args, "screenshot"))
	case "browser_browse":
		return BrowserBrowse(arg(args, "action"), arg(args, "session_id"), arg(args, "url", "target"), arg(args, "mode"), arg(args, "selector", "ref"), argInt(args, "wait_ms", 700, 10000), argInt(args, "max_text", 20000, 100000), argInt(args, "text_offset", 0, 10000000), argInt(args, "element_offset", 0, 100000), argInt(args, "element_limit", 120, 300))
	case "browser_interact":
		return BrowserInteract(arg(args, "action"), arg(args, "session_id"), arg(args, "selector", "ref"), args["text"], args["value"], arg(args, "key"), argBoolDefault(args, "clear", true), argBool(args, "submit"), argInt(args, "wait_ms", 700, 10000), argInt(args, "max_text", 20000, 100000), argInt(args, "text_offset", 0, 10000000), argInt(args, "element_offset", 0, 100000), argInt(args, "element_limit", 120, 300))
	case "traceroute":
		return tracerouteNative(rt, arg(args, "host", "target"))
	case "firewall_status":
		return firewallNative(rt)
	case "file_ident":
		return FileIdentify(rt, arg(args, "path", "file"))
	case "file_strings":
		return FileStrings(rt, arg(args, "path", "file"), argInt(args, "min_len", 4, 32), argInt(args, "limit", 500, 2000), arg(args, "pattern", "grep"))
	case "document_parse":
		return DocumentParse(rt, arg(args, "path", "file"), arg(args, "mode"), argInt(args, "max_text", 30000, 100000), argInt(args, "max_rows", 120, 1000), argInt(args, "max_sheets", 8, 50))
	case "chat_send_file":
		return ChatSendFile(arg(args, "path", "file", "local_path"), arg(args, "name", "filename"))
	case "read_gzip":
		return ReadGzip(rt, arg(args, "path", "file"), argInt(args, "lines", 200, 2000), arg(args, "pattern", "grep"))
	case "read_log":
		return ReadLog(rt, arg(args, "path", "file"), argInt(args, "lines", 200, 2000), arg(args, "pattern", "grep"))
	case "proc_socket_map":
		return ProcSocketMap(rt, arg(args, "filter"), argInt(args, "limit", 80, 300))
	case "service_fingerprint":
		return ServiceFingerprint(rt, arg(args, "host", "target", "ip"), arg(args, "port"), argInt(args, "timeout", 3, 10))
	case "redis_probe":
		return RedisProbe(rt, arg(args, "host", "target", "ip"), arg(args, "port"), arg(args, "password", "pass"), argInt(args, "timeout", 3, 10))
	case "mysql_probe":
		return MySQLProbe(rt, arg(args, "host", "target", "ip"), arg(args, "port"), argInt(args, "timeout", 3, 10))
	case "postgres_probe":
		return PostgresProbe(rt, arg(args, "host", "target", "ip"), arg(args, "port"), argInt(args, "timeout", 3, 10))
	case "oracle_probe":
		return OracleProbe(rt, arg(args, "host", "target", "ip"), arg(args, "port"), argInt(args, "timeout", 3, 10))
	case "sqlite_inspect":
		return SQLiteInspect(rt, arg(args, "path", "file"))
	case "app_config_discover":
		return AppConfigDiscover(rt, arg(args, "roots", "root", "path"), arg(args, "query", "pattern"), argInt(args, "limit", 80, 300))
	case "db_config_audit":
		return DBConfigAudit(rt, arg(args, "type", "db_type"), arg(args, "paths", "path"))
	case "db_log_read":
		return DBLogRead(rt, arg(args, "type", "db_type"), arg(args, "path", "file"), arg(args, "pattern", "grep"), argInt(args, "lines", 200, 2000))
	case "secret_scan":
		return SecretScan(rt, arg(args, "root", "path"), arg(args, "pattern", "grep"), argInt(args, "limit", 80, 300))
	case "service_unit_audit":
		return ServiceUnitAudit(rt, arg(args, "query", "pattern"), argInt(args, "limit", 80, 300))
	case "container_inventory":
		return ContainerInventory(rt)
	case "flag_scan":
		return FlagScan(rt, arg(args, "root", "path"), arg(args, "pattern", "grep"), argInt(args, "limit", 80, 500))
	case "awd_service_check":
		return AWDServiceCheckWithOptions(arg(args, "targets", "target", "urls"), argInt(args, "timeout", 3, 15), argInt(args, "concurrency", 10, 32), argInt(args, "expected_status", 0, 599), arg(args, "contains"))
	case "tsecbench":
		return TSecBench(rt, args)
	case "script_run":
		return ScriptRun(rt, arg(args, "language", "lang"), arg(args, "content", "script"), arg(args, "path", "file"), arg(args, "args"), argInt(args, "timeout", 30, 300))
	case "file_download":
		return FileDownload(rt, arg(args, "remote_path", "remote", "src", "source"), arg(args, "local_path", "local", "dst", "dest"), argInt(args, "chunk_size", 4<<20, 64<<20))
	case "file_upload":
		return FileUpload(rt, arg(args, "local_path", "local", "src", "source"), arg(args, "remote_path", "remote", "dst", "dest"), argInt(args, "chunk_size", 4<<20, 64<<20))
	case "archive_pack":
		return ArchivePack(rt, arg(args, "format", "type"), arg(args, "source", "src", "path"), arg(args, "dest", "dst", "output"))
	case "archive_extract":
		return ArchiveExtract(rt, arg(args, "format", "type"), arg(args, "source", "src", "path"), arg(args, "dest", "dst", "output"))
	case "zip_password_recover":
		return ZipPasswordRecover(rt, args)
	case "tcp_forward":
		return TCPForward(rt, arg(args, "action"), arg(args, "listen_host", "lhost"), arg(args, "listen_port", "lport"), arg(args, "target_host", "rhost", "host"), arg(args, "target_port", "rport", "port"))
	case "socks5_proxy":
		return Socks5Proxy(rt, arg(args, "action"), arg(args, "listen_host", "lhost"), arg(args, "listen_port", "lport"), arg(args, "username", "user"), arg(args, "password", "pass"), argBool(args, "allow_lan"))
	case "http_proxy":
		return HTTPProxy(rt, arg(args, "action"), arg(args, "listen_host", "lhost"), arg(args, "listen_port", "lport"), arg(args, "username", "user"), arg(args, "password", "pass"), argBool(args, "allow_lan"))
	case "inspection_run":
		return InspectionRun(args)
	case "task_wait", "task_context":
		return "", fmt.Errorf("此工具必须通过 Agent 会话调用")
	case "schedule_task":
		return ScheduleTask(rt, args)
	case "fleet_inventory":
		return FleetInventory(rt, arg(args, "selector", "target", "targets"))
	case "fleet_exec":
		return FleetExec(rt, arg(args, "selector", "target", "targets"), arg(args, "command", "cmd"), argInt(args, "concurrency", 5, 20))
	case "fleet_file":
		return FleetFile(rt, arg(args, "selector", "target", "targets"), arg(args, "action"), arg(args, "remote_path", "remote", "path"), arg(args, "local_path", "local"), argInt(args, "concurrency", 5, 20))
	case "config_manage":
		return config.ManageConfig(args)
	case "skill_market":
		return skills.ManageMarketplace(args)
	case "mcp_resource":
		return manageMCPResource(args)
	case "mcp_prompt":
		return manageMCPPrompt(args)
	default:
		return "", fmt.Errorf("未知内置工具: %s", name)
	}
}

func manageMCPResource(args map[string]string) (string, error) {
	action := strings.ToLower(arg(args, "action"))
	if action == "" {
		action = "list"
	}
	server := arg(args, "server")
	switch action {
	case "list":
		resources := mcp.ListResources(server)
		templates := mcp.ListResourceTemplates(server)
		if len(resources) == 0 && len(templates) == 0 {
			return "没有发现匹配的 MCP Resource。", nil
		}
		var b strings.Builder
		for _, resource := range resources {
			fmt.Fprintf(&b, "- %s · %s · %s", resource.Server, resource.URI, valueOrBuiltin(resource.Title, resource.Name))
			if resource.MIMEType != "" {
				fmt.Fprintf(&b, " · %s", resource.MIMEType)
			}
			if resource.Description != "" {
				fmt.Fprintf(&b, "\n  %s", resource.Description)
			}
			b.WriteByte('\n')
		}
		for _, template := range templates {
			fmt.Fprintf(&b, "- %s · template=%s · %s", template.Server, template.URITemplate, valueOrBuiltin(template.Title, template.Name))
			if template.MIMEType != "" {
				fmt.Fprintf(&b, " · %s", template.MIMEType)
			}
			b.WriteByte('\n')
		}
		return strings.TrimSpace(b.String()), nil
	case "read":
		if server == "" || arg(args, "uri") == "" {
			return "", fmt.Errorf("mcp_resource read 需要 server 和 uri")
		}
		return mcp.ReadResource(server, arg(args, "uri"))
	default:
		return "", fmt.Errorf("未知 mcp_resource action: %s", action)
	}
}

func manageMCPPrompt(args map[string]string) (string, error) {
	action := strings.ToLower(arg(args, "action"))
	if action == "" {
		action = "list"
	}
	server := arg(args, "server")
	switch action {
	case "list":
		prompts := mcp.ListPrompts(server)
		if len(prompts) == 0 {
			return "没有发现匹配的 MCP Prompt。", nil
		}
		var b strings.Builder
		for _, prompt := range prompts {
			fmt.Fprintf(&b, "- %s · %s · %s", prompt.Server, prompt.Name, valueOrBuiltin(prompt.Title, prompt.Name))
			if prompt.Description != "" {
				fmt.Fprintf(&b, "\n  %s", prompt.Description)
			}
			if len(prompt.Arguments) > 0 {
				var names []string
				for _, item := range prompt.Arguments {
					if item != nil {
						names = append(names, item.Name)
					}
				}
				if len(names) > 0 {
					fmt.Fprintf(&b, "\n  args: %s", strings.Join(names, ", "))
				}
			}
			b.WriteByte('\n')
		}
		return strings.TrimSpace(b.String()), nil
	case "get":
		name := arg(args, "name", "prompt")
		if server == "" || name == "" {
			return "", fmt.Errorf("mcp_prompt get 需要 server 和 name")
		}
		promptArgs := make(map[string]string)
		for key, value := range args {
			switch key {
			case "action", "server", "name", "prompt":
			default:
				promptArgs[key] = value
			}
		}
		return mcp.GetPrompt(server, name, promptArgs)
	default:
		return "", fmt.Errorf("未知 mcp_prompt action: %s", action)
	}
}

func valueOrBuiltin(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func tracerouteNative(rt Runtime, host string) (string, error) {
	if err := validateHost(host); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 路由追踪 (Go 简化版)\n", rt.tag()))
	b.WriteString("说明: 极简系统无 traceroute 命令，使用 TCP 探活 + DNS 辅助定位\n\n")

	if out, err := DNSLookup(rt, host, "A"); err == nil {
		b.WriteString(out + "\n\n")
	}
	if out, err := TCPProbe(rt, host, "443", 5); err == nil {
		b.WriteString(out + "\n")
	} else if out, err := TCPProbe(rt, host, "80", 5); err == nil {
		b.WriteString(out + "\n")
	}
	if out, err := Ping(rt, host, 3); err == nil {
		b.WriteString("\n" + out)
	}
	return b.String(), nil
}

func firewallNative(rt Runtime) (string, error) {
	if rt.IsWindows {
		return "", fmt.Errorf("当前 Windows 防火墙请使用 execute netsh advfirewall show allprofiles")
	}
	paths := []string{"/proc/net/ip_tables_names", "/proc/sys/net/ipv4/ip_forward"}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 防火墙/网络内核状态\n", rt.tag()))
	found := false
	for _, p := range paths {
		data, err := readTarget(p)
		if err == nil {
			found = true
			b.WriteString(fmt.Sprintf("--- %s ---\n%s\n", p, strings.TrimSpace(string(data))))
		}
	}
	if !found {
		b.WriteString("无法读取 /proc 防火墙信息。\n")
		b.WriteString("建议: 使用 port_listen + net_connections 审计网络暴露面\n")
	}
	return b.String(), nil
}

func netConnectionsWindows(rt Runtime, filter string) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	cmd := "netstat -ano"
	if strings.EqualFold(strings.TrimSpace(filter), "listen") {
		cmd = `netstat -ano | findstr LISTENING`
	} else if strings.EqualFold(strings.TrimSpace(filter), "established") {
		cmd = `netstat -ano | findstr ESTABLISHED`
	}
	out, err := rt.Exec.Run(cmd)
	return fmt.Sprintf("%s Windows 网络连接\n%s", rt.tag(), truncate(out, 12000)), err
}
