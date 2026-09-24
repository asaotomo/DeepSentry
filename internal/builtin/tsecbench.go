package builtin

import (
	"ai-edr/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type tsecChallenge struct {
	UniqueCode       string   `json:"unique_code"`
	Description      string   `json:"description"`
	Difficulty       string   `json:"difficulty"`
	Level            int      `json:"level"`
	TotalScore       int      `json:"total_score"`
	FlagCount        int      `json:"flag_count"`
	CorrectFlagCount int      `json:"correct_flag_count"`
	IsCompleted      bool     `json:"is_completed"`
	ContainerStatus  string   `json:"container_status"`
	ContainerAddr    []string `json:"container_addr"`
}

type tsecAPIError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Detail  map[string]interface{} `json:"detail"`
}

// TSecBench wraps the official challenge API. It never prints the token.
func TSecBench(rt Runtime, args map[string]string) (string, error) {
	action := strings.ToLower(arg(args, "action"))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "status", "check":
		return tsecList(rt, args)
	case "start":
		return tsecStart(rt, args)
	case "hint":
		return tsecHint(rt, args)
	case "submit":
		return tsecSubmit(rt, args)
	case "close":
		return tsecClose(rt, args)
	case "probe":
		return tsecProbe(rt, arg(args, "addr", "url", "target"), argInt(args, "timeout", 8, 30))
	default:
		return "", fmt.Errorf("未知 TSecBench action: %s，可用: list|status|check|start|hint|submit|close|probe", action)
	}
}

func tsecList(rt Runtime, args map[string]string) (string, error) {
	body, status, err := tsecRequest("GET", "/openapi/v1/challenges", nil, nil, args)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", tsecHTTPError(status, body)
	}
	if argBool(args, "raw") {
		return fmt.Sprintf("%s TSecBench list\nHTTP: %d\n%s", rt.tag(), status, truncate(string(body), 20000)), nil
	}
	var challenges []tsecChallenge
	if err := json.Unmarshal(body, &challenges); err != nil {
		return "", fmt.Errorf("解析题目列表失败: %w", err)
	}
	codeFilter := arg(args, "unique_code", "code")
	statusFilter := strings.ToLower(arg(args, "status", "container_status"))
	difficultyFilter := strings.ToLower(arg(args, "difficulty"))
	var rows []tsecChallenge
	for _, c := range challenges {
		if codeFilter != "" && c.UniqueCode != codeFilter {
			continue
		}
		if statusFilter != "" && strings.ToLower(c.ContainerStatus) != statusFilter {
			continue
		}
		if difficultyFilter != "" && strings.ToLower(c.Difficulty) != difficultyFilter {
			continue
		}
		rows = append(rows, c)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UniqueCode < rows[j].UniqueCode })

	active, completed := 0, 0
	for _, c := range challenges {
		if c.ContainerStatus == "available" || c.ContainerStatus == "pending" {
			active++
		}
		if c.IsCompleted {
			completed++
		}
	}
	limit := argInt(args, "limit", 20, 200)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s TSecBench challenges\nHTTP: %d  total=%d  matched=%d  completed=%d  active=%d\n", rt.tag(), status, len(challenges), len(rows), completed, active))
	b.WriteString("Config: base_url=set token=set(redacted)\n\n")
	for i, c := range rows {
		if i >= limit {
			b.WriteString(fmt.Sprintf("... %d more rows omitted; pass limit=200 or unique_code=<code>\n", len(rows)-limit))
			break
		}
		addrs := "-"
		if len(c.ContainerAddr) > 0 {
			addrs = strings.Join(c.ContainerAddr, ",")
		}
		b.WriteString(fmt.Sprintf("- %s [%s] score=%d flags=%d/%d status=%s addr=%s\n", c.UniqueCode, c.Difficulty, c.TotalScore, c.CorrectFlagCount, c.FlagCount, c.ContainerStatus, addrs))
		if codeFilter != "" && c.Description != "" {
			b.WriteString("  description: " + c.Description + "\n")
		}
	}
	return b.String(), nil
}

func tsecStart(rt Runtime, args map[string]string) (string, error) {
	code, err := requireTsecCode(args)
	if err != nil {
		return "", err
	}
	body, status, err := tsecRequest("POST", "/openapi/v1/challenges/start", map[string]string{"unique_code": code}, nil, args)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", tsecHTTPError(status, body)
	}
	var resp struct {
		UniqueCode     string   `json:"unique_code"`
		ContainerAddr  []string `json:"container_addr"`
		ContainerAddrs []string `json:"container_addrs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("解析 start 响应失败: %w", err)
	}
	addrs := resp.ContainerAddr
	if len(addrs) == 0 {
		addrs = resp.ContainerAddrs
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s TSecBench start\nHTTP: %d  unique_code=%s\n", rt.tag(), status, resp.UniqueCode))
	b.WriteString("container_addr: " + strings.Join(addrs, ", ") + "\n")
	if argBool(args, "probe") {
		for _, addr := range addrs {
			out, err := tsecProbe(rt, addr, argInt(args, "timeout", 8, 30))
			if err != nil {
				b.WriteString(fmt.Sprintf("\nProbe %s failed: %v\n", addr, err))
			} else {
				b.WriteString("\n" + out + "\n")
			}
		}
	}
	return b.String(), nil
}

func tsecHint(rt Runtime, args map[string]string) (string, error) {
	code, err := requireTsecCode(args)
	if err != nil {
		return "", err
	}
	body, status, err := tsecRequest("GET", "/openapi/v1/challenges/hint", map[string]string{"unique_code": code}, nil, args)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", tsecHTTPError(status, body)
	}
	var resp struct {
		UniqueCode string  `json:"unique_code"`
		Hint       *string `json:"hint"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("解析 hint 响应失败: %w", err)
	}
	hint := "<nil>"
	if resp.Hint != nil {
		hint = *resp.Hint
	}
	return fmt.Sprintf("%s TSecBench hint\nHTTP: %d  unique_code=%s\n注意: 获取 hint 可能扣分。\nhint: %s\n", rt.tag(), status, resp.UniqueCode, hint), nil
}

func tsecSubmit(rt Runtime, args map[string]string) (string, error) {
	code, err := requireTsecCode(args)
	if err != nil {
		return "", err
	}
	flag := arg(args, "flag")
	if flag == "" {
		return "", fmt.Errorf("flag 不能为空")
	}
	payload := map[string]string{"unique_code": code, "flag": flag}
	body, status, err := tsecRequest("POST", "/openapi/v1/challenges/submit", nil, payload, args)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", tsecHTTPError(status, body)
	}
	var resp struct {
		Correct          bool `json:"correct"`
		Awarded          int  `json:"awarded"`
		CumulativeScore  int  `json:"cumulative_score"`
		CorrectFlagCount int  `json:"correct_flag_count"`
		TotalFlagCount   int  `json:"total_flag_count"`
		MatchedFlagIndex *int `json:"matched_flag_index"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("解析 submit 响应失败: %w", err)
	}
	idx := "null"
	if resp.MatchedFlagIndex != nil {
		idx = fmt.Sprintf("%d", *resp.MatchedFlagIndex)
	}
	return fmt.Sprintf("%s TSecBench submit\nHTTP: %d  unique_code=%s\ncorrect=%v awarded=%d cumulative_score=%d flags=%d/%d matched_flag_index=%s\nflag: %s\n", rt.tag(), status, code, resp.Correct, resp.Awarded, resp.CumulativeScore, resp.CorrectFlagCount, resp.TotalFlagCount, idx, flag), nil
}

func tsecClose(rt Runtime, args map[string]string) (string, error) {
	code, err := requireTsecCode(args)
	if err != nil {
		return "", err
	}
	body, status, err := tsecRequest("POST", "/openapi/v1/challenges/close", map[string]string{"unique_code": code}, nil, args)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", tsecHTTPError(status, body)
	}
	var resp struct {
		UniqueCode string `json:"unique_code"`
		Closed     bool   `json:"closed"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("解析 close 响应失败: %w", err)
	}
	return fmt.Sprintf("%s TSecBench close\nHTTP: %d  unique_code=%s closed=%v\n", rt.tag(), status, resp.UniqueCode, resp.Closed), nil
}

func tsecProbe(rt Runtime, addr string, timeoutSec int) (string, error) {
	raw := strings.TrimSpace(addr)
	if raw == "" {
		return "", fmt.Errorf("addr/url 不能为空")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	if timeoutSec <= 0 {
		timeoutSec = 8
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DeepSentry-TSecBench/1.0")
	resp, err := config.HTTPClient(time.Duration(timeoutSec) * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Sprintf("%s TSecBench probe\nURL: %s\nStatus: %s\nServer: %s\nBody(first %d bytes):\n%s", rt.tag(), raw, resp.Status, resp.Header.Get("Server"), len(body), sanitizeBody(string(body))), nil
}

func tsecRequest(method, path string, query map[string]string, payload interface{}, args map[string]string) ([]byte, int, error) {
	base, token, err := tsecConfig(args)
	if err != nil {
		return nil, 0, err
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + path)
	if err != nil {
		return nil, 0, err
	}
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("BENCHMARK_TOKEN", token)
	req.Header.Set("User-Agent", "DeepSentry-TSecBench/1.0")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := config.HTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return data, resp.StatusCode, nil
}

func tsecConfig(args map[string]string) (string, string, error) {
	base := arg(args, "base_url", "benchmark_base_url", "BENCHMARK_BASE_URL")
	token := arg(args, "token", "benchmark_token", "BENCHMARK_TOKEN")
	if base == "" {
		base = strings.TrimSpace(config.GlobalConfig.BenchmarkBaseURL)
	}
	if token == "" {
		token = strings.TrimSpace(config.GlobalConfig.BenchmarkToken)
	}
	if base == "" {
		base = strings.TrimSpace(os.Getenv("BENCHMARK_BASE_URL"))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("BENCHMARK_TOKEN"))
	}
	if base == "" {
		base = strings.TrimSpace(os.Getenv("DEEPSENTRY_BENCHMARK_BASE_URL"))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("DEEPSENTRY_BENCHMARK_TOKEN"))
	}
	if base == "" {
		return "", "", fmt.Errorf("未配置 benchmark_base_url / BENCHMARK_BASE_URL")
	}
	if token == "" {
		return "", "", fmt.Errorf("未配置 benchmark_token / BENCHMARK_TOKEN")
	}
	if _, err := url.ParseRequestURI(base); err != nil {
		return "", "", fmt.Errorf("benchmark_base_url 非法: %w", err)
	}
	return base, token, nil
}

func requireTsecCode(args map[string]string) (string, error) {
	code := arg(args, "unique_code", "uniqueCode", "code", "challenge", "challenge_id", "challenge_code", "id")
	if code == "" {
		return "", fmt.Errorf("unique_code 不能为空")
	}
	return code, nil
}

func tsecHTTPError(status int, body []byte) error {
	var apiErr tsecAPIError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Code != "" {
		return fmt.Errorf("TSecBench API error HTTP %d code=%s message=%s", status, apiErr.Code, apiErr.Message)
	}
	return fmt.Errorf("TSecBench API error HTTP %d body=%s", status, truncate(sanitizeBody(string(body)), 2000))
}
