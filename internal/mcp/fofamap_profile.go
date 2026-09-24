package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FofaMapToolProfile is DeepSentry's local contract for FofaMap v2.0.1.
// MCP annotations are useful diagnostics, but only this versioned allow-list
// enables lower-risk routing for a server that can query external FOFA data or
// start an active Nuclei scan.
type FofaMapToolProfile struct {
	Category string
	Priority int
}

var fofaMapToolProfiles = map[string]FofaMapToolProfile{
	"fofa_validate_query": {"validation", 1000},
	"fofa_fields":         {"catalog", 970},
	"fofa_syntax":         {"catalog", 880},
	"fofa_rules":          {"catalog", 990},
	"fofa_job_status":     {"jobs", 720},
	"fofa_account":        {"account", 960},
	"fofa_search":         {"search", 940},
	"fofa_search_next":    {"search", 920},
	"fofa_icon_search":    {"search", 760},
	"fofa_host_profile":   {"search", 780},
	"fofa_stats":          {"analysis", 740},
	"fofa_agent_run":      {"agent", 700},
	"fofa_export":         {"export", 680},
	"nuclei_plan":         {"active", 620},
	"nuclei_execute":      {"active", 580},
}

func fofaMapOriginalName(tool *ExternalTool) string {
	if tool == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(firstNonEmptyMCP(tool.OriginalName, tool.Name)))
}

// FofaMapKnownToolNames returns a stable copy for contract tests and operator
// diagnostics.
func FofaMapKnownToolNames() []string {
	names := make([]string, 0, len(fofaMapToolProfiles))
	for name := range fofaMapToolProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func FofaMapToolProfileForName(name string) (FofaMapToolProfile, bool) {
	profile, ok := fofaMapToolProfiles[strings.ToLower(strings.TrimSpace(name))]
	return profile, ok
}

func explicitFofaMapIdentity(tool *ExternalTool) bool {
	if tool == nil {
		return false
	}
	server := strings.ToLower(strings.TrimSpace(tool.Server))
	return strings.Contains(server, "fofamap") || strings.Contains(server, "fofa-map")
}

func fofaMapServerSignatures(tools []*ExternalTool) map[string]bool {
	signatures := make(map[string]map[string]bool)
	trusted := make(map[string]bool)
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		server := strings.ToLower(strings.TrimSpace(tool.Server))
		if server == "" {
			continue
		}
		if explicitFofaMapIdentity(tool) {
			trusted[server] = true
		}
		name := fofaMapOriginalName(tool)
		if !strings.HasPrefix(name, "fofa_") {
			continue
		}
		if _, known := FofaMapToolProfileForName(name); !known {
			continue
		}
		if signatures[server] == nil {
			signatures[server] = make(map[string]bool)
		}
		signatures[server][name] = true
	}
	// Three exact fofa_* tools form a strong capability signature while
	// avoiding accidental trust of an unrelated server exposing nuclei_plan.
	for server, names := range signatures {
		if len(names) >= 3 {
			trusted[server] = true
		}
	}
	return trusted
}

// HasFofaMapTools reports whether the global MCP registry currently exposes
// the FofaMap v2.0.1 contract. Agent routing uses this to prefer MCP tools
// over ClawHub's python fofa_recon.py playbook.
func HasFofaMapTools() bool {
	return Global().HasFofaMapTools()
}

func (r *Registry) HasFofaMapTools() bool {
	if r == nil {
		return false
	}
	tools := r.ListTools()
	for i := range tools {
		if IsFofaMapTool(&tools[i], tools) {
			return true
		}
	}
	return false
}

// IsFofaMapTool recognizes the v2.0.1 contract even when the user gives the
// MCP server a custom alias. Generic Nuclei MCP servers are deliberately not
// treated as FofaMap unless the same server exposes a FofaMap signature.
func IsFofaMapTool(tool *ExternalTool, all []ExternalTool) bool {
	if tool == nil {
		return false
	}
	if _, known := FofaMapToolProfileForName(fofaMapOriginalName(tool)); !known {
		return false
	}
	if explicitFofaMapIdentity(tool) {
		return true
	}
	pointers := make([]*ExternalTool, 0, len(all))
	for i := range all {
		pointers = append(pointers, &all[i])
	}
	return fofaMapServerSignatures(pointers)[strings.ToLower(strings.TrimSpace(tool.Server))]
}

func isFofaMapExternalToolInSet(tool *ExternalTool, all map[string]*ExternalTool) bool {
	if explicitFofaMapIdentity(tool) {
		_, known := FofaMapToolProfileForName(fofaMapOriginalName(tool))
		return known
	}
	items := make([]*ExternalTool, 0, len(all))
	for _, item := range all {
		items = append(items, item)
	}
	return IsFofaMapTool(tool, dereferenceExternalTools(items))
}

func fofaMapToolPromptPriorityInSet(tool *ExternalTool, all map[string]*ExternalTool) int {
	if all != nil {
		if !isFofaMapExternalToolInSet(tool, all) {
			return 0
		}
	} else if !IsFofaMapTool(tool, nil) {
		return 0
	}
	if profile, ok := FofaMapToolProfileForName(fofaMapOriginalName(tool)); ok {
		return profile.Priority
	}
	return 100
}

// FofaMapToolRisk maps the actual FofaMap action to DeepSentry's local safety
// policy. Unknown variants fail closed even when a server annotation claims
// they are read-only.
func FofaMapToolRisk(tool *ExternalTool, _ map[string]string) (risk, reason string, ok bool) {
	if tool == nil {
		return "", "", false
	}
	name := fofaMapOriginalName(tool)
	if _, known := FofaMapToolProfileForName(name); !known {
		return "", "", false
	}
	switch name {
	case "fofa_validate_query", "fofa_fields", "fofa_syntax", "fofa_rules", "fofa_job_status",
		"fofa_account", "fofa_search", "fofa_search_next", "fofa_icon_search",
		"fofa_host_profile", "fofa_stats", "fofa_agent_run":
		return MCPRiskLow, fmt.Sprintf("FofaMap %s 只读取本地规则、账户信息或 FOFA 查询结果", name), true
	case "fofa_export":
		return MCPRiskMedium, "FofaMap export 会在控制端写入查询结果文件", true
	case "nuclei_plan":
		return MCPRiskMedium, "FofaMap nuclei_plan 仅生成受范围约束的扫描计划和一次性确认令牌，不执行扫描", true
	case "nuclei_execute":
		return MCPRiskHigh, "FofaMap nuclei_execute 会对用户已授权目标发起主动 Nuclei 扫描", true
	default:
		return MCPRiskHigh, fmt.Sprintf("FofaMap %s 未配置本地动作策略（readOnlyHint=%t）", name, tool.Annotations.ReadOnlyHint), true
	}
}

// FofaMapToolTimeout keeps long-running agent/scan calls from being cancelled
// by the generic 60-second MCP deadline. Explicit operator configuration wins.
func FofaMapToolTimeout(name string, configured time.Duration, explicitlyConfigured bool) time.Duration {
	if explicitlyConfigured {
		return configured
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "nuclei_execute":
		return 10 * time.Minute
	case "fofa_agent_run":
		return 5 * time.Minute
	case "fofa_search", "fofa_search_next", "fofa_export", "fofa_icon_search", "fofa_host_profile", "fofa_stats":
		return 2 * time.Minute
	default:
		return configured
	}
}

func FofaMapToolDescriptionOverlay(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fofa_account":
		return "DeepSentry: 查询前先调用。看 vip_level/额度。注册用户无 Host API；个人/教育账户无 stats API。"
	case "fofa_fields":
		return "DeepSentry: 不耗额度。按账户等级选择最小字段集再 search。"
	case "fofa_rules":
		return "DeepSentry: 用户点名产品/OA/VPN/CMS 时必须先调用，原样使用返回的 query。空 keyword 列出内置目录。禁止编造 app=。"
	case "fofa_validate_query":
		return "DeepSentry: 每个耗额度查询前先本地校验。失败则先修语法，不要直接 search。"
	case "fofa_syntax":
		return "DeepSentry: 官方运算符和字段附录，不耗额度。"
	case "fofa_search":
		return "DeepSentry: 一页只读搜索。产品名先走 fofa_rules。fields 用 JSON 数组或逗号分隔。翻页把 next_cursor 交给 fofa_search_next。"
	case "fofa_search_next":
		return "DeepSentry: cursor 必须是上一页原样 next_cursor（参数名 cursor；传 next_cursor 也会自动映射）。query 与 fields 与上一页保持一致。"
	case "fofa_icon_search":
		return "DeepSentry: 对公开网站 favicon 取 hash 再搜。不要对内网或需登录页使用。"
	case "fofa_host_profile":
		return "DeepSentry: 单主机聚合。先看 fofa_account 是否有 Host API；注册用户不要调用。"
	case "fofa_stats":
		return "DeepSentry: 聚合统计。个人/教育账户没有该 API。size 是 Top-N，默认 5。"
	case "fofa_export":
		return "DeepSentry: schema 需要 request.search.query。可直接传 request JSON，也可扁平传 query/fields/format；会自动包进 request。写本地文件，用 job_status 取路径。"
	case "fofa_agent_run":
		return "DeepSentry: 宽泛中英文测绘意图。会查 app= 指纹。组织官网结果保留 website_candidates 的 corroborated/observed/candidate，不要当成已确认归属。不扫描。"
	case "fofa_job_status":
		return "DeepSentry: 读导出/agent/扫描任务状态和产物路径。也可读 Resource fofamap://jobs/{job_id}。"
	case "nuclei_plan":
		return "DeepSentry: 仅当用户明确授权主动扫描时使用。schema 需要 request.targets。可扁平传 targets（逗号或 JSON 数组）。返回一次性令牌，不得伪造。"
	case "nuclei_execute":
		return "DeepSentry: 必须原样传递 nuclei_plan 的 plan_id 与 approval_token。不得扩大范围或复用令牌。"
	default:
		return ""
	}
}

func applyFofaMapCallDefaults(name string, args map[string]string) map[string]string {
	out := make(map[string]string, len(args)+2)
	for key, value := range args {
		out[key] = value
	}
	normalizeFofaMapListArg(out, "fields")
	normalizeFofaMapListArg(out, "targets")
	normalizeFofaMapListArg(out, "templates")
	normalizeFofaMapListArg(out, "template_ids")
	normalizeFofaMapListArg(out, "severities")
	normalizeFofaMapListArg(out, "dedupe_by")
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fofa_search_next":
		if strings.TrimSpace(out["cursor"]) == "" {
			if cursor := firstNonEmptyMCP(out["next_cursor"], out["cursor"]); cursor != "" {
				out["cursor"] = cursor
			}
		}
		delete(out, "next_cursor")
	case "fofa_export":
		if size := strings.TrimSpace(out["size"]); size != "" {
			if strings.TrimSpace(out["max_records"]) == "" {
				out["max_records"] = size
			}
			if strings.TrimSpace(out["max_pages"]) == "" {
				out["max_pages"] = "1"
			}
		}
		wrapFofaMapNestedRequest(out, []string{
			"query", "fields", "size", "full", "cursor", "continuous", "page", "max_records", "max_pages", "cost_budget", "dedupe_by",
		}, []string{"format", "filename"}, "search")
	case "nuclei_plan":
		wrapFofaMapNestedRequest(out, []string{
			"targets", "templates", "template_ids", "severities", "all_templates", "all_severities", "ttl_seconds",
		}, nil, "")
	}
	return out
}

func normalizeFofaMapListArg(args map[string]string, key string) {
	raw := strings.TrimSpace(args[key])
	if raw == "" || strings.HasPrefix(raw, "[") {
		return
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	if len(values) == 0 {
		return
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return
	}
	args[key] = string(encoded)
}

func wrapFofaMapNestedRequest(args map[string]string, innerKeys, outerKeys []string, nestedKey string) {
	if strings.TrimSpace(args["request"]) != "" {
		return
	}
	inner := make(map[string]any, len(innerKeys))
	for _, key := range innerKeys {
		raw := strings.TrimSpace(args[key])
		if raw == "" {
			continue
		}
		inner[key] = fofaMapTypedArg(key, raw)
		delete(args, key)
	}
	if len(inner) == 0 {
		return
	}
	request := any(inner)
	if nestedKey != "" {
		wrapped := map[string]any{nestedKey: inner}
		for _, key := range outerKeys {
			raw := strings.TrimSpace(args[key])
			if raw == "" {
				continue
			}
			wrapped[key] = fofaMapTypedArg(key, raw)
			delete(args, key)
		}
		request = wrapped
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return
	}
	args["request"] = string(encoded)
}

func fofaMapTypedArg(key, raw string) any {
	switch key {
	case "fields", "targets", "templates", "template_ids", "severities", "dedupe_by":
		var values []any
		if json.Unmarshal([]byte(raw), &values) == nil {
			return values
		}
	case "size", "page", "max_records", "max_pages", "cost_budget", "ttl_seconds":
		if value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
			return value
		}
	case "full", "detail", "continuous", "all_templates", "all_severities":
		if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			return value
		}
	}
	return raw
}
