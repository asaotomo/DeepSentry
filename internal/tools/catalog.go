package tools

import (
	"fmt"
	"sort"
	"strings"
)

// Get 按名称获取工具
func Get(name string) (*Tool, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	t, ok := Registry[name]
	if !ok || !t.Enabled {
		return nil, false
	}
	return t, true
}

// ListNames 返回所有工具名
func ListNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(Registry))
	for n, t := range Registry {
		if t.Enabled {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func CountEnabled() int {
	return len(ListNames())
}

// FormatCatalogPrompt 生成工具发现入口，避免每轮把全量工具参数塞给模型。
func FormatCatalogPrompt() string {
	names := ListNames()
	return fmt.Sprintf(`
【内置工具按需发现】
DeepSentry 内置 Go 原生工具支持热插拔，当前启用 %d 个。
不要为了流程感固定调用工具；优先用普通 shell/read/grep 完成简单只读排查。
只有当目标系统缺少常用命令、需要跨平台 /proc 解析、控制端网络探测、日志/取证辅助时，再按需调用工具。

发现工具:
{"action":"tool","tool_name":"tool_catalog","tool_args":{"name":"已知工具名，精确查看完整用法","category":"分类或all","query":"可选的空格分隔关键词"}}
准备调用工具但不确定 action、参数名、格式或流程时，必须先用 name 精确查询；不要猜参数。工具报错返回的用法就是下一次调用的权威依据。

已启用工具名概览: %s
`, len(names), joinNames(names))
}

// FormatCompactCatalogPrompt keeps the discovery workflow but omits the full
// full tool name list for small local models. Exact schemas arrive on demand.
func FormatCompactCatalogPrompt() string {
	return `
【内置工具 — 按需发现】
简单排查优先 Shell。需要专用工具时先调用:
{"action":"tool","tool_name":"tool_catalog","tool_args":{"name":"已知工具名"}}
不知道名称时使用 category/query 搜索。严格复制目录返回的参数、action 和示例，不要猜字段。
常用入口: config_manage、fleet_inventory、fleet_exec、fleet_file、target_health_summary、read_log。
`
}

// FormatCatalogDetail 生成按分类/关键词过滤后的详细工具目录。
func FormatCatalogDetail(category, query string) string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	category = strings.TrimSpace(category)
	query = strings.ToLower(strings.TrimSpace(query))
	searchQuery := NewSearchQuery(query)
	byCat := make(map[string][]*Tool)
	scores := make(map[string]int)
	var matched []*Tool
	catOrder := []string{"网络连通", "连接审计", "系统应急", "取证分析", "文档解析", "端口扫描", "内网发现", "Web探测", "浏览器控制", "抓包分析", "协议探测", "系统关联", "协议指纹", "数据库探测", "数据库取证", "配置取证", "日志取证", "脚本执行", "文件传输", "代理转发", "自动化任务", "配置管理", "批量运维", "比赛辅助"}

	for _, t := range Registry {
		if !t.Enabled {
			continue
		}
		if category != "" && category != "all" && t.Category != category {
			continue
		}
		if query != "" {
			score := SearchRelevanceForQuery(t, searchQuery)
			if score <= 0 {
				continue
			}
			scores[t.Name] = score
		}
		matched = append(matched, t)
	}
	sort.Slice(matched, func(i, j int) bool {
		if scores[matched[i].Name] != scores[matched[j].Name] {
			return scores[matched[i].Name] > scores[matched[j].Name]
		}
		return matched[i].Name < matched[j].Name
	})
	if query != "" && len(matched) > 12 {
		matched = matched[:12]
	}
	for _, item := range matched {
		byCat[item.Category] = append(byCat[item.Category], item)
	}
	for cat := range byCat {
		sort.Slice(byCat[cat], func(i, j int) bool {
			if scores[byCat[cat][i].Name] != scores[byCat[cat][j].Name] {
				return scores[byCat[cat][i].Name] > scores[byCat[cat][j].Name]
			}
			return byCat[cat][i].Name < byCat[cat][j].Name
		})
	}

	var b strings.Builder
	b.WriteString("【Go 原生内置工具目录 — 按需加载】\n")
	b.WriteString("DeepSentry 内置实现，**不依赖**目标系统的 nmap/ping/ss/tcpdump/ps/strings/file/zcat 等命令。\n")
	b.WriteString("**视角说明**: 🎯=目标机数据(SFTP//proc) | 💻=控制端发起探测\n")
	b.WriteString("简单 shell 能完成时不要调用工具；目标机缺命令或需要跨平台结构化信息时再选择。\n")
	b.WriteString("格式: {\"action\":\"tool\",\"tool_name\":\"net_connections\",\"tool_args\":{\"filter\":\"established\"}}\n\n")

	if len(byCat) == 0 {
		return "未找到匹配的已启用工具。可用 category=all 查看全部。"
	}
	writeTool := func(t *Tool) {
		riskIcon := "🟢"
		switch t.RiskLevel {
		case RiskMedium:
			riskIcon = "🟡"
		case RiskHigh:
			riskIcon = "🔴"
		}
		persIcon := "🎯"
		persLabel := "目标机"
		if t.Perspective == PerspectiveController {
			persIcon = "💻"
			persLabel = "控制端"
		}
		b.WriteString(fmt.Sprintf("- %s %s**%s** [%s]: %s\n", riskIcon, persIcon, t.Name, persLabel, t.Description))
		if help := FormatToolHelp(t.Name); help != "" {
			for _, line := range strings.Split(help, "\n") {
				b.WriteString("  " + line + "\n")
			}
		} else {
			b.WriteString("  参数: 无参数\n")
		}
	}
	if query != "" {
		b.WriteString("### 按相关性排序的候选\n")
		b.WriteString("下一步直接调用最匹配的具体工具；不得再次调用 tool_catalog，也不要用 agent_action 包装已展示的原生工具。\n")
		for _, item := range matched {
			writeTool(item)
		}
		return b.String()
	}
	for _, cat := range catOrder {
		tools, ok := byCat[cat]
		if !ok {
			continue
		}
		b.WriteString(fmt.Sprintf("### %s\n", cat))
		for _, t := range tools {
			writeTool(t)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// FormatFullCatalogPrompt 保留给测试/文档，返回全部启用工具详情。
func FormatFullCatalogPrompt() string {
	return FormatCatalogDetail("all", "")
}
