//go:build live

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestLiveFofaMap201ViaDeepSentryClient(t *testing.T) {
	root := repoRoot(t)
	cfgPath := filepath.Join(root, "build", "config_live_eval.yaml")
	if err := config.InitConfig(cfgPath); err != nil {
		t.Fatalf("load config: %v", err)
	}
	var fofa config.MCPServerConfig
	for _, spec := range config.GlobalConfig.MCPServerConfigs {
		if strings.EqualFold(strings.TrimSpace(spec.Name), "fofamap") {
			fofa = spec
			break
		}
	}
	if strings.TrimSpace(fofa.Command) == "" {
		t.Fatal("config_live_eval.yaml 没有 fofamap mcp_server_configs")
	}

	t.Cleanup(CloseAll)
	if err := Connect(ServerConfig{
		Name:              fofa.Name,
		Type:              fofa.Type,
		Command:           fofa.Command,
		Args:              fofa.Args,
		Env:               fofa.Env,
		CWD:               fofa.CWD,
		StartupTimeoutSec: fofa.StartupTimeoutSec,
		ToolTimeoutSec:    fofa.ToolTimeoutSec,
	}); err != nil {
		t.Fatalf("DeepSentry Connect FofaMap MCP: %v", err)
	}

	tools := Global().ListTools()
	got := map[string]bool{}
	for _, tool := range tools {
		if IsFofaMapTool(&tool, tools) {
			got[fofaMapOriginalName(&tool)] = true
			t.Logf("tool %s orig=%s risk_profile=%v", tool.Name, tool.OriginalName, mustProfile(tool.OriginalName))
		}
	}
	want := FofaMapKnownToolNames()
	if len(got) != len(want) {
		t.Errorf("discovered %d FofaMap tools, want %d: %v", len(got), len(want), keys(got))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	if !HasFofaMapTools() {
		t.Fatal("HasFofaMapTools()=false after connect")
	}

	resources := ListResources("fofamap")
	uris := make([]string, 0, len(resources))
	for _, resource := range resources {
		uris = append(uris, resource.URI)
	}
	t.Logf("resources=%v", uris)
	for _, uri := range []string{"fofamap://account", "fofamap://fields", "fofamap://rules"} {
		body, err := ReadResource("fofamap", uri)
		if err != nil {
			t.Errorf("read %s: %v", uri, err)
			continue
		}
		t.Logf("resource %s ok bytes=%d", uri, len(body))
	}

	call := func(name string, args map[string]string) string {
		t.Helper()
		start := time.Now()
		out, err := Global().Run(name, args)
		elapsed := time.Since(start)
		if err != nil {
			t.Errorf("%s failed in %s: %v\n%s", name, elapsed, err, redact(out))
			return ""
		}
		if looksLikeMCPError(out) {
			t.Logf("%s returned error-shaped result in %s: %s", name, elapsed, summarize(out))
		} else {
			t.Logf("%s ok in %s: %s", name, elapsed, summarize(out))
		}
		return out
	}

	account := call("fofamap__fofa_account", nil)
	if account == "" || !strings.Contains(account, "vip_level") && !strings.Contains(account, "FOFA account") {
		t.Errorf("fofa_account did not look like an account payload")
	}
	call("fofamap__fofa_fields", nil)
	call("fofamap__fofa_syntax", nil)
	rules := call("fofamap__fofa_rules", map[string]string{"keyword": "nginx"})
	query := firstJSONString(rules, "query")
	if query == "" {
		query = `domain="example.com"`
	}
	call("fofamap__fofa_validate_query", map[string]string{"query": query})
	call("fofamap__fofa_validate_query", map[string]string{"query": "this is not valid fofa"})

	search := call("fofamap__fofa_search", map[string]string{
		"query":  `domain="example.com"`,
		"fields": "host,ip,port",
		"size":   "3",
	})
	if cursor := firstJSONString(search, "next_cursor"); cursor != "" {
		next := call("fofamap__fofa_search_next", map[string]string{
			"query":       `domain="example.com"`,
			"next_cursor": cursor,
			"fields":      "host,ip,port",
			"size":        "3",
		})
		if next == "" {
			t.Error("next_cursor alias did not produce a search_next result")
		}
	} else {
		t.Log("search had no next_cursor; skip search_next")
	}

	call("fofamap__fofa_icon_search", map[string]string{
		"url":  "https://example.com",
		"size": "2",
	})
	call("fofamap__fofa_host_profile", map[string]string{"host": "example.com", "detail": "false"})
	call("fofamap__fofa_stats", map[string]string{
		"query":  `domain="example.com"`,
		"fields": "country,port",
		"size":   "3",
	})

	exportOut := call("fofamap__fofa_export", map[string]string{
		"query":  `domain="example.com"`,
		"fields": "host,ip,port",
		"format": "jsonl",
		"size":   "5",
	})
	if jobID := firstJSONString(exportOut, "id"); jobID != "" {
		status := call("fofamap__fofa_job_status", map[string]string{"job_id": jobID})
		if status == "" {
			t.Error("fofa_job_status failed for export job")
		}
		jobURI := "fofamap://jobs/" + jobID
		if body, err := ReadResource("fofamap", jobURI); err != nil {
			t.Logf("resource %s: %v", jobURI, err)
		} else {
			t.Logf("resource %s ok bytes=%d", jobURI, len(body))
		}
	}

	agentOut := call("fofamap__fofa_agent_run", map[string]string{
		"intent":      "查找 example.com 官网候选，不要扫描",
		"max_records": "3",
		"max_pages":   "1",
	})
	if strings.Contains(strings.ToLower(agentOut), "fofamap init") {
		t.Log("fofa_agent_run needs local planner model; MCP call itself succeeded with a capability error")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("go.mod not found from %s", wd)
	return ""
}

func mustProfile(name string) string {
	profile, ok := FofaMapToolProfileForName(name)
	if !ok {
		return "unknown"
	}
	return profile.Category
}

func keys(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	return out
}

func looksLikeMCPError(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "mcp 工具返回可修正错误") ||
		strings.Contains(lower, "\"error\": true") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "permission") ||
		strings.Contains(lower, "not available") ||
		strings.Contains(lower, "fofamap init")
}

func firstJSONString(raw, key string) string {
	var payload any
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &payload); err != nil {
		re := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*"([^"]+)"`)
		if match := re.FindStringSubmatch(raw); len(match) == 2 {
			return match[1]
		}
		return ""
	}
	return findStringKey(payload, key)
}

func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return raw
	}
	return raw[start : end+1]
}

func findStringKey(node any, key string) string {
	switch typed := node.(type) {
	case map[string]any:
		if value, ok := typed[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" && text != "null" {
				return text
			}
		}
		for _, child := range typed {
			if found := findStringKey(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findStringKey(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func summarize(out string) string {
	text := redact(out)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 400 {
		return text[:400] + "…"
	}
	return text
}

func redact(out string) string {
	text := out
	text = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`).ReplaceAllString(text, "[redacted-email]")
	text = regexp.MustCompile(`(?i)(api[_-]?key|approval_token|token)("?\s*[:=]\s*"?)[^"\s,]+`).ReplaceAllString(text, `${1}${2}[redacted]`)
	return text
}
