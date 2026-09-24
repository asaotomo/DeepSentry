package harness

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"
)

const maxSessionCoreClues = 48

// CoreClue 是从用户目标、工具输出和子 Agent 交接中提取的高信号事实。
// 它属于当前会话，会随 checkpoint 保存，但不会自动写入跨会话 Memory。
type CoreClue struct {
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Evidence string `json:"evidence,omitempty"`
	Source   string `json:"source,omitempty"`
}

var (
	cvePattern      = regexp.MustCompile(`(?i)\bCVE-\d{4}-\d{4,7}\b`)
	ipv4Pattern     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	urlPattern      = regexp.MustCompile(`https?://[^\s<>"']+`)
	unixPathPattern = regexp.MustCompile(`(?:^|[\s=:,(])(/(?:[^\s<>"'|]+/?)+)`)
	winPathPattern  = regexp.MustCompile(`(?i)\b[A-Z]:\\[^\r\n<>"|]+`)
	hashPattern     = regexp.MustCompile(`(?i)\b[0-9a-f]{32}(?:[0-9a-f]{8}|[0-9a-f]{32})?\b`)
	flagPattern     = regexp.MustCompile(`(?i)\bflag\{[^\r\n}]{1,160}\}`)
)

// ObserveCoreClues 从文本中增量提取线索。并发子 Agent 可以安全合并到同一主状态。
func (s *AgentState) ObserveCoreClues(text, source string) {
	if s == nil || strings.TrimSpace(text) == "" {
		return
	}
	clues := extractCoreClues(text, source)
	if len(clues) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.CoreClues == nil {
		s.CoreClues = []CoreClue{}
	}
	for _, clue := range clues {
		merged := false
		for i := range s.CoreClues {
			if strings.EqualFold(s.CoreClues[i].Kind, clue.Kind) && strings.EqualFold(s.CoreClues[i].Value, clue.Value) {
				if clue.Evidence != "" {
					s.CoreClues[i].Evidence = mergeClueProvenance(s.CoreClues[i].Evidence, clue.Evidence, 560)
				}
				if clue.Source != "" {
					s.CoreClues[i].Source = mergeClueProvenance(s.CoreClues[i].Source, clue.Source, 280)
				}
				merged = true
				break
			}
		}
		if !merged {
			s.CoreClues = append(s.CoreClues, clue)
		}
	}
	for len(s.CoreClues) > maxSessionCoreClues {
		drop := 0
		for i := 1; i < len(s.CoreClues); i++ {
			if coreCluePriority(s.CoreClues[i].Kind) < coreCluePriority(s.CoreClues[drop].Kind) {
				drop = i
			}
		}
		s.CoreClues = append(s.CoreClues[:drop], s.CoreClues[drop+1:]...)
	}
}

func mergeClueProvenance(current, next string, maxRunes int) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if current == "" {
		return compactClueField(next, maxRunes)
	}
	if next == "" || current == next || strings.Contains(current, " | "+next) || strings.HasPrefix(current, next+" | ") {
		return compactClueField(current, maxRunes)
	}
	return compactClueField(current+" | "+next, maxRunes)
}

func compactClueField(s string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(s))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return strings.TrimSpace(s)
	}
	return string(runes[:maxRunes]) + "…"
}

func coreCluePriority(kind string) int {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "FLAG", "CVE", "HASH":
		return 5
	case "IP", "URL", "PATH":
		return 4
	case "FINDING":
		return 3
	default:
		return 1
	}
}

func (s *AgentState) CoreCluesSnapshot() []CoreClue {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]CoreClue(nil), s.CoreClues...)
	sort.Slice(out, func(i, j int) bool {
		if coreCluePriority(out[i].Kind) != coreCluePriority(out[j].Kind) {
			return coreCluePriority(out[i].Kind) > coreCluePriority(out[j].Kind)
		}
		if out[i].Kind == out[j].Kind {
			return out[i].Value < out[j].Value
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func (s *AgentState) ReplaceCoreClues(clues []CoreClue) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CoreClues = append([]CoreClue(nil), clues...)
}

func (s *AgentState) CoreCluesPrompt(maxChars int) string {
	clues := s.CoreCluesSnapshot()
	if len(clues) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = 6000
	}
	var b strings.Builder
	b.WriteString("\n【会话核心线索】\n这些是从用户输入、执行结果和子 Agent 交接中提取的高信号候选事实。结合来源与证据判断是否已验证；优先复用，发现冲突时明确指出，不要重复无意义探测。\n")
	for _, clue := range clues {
		line := fmt.Sprintf("- [%s] %s", clue.Kind, clue.Value)
		if clue.Evidence != "" && clue.Evidence != clue.Value {
			line += " — " + clue.Evidence
		}
		if clue.Source != "" {
			line += " (来源: " + clue.Source + ")"
		}
		line += "\n"
		if b.Len()+len(line) > maxChars {
			b.WriteString("- ...(其余线索已省略，保留在 checkpoint 中)\n")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

func extractCoreClues(text, source string) []CoreClue {
	seen := map[string]bool{}
	var out []CoreClue
	add := func(kind, value, evidence string) {
		value = strings.Trim(strings.TrimSpace(value), "`*.,;，。；:：()[]")
		if value == "" || looksSensitive(value) || (kind == "PATH" && strings.HasPrefix(value, "//")) {
			return
		}
		key := strings.ToLower(kind + "\x00" + value)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, CoreClue{Kind: kind, Value: value, Evidence: compactClueLine(evidence), Source: compactClueLine(source)})
	}

	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || looksSensitive(line) {
			continue
		}
		for _, v := range cvePattern.FindAllString(line, -1) {
			add("CVE", strings.ToUpper(v), line)
		}
		for _, v := range ipv4Pattern.FindAllString(line, -1) {
			if net.ParseIP(v) != nil {
				add("IP", v, line)
			}
		}
		for _, v := range urlPattern.FindAllString(line, -1) {
			add("URL", v, line)
		}
		for _, match := range unixPathPattern.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 && len(match[1]) > 1 {
				add("PATH", match[1], line)
			}
		}
		for _, v := range winPathPattern.FindAllString(line, -1) {
			add("PATH", v, line)
		}
		for _, v := range hashPattern.FindAllString(line, -1) {
			add("HASH", strings.ToLower(v), line)
		}
		for _, v := range flagPattern.FindAllString(line, -1) {
			add("FLAG", v, line)
		}
		lower := strings.ToLower(line)
		if len([]rune(line)) <= 260 && containsAny(lower, "核心结论", "关键结论", "发现:", "发现：", "证据:", "证据：", "失败原因", "未决", "冲突", "高风险", "critical") {
			add("FINDING", line, line)
		}
		if len(out) >= maxSessionCoreClues {
			break
		}
	}
	return out
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func compactClueLine(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	return runewidth.Truncate(s, 280, "…")
}

func looksSensitive(s string) bool {
	lower := strings.ToLower(s)
	return containsAny(lower, "password=", "passwd=", "api_key=", "apikey=", "bearer ", "private_key", "ssh_password")
}
