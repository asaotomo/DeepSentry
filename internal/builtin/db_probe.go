package builtin

import (
	"ai-edr/internal/config"
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

func ServiceFingerprint(rt Runtime, host, port string, timeoutSec int) (string, error) {
	p, err := parseTCPPort(port)
	if err != nil {
		return "", err
	}
	if err := validateHost(host); err != nil {
		return "", err
	}
	if timeoutSec <= 0 {
		timeoutSec = 3
	}
	addr := net.JoinHostPort(host, strconv.Itoa(p))
	conn, err := config.ControllerDialTimeout("tcp", addr, time.Duration(timeoutSec)*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Duration(timeoutSec) * time.Second))

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 服务指纹\n目标: %s\n", rt.tag(), addr))

	if guess := guessByPort(p); guess != "" {
		b.WriteString("端口线索: " + guess + "\n")
	}
	banner := readBanner(conn)
	if banner != "" {
		b.WriteString("Banner: " + sanitizeLine(banner, 300) + "\n")
		if kind := classifyBanner(banner); kind != "" {
			b.WriteString("识别: " + kind + "\n")
			return b.String(), nil
		}
	}

	switch p {
	case 6379:
		_, _ = conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
		resp := readBanner(conn)
		if strings.Contains(resp, "PONG") || strings.HasPrefix(resp, "-NOAUTH") {
			b.WriteString("识别: Redis RESP\n")
			b.WriteString("响应: " + sanitizeLine(resp, 300) + "\n")
		}
	case 5432:
		b.WriteString(probePostgresSSL(conn))
	case 80, 8080, 8000, 443:
		_, _ = conn.Write([]byte("HEAD / HTTP/1.0\r\nUser-Agent: DeepSentry\r\n\r\n"))
		resp := readBanner(conn)
		if resp != "" {
			b.WriteString("HTTP 响应: " + sanitizeLine(resp, 500) + "\n")
		}
	}
	if !strings.Contains(b.String(), "识别:") {
		b.WriteString("识别: 未确认协议，仅确认 TCP 可连接\n")
	}
	return b.String(), nil
}

func RedisProbe(rt Runtime, host, port, password string, timeoutSec int) (string, error) {
	if port == "" {
		port = "6379"
	}
	conn, err := dialHostPort(host, port, timeoutSec)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Redis 只读探测\n目标: %s:%s\n", rt.tag(), host, port))
	if password != "" {
		resp := redisCommand(conn, "AUTH", password)
		b.WriteString("AUTH: " + summarizeRedis(resp) + "\n")
	}
	pong := redisCommand(conn, "PING")
	b.WriteString("PING: " + summarizeRedis(pong) + "\n")
	if strings.HasPrefix(pong, "-NOAUTH") {
		b.WriteString("认证: 需要密码\n")
		return b.String(), nil
	}
	info := redisCommand(conn, "INFO", "server")
	b.WriteString("\nINFO server 摘要:\n" + filterRedisInfo(info, []string{"redis_version", "os", "arch_bits", "tcp_port", "process_id", "uptime_in_seconds"}))
	cfg := redisCommand(conn, "CONFIG", "GET", "dir")
	b.WriteString("\nCONFIG dir: " + summarizeRedis(cfg) + "\n")
	dbfile := redisCommand(conn, "CONFIG", "GET", "dbfilename")
	b.WriteString("CONFIG dbfilename: " + summarizeRedis(dbfile) + "\n")
	return b.String(), nil
}

func MySQLProbe(rt Runtime, host, port string, timeoutSec int) (string, error) {
	if port == "" {
		port = "3306"
	}
	conn, err := dialHostPort(host, port, timeoutSec)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	return fmt.Sprintf("%s MySQL 握手探测\n目标: %s:%s\n%s", rt.tag(), host, port, parseMySQLHandshake(buf[:n])), nil
}

func PostgresProbe(rt Runtime, host, port string, timeoutSec int) (string, error) {
	if port == "" {
		port = "5432"
	}
	conn, err := dialHostPort(host, port, timeoutSec)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s PostgreSQL 探测\n目标: %s:%s\n", rt.tag(), host, port))
	b.WriteString(probePostgresSSL(conn))
	return b.String(), nil
}

func OracleProbe(rt Runtime, host, port string, timeoutSec int) (string, error) {
	if port == "" {
		port = "1521"
	}
	conn, err := dialHostPort(host, port, timeoutSec)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write([]byte{0, 58, 0, 0, 1, 0, 0, 0, 1, 54, 1, 44, 0, 0, 8, 0, 127, 255, 127, 8, 0, 0, 0, 1, 0, 32, 0, 60, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	resp := readBanner(conn)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Oracle TNS 探测\n目标: %s:%s\n", rt.tag(), host, port))
	if resp == "" {
		b.WriteString("TCP 可连接，但未收到 TNS 响应。\n")
	} else {
		b.WriteString("响应: " + sanitizeLine(resp, 500) + "\n")
		b.WriteString("识别: 可能为 Oracle TNS Listener\n")
	}
	return b.String(), nil
}

func dialHostPort(host, port string, timeoutSec int) (net.Conn, error) {
	if err := validateHost(host); err != nil {
		return nil, err
	}
	p, err := parseTCPPort(port)
	if err != nil {
		return nil, err
	}
	if timeoutSec <= 0 {
		timeoutSec = 3
	}
	conn, err := config.ControllerDialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(p)), time.Duration(timeoutSec)*time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(time.Duration(timeoutSec) * time.Second))
	return conn, nil
}

func parseTCPPort(port string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("非法 port: %s", port)
	}
	return p, nil
}

func readBanner(conn net.Conn) string {
	_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	if n <= 0 {
		return ""
	}
	return string(buf[:n])
}

func classifyBanner(b string) string {
	l := strings.ToLower(b)
	switch {
	case strings.Contains(l, "ssh-"):
		return "SSH"
	case strings.Contains(l, "http/"):
		return "HTTP"
	case strings.Contains(l, "redis") || strings.Contains(l, "noauth") || strings.Contains(l, "pong"):
		return "Redis"
	case len(b) > 5 && b[4] == 10:
		return "可能为 MySQL/MariaDB handshake"
	default:
		return ""
	}
}

func guessByPort(p int) string {
	switch p {
	case 6379:
		return "Redis"
	case 3306:
		return "MySQL/MariaDB"
	case 5432:
		return "PostgreSQL"
	case 1521:
		return "Oracle TNS"
	case 9200:
		return "Elasticsearch"
	case 27017:
		return "MongoDB"
	default:
		return ""
	}
}

func redisCommand(conn net.Conn, parts ...string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("*%d\r\n", len(parts)))
	for _, p := range parts {
		b.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(p), p))
	}
	_, _ = conn.Write([]byte(b.String()))
	reader := bufio.NewReader(conn)
	resp, _ := reader.ReadString('\n')
	if strings.HasPrefix(resp, "$") {
		size, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(resp, "$")))
		if size > 0 && size < 1<<20 {
			body := make([]byte, size+2)
			_, _ = io.ReadFull(reader, body)
			resp += string(body)
		}
	} else if strings.HasPrefix(resp, "*") {
		for i := 0; i < 8; i++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			resp += line
		}
	}
	return resp
}

func summarizeRedis(resp string) string {
	return sanitizeLine(strings.TrimSpace(resp), 300)
}

func filterRedisInfo(info string, keys []string) string {
	var b strings.Builder
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		for _, k := range keys {
			if strings.HasPrefix(line, k+":") {
				b.WriteString("  " + line + "\n")
			}
		}
	}
	if b.Len() == 0 {
		return "  (无可用 INFO，可能需要认证或权限不足)\n"
	}
	return b.String()
}

func parseMySQLHandshake(data []byte) string {
	if len(data) < 6 {
		return "响应过短，无法解析 MySQL handshake\n"
	}
	payload := data[4:]
	proto := payload[0]
	rest := payload[1:]
	end := bytes.IndexByte(rest, 0)
	version := ""
	if end >= 0 {
		version = string(rest[:end])
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("协议版本: %d\n", proto))
	b.WriteString(fmt.Sprintf("服务版本: %s\n", version))
	if len(payload) > 32 {
		capLow := binary.LittleEndian.Uint16(payload[13:15])
		b.WriteString(fmt.Sprintf("Capabilities(low): 0x%04x\n", capLow))
	}
	if strings.Contains(strings.ToLower(version), "mariadb") {
		b.WriteString("识别: MariaDB/MySQL\n")
	} else {
		b.WriteString("识别: MySQL-compatible\n")
	}
	return b.String()
}

func probePostgresSSL(conn net.Conn) string {
	req := []byte{0, 0, 0, 8, 4, 210, 22, 47}
	_, _ = conn.Write(req)
	resp := make([]byte, 1)
	n, _ := conn.Read(resp)
	if n == 0 {
		return "未收到 SSLRequest 响应，可能不是 PostgreSQL 或被防火墙阻断。\n"
	}
	switch resp[0] {
	case 'S':
		return "识别: PostgreSQL\nSSL: 支持\n"
	case 'N':
		return "识别: PostgreSQL\nSSL: 不支持/禁用\n"
	default:
		return fmt.Sprintf("收到未知响应: 0x%02x\n", resp[0])
	}
}

func sanitizeLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 32 || r == 127 {
			return '.'
		}
		return r
	}, s)
	return truncateOneLine(s, max)
}
