package builtin

import (
	"ai-edr/internal/config"
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ping TCP 连通性探测（不依赖系统 ping 命令，无 ICMP 权限也可工作）
func Ping(rt Runtime, host string, count int) (string, error) {
	if err := validateHost(host); err != nil {
		return "", err
	}
	if count <= 0 {
		count = 4
	}
	if count > 10 {
		count = 10
	}

	ports := []int{443, 80, 22, 53}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Ping (TCP 探活)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("目标: %s  次数: %d\n\n", host, count))

	success := 0
	for i := 1; i <= count; i++ {
		start := time.Now()
		ok, port := tcpReachable(host, ports, 3*time.Second)
		elapsed := time.Since(start)
		if ok {
			success++
			b.WriteString(fmt.Sprintf("[%d] %s:%d 可达  耗时 %v\n", i, host, port, elapsed.Round(time.Millisecond)))
		} else {
			b.WriteString(fmt.Sprintf("[%d] %s 不可达  耗时 %v\n", i, host, elapsed.Round(time.Millisecond)))
		}
		if i < count {
			time.Sleep(500 * time.Millisecond)
		}
	}
	b.WriteString(fmt.Sprintf("\n统计: %d/%d 成功\n", success, count))
	if success == 0 {
		b.WriteString("提示: 目标可能禁 ICMP/常见端口，可尝试 netcat_probe 指定端口\n")
	}
	return b.String(), nil
}

func tcpReachable(host string, ports []int, timeout time.Duration) (bool, int) {
	for _, port := range ports {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		conn, err := config.ControllerDialTimeout("tcp", addr, timeout)
		if err == nil {
			conn.Close()
			return true, port
		}
	}
	return false, 0
}

// DNSLookup Go 原生 DNS 解析
func DNSLookup(rt Runtime, host, rrType string) (string, error) {
	if err := validateHost(host); err != nil {
		return "", err
	}
	if rrType == "" {
		rrType = "A"
	}
	rrType = strings.ToUpper(rrType)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s DNS 查询\n", rt.tag()))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resolver := net.Resolver{}

	switch rrType {
	case "A", "AAAA":
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return "", err
		}
		for _, ip := range ips {
			if rrType == "A" && ip.IP.To4() != nil {
				b.WriteString(ip.String() + "\n")
			} else if rrType == "AAAA" && ip.IP.To4() == nil {
				b.WriteString(ip.String() + "\n")
			}
		}
	case "MX":
		records, err := resolver.LookupMX(ctx, host)
		if err != nil {
			return "", err
		}
		for _, r := range records {
			b.WriteString(fmt.Sprintf("%d %s\n", r.Pref, r.Host))
		}
	case "NS":
		records, err := resolver.LookupNS(ctx, host)
		if err != nil {
			return "", err
		}
		for _, r := range records {
			b.WriteString(r.Host + "\n")
		}
	case "TXT":
		records, err := resolver.LookupTXT(ctx, host)
		if err != nil {
			return "", err
		}
		for _, r := range records {
			b.WriteString(r + "\n")
		}
	case "CNAME":
		cname, err := resolver.LookupCNAME(ctx, host)
		if err != nil {
			return "", err
		}
		b.WriteString(cname + "\n")
	default:
		return "", fmt.Errorf("不支持的 DNS 类型: %s", rrType)
	}

	out := strings.TrimSpace(b.String())
	if out == "" {
		return fmt.Sprintf("%s DNS 查询\n(无 %s 记录)", rt.tag(), rrType), nil
	}
	return fmt.Sprintf("%s DNS 查询 (%s)\n%s", rt.tag(), rrType, out), nil
}

// HTTPProbe Go 原生 HTTP 探测
func HTTPProbe(rt Runtime, rawURL, method string) (string, error) {
	url := rawURL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	if method == "" {
		method = "HEAD"
	}
	method = strings.ToUpper(method)
	if method != "HEAD" && method != "GET" {
		return "", fmt.Errorf("method 仅支持 HEAD/GET")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DeepSentry-Builtin/1.0")

	client := config.HTTPClient(10 * time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s HTTP 探测\n", rt.tag()))
	b.WriteString(fmt.Sprintf("URL: %s\n", url))
	b.WriteString(fmt.Sprintf("Status: %s\n\n", resp.Status))
	b.WriteString("Headers:\n")
	for k, vals := range resp.Header {
		b.WriteString(fmt.Sprintf("  %s: %s\n", k, strings.Join(vals, ", ")))
	}
	return b.String(), nil
}

// TCPProbe Go 原生 TCP 端口探活
func TCPProbe(rt Runtime, host, port string, timeoutSec int) (string, error) {
	if err := validateHost(host); err != nil {
		return "", err
	}
	p, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("非法 port: %s", port)
	}
	if timeoutSec <= 0 {
		timeoutSec = 3
	}
	if timeoutSec > 10 {
		timeoutSec = 10
	}

	addr := net.JoinHostPort(host, strconv.Itoa(p))
	start := time.Now()
	conn, err := config.ControllerDialTimeout("tcp", addr, time.Duration(timeoutSec)*time.Second)
	elapsed := time.Since(start)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s TCP 探活\n", rt.tag()))
	if err != nil {
		b.WriteString(fmt.Sprintf("%s:%d 不可达 (%v)  耗时 %v\n", host, p, err, elapsed.Round(time.Millisecond)))
	} else {
		conn.Close()
		b.WriteString(fmt.Sprintf("%s:%d 开放/可达  耗时 %v\n", host, p, elapsed.Round(time.Millisecond)))
	}
	return b.String(), nil
}

// PortScan Go 原生 TCP 端口扫描（替代 nmap，从 DeepSentry 进程发起）
func PortScan(rt Runtime, host, portsSpec, mode string) (string, error) {
	if err := validateHost(host); err != nil {
		return "", err
	}

	var ports []int
	if mode == "quick" || portsSpec == "" {
		ports = []int{21, 22, 23, 25, 53, 80, 110, 135, 139, 143, 443, 445, 993, 995, 1433, 3306, 3389, 5432, 5900, 6379, 8080, 8443, 27017, 11211}
	} else {
		var err error
		ports, err = parsePortSpec(portsSpec)
		if err != nil {
			return "", err
		}
	}
	if len(ports) > 200 {
		return "", fmt.Errorf("端口数量过多 (最大 200)")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s TCP 端口扫描 (Go 原生，无需 nmap)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("目标: %s  端口数: %d\n\n", host, len(ports)))

	type result struct {
		port int
		open bool
		ms   time.Duration
	}
	results := make([]result, 0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 50)

	for _, port := range ports {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			addr := net.JoinHostPort(host, strconv.Itoa(p))
			start := time.Now()
			conn, err := config.ControllerDialTimeout("tcp", addr, 2*time.Second)
			elapsed := time.Since(start)
			open := err == nil
			if open {
				conn.Close()
			}
			mu.Lock()
			results = append(results, result{port: p, open: open, ms: elapsed})
			mu.Unlock()
		}(port)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].port < results[j].port })
	openCount := 0
	for _, r := range results {
		if r.open {
			openCount++
			b.WriteString(fmt.Sprintf("  OPEN  %5d  (%v)\n", r.port, r.ms.Round(time.Millisecond)))
		}
	}
	b.WriteString(fmt.Sprintf("\n扫描完成: %d/%d 端口开放\n", openCount, len(ports)))
	return b.String(), nil
}

func parsePortSpec(spec string) ([]int, error) {
	var ports []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil || start > end || end-start > 1000 {
				return nil, fmt.Errorf("非法端口范围: %s", part)
			}
			for p := start; p <= end && len(ports) < 200; p++ {
				ports = append(ports, p)
			}
		} else {
			p, err := strconv.Atoi(part)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("非法端口: %s", part)
			}
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// FlowSnapshot 连接快照对比（极简系统替代 tcpdump，仅依赖 /proc/net/tcp）
func FlowSnapshot(rt Runtime, intervalSec int) (string, error) {
	if rt.IsWindows {
		return "", fmt.Errorf("flow_snapshot 仅支持 Linux /proc，Windows 请用 net_connections")
	}
	if intervalSec <= 0 {
		intervalSec = 2
	}
	if intervalSec > 10 {
		intervalSec = 10
	}

	before, err := readProcNetTCP()
	if err != nil {
		return "", fmt.Errorf("读取初始连接快照失败: %w", err)
	}
	time.Sleep(time.Duration(intervalSec) * time.Second)
	after, err := readProcNetTCP()
	if err != nil {
		return "", fmt.Errorf("读取二次连接快照失败: %w", err)
	}

	beforeSet := make(map[string]socketEntry)
	for _, e := range before {
		key := fmt.Sprintf("%s:%d->%s:%d", e.LocalIP, e.LocalPort, e.RemoteIP, e.RemotePort)
		beforeSet[key] = e
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 连接流快照 (/%d 秒/，无需 tcpdump)\n", rt.tag(), intervalSec))
	newCount := 0
	for _, e := range after {
		if e.State != "ESTABLISHED" {
			continue
		}
		key := fmt.Sprintf("%s:%d->%s:%d", e.LocalIP, e.LocalPort, e.RemoteIP, e.RemotePort)
		if _, exists := beforeSet[key]; !exists {
			newCount++
			b.WriteString("  [NEW] " + formatSocket(e) + "\n")
		}
	}
	if newCount == 0 {
		b.WriteString("  (期间无新增 ESTABLISHED 连接)\n")
	}
	b.WriteString(fmt.Sprintf("\n新增连接: %d\n", newCount))
	return b.String(), nil
}

func validateHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("缺少参数 host")
	}
	if len(host) > 253 {
		return fmt.Errorf("非法 host")
	}
	for _, c := range host {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == ':' || c == '_' {
			continue
		}
		return fmt.Errorf("非法 host: %s", host)
	}
	return nil
}
