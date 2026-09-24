package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/tools"
)

type smokeCase struct {
	Name   string
	Args   map[string]string
	Expect []string
}

type smokeResult struct {
	Name     string
	Passed   bool
	Risk     string
	Duration time.Duration
	Error    string
	Evidence string
}

type envFixture struct {
	LocalDir     string
	RemoteDir    string
	HTTPURL      string
	BannerPort   string
	RedisPort    string
	MySQLPort    string
	PostgresPort string
	OraclePort   string
	OpenPort     string
}

func main() {
	cfgPath := flag.String("c", "build/config.yaml", "配置文件路径")
	outDir := flag.String("o", "build/reports/toolsmoke", "报告目录")
	flag.Parse()

	if err := config.InitConfig(*cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	if err := executor.Init(config.GlobalConfig); err != nil {
		fmt.Fprintf(os.Stderr, "executor: %v\n", err)
		os.Exit(2)
	}
	defer executor.Current.Close()
	ensureFleetTarget()

	env, cleanup, err := prepareFixtures()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixtures: %v\n", err)
		os.Exit(3)
	}
	defer cleanup()

	cases := buildCases(env)
	results := make([]smokeResult, 0, len(cases))
	fmt.Printf("[RUN] DeepSentry ToolSmoke: %d tools\n", len(cases))
	for _, c := range cases {
		start := time.Now()
		out, risk, err := tools.Run(c.Name, c.Args, false)
		res := smokeResult{Name: c.Name, Risk: risk, Duration: time.Since(start)}
		if err != nil {
			res.Error = err.Error()
		}
		res.Evidence = truncateOneLine(out, 220)
		res.Passed = err == nil && containsAll(out, c.Expect)
		if res.Passed {
			fmt.Printf("  [OK]   %-22s %s %s\n", c.Name, risk, res.Duration.Round(time.Millisecond))
		} else {
			fmt.Printf("  [ERR]  %-22s %s %s %s\n", c.Name, risk, res.Duration.Round(time.Millisecond), firstNonEmpty(res.Error, res.Evidence))
		}
		results = append(results, res)
	}

	reportPath, err := writeReport(*outDir, results)
	if err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
	}
	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	fmt.Printf("\n[STAT] ToolSmoke: %d/%d passed\n", passed, len(results))
	if reportPath != "" {
		fmt.Printf("[STAT] Report: %s\n", reportPath)
	}
	if passed != len(results) {
		os.Exit(1)
	}
}

func ensureFleetTarget() {
	if len(config.GlobalConfig.Targets) > 0 {
		return
	}
	if strings.TrimSpace(config.GlobalConfig.SSHHost) == "" {
		return
	}
	config.GlobalConfig.Targets = []config.TargetConfig{{
		Name:     "toolsmoke",
		Protocol: "ssh",
		Host:     config.GlobalConfig.SSHHost,
		User:     config.GlobalConfig.SSHUser,
		Password: config.GlobalConfig.SSHPassword,
		KeyPath:  config.GlobalConfig.SSHKeyPath,
		Tags:     []string{"toolsmoke"},
	}}
}

func prepareFixtures() (envFixture, func(), error) {
	localDir, err := os.MkdirTemp("", "deepsentry-toolsmoke-*")
	if err != nil {
		return envFixture{}, nil, err
	}
	env := envFixture{LocalDir: localDir, RemoteDir: fmt.Sprintf("/tmp/dst_smoke_%d", time.Now().UnixNano())}
	cleanup := func() {
		_, _ = executor.Current.Run("rm -rf " + shellQuote(env.RemoteDir))
		_ = os.RemoveAll(localDir)
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "ToolSmoke")
		_, _ = io.WriteString(w, "<html><head><title>ToolSmoke</title></head><body>ToolSmoke HTTP OK <form action='/x'></form><a href='/a'>a</a></body></html>")
	}))
	env.HTTPURL = httpSrv.URL
	oldCleanup := cleanup
	cleanup = func() {
		httpSrv.Close()
		oldCleanup()
	}

	env.BannerPort = startBannerServer("SSH-2.0-ToolSmoke\r\n")
	env.RedisPort = startRedisMock()
	env.MySQLPort = startMySQLMock()
	env.PostgresPort = startStaticResponder([]byte{'N'})
	env.OraclePort = startOracleMock()
	env.OpenPort = startBannerServer("TOOLSMOKE_TCP\r\n")

	if _, err := executor.Current.Run("mkdir -p " + shellQuote(env.RemoteDir)); err != nil {
		cleanup()
		return envFixture{}, nil, err
	}
	if err := writeLocalFixtures(env); err != nil {
		cleanup()
		return envFixture{}, nil, err
	}
	if err := uploadFixtures(env); err != nil {
		cleanup()
		return envFixture{}, nil, err
	}
	if _, err := executor.Current.Run(remoteFixtureScript(env.RemoteDir)); err != nil {
		cleanup()
		return envFixture{}, nil, err
	}
	return env, cleanup, nil
}

func writeLocalFixtures(env envFixture) error {
	if err := os.WriteFile(filepath.Join(env.LocalDir, "upload.txt"), []byte("TOOLSMOKE_UPLOAD\n"), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(env.LocalDir, "sample.csv"), []byte("name,value\nToolSmoke,42\n"), 0644); err != nil {
		return err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("TOOLSMOKE_GZIP\nsecond\n"))
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(env.LocalDir, "sample.log.gz"), gz.Bytes(), 0644); err != nil {
		return err
	}
	sqlite := make([]byte, 4096)
	copy(sqlite, []byte("SQLite format 3\x00"))
	binary.BigEndian.PutUint16(sqlite[16:18], 4096)
	binary.BigEndian.PutUint32(sqlite[28:32], 1)
	copy(sqlite[120:], []byte("CREATE TABLE toolsmoke(id INTEGER PRIMARY KEY, name TEXT);"))
	if err := os.WriteFile(filepath.Join(env.LocalDir, "sample.db"), sqlite, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(env.LocalDir, "sample.pcap"), minimalPcap(), 0644); err != nil {
		return err
	}
	return nil
}

func uploadFixtures(env envFixture) error {
	for _, name := range []string{"upload.txt", "sample.csv", "sample.log.gz", "sample.db", "sample.pcap"} {
		local := filepath.Join(env.LocalDir, name)
		remote := env.RemoteDir + "/" + name
		if _, err := executor.Current.Run("upload " + shellQuote(local) + " " + shellQuote(remote)); err != nil {
			return err
		}
	}
	return nil
}

func remoteFixtureScript(dir string) string {
	return fmt.Sprintf(`cat > %s/plain.log <<'EOF'
TOOLSMOKE_PLAIN
root:x:0:0:root:/root:/bin/bash
flag{TOOLSMOKE_FLAG}
password=SuperSecret123
AKIA%s
mysql_error: access denied for user toolsmoke
EOF
cat > %s/app.yaml <<'EOF'
database:
  url: mysql://tool:secret@127.0.0.1:3306/app
  password: SuperSecret123
EOF
cat > %s/mysql.cnf <<'EOF'
[mysqld]
bind-address=0.0.0.0
password=SuperSecret123
EOF
mkdir -p %s/archive_src
echo TOOLSMOKE_ARCHIVE > %s/archive_src/item.txt
`, shellQuote(dir), "1234567890ABCDEF", shellQuote(dir), shellQuote(dir), shellQuote(dir), shellQuote(dir))
}

func buildCases(env envFixture) []smokeCase {
	rd := env.RemoteDir
	lp := env.OpenPort
	return []smokeCase{
		{"ping", map[string]string{"host": "127.0.0.1", "count": "1"}, []string{"127.0.0.1"}},
		{"traceroute", map[string]string{"host": "127.0.0.1"}, []string{"路由追踪"}},
		{"dns_lookup", map[string]string{"host": "localhost"}, []string{"127.0.0.1"}},
		{"bandwidth_test", map[string]string{"host": "127.0.0.1"}, []string{"TCP"}},
		{"net_connections", map[string]string{"filter": "all"}, []string{"网络连接"}},
		{"port_listen", nil, []string{"LISTEN"}},
		{"route_table", nil, []string{"路由"}},
		{"arp_table", nil, []string{"ARP"}},
		{"firewall_status", nil, []string{"防火墙"}},
		{"mem_info", nil, []string{"MemTotal"}},
		{"process_list", map[string]string{"limit": "5"}, []string{"PID"}},
		{"target_health_summary", nil, []string{"Linux"}},
		{"disk_usage", map[string]string{"path": "/"}, []string{"磁盘"}},
		{"file_tail", map[string]string{"path": rd + "/plain.log", "lines": "10"}, []string{"TOOLSMOKE_PLAIN"}},
		{"login_audit", map[string]string{"lines": "20"}, []string{"登录"}},
		{"service_units", map[string]string{"query": "ssh", "limit": "5"}, []string{"服务"}},
		{"file_hash", map[string]string{"path": rd + "/plain.log"}, []string{"SHA256"}},
		{"file_ident", map[string]string{"path": rd + "/plain.log"}, []string{"文本"}},
		{"file_strings", map[string]string{"path": rd + "/plain.log", "pattern": "TOOLSMOKE", "limit": "5"}, []string{"TOOLSMOKE"}},
		{"document_parse", map[string]string{"path": rd + "/sample.csv"}, []string{"CSV", "ToolSmoke"}},
		{"read_gzip", map[string]string{"path": rd + "/sample.log.gz", "lines": "5"}, []string{"TOOLSMOKE_GZIP"}},
		{"read_log", map[string]string{"path": rd + "/plain.log", "lines": "10"}, []string{"TOOLSMOKE_PLAIN"}},
		{"nmap_scan", map[string]string{"host": "127.0.0.1", "ports": lp}, []string{"open"}},
		{"cidr_scan", map[string]string{"cidr": "127.0.0.0/30", "ports": lp, "timeout": "1", "limit": "4"}, []string{"127.0.0.1"}},
		{"netcat_probe", map[string]string{"host": "127.0.0.1", "port": lp}, []string{"可达"}},
		{"http_probe", map[string]string{"url": env.HTTPURL, "method": "GET"}, []string{"HTTP"}},
		{"http_fetch", map[string]string{"url": env.HTTPURL, "method": "GET"}, []string{"ToolSmoke"}},
		{"web_snapshot", map[string]string{"url": env.HTTPURL}, []string{"ToolSmoke"}},
		{"headless_browser", map[string]string{"url": env.HTTPURL, "mode": "text", "wait_ms": "200"}, []string{"ToolSmoke"}},
		{"flow_snapshot", map[string]string{"interval": "1"}, []string{"连接"}},
		{"pcap_analyze", map[string]string{"path": rd + "/sample.pcap"}, []string{"PCAP", "Packets"}},
		{"packet_capture", map[string]string{"interval": "1"}, []string{"flow_snapshot"}},
		{"proc_socket_map", map[string]string{"filter": "listen", "limit": "5"}, []string{"socket"}},
		{"service_fingerprint", map[string]string{"host": "127.0.0.1", "port": env.BannerPort}, []string{"SSH"}},
		{"redis_probe", map[string]string{"host": "127.0.0.1", "port": env.RedisPort}, []string{"PONG"}},
		{"mysql_probe", map[string]string{"host": "127.0.0.1", "port": env.MySQLPort}, []string{"MySQL"}},
		{"postgres_probe", map[string]string{"host": "127.0.0.1", "port": env.PostgresPort}, []string{"PostgreSQL"}},
		{"oracle_probe", map[string]string{"host": "127.0.0.1", "port": env.OraclePort}, []string{"Oracle"}},
		{"sqlite_inspect", map[string]string{"path": rd + "/sample.db"}, []string{"SQLite", "CREATE TABLE"}},
		{"app_config_discover", map[string]string{"roots": rd, "query": "mysql"}, []string{"app.yaml"}},
		{"db_config_audit", map[string]string{"type": "mysql", "paths": rd + "/mysql.cnf"}, []string{"数据库配置审计"}},
		{"db_log_read", map[string]string{"type": "mysql", "path": rd + "/plain.log", "pattern": "mysql_error", "lines": "10"}, []string{"mysql_error"}},
		{"secret_scan", map[string]string{"root": rd, "pattern": "AKIA[0-9A-Z]{16}", "limit": "5"}, []string{"AKIA"}},
		{"service_unit_audit", map[string]string{"limit": "1"}, []string{"服务"}},
		{"container_inventory", nil, []string{"容器"}},
		{"flag_scan", map[string]string{"root": rd, "limit": "5"}, []string{"flag{TOOLSMOKE_FLAG}"}},
		{"awd_service_check", map[string]string{"targets": env.HTTPURL, "timeout": "2"}, []string{"HTTP"}},
		{"script_run", map[string]string{"language": "shell", "content": "echo TOOLSMOKE_SCRIPT", "timeout": "5"}, []string{"TOOLSMOKE_SCRIPT"}},
		{"file_upload", map[string]string{"local_path": filepath.Join(env.LocalDir, "upload.txt"), "remote_path": rd + "/uploaded.txt"}, []string{"完成"}},
		{"file_download", map[string]string{"remote_path": rd + "/plain.log", "local_path": filepath.Join(env.LocalDir, "download.txt")}, []string{"完成"}},
		{"archive_pack", map[string]string{"format": "tar.gz", "source": rd + "/archive_src", "dest": rd + "/archive.tgz"}, []string{"完成"}},
		{"archive_extract", map[string]string{"format": "tar.gz", "source": rd + "/archive.tgz", "dest": rd + "/archive_out"}, []string{"完成"}},
		{"tcp_forward", map[string]string{"action": "start", "listen_host": "127.0.0.1", "listen_port": "0", "target_host": "127.0.0.1", "target_port": lp}, []string{"已启动"}},
		{"socks5_proxy", map[string]string{"action": "start", "listen_host": "127.0.0.1", "listen_port": "0"}, []string{"已启动"}},
		{"http_proxy", map[string]string{"action": "start", "listen_host": "127.0.0.1", "listen_port": "0"}, []string{"已启动"}},
		{"schedule_task", map[string]string{"action": "plan", "task": "巡检服务器并生成报告", "run_at": "2099-01-01 09:00", "kind": "inspection"}, []string{"解析完成"}},
		{"fleet_inventory", map[string]string{"selector": "toolsmoke"}, []string{"toolsmoke"}},
		{"fleet_exec", map[string]string{"selector": "toolsmoke", "command": "echo TOOLSMOKE_FLEET", "concurrency": "1"}, []string{"TOOLSMOKE_FLEET"}},
		{"fleet_file", map[string]string{"selector": "toolsmoke", "action": "ls", "remote_path": rd}, []string{"plain.log"}},
		{"config_manage", map[string]string{"config_path": filepath.Join(env.LocalDir, "managed-config.yaml"), "action": "add_skill_source", "source": filepath.Join(env.LocalDir, "skills")}, []string{"已更新配置", "skill_sources"}},
	}
}

func startBannerServer(banner string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte(banner))
				_, _ = io.Copy(io.Discard, c)
			}(conn)
		}
	}()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func startStaticResponder(resp []byte) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				_, _ = c.Read(buf)
				_, _ = c.Write(resp)
			}(conn)
		}
	}()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func startRedisMock() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
					raw, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					for strings.TrimSpace(raw) != "" && !strings.HasSuffix(raw, "\r\n\r\n") && reader.Buffered() > 0 {
						line, _ := reader.ReadString('\n')
						raw += line
					}
					up := strings.ToUpper(raw)
					switch {
					case strings.Contains(up, "PING"):
						_, _ = c.Write([]byte("+PONG\r\n"))
					case strings.Contains(up, "INFO"):
						body := "redis_version:7.0.0\r\nos:Linux\r\narch_bits:64\r\n"
						_, _ = c.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(body), body)))
					case strings.Contains(up, "CONFIG"):
						_, _ = c.Write([]byte("*2\r\n$3\r\ndir\r\n$4\r\n/tmp\r\n"))
					default:
						_, _ = c.Write([]byte("+OK\r\n"))
					}
				}
			}(conn)
		}
	}()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func startMySQLMock() string {
	payload := make([]byte, 64)
	payload[0] = 10
	copy(payload[1:], []byte("5.7.0-toolsmoke\x00"))
	packet := []byte{64, 0, 0, 0}
	packet = append(packet, payload...)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write(packet)
			}(conn)
		}
	}()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func startOracleMock() string {
	return startStaticResponder([]byte("(DESCRIPTION=(ERR=0)(VSNNUM=0))"))
}

func minimalPcap() []byte {
	return []byte{
		0xd4, 0xc3, 0xb2, 0xa1, 0x02, 0x00, 0x04, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xff, 0xff, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
	}
}

func writeReport(outDir string, results []smokeResult) (string, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(outDir, fmt.Sprintf("toolsmoke_%s.md", time.Now().Format("20060102_150405")))
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	var b strings.Builder
	b.WriteString("# DeepSentry ToolSmoke Report\n\n")
	b.WriteString(fmt.Sprintf("- Time: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("- Tools: %d\n\n", len(results)))
	b.WriteString("| Tool | Status | Risk | Duration | Evidence |\n|---|---|---|---:|---|\n")
	for _, r := range results {
		status := "OK"
		if !r.Passed {
			status = "ERR"
		}
		ev := r.Evidence
		if r.Error != "" {
			ev = r.Error + " / " + ev
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s |\n", r.Name, status, r.Risk, r.Duration.Round(time.Millisecond), escapeTable(ev)))
	}
	return path, os.WriteFile(path, []byte(b.String()), 0644)
}

func containsAll(s string, expects []string) bool {
	if len(expects) == 0 {
		return strings.TrimSpace(s) != ""
	}
	lower := strings.ToLower(s)
	for _, e := range expects {
		if !strings.Contains(lower, strings.ToLower(e)) {
			return false
		}
	}
	return true
}

func truncateOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func escapeTable(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return truncateOneLine(s, 180)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
