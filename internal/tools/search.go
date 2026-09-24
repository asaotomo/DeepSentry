package tools

import (
	"strings"
	"sync"
	"unicode"
)

// SearchQuery keeps query tokenization out of per-tool scoring loops.
type SearchQuery struct {
	text   string
	tokens []string
}

func NewSearchQuery(query string) SearchQuery {
	text := strings.ToLower(strings.TrimSpace(query))
	if text == "" {
		return SearchQuery{}
	}
	return SearchQuery{text: text, tokens: searchTokens(text)}
}

type searchDocument struct {
	name, category, description, argsHint string
	tokens                                map[string]bool
}

var searchDocuments sync.Map // map[*Tool]searchDocument

func documentTokens(tool *Tool) map[string]bool {
	if value, ok := searchDocuments.Load(tool); ok {
		cached := value.(searchDocument)
		if cached.name == tool.Name && cached.category == tool.Category &&
			cached.description == tool.Description && cached.argsHint == tool.ArgsHint {
			return cached.tokens
		}
	}
	aliases := strings.Join(toolSearchAliases[tool.Name], " ")
	haystack := strings.ToLower(tool.Name + " " + tool.Category + " " + tool.Description + " " + tool.ArgsHint + " " + aliases)
	entry := searchDocument{
		name: tool.Name, category: tool.Category, description: tool.Description,
		argsHint: tool.ArgsHint, tokens: searchTokenSet(haystack),
	}
	searchDocuments.Store(tool, entry)
	return entry.tokens
}

// SearchRelevance is the shared lexical retrieval contract used both by the
// Runtime v3 deferred schema selector and tool_catalog. Sharing it prevents a
// tool from being discoverable in the first turn but disappearing when the
// model explicitly searches the catalog on the next turn.
func SearchRelevance(tool *Tool, query string) int {
	return SearchRelevanceForQuery(tool, NewSearchQuery(query))
}

func SearchRelevanceForQuery(tool *Tool, query SearchQuery) int {
	if tool == nil {
		return 0
	}
	if query.text == "" {
		return 1
	}
	score := 0
	if strings.Contains(query.text, strings.ToLower(tool.Name)) {
		score += 100
	}
	for _, alias := range toolSearchAliases[tool.Name] {
		if alias = strings.ToLower(strings.TrimSpace(alias)); alias != "" && strings.Contains(query.text, alias) {
			score += 24
		}
	}
	tokens := documentTokens(tool)
	for _, token := range query.tokens {
		if tokens[token] {
			score += searchTokenWeight(token)
		}
	}
	return score
}

// SearchAliases returns a defensive copy so schema adapters can make the
// retrieval vocabulary visible to the model without exposing mutable registry
// state.
func SearchAliases(name string) []string {
	return append([]string(nil), toolSearchAliases[name]...)
}

var toolSearchAliases = map[string][]string{
	"task_wait":              {"等待下载", "下载完成", "等待文件", "等下载", "smart wait", "等待条件"},
	"task_context":           {"长任务", "任务进度", "恢复任务", "工作记录"},
	"schedule_task":          {"每分钟", "每30分钟", "两小时后", "2小时后", "周期任务", "定时任务", "延时任务"},
	"host_incident_baseline": {"主机应急", "应急基线", "incident baseline"},
	"proc_socket_map":        {"异常外联", "外联进程", "socket pid", "c2 process"},
	"service_unit_audit":     {"异常自启动", "启动项", "持久化", "persistence"},
	"webshell_hunt":          {"webshell", "网页木马", "后门排查"},
	"read_log":               {"ssh爆破", "认证日志", "auth bruteforce"},
	"login_audit":            {"成功登录", "登录关联", "login audit"},
	"read_gzip":              {"gzip轮转", "压缩日志", "rotated log"},
	"db_log_read":            {"数据库认证失败", "数据库日志", "db auth"},
	"file_ident":             {"伪装二进制", "文件类型", "魔数", "magic"},
	"file_hash":              {"伪装二进制", "文件哈希", "sha256", "完整性"},
	"pcap_analyze":           {"pcap", "恶意域名", "tls sni", "流量线索"},
	"document_parse":         {"office文档", "docx", "xlsx", "文档元信息"},
	"chat_send_file":         {"发给我", "发文件", "微信发文件", "桌面文件", "发送附件"},
	"zip_password_recover":   {"zipcracker", "zip密码", "压缩包密码", "伪加密", "zipcrypto", "winzip aes", "字典爆破", "掩码爆破", "crc32", "四位数字"},
	"fleet_inventory":        {"fleet清单", "多目标清单", "目标盘点"},
	"fleet_exec":             {"fleet批量", "批量端口", "多目标健康", "ssh中断", "批量巡检"},
	"fleet_file":             {"多目标证据文件", "批量文件", "fleet file"},
	"web_snapshot":           {"网页表单", "页面脚本", "forms scripts", "网页快照"},
	"computer_use":           {"computer use", "桌面操作", "操作电脑", "电脑操作", "鼠标", "键盘", "桌面截图", "点击屏幕", "desktop", "desktop automation", "打开微信", "微信客户端", "本机微信", "桌面应用", "原生应用", "辅助功能", "ui自动化", "屏幕截图", "屏幕操作"},
	"browser_browse":         {"分页公告", "跟进链接", "持续浏览", "安全公告", "advisory"},
	"db_config_audit":        {"redis危险配置", "数据库配置审计"},
	"mysql_probe":            {"mysql版本", "mysql握手", "mysql handshake"},
	"sqlite_inspect":         {"sqlite schema", "sqlite结构"},
	"flag_scan":              {"扫描flag", "标准flag", "ctf flag"},
	"awd_service_check":      {"awd服务可用性", "awd探活", "服务可用性", "service check"},
}

func searchTokens(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := make(map[string]bool)
	var tokens []string
	add := func(token string) {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			return
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	for _, word := range words {
		runes := []rune(word)
		if allHanRunes(runes) {
			if len(runes) <= 4 {
				add(word)
			}
			for size := 2; size <= 3; size++ {
				for start := 0; start+size <= len(runes); start++ {
					add(string(runes[start : start+size]))
				}
			}
			continue
		}
		if len(runes) >= 2 {
			add(word)
		}
	}
	return tokens
}

func searchTokenSet(text string) map[string]bool {
	out := make(map[string]bool)
	for _, token := range searchTokens(text) {
		out[token] = true
	}
	return out
}

func searchTokenWeight(token string) int {
	runes := []rune(token)
	if allHanRunes(runes) {
		if len(runes) >= 3 {
			return 6
		}
		return 3
	}
	if len(runes) >= 5 {
		return 8
	}
	return 4
}

func allHanRunes(runes []rune) bool {
	if len(runes) == 0 {
		return false
	}
	for _, r := range runes {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}
