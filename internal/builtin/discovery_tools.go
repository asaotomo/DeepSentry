package builtin

import (
	"ai-edr/internal/config"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func CIDRScan(rt Runtime, cidr, portsSpec string, timeoutSec, limit int) (string, error) {
	ips, err := expandCIDR(cidr, limit)
	if err != nil {
		return "", err
	}
	ports, err := discoveryPorts(portsSpec)
	if err != nil {
		return "", err
	}
	if timeoutSec <= 0 {
		timeoutSec = 1
	}
	if timeoutSec > 5 {
		timeoutSec = 5
	}

	type hit struct {
		IP   string
		Port int
	}
	var hits []hit
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 128)
	timeout := time.Duration(timeoutSec) * time.Second
	for _, ip := range ips {
		for _, port := range ports {
			wg.Add(1)
			go func(ip string, port int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				conn, err := config.ControllerDialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
				if err == nil {
					_ = conn.Close()
					mu.Lock()
					hits = append(hits, hit{IP: ip, Port: port})
					mu.Unlock()
				}
			}(ip, port)
		}
	}
	wg.Wait()
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].IP == hits[j].IP {
			return hits[i].Port < hits[j].Port
		}
		return ipLess(hits[i].IP, hits[j].IP)
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 内网轻量发现 (fscan-like, TCP only)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("范围: %s  主机数: %d  端口: %v\n\n", cidr, len(ips), ports))
	grouped := map[string][]int{}
	for _, h := range hits {
		grouped[h.IP] = append(grouped[h.IP], h.Port)
	}
	if len(grouped) == 0 {
		b.WriteString("(未发现开放端口)\n")
		return b.String(), nil
	}
	for _, ip := range sortedKeys(grouped) {
		b.WriteString(fmt.Sprintf("%s  open: %v\n", ip, grouped[ip]))
	}
	return b.String(), nil
}

func HTTPFetch(rt Runtime, rawURL, method string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	if maxBytes > 512*1024 {
		maxBytes = 512 * 1024
	}
	u, err := normalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)
	if method != "GET" && method != "HEAD" {
		return "", fmt.Errorf("http_fetch 仅支持 GET/HEAD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DeepSentry-Builtin/1.0")
	resp, err := config.HTTPClient(12 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s HTTP Fetch\nURL: %s\nStatus: %s\n", rt.tag(), u, resp.Status))
	b.WriteString("Headers:\n")
	for k, vals := range resp.Header {
		b.WriteString(fmt.Sprintf("  %s: %s\n", k, strings.Join(vals, ", ")))
	}
	if method != "HEAD" {
		b.WriteString(fmt.Sprintf("\nBody(first %d bytes):\n%s\n", len(body), sanitizeBody(string(body))))
	}
	return b.String(), nil
}

func WebSnapshot(rt Runtime, rawURL string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 128 * 1024
	}
	u, err := normalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DeepSentry-Builtin/1.0")
	resp, err := config.HTTPClient(12 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))
	body := string(bodyBytes)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Web Snapshot\nURL: %s\nStatus: %s\n", rt.tag(), u, resp.Status))
	b.WriteString("Server: " + resp.Header.Get("Server") + "\n")
	b.WriteString("Title: " + firstMatch(body, `(?is)<title[^>]*>(.*?)</title>`) + "\n")
	b.WriteString("Meta generator: " + firstMatch(body, `(?is)<meta[^>]+name=["']generator["'][^>]+content=["']([^"']+)`) + "\n")
	b.WriteString("\nForms:\n")
	for _, f := range allMatches(body, `(?is)<form[^>]*>`, 10) {
		b.WriteString("  - " + compactHTML(f) + "\n")
	}
	b.WriteString("\nScripts:\n")
	for _, s := range allMatches(body, `(?is)<script[^>]+src=["']([^"']+)`, 20) {
		b.WriteString("  - " + s + "\n")
	}
	b.WriteString("\nLinks:\n")
	for _, l := range allMatches(body, `(?is)<a[^>]+href=["']([^"']+)`, 20) {
		b.WriteString("  - " + l + "\n")
	}
	return b.String(), nil
}

func expandCIDR(cidr string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 256
	}
	if limit > 1024 {
		limit = 1024
	}
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return nil, err
	}
	ip = ip.To4()
	if ip == nil {
		return nil, fmt.Errorf("仅支持 IPv4 CIDR")
	}
	var ips []string
	for cur := ip.Mask(ipnet.Mask).To4(); ipnet.Contains(cur); incIP(cur) {
		dup := append(net.IP(nil), cur...)
		if !dup.Equal(ipnet.IP) {
			ips = append(ips, dup.String())
		}
		if len(ips) >= limit {
			break
		}
	}
	return ips, nil
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func discoveryPorts(spec string) ([]int, error) {
	if strings.TrimSpace(spec) == "" {
		return []int{21, 22, 23, 25, 53, 80, 135, 139, 443, 445, 1433, 1521, 3306, 3389, 5432, 6379, 8080, 8443, 9200, 27017}, nil
	}
	return parsePortSpec(spec)
}

func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url 不能为空")
	}
	lower := strings.ToLower(raw)
	for _, blocked := range []string{"javascript:", "data:", "file:", "ftp:", "mailto:", "chrome:"} {
		if strings.HasPrefix(lower, blocked) {
			return "", fmt.Errorf("仅允许 http/https URL: %s", raw)
		}
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("非法 URL: %s", raw)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("仅允许 http/https URL: %s", raw)
	}
	return u.String(), nil
}

func sanitizeBody(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if r < 32 || r == 127 {
			return '.'
		}
		return r
	}, s)
	return truncate(s, 20000)
}

func firstMatch(s, pattern string) string {
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(stripTags(m[1]))
}

func allMatches(s, pattern string, limit int) []string {
	re := regexp.MustCompile(pattern)
	ms := re.FindAllStringSubmatch(s, limit)
	var out []string
	for _, m := range ms {
		if len(m) > 1 {
			out = append(out, strings.TrimSpace(m[1]))
		} else if len(m) == 1 {
			out = append(out, strings.TrimSpace(m[0]))
		}
	}
	return out
}

func stripTags(s string) string {
	re := regexp.MustCompile(`(?is)<[^>]+>`)
	return re.ReplaceAllString(s, "")
}

func compactHTML(s string) string {
	return truncateOneLine(strings.Join(strings.Fields(s), " "), 220)
}

func sortedKeys(m map[string][]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return ipLess(keys[i], keys[j]) })
	return keys
}

func ipLess(a, b string) bool {
	ia := net.ParseIP(a).To4()
	ib := net.ParseIP(b).To4()
	if ia == nil || ib == nil {
		return a < b
	}
	for i := 0; i < 4; i++ {
		if ia[i] != ib[i] {
			return ia[i] < ib[i]
		}
	}
	return false
}
