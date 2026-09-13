package tools

import "sync"

// Risk 工具风险等级
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// Perspective 工具数据来源视角
const (
	PerspectiveTarget     = "target"     // 目标机 /proc、SFTP 直读
	PerspectiveController = "controller" // 控制端发起探测
)

// Tool 内置场景工具元数据
type Tool struct {
	Name        string
	Category    string
	Description string
	RiskLevel   string
	Perspective string // target | controller
	ArgsHint    string
	Enabled     bool
}

// Registry 工具注册表（实现均在 internal/builtin Go 原生代码）
var Registry = buildRegistry()

var registryMu sync.RWMutex

func buildRegistry() map[string]*Tool {
	all := []*Tool{
		{Name: "ping", Category: "网络连通", Description: "TCP 探活 (Go 原生，无需 ping 命令)", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "host(必填), count(默认4)"},
		{Name: "traceroute", Category: "网络连通", Description: "路由追踪简化版 (DNS+TCP 探活)", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "host(必填)"},
		{Name: "dns_lookup", Category: "网络连通", Description: "DNS 解析 (Go net.Resolver)", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "host(必填), type(A/MX/NS/TXT)"},
		{Name: "bandwidth_test", Category: "网络连通", Description: "连通性延迟粗测", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "host(可选)"},
		{Name: "net_connections", Category: "连接审计", Description: "解析 /proc/net (无需 ss/netstat)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "filter(all|established|listen)"},
		{Name: "port_listen", Category: "连接审计", Description: "监听端口 (/proc/net/tcp LISTEN)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "无参数"},
		{Name: "route_table", Category: "连接审计", Description: "路由表 (/proc/net/route)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "无参数"},
		{Name: "arp_table", Category: "连接审计", Description: "ARP 缓存 (/proc/net/arp)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "无参数"},
		{Name: "firewall_status", Category: "连接审计", Description: "内核网络/防火墙状态 (/proc)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "无参数"},
		{Name: "mem_info", Category: "系统应急", Description: "内存信息 (/proc/meminfo，无需 free)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "无参数"},
		{Name: "process_list", Category: "系统应急", Description: "进程列表 (/proc/[pid]/comm，无需 ps)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "limit(默认50)"},
		{Name: "target_health_summary", Category: "系统应急", Description: "跨平台基础健康摘要：主机、系统、负载、磁盘、内存、Top 进程", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "无参数"},
		{Name: "network_device_baseline", Category: "网络设备", Description: "华为/H3C/锐捷/Cisco/ASA/Juniper/Fortinet/Palo Alto/山石/深信服/Check Point 只读基线：版本、板卡、接口、路由、日志", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "profile(auto|huawei|h3c|ruijie|cisco|asa|juniper|fortinet|paloalto|hillstone|sangfor|checkpoint)"},
		{Name: "network_device_diagnose", Category: "网络设备", Description: "10 分钟比赛/故障快诊：按 overview、interfaces、routing、l2、logs 只采集 2-3 组最相关证据", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "profile, focus(overview|interfaces|routing|l2|logs|full)"},
		{Name: "competition_answer_check", Category: "综合运维", Description: "AI智运比赛答案提交前自检：完成度、证据、复验、AI纠错和输出规范", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "task, answer"},
		{Name: "host_incident_baseline", Category: "系统应急", Description: "建立主机应急基线；确定性并行采集主机健康、进程、端口、连接、登录和服务证据，再交给模型研判", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "limit, lines, concurrency"},
		{Name: "webshell_hunt", Category: "系统应急", Description: "确定性并行扫描 WebShell 代码特征、进程、连接和启动项证据", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "root(默认/var/www), pattern(可选), limit, concurrency"},
		{Name: "disk_usage", Category: "系统应急", Description: "跨平台磁盘使用情况", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(可选)"},
		{Name: "file_tail", Category: "系统应急", Description: "读取文件末尾若干行，适合大日志快速查看", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填), lines(默认100)"},
		{Name: "login_audit", Category: "系统应急", Description: "跨平台登录审计摘要：Linux auth.log/secure/last，Windows Security 事件", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "lines(默认200)"},
		{Name: "service_units", Category: "系统应急", Description: "跨平台服务列表：systemd 或 Windows Service", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "query, limit"},
		{Name: "file_hash", Category: "系统应急", Description: "文件 SHA256 (SFTP/直读，无需 sha256sum)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填)"},
		{Name: "file_ident", Category: "取证分析", Description: "魔数/文件类型识别 (无需 file 命令)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填)"},
		{Name: "file_strings", Category: "取证分析", Description: "提取可打印字符串 (无需 strings 命令)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填), min_len(默认4), limit, pattern"},
		{Name: "document_parse", Category: "文档解析", Description: "原生解析 PDF/Word DOCX/Excel XLSX/XLS/CSV/RTF，提取文本、表格和元信息", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填), mode(auto|text|tables|metadata), max_text, max_rows, max_sheets"},
		{Name: "chat_send_file", Category: "聊天文件", Description: "把本机文件发到当前聊天（微信/QQ）。仅聊天机器人任务可用；path 用本机绝对路径，例如桌面上的文件", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "path(必填), name(可选)"},
		{Name: "read_gzip", Category: "取证分析", Description: "分析 gzip 压缩或轮转日志，无需 zcat/gunzip", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填), lines(默认200), pattern"},
		{Name: "read_log", Category: "取证分析", Description: "智能读日志 (自动识别 plain/gzip)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填), lines(默认200), pattern"},
		{Name: "nmap_scan", Category: "端口扫描", Description: "Go TCP 端口扫描 (无需 nmap)", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "host(必填), ports, mode(quick)"},
		{Name: "cidr_scan", Category: "内网发现", Description: "fscan-like 轻量 TCP 内网发现，限制主机数/并发，不做漏洞探测", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "cidr(必填), ports(可选), timeout, limit(默认256最大1024)"},
		{Name: "netcat_probe", Category: "协议探测", Description: "TCP 端口探活 (Go net.Dial)", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "host(必填), port(必填)"},
		{Name: "http_probe", Category: "协议探测", Description: "HTTP 探测 (Go net/http)", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "url(必填), method(HEAD|GET)"},
		{Name: "http_fetch", Category: "Web探测", Description: "Go 原生 curl/wget 替代，只支持 GET/HEAD，返回响应头和有限正文", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "url(必填), method(GET|HEAD), max_bytes"},
		{Name: "web_snapshot", Category: "Web探测", Description: "网页快照解析：title/meta/forms/scripts/links/headers", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "url(必填), max_bytes"},
		{Name: "headless_browser", Category: "Web探测", Description: "控制端无头 Chrome/Chromium 渲染解析，失败自动降级静态快照", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "url(必填), mode(snapshot|text|forms|links|screenshot), wait_ms, selector, max_text, screenshot"},
		{Name: "browser_browse", Category: "浏览器控制", Description: "启动隔离的可见 Chrome 或无头会话并持续浏览：分页快照、按 @ref 低风险跟进超链接、导航、截图、前进后退；缺少浏览器时返回安装引导", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "action(status|list|open|navigate|follow|snapshot|screenshot|back|forward|reload|wait|close), session_id, url, mode(auto|visible|headless), selector, wait_ms, max_text, text_offset, element_offset, element_limit"},
		{Name: "browser_interact", Category: "浏览器控制", Description: "在受控浏览器会话中点击按钮、输入、选择或按键；普通超链接优先用低风险 browser_browse follow；交互必须使用快照 @ref/CSS selector", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "action(click|type|select|key), session_id, selector, text, value, key, clear, submit, wait_ms, max_text, text_offset, element_offset, element_limit"},
		{Name: "flow_snapshot", Category: "抓包分析", Description: "连接流快照对比 (/proc，替代 tcpdump)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "interval秒(默认2)"},
		{Name: "pcap_analyze", Category: "抓包分析", Description: "Go gopacket 离线解析 pcap：协议统计、会话、DNS/HTTP/TLS SNI/SMB/NTLM 线索", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填), mode(summary|dns|http|tls|smb|flows|packets), limit"},
		{Name: "packet_capture", Category: "抓包分析", Description: "无 tcpdump 时自动降级 flow_snapshot", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "interval(默认3)"},
		{Name: "proc_socket_map", Category: "系统关联", Description: "定位异常外联进程；将 /proc/net socket inode 映射到 PID/进程/命令行，无需 lsof/ss -p", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "filter(all|listen|established|owned|端口/关键词), limit"},
		{Name: "service_fingerprint", Category: "协议指纹", Description: "TCP banner/轻量握手识别 Redis/MySQL/PostgreSQL/HTTP/SSH/Oracle 等", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "host(必填), port(必填), timeout"},
		{Name: "redis_probe", Category: "数据库探测", Description: "Redis RESP 只读探测 PING/INFO/CONFIG GET dir/dbfilename", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "host(必填), port(默认6379), password(可选), timeout"},
		{Name: "mysql_probe", Category: "数据库探测", Description: "MySQL/MariaDB handshake 解析版本、协议、capabilities", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "host(必填), port(默认3306), timeout"},
		{Name: "postgres_probe", Category: "数据库探测", Description: "PostgreSQL SSLRequest 识别 SSL 支持和协议响应", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "host(必填), port(默认5432), timeout"},
		{Name: "oracle_probe", Category: "数据库探测", Description: "Oracle TNS Listener 轻量连接探测", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "host(必填), port(默认1521), timeout"},
		{Name: "sqlite_inspect", Category: "数据库取证", Description: "读取 SQLite 文件头、页大小、schema 字符串 (无需 sqlite3)", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "path(必填)"},
		{Name: "app_config_discover", Category: "配置取证", Description: "扫描常见应用配置，提取数据库连接串/凭据线索并脱敏", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "roots(/etc,/opt...), query, limit"},
		{Name: "db_config_audit", Category: "配置取证", Description: "审计 Redis/MySQL/Postgres/Oracle 常见配置与危险项", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "type(redis|mysql|postgres|oracle|auto), paths(可选逗号分隔)"},
		{Name: "db_log_read", Category: "日志取证", Description: "读取数据库日志并抽取认证失败、错误、连接等线索", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "type, path(可选), pattern(可选), lines"},
		{Name: "secret_scan", Category: "配置取证", Description: "轻量扫描配置/源码中的 password/token/dsn/private key 并脱敏", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "root, pattern(可选), limit"},
		{Name: "service_unit_audit", Category: "系统关联", Description: "读取 systemd/init/cron 启动项，识别 ExecStart/Environment/User", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "query, limit"},
		{Name: "container_inventory", Category: "系统关联", Description: "无 docker 命令时通过 cgroup/mountinfo 标记识别容器环境", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "无参数"},
		{Name: "flag_scan", Category: "比赛辅助", Description: "CTF/AWD flag 与关键线索扫描，支持常见 flag 正则和自定义 pattern", RiskLevel: RiskLow, Perspective: PerspectiveTarget, ArgsHint: "root/path(默认.), pattern(可选), limit"},
		{Name: "awd_service_check", Category: "比赛辅助", Description: "AWD 服务可用性检查：控制端批量 HTTP/TCP 探活并汇总状态", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "targets(必填，url/host:port逗号分隔), timeout"},
		{Name: "tsecbench", Category: "比赛辅助", Description: "TSecBench 平台接口封装：读取题目、启动/关闭容器、提交 flag、按需探活；使用 config 中 benchmark_base_url/benchmark_token 或 BENCHMARK_*", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "action(list|status|check|start|hint|submit|close|probe), unique_code/code/challenge_id, flag, addr, probe(true), limit, raw"},
		{Name: "script_run", Category: "脚本执行", Description: "经用户确认后执行 AI 编写/用户提供的 Python 或 Shell 脚本，并记录执行日志", RiskLevel: RiskHigh, Perspective: PerspectiveTarget, ArgsHint: "language(python|shell), content 或 path, args, timeout"},
		{Name: "file_download", Category: "文件传输", Description: "下载目标服务器文件到控制端本地，复用 SFTP/本地通道并记录日志", RiskLevel: RiskMedium, Perspective: PerspectiveTarget, ArgsHint: "remote_path, local_path, chunk_size"},
		{Name: "file_upload", Category: "文件传输", Description: "上传控制端本地文件到目标服务器，复用 SFTP/本地通道并记录日志", RiskLevel: RiskHigh, Perspective: PerspectiveTarget, ArgsHint: "local_path, remote_path, chunk_size"},
		{Name: "archive_pack", Category: "文件传输", Description: "打包 zip/tar/tar.gz；远程 rar/7z 依赖目标系统 rar/7z", RiskLevel: RiskMedium, Perspective: PerspectiveTarget, ArgsHint: "format(zip|tar|tar.gz|rar|7z), source, dest"},
		{Name: "archive_extract", Category: "文件传输", Description: "控制端安全解压 zip/tar/tar.gz（防路径逃逸/链接/解压炸弹）；远程直接解压禁用", RiskLevel: RiskHigh, Perspective: PerspectiveTarget, ArgsHint: "format, source, dest"},
		{Name: "zip_password_recover", Category: "取证分析", Description: "ZipCracker 兼容 ZIP 恢复：auto 伪加密/CRC32/内置6000字典，或 inspect、repair、crc32、recover", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "action(auto|inspect|repair|crc32|recover), source, dictionary(builtin|6000.txt|路径), passwords, mask, workers, max_attempts, timeout_sec, extract, dest"},
		{Name: "tcp_forward", Category: "代理转发", Description: "授权 TCP 端口映射 (iox/lcx 风格) start/list/stop，短生命周期、无持久化、无反连控制面", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "action(start|list|stop), listen_host, listen_port, target_host, target_port"},
		{Name: "socks5_proxy", Category: "代理转发", Description: "授权 SOCKS5 代理 (nps/iox 风格) start/list/stop，仅 CONNECT，可选用户名密码，默认仅监听本机", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "action(start|list|stop), listen_host(默认127.0.0.1), listen_port, username, password, allow_lan"},
		{Name: "inspection_run", Category: "设备巡检", Description: "设备清单供 HawkEye 巡检；一键把当前/指定 Markdown 审计报告编成 Word；也支持固定检查采集。生成 Word 不必手写 JSON", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "action(inventory|collect|report), selector, source(可选 .md/.json；report 省略则用最新会话报告)"},
		{Name: "schedule_task", Category: "自动化任务", Description: "本地控制端定时任务：先 plan 预览，只在用户明确要求后 add 持久化；支持巡检、报告与通知", RiskLevel: RiskMedium, Perspective: PerspectiveController, ArgsHint: "action(plan|add|list|remove|run|run-due), text/task, run_at, repeat, notify, selector, kind, confirm_create(add必填), allow_batch, confirm_unattended(agent必填)"},
		{Name: "config_manage", Category: "配置管理", Description: "受控维护控制端 config.yaml：查看/读取/校验/备份/修复替换、全局或按名称启停 Skill、维护 skill_sources、mcp_servers、SSH/Telnet/FTP/Fleet 目标；写入前自动备份", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "action(status|get|validate|backup|disable_skills|enable_skills|disable_skill|enable_skill|add_skill_source|add_mcp_server|add_target|enable_fleet|set_ssh|set|replace_yaml), config_path, key, name, source, spec, protocol, host, port, user, password, key_path, tags, content；添加目标推荐分开传 host 和 port；已有单台配置转 Fleet 用 enable_fleet"},
		{Name: "skill_market", Category: "配置管理", Description: "导入本地 .skill，并跨 ClawHub / skills.sh 搜索、审查、安装、更新和可恢复回滚 Agent Skill；全程记录来源锁", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "action(search|inspect|install|import|managed|check_updates|update|pin|unpin|uninstall|rollback|audit), query, market, source, name, version, confirm_install/update/remove/rollback, acknowledge_risk, force, dest"},
		{Name: "mcp_resource", Category: "配置管理", Description: "列出或读取已连接 MCP Server 暴露的 Resource", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "action(list|read), server, uri"},
		{Name: "mcp_prompt", Category: "配置管理", Description: "列出或获取已连接 MCP Server 暴露的 Prompt 模板", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "action(list|get), server, name, 以及模板参数"},
		{Name: "fleet_inventory", Category: "批量运维", Description: "列出 config.yaml targets 多目标清单，支持 selector/name/tag/protocol 过滤", RiskLevel: RiskLow, Perspective: PerspectiveController, ArgsHint: "selector(all|name|tag|protocol)"},
		{Name: "fleet_exec", Category: "批量运维", Description: "多目标健康/日志巡检与 SSH 中断重试汇总；对多个 SSH/Telnet 目标并发执行同一命令并汇总每台结果", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "selector, command, concurrency"},
		{Name: "fleet_file", Category: "批量运维", Description: "对多个 SSH/Telnet/FTP 目标执行 ls/read/download/upload 文件动作", RiskLevel: RiskHigh, Perspective: PerspectiveController, ArgsHint: "selector, action(ls|read|download|upload), remote_path, local_path"},
	}
	m := make(map[string]*Tool, len(all))
	for _, t := range all {
		t.Enabled = true
		m[t.Name] = t
	}
	return m
}

// ConfigureEnabled 根据配置热插拔工具。enabled 非空时采用白名单，disabled 为黑名单。
func ConfigureEnabled(enabled, disabled []string) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if len(enabled) == 0 && len(disabled) == 0 {
		for _, t := range Registry {
			t.Enabled = true
		}
		return
	}
	if len(enabled) > 0 {
		for _, t := range Registry {
			t.Enabled = false
		}
		for _, name := range enabled {
			if t, ok := Registry[name]; ok {
				t.Enabled = true
			}
		}
	}
	for _, name := range disabled {
		if t, ok := Registry[name]; ok {
			t.Enabled = false
		}
	}
}

func SetEnabled(name string, enabled bool) bool {
	registryMu.Lock()
	defer registryMu.Unlock()
	t, ok := Registry[name]
	if !ok {
		return false
	}
	t.Enabled = enabled
	return true
}
