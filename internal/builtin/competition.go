package builtin

import (
	"ai-edr/internal/config"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

func FlagScan(rt Runtime, root, pattern string, limit int) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if strings.TrimSpace(pattern) == "" {
		pattern = `flag\{[^}]{1,200}\}|ctf\{[^}]{1,200}\}|key\{[^}]{1,200}\}`
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return "", fmt.Errorf("pattern 正则无效: %w", err)
	}
	if limit <= 0 {
		limit = 80
	}
	cmd := fmt.Sprintf("find %s -type f -size -2M -print0 2>/dev/null | xargs -0 grep -HnE -m 3 %s 2>/dev/null | head -n %d", shellQuote(root), shellQuote(pattern), limit)
	out, err := rt.Exec.Run(cmd)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		out = "未发现匹配的 flag 线索"
	}
	return fmt.Sprintf("【Flag 只读扫描】root=%s limit=%d\n%s", root, limit, out), nil
}

func AWDServiceCheck(_ Runtime, targets string, timeoutSec int) (string, error) {
	return AWDServiceCheckWithOptions(targets, timeoutSec, 10, 0, "")
}

var awdTitlePattern = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)

// AWDServiceCheckWithOptions probes services concurrently but reports them in
// input order, so a slow/down host cannot hide the rest of the fleet.
func AWDServiceCheckWithOptions(targets string, timeoutSec, concurrency, expectedStatus int, contains string) (string, error) {
	rawTargets := strings.FieldsFunc(targets, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
	targetList := make([]string, 0, len(rawTargets))
	for _, raw := range rawTargets {
		if target := strings.TrimSpace(raw); target != "" {
			targetList = append(targetList, target)
		}
	}
	if len(targetList) == 0 {
		return "", fmt.Errorf("targets 不能为空")
	}
	if timeoutSec <= 0 {
		timeoutSec = 3
	}
	timeout := time.Duration(timeoutSec) * time.Second
	client := config.HTTPClient(timeout)
	// Preserve the status of the configured service endpoint. Following a
	// redirect to a login page would make expected_status=302 impossible.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	defer client.CloseIdleConnections()
	if concurrency <= 0 {
		concurrency = 10
	}
	if concurrency > 32 {
		concurrency = 32
	}
	type probeResult struct {
		status string
		detail string
	}
	results := make([]probeResult, len(targetList))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, target := range targetList {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			started := time.Now()
			status, detail := probeAWDService(client, target, timeout, expectedStatus, contains)
			results[i] = probeResult{status: status, detail: fmt.Sprintf("%s latency=%s", detail, time.Since(started).Round(time.Millisecond))}
		}(i, target)
	}
	wg.Wait()
	var b strings.Builder
	b.WriteString("【AWD 服务可用性】\n")
	counts := map[string]int{}
	for i, target := range targetList {
		result := results[i]
		counts[result.status]++
		fmt.Fprintf(&b, "- %s %s %s\n", target, result.status, result.detail)
	}
	fmt.Fprintf(&b, "汇总: UP=%d WARN=%d DOWN=%d INVALID=%d TOTAL=%d", counts["UP"], counts["WARN"], counts["DOWN"], counts["INVALID"], len(targetList))
	return strings.TrimSpace(b.String()), nil
}

func probeAWDService(client *http.Client, target string, timeout time.Duration, expectedStatus int, contains string) (string, string) {
	if strings.HasPrefix(strings.ToLower(target), "http://") || strings.HasPrefix(strings.ToLower(target), "https://") {
		request, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return "INVALID", "error=" + err.Error()
		}
		response, err := client.Do(request)
		if err != nil {
			return "DOWN", "error=" + err.Error()
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		if err != nil {
			return "DOWN", fmt.Sprintf("HTTP %d read_error=%v", response.StatusCode, err)
		}
		status := "UP"
		if response.StatusCode >= 500 {
			status = "DOWN"
		} else if response.StatusCode >= 400 {
			status = "WARN"
		}
		if expectedStatus != 0 {
			if response.StatusCode == expectedStatus {
				status = "UP"
			} else if status == "UP" {
				status = "WARN"
			}
		}
		detail := fmt.Sprintf("HTTP %d", response.StatusCode)
		if matches := awdTitlePattern.FindSubmatch(body); len(matches) > 1 {
			title := strings.Join(strings.Fields(html.UnescapeString(string(matches[1]))), " ")
			if title != "" {
				detail += " title=" + strconv.Quote(truncate(title, 100))
			}
		}
		if contains != "" && !strings.Contains(string(body), contains) {
			if status == "UP" {
				status = "WARN"
			}
			detail += " expected_text_missing=true"
		}
		return status, detail
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil || strings.TrimSpace(host) == "" {
		return "INVALID", "请使用 URL 或 host:port"
	}
	if parsedPort, parseErr := strconv.Atoi(port); parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
		return "INVALID", "port 无效"
	}
	conn, err := config.ControllerDialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return "DOWN", "error=" + err.Error()
	}
	_ = conn.Close()
	return "UP", "TCP OPEN"
}

// CompetitionAnswerCheck is a deterministic pre-submit rubric check. It does
// not certify technical truth; it makes missing evidence, verification, and
// correction sections visible before the operator emails the answer.
func CompetitionAnswerCheck(task, answer string) (string, error) {
	task = strings.TrimSpace(task)
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", fmt.Errorf("answer 不能为空")
	}
	sections := []struct {
		name    string
		aliases []string
	}{
		{"任务状态", []string{"任务状态"}},
		{"结论", []string{"结论"}},
		{"关键证据", []string{"关键证据", "证据"}},
		{"处置/答案", []string{"处置", "答案"}},
		{"复验", []string{"复验", "验证"}},
		{"AI复核与纠错", []string{"ai复核", "ai 复核", "纠错"}},
		{"风险与回滚", []string{"风险", "回滚"}},
	}
	lower := strings.ToLower(answer)
	present := make([]string, 0, len(sections))
	missing := make([]string, 0, len(sections))
	lastIndex := -1
	orderOK := true
	for _, section := range sections {
		index := -1
		for _, alias := range section.aliases {
			if candidate := strings.Index(lower, strings.ToLower(alias)); candidate >= 0 && (index < 0 || candidate < index) {
				index = candidate
			}
		}
		if index < 0 {
			missing = append(missing, section.name)
			continue
		}
		present = append(present, section.name)
		if index < lastIndex {
			orderOK = false
		}
		lastIndex = index
	}

	evidenceSignals := countEvidenceSignals(answer)
	verificationPresent := containsAny(lower, "复验", "验证", "恢复正常", "状态正常")
	correctionPresent := containsAny(lower, "纠错", "已否定", "未采纳", "尚未验证", "证据不足")
	rollbackPresent := containsAny(lower, "回滚", "撤销", "恢复原配置", "无变更")

	completeness := 40 * len(present) / len(sections)
	accuracy := 0
	if evidenceSignals >= 3 {
		accuracy += 15
	} else {
		accuracy += evidenceSignals * 5
	}
	if verificationPresent {
		accuracy += 8
	}
	if correctionPresent {
		accuracy += 7
	}
	efficiency := 20
	runes := utf8.RuneCountInString(answer)
	if runes < 120 {
		efficiency -= 8
	}
	if runes > 5000 {
		efficiency -= 8
	}
	format := 0
	if len(missing) == 0 {
		format += 7
	} else {
		format += 7 * len(present) / len(sections)
	}
	if orderOK {
		format += 3
	}

	var b strings.Builder
	fmt.Fprintf(&b, "【比赛答案提交前自检】估算=%d/100（仅检查结构与证据信号，不代替技术事实复核）\n", completeness+accuracy+efficiency+format)
	fmt.Fprintf(&b, "- 任务完成度: %d/40；已有=%s；缺失=%s\n", completeness, valueOrCompetition(strings.Join(present, ", "), "无"), valueOrCompetition(strings.Join(missing, ", "), "无"))
	fmt.Fprintf(&b, "- 技术准确性: %d/30；证据信号=%d，复验=%t，AI纠错=%t\n", accuracy, evidenceSignals, verificationPresent, correctionPresent)
	fmt.Fprintf(&b, "- AI应用效率: %d/20；答案长度=%d字\n", efficiency, runes)
	fmt.Fprintf(&b, "- 输出规范: %d/10；顺序正确=%t，风险/回滚=%t\n", format, orderOK, rollbackPresent)
	if task != "" {
		fmt.Fprintf(&b, "- 题干复核: 请逐项勾选原题要求，工具不会将文字相似度当作任务已完成。题干摘要=%s\n", truncate(task, 240))
	}
	if evidenceSignals < 3 {
		b.WriteString("- 必修: 至少补齐 3 条“命令/工具 -> 关键输出/数值 -> 结论”证据。\n")
	}
	if !correctionPresent {
		b.WriteString("- 加分机会: 写明被证据否定的 AI 初始假设；如无纠错，如实写“未发现可证实的 AI 错误”。\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func countEvidenceSignals(answer string) int {
	count := 0
	for _, line := range strings.Split(answer, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if containsAny(lower, "display ", "show ", "journalctl", "systemctl", "ip ", "ss ", "ping ", "工具", "输出", "日志", "%", "ms", "bps", "packets") {
			count++
		}
	}
	if count > 6 {
		return 6
	}
	return count
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func valueOrCompetition(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
