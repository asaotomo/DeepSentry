package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	relativeAfterRE = regexp.MustCompile(`(?i)(\d+)\s*(分钟|分|小时|时|天|日)\s*后`)
	clockColonRE    = regexp.MustCompile(`(凌晨|早上|上午|中午|下午|晚上|今晚)?\s*(\d{1,2})[:：]\s*(\d{2})`)
	clockPointRE    = regexp.MustCompile(`(凌晨|早上|上午|中午|下午|晚上|今晚)?\s*(\d{1,2})[点时]\s*(\d{1,2})?\s*(?:分)?`)
	markerClockRE   = regexp.MustCompile(`(凌晨|早上|上午|中午|下午|晚上|今晚)\s*(\d{1,2})(?:\s*(\d{1,2})\s*分)?`)
	ymdRE           = regexp.MustCompile(`(\d{4})[-/年](\d{1,2})[-/月](\d{1,2})日?`)
	mdRE            = regexp.MustCompile(`(\d{1,2})月(\d{1,2})[日号]?`)
	intervalRE      = regexp.MustCompile(`每(?:隔)?\s*(\d+)\s*(分钟|分|小时|时|天|日)`)
	ipPortRE        = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}:\d{2,5}\b`)
)

func PlanTask(input PlanInput, now time.Time) (Plan, error) {
	loc, tzName, err := resolveLocation(input.Timezone)
	if err != nil {
		return Plan{}, err
	}
	if now.IsZero() {
		now = time.Now().In(loc)
	} else {
		now = now.In(loc)
	}

	text := strings.TrimSpace(input.Text)
	if text == "" {
		text = strings.TrimSpace(input.Prompt)
	}
	if text == "" && strings.TrimSpace(input.RunAt) == "" && strings.TrimSpace(input.IntervalSec) == "" {
		return Plan{}, fmt.Errorf("text/task、run_at 或 interval_sec 至少提供一个")
	}

	task := Task{
		ID:        makeTaskID(text+"\x00"+strings.TrimSpace(input.ReplySession)+"\x00"+input.ReplyText, now),
		Name:      shortName(firstNonEmpty(input.Prompt, input.Text, "定时任务"), 28),
		Prompt:    strings.TrimSpace(firstNonEmpty(input.Prompt, input.Text)),
		Kind:      normalizeKind(input.Kind),
		Selector:  normalizeSelector(input.Selector, ""),
		Timezone:  tzName,
		Repeat:    RepeatOnce,
		Report:    input.Report != nil && *input.Report,
		Notify:    NotifyNone,
		Status:    StatusEnabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if task.Prompt == "" {
		task.Prompt = text
	}
	if task.Name == "" {
		task.Name = "定时任务"
	}
	if task.Kind == "" {
		task.Kind = KindAgent
	}
	if task.Notify == "" {
		task.Notify = NotifyNone
	}
	if input.Notify != "" {
		task.Notify = normalizeNotify(input.Notify)
	}
	if input.Report != nil {
		task.Report = *input.Report
	}
	task.AllowBatch = input.AllowBatch
	task.ReplySession = strings.TrimSpace(input.ReplySession)
	if text, ok := directChatReply(Task{Prompt: task.Prompt, ReplyText: input.ReplyText}); ok {
		task.ReplyText = text
	}
	if input.TimeoutSec != "" {
		n, e := strconv.Atoi(input.TimeoutSec)
		if e != nil || n < 1 || n > 604800 {
			return Plan{}, fmt.Errorf("timeout_sec must be 1..604800")
		}
		task.TimeoutSec = n
	}

	notes := []string{}
	repeat, weekday, interval := RepeatOnce, 0, 0
	if strings.TrimSpace(input.Repeat) != "" {
		repeat, weekday, interval, err = parseRepeat(input.Repeat)
		if err != nil {
			return Plan{}, err
		}
	}
	if spec := strings.TrimSpace(input.IntervalSec); spec != "" {
		sec, err := ParseIntervalSpec(spec)
		if err != nil {
			return Plan{}, err
		}
		repeat, weekday, interval = RepeatInterval, 0, sec
	}
	task.Repeat = repeat
	task.Weekday = weekday
	task.IntervalSec = interval
	if task.Repeat == RepeatInterval && (task.IntervalSec < minIntervalSec || task.IntervalSec > maxIntervalSec) {
		return Plan{}, fmt.Errorf("周期必须在 60 秒到 7 天之间")
	}

	switch {
	case task.Repeat == RepeatInterval && task.IntervalSec > 0 && strings.TrimSpace(input.RunAt) == "":
		task.RunAt = now.Add(time.Duration(task.IntervalSec) * time.Second)
		notes = append(notes, fmt.Sprintf("已按周期 %d 秒安排，首次执行在一个周期后", task.IntervalSec))
	case strings.TrimSpace(input.RunAt) != "":
		runAt, timeNotes, err := parseRunTime(input.RunAt, now, loc, task.Repeat, task.Weekday, task.IntervalSec)
		if err != nil {
			return Plan{}, err
		}
		task.RunAt = runAt
		notes = append(notes, timeNotes...)
	default:
		return Plan{}, fmt.Errorf("不会从任务正文猜测执行时间。周期请提供 interval_sec（每分钟=60），单次或定点请提供 run_at")
	}
	if task.Kind == KindInspection {
		task.Report = true
		notes = append(notes, "巡检类任务优先使用 inspection.devices 设备检查项生成 Word/Markdown；未配置时只做基础状态采集")
	}
	if task.ReplyText != "" {
		notes = append(notes, "到点会把这句话直接发进当前聊天，不另起一轮报告: "+task.ReplyText)
	}
	if task.Kind == KindAgent && !task.AllowBatch && task.ReplyText == "" {
		notes = append(notes, "泛化 Agent 定时任务默认不会无人值守执行；如确需 batch，创建时显式 allow_batch=true")
	}
	for _, ch := range NotifyChannels(task.Notify) {
		switch ch {
		case NotifyDingTalk:
			notes = append(notes, "钉钉通知需要配置 dingtalk_webhook，可选 dingtalk_secret")
		case NotifyFeishu:
			notes = append(notes, "飞书通知需要配置 feishu_webhook，可选 feishu_secret")
		case NotifyEmail:
			notes = append(notes, "邮件网关通知需要配置 email_gateway_url 与 email_to，可选 email_gateway_token/email_from")
		}
	}
	return Plan{Task: task, Notes: notes}, nil
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func parseRunTime(raw string, now time.Time, loc *time.Location, repeat string, weekday, intervalSec int) (time.Time, []string, error) {
	raw = normalizeDelayWords(strings.TrimSpace(raw))
	if raw == "" {
		return time.Time{}, nil, fmt.Errorf("无法识别执行时间")
	}
	if t, ok := parseAbsolute(raw, loc); ok {
		if !t.After(now) {
			if repeat == RepeatOnce {
				return time.Time{}, nil, fmt.Errorf("执行时间已过去: %s", t.Format(time.RFC3339))
			}
			t = advanceRepeat(t, now, repeat, intervalSec)
		}
		return t, []string{"已识别明确日期时间"}, nil
	}
	if strings.Contains(raw, "半小时后") {
		return now.Add(30 * time.Minute), []string{"已识别相对时间: 半小时后"}, nil
	}
	if m := relativeAfterRE.FindStringSubmatch(raw); len(m) == 3 {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return time.Time{}, nil, fmt.Errorf("相对时间必须大于 0")
		}
		d := durationForUnit(n, m[2])
		return now.Add(d), []string{"已识别相对时间: " + m[0]}, nil
	}

	date := dateFromWords(raw, now)
	explicitDate := containsAny(raw, "今天", "今晚", "明天", "后天")
	if ymd := ymdRE.FindStringSubmatch(raw); len(ymd) == 4 {
		y, _ := strconv.Atoi(ymd[1])
		mon, _ := strconv.Atoi(ymd[2])
		day, _ := strconv.Atoi(ymd[3])
		date = time.Date(y, time.Month(mon), day, 0, 0, 0, 0, loc)
		if date.Year() != y || int(date.Month()) != mon || date.Day() != day {
			return time.Time{}, nil, fmt.Errorf("无效日期: %s", ymd[0])
		}
		explicitDate = true
	} else if md := mdRE.FindStringSubmatch(raw); len(md) == 3 {
		mon, _ := strconv.Atoi(md[1])
		day, _ := strconv.Atoi(md[2])
		date = time.Date(now.Year(), time.Month(mon), day, 0, 0, 0, 0, loc)
		if int(date.Month()) != mon || date.Day() != day {
			return time.Time{}, nil, fmt.Errorf("无效日期: %s", md[0])
		}
		if date.Before(now) {
			date = date.AddDate(1, 0, 0)
		}
		explicitDate = true
	} else if repeat == RepeatWeekly {
		date = nextWeekday(now, weekday)
	}

	hour, minute, ok := extractClock(raw)
	if !ok && repeat == RepeatInterval && intervalSec > 0 {
		return now.Add(time.Duration(intervalSec) * time.Second), []string{"首次执行在一个周期后；后续保持周期"}, nil
	}
	if !ok {
		return time.Time{}, nil, fmt.Errorf("无法识别执行时间。周期请给 interval_sec（每分钟=60）或 repeat=interval；单次请给 run_at，例如 2026-06-27 09:00 或 10分钟后")
	}
	runAt := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc)
	if !runAt.After(now) {
		if repeat == RepeatOnce && explicitDate {
			return time.Time{}, nil, fmt.Errorf("执行时间已过去: %s", runAt.Format(time.RFC3339))
		}
		runAt = advanceRepeat(runAt, now, repeat, intervalSec)
	}
	if !runAt.After(now) {
		runAt = runAt.AddDate(0, 0, 1)
	}
	return runAt, []string{"已识别中文自然语言时间"}, nil
}

func parseAbsolute(raw string, loc *time.Location) (time.Time, bool) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006/01/02 15:04:05",
		"2006/01/02 15:04",
		"2006-1-2 15:04",
		"2006/1/2 15:04",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func dateFromWords(raw string, now time.Time) time.Time {
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case strings.Contains(raw, "后天"):
		return base.AddDate(0, 0, 2)
	case strings.Contains(raw, "明天"):
		return base.AddDate(0, 0, 1)
	default:
		return base
	}
}

func extractClock(raw string) (int, int, bool) {
	type clockMatch struct {
		parts []string
		start int
		end   int
	}
	matches := []clockMatch{}
	for _, re := range []*regexp.Regexp{clockColonRE, clockPointRE, markerClockRE} {
		for _, idx := range re.FindAllStringSubmatchIndex(raw, -1) {
			if len(idx) >= 2 {
				matches = append(matches, clockMatch{parts: re.FindStringSubmatch(raw[idx[0]:idx[1]]), start: idx[0], end: idx[1]})
			}
		}
	}
	for _, m := range matches {
		if clockMatchIsEmbedded(raw, m.start, m.end) {
			continue
		}
		if len(m.parts) < 3 {
			continue
		}
		marker := strings.TrimSpace(m.parts[1])
		hour, _ := strconv.Atoi(m.parts[2])
		minute := 0
		if len(m.parts) > 3 && strings.TrimSpace(m.parts[3]) != "" {
			minute, _ = strconv.Atoi(m.parts[3])
		}
		if hour > 23 || minute > 59 {
			continue
		}
		switch marker {
		case "下午", "晚上", "今晚":
			if hour < 12 {
				hour += 12
			}
		case "中午":
			if hour < 11 {
				hour += 12
			}
		}
		return hour, minute, true
	}
	return 0, 0, false
}

func clockMatchIsEmbedded(raw string, start, end int) bool {
	if start > 0 {
		prev := raw[start-1]
		if (prev >= '0' && prev <= '9') || (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') || prev == '_' || prev == '.' || prev == ':' {
			return true
		}
	}
	if end < len(raw) {
		next := raw[end]
		if (next >= '0' && next <= '9') || next == '.' || next == ':' {
			return true
		}
	}
	return false
}

func parseRepeat(raw string) (string, int, int, error) {
	raw = strings.TrimSpace(raw)
	if sec, ok := exactBareInterval(raw); ok {
		return RepeatInterval, 0, sec, nil
	}
	if sec, err := ParseIntervalSpec(raw); err == nil {
		return RepeatInterval, 0, sec, nil
	}
	lower := strings.ToLower(raw)
	if lower == RepeatInterval {
		return RepeatInterval, 0, 0, nil
	}
	switch lower {
	case "", RepeatOnce:
		return RepeatOnce, 0, 0, nil
	case RepeatDaily, "day", "每天", "每日":
		return RepeatDaily, 0, 0, nil
	case RepeatWeekly, "week", "每周":
		return RepeatWeekly, inferWeekday(raw), 0, nil
	}
	if strings.HasPrefix(lower, "interval:") {
		sec, _ := strconv.Atoi(strings.TrimPrefix(lower, "interval:"))
		if sec > 0 {
			return RepeatInterval, 0, sec, nil
		}
	}
	if m := intervalRE.FindStringSubmatch(raw); len(m) == 3 && strings.TrimSpace(m[0]) == strings.TrimSpace(raw) {
		n, _ := strconv.Atoi(m[1])
		return RepeatInterval, 0, int(durationForUnit(n, m[2]).Seconds()), nil
	}
	return "", 0, 0, fmt.Errorf("repeat 无法识别。请用 once、daily、weekly、interval，或直接提供 interval_sec")
}

func inferWeekday(text string) int {
	mapper := map[string]int{"日": 0, "天": 0, "一": 1, "二": 2, "三": 3, "四": 4, "五": 5, "六": 6}
	for ch, day := range mapper {
		if strings.Contains(text, "周"+ch) || strings.Contains(text, "星期"+ch) || strings.Contains(text, "礼拜"+ch) {
			return day
		}
	}
	return int(time.Now().Weekday())
}

func nextWeekday(now time.Time, weekday int) time.Time {
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	delta := (weekday - int(now.Weekday()) + 7) % 7
	return base.AddDate(0, 0, delta)
}

func advanceRepeat(t, now time.Time, repeat string, intervalSec int) time.Time {
	switch repeat {
	case RepeatDaily:
		for !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
	case RepeatWeekly:
		for !t.After(now) {
			t = t.AddDate(0, 0, 7)
		}
	case RepeatInterval:
		if intervalSec <= 0 {
			intervalSec = 3600
		}
		for !t.After(now) {
			t = t.Add(time.Duration(intervalSec) * time.Second)
		}
	}
	return t
}

func durationForUnit(n int, unit string) time.Duration {
	switch unit {
	case "小时", "时":
		return time.Duration(n) * time.Hour
	case "天", "日":
		return time.Duration(n) * 24 * time.Hour
	default:
		return time.Duration(n) * time.Minute
	}
}

func normalizeKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "", "auto":
		return ""
	case "inspect", "health", "health_report", "巡检", "inspection":
		return KindInspection
	default:
		return KindAgent
	}
}

func normalizeNotify(notify string) string {
	notify = strings.ToLower(strings.TrimSpace(notify))
	if notify == "" || notify == "none" || notify == "off" || notify == "false" {
		return NotifyNone
	}
	parts := strings.FieldsFunc(notify, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '+' || r == '、' || r == '|' || r == '/' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	channels := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		switch part {
		case "", "none", "off", "false":
			continue
		case "dingtalk", "ding", "钉钉":
			channels = append(channels, NotifyDingTalk)
		case "feishu", "lark", "飞书":
			channels = append(channels, NotifyFeishu)
		case "email", "mail", "smtp", "邮件", "邮箱":
			channels = append(channels, NotifyEmail)
		default:
			channels = append(channels, part)
		}
	}
	channels = dedupeNotifyChannels(channels)
	if len(channels) == 0 {
		return NotifyNone
	}
	return strings.Join(channels, ",")
}

func NotifyChannels(notify string) []string {
	normalized := normalizeNotify(notify)
	if normalized == NotifyNone {
		return nil
	}
	return strings.Split(normalized, ",")
}

func dedupeNotifyChannels(channels []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(channels))
	for _, ch := range channels {
		ch = strings.TrimSpace(strings.ToLower(ch))
		if ch == "" || seen[ch] {
			continue
		}
		seen[ch] = true
		out = append(out, ch)
	}
	return out
}

func normalizeSelector(selector, text string) string {
	selector = strings.TrimSpace(selector)
	if selector != "" {
		return selector
	}
	if strings.Contains(text, "全部") || strings.Contains(text, "所有") || strings.Contains(strings.ToLower(text), "all") {
		return "all"
	}
	return ""
}

func resolveLocation(name string) (*time.Location, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, time.Local.String(), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, "", fmt.Errorf("加载时区失败 %s: %w", name, err)
	}
	return loc, name, nil
}

func makeTaskID(seed string, now time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", seed, now.UnixNano())))
	return "sched_" + now.Format("20060102_150405") + "_" + hex.EncodeToString(sum[:])[:8]
}

func shortName(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func normalizeDelayWords(s string) string {
	return strings.NewReplacer("两小时后", "2小时后", "两个小时后", "2小时后", "一小时后", "1小时后", "一个小时后", "1小时后").Replace(s)
}

const (
	minIntervalSec = 60
	maxIntervalSec = 604800
)

var (
	intervalSpecRE      = regexp.MustCompile(`(?i)^(\d+)\s*(s|sec|secs|m|min|mins|h|hr|hour|d|day)?$`)
	bareIntervalPhrases = []struct {
		phrase string
		sec    int
	}{
		{"每半个小时", 1800},
		{"每半小时", 1800},
		{"每一个小时", 3600},
		{"每个小时", 3600},
		{"每一小时", 3600},
		{"每小时", 3600},
		{"每钟头", 3600},
		{"每一分钟", 60},
		{"每个分钟", 60},
		{"每分钟", 60},
	}
)

func exactBareInterval(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	for _, phrase := range bareIntervalPhrases {
		if raw == phrase.phrase {
			return phrase.sec, true
		}
	}
	return 0, false
}

func ParseIntervalSpec(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(strings.ToLower(raw), "interval:")
	raw = strings.TrimSpace(raw)
	if sec, ok := exactBareInterval(raw); ok {
		return sec, nil
	}
	m := intervalSpecRE.FindStringSubmatch(raw)
	if m == nil {
		return 0, fmt.Errorf("无法识别周期 %q，例如 60、1m、30m、1h、每分钟", raw)
	}
	n, _ := strconv.Atoi(m[1])
	if n <= 0 {
		return 0, fmt.Errorf("周期必须大于 0")
	}
	sec := n
	switch strings.ToLower(m[2]) {
	case "", "s", "sec", "secs":
		sec = n
	case "m", "min", "mins":
		sec = n * 60
	case "h", "hr", "hour":
		sec = n * 3600
	case "d", "day":
		sec = n * 86400
	}
	if sec < minIntervalSec || sec > maxIntervalSec {
		return 0, fmt.Errorf("周期必须在 60 秒到 7 天之间，当前 %d 秒", sec)
	}
	return sec, nil
}

func UnattendedPromptRisk(prompt string) string {
	text := strings.TrimSpace(prompt)
	if text == "" {
		return "任务内容为空"
	}
	lower := strings.ToLower(text)
	if ipPortRE.MatchString(text) {
		for _, needle := range []string{"回连", "反连", "reverse shell", "callback", "connect back"} {
			if strings.Contains(lower, needle) || strings.Contains(text, needle) {
				return "任务文本包含回连/反连语义和 IP:端口，像是攻击取证答案而不是授权自动化任务"
			}
		}
	}
	for _, needle := range []string{"恶意回连", "反弹 shell", "reverse shell", "木马", "后门", "持久化", "篡改系统命令"} {
		if strings.Contains(lower, needle) || strings.Contains(text, needle) {
			return "任务文本包含高危攻击/持久化语义"
		}
	}
	return ""
}
