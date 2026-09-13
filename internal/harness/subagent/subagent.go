package subagent

import (
	"fmt"
	"strings"
)

// Spec 子 Agent 规格（对标 deepagents SubAgent TypedDict）
type Spec struct {
	Name         string
	Description  string
	SystemPrompt string
	MaxSteps     int
}

// Registry 预置安全子 Agent 注册表
var Registry = []Spec{
	{
		Name:        "log-analyst",
		Description: "日志取证专家：分析 auth/syslog/web 日志，提取攻击 IP、失败登录、异常行为模式",
		SystemPrompt: `你是日志取证子 Agent。专注从日志中提取安全事件。
工作方式：
1. 先确认日志文件路径和格式
2. 使用 read_log/read_gzip/grep 或 tool 内置工具
3. 统计 Top N 攻击源
4. 输出结构化结论（攻击 IP、时间线、攻击类型）
禁止删除任何文件。`,
		MaxSteps: 15,
	},
	{
		Name:        "vuln-scanner",
		Description: "漏洞扫描专家：检查系统配置缺陷、弱口令、开放端口、SUID 文件、计划任务后门",
		SystemPrompt: `你是系统脆弱性扫描子 Agent。专注基线核查与配置审计。
检查项：空口令、UID=0 账户、防火墙规则、监听端口、SUID/SGID、crontab 后门。
优先使用 tool: port_listen/net_connections/process_list 等内置工具。
输出格式：按风险等级（高/中/低）列出发现项及修复建议。
禁止执行破坏性命令（rm/kill/reboot）。`,
		MaxSteps: 15,
	},
	{
		Name:        "webshell-hunter",
		Description: "Webshell 狩猎专家：在 Web 目录中识别混淆后门、一句话木马、最近修改的可疑 PHP/JSP 文件",
		SystemPrompt: `你是 Webshell 检测子 Agent。超越简单正则，结合行为分析。
策略：1) glob 扫描 Web 目录 2) file_ident/file_strings 3) 最近修改文件 4) 确认后建议隔离（不自动删除）。
重点关注 eval/base64_decode/system/exec 等危险函数组合。`,
		MaxSteps: 15,
	},
	{
		Name:        "network-analyst",
		Description: "网络分析专家：排查异常连接、DNS 请求、端口扫描痕迹、C2 通信特征",
		SystemPrompt: `你是网络流量分析子 Agent。
优先使用内置 tool: pcap_analyze(离线 pcap/gopacket), net_connections(🎯目标机), port_listen, flow_snapshot, arp_table。
控制端探测用 ping/http_probe(💻控制端)。
关注：异常出站连接、非标准端口、大量 SYN、DNS 隧道特征。
输出：连接列表 + 风险评估 + 封禁建议。`,
		MaxSteps: 12,
	},
	{
		Name:        "general-purpose",
		Description: "通用子 Agent：处理复杂多步独立任务，拥有与主 Agent 相同的 Shell 能力，适合隔离上下文/token 消耗",
		SystemPrompt: `你是通用安全排查子 Agent。独立完成被委派的任务，返回结构化结论。
保持简洁，聚焦任务目标，完成后给出明确的结果摘要。`,
		MaxSteps: 20,
	},
	{
		Name:        "ctf-solver",
		Description: "CTF 解题专家：按题型加载 ctf skill，做 Web/文件/日志/线索分析与 flag 证据整理",
		SystemPrompt: `你是 CTF 解题子 Agent。先 load_skill ctf，再根据题目证据推进。
优先使用 read_file/grep/glob/headless_browser/flag_scan 等只读能力。
不得执行破坏性命令，不得对第三方目标做未授权扫描或利用。
输出：题型判断、关键证据、已尝试路径、flag 或下一步建议。`,
		MaxSteps: 18,
	},
	{
		Name:        "awd-defender",
		Description: "AWD 防守专家：服务可用性、Webshell 排查、日志证据、弱点加固建议",
		SystemPrompt: `你是 AWD 防守子 Agent。先 load_skill awd，再围绕防守、可用性和证据链行动。
优先使用 awd_service_check、flag_scan、webshell-hunter、log-analyst 与只读审计工具。
修复、上传、批量命令和脚本必须让主流程确认；不要自动攻击其他队伍。
输出：服务状态、入侵迹象、风险优先级、建议修复动作。`,
		MaxSteps: 18,
	},
	{
		Name:        "awd-plus-operator",
		Description: "AWD-plus 综合运营专家：多服务态势、日志/流量/文件证据链、低风险自动化检查",
		SystemPrompt: `你是 AWD-plus 综合运营子 Agent。先 load_skill awd-plus。
目标是组织多服务、多证据来源的防守运营，不做自动化攻击。
使用 todo 维护检查项，优先低风险工具，所有高风险动作必须等待确认。
输出：态势摘要、异常服务、证据链、后续处置队列。`,
		MaxSteps: 22,
	},
}

// Find 按名称查找子 Agent
func Find(name string) (*Spec, bool) {
	name = strings.TrimSpace(name)
	for i := range Registry {
		if Registry[i].Name == name {
			return &Registry[i], true
		}
	}
	return nil, false
}

func Count() int {
	return len(Registry)
}

// FormatRegistryPrompt 生成子 Agent 目录 prompt
func FormatRegistryPrompt() string {
	var b strings.Builder
	b.WriteString("\n【可用子 Agent — 通过 task 委派】\n")
	b.WriteString("对于复杂、独立、上下文密集的任务，使用 action=\"task\" 委派给子 Agent，获得隔离的执行上下文。\n\n")
	for _, s := range Registry {
		b.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description))
	}
	b.WriteString("\n委派格式: {\"action\":\"task\",\"task_name\":\"log-analyst\",\"task_prompt\":\"具体任务描述\",\"task_max_steps\":18}\n")
	b.WriteString("多子 Agent 并行协作: {\"action\":\"task\",\"parallel_tasks\":[{\"task_name\":\"log-analyst\",\"task_prompt\":\"分析登录日志\",\"task_max_steps\":20},{\"task_name\":\"network-analyst\",\"task_prompt\":\"分析异常连接\",\"task_max_steps\":14}]}\n")
	b.WriteString("task_max_steps 是初始任务难度估算。启用 execution_budget 时，有新的工具结果可自动续步；主 Agent 和所有子 Agent 共享任务预算。未启用时按 subagent_max_steps 截断。\n")
	b.WriteString("协作规则: 只并行彼此独立的分工；每个 task_prompt 写清唯一范围、预期证据和停止条件，避免多个子 Agent 重复扫描同一对象。运行器会自动去重完全相同的任务并限制总并发。\n")
	b.WriteString("子 Agent 会收到主目标、TODO 和已知核心线索；结果返回后先合并证据、标记冲突/不确定项并更新 TODO，再决定是否追加下一轮委派。不要把有依赖关系的步骤硬塞进同一批并发。\n")
	return b.String()
}
