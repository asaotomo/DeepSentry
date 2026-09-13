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
	questionRE      = regexp.MustCompile(`(?i)(?:^|[\s*_#])q\d+\s*[:：]`)
	hashAnswerRE    = regexp.MustCompile(`(?i)\b[0-9a-f]{32}(?:[0-9a-f]{8}|[0-9a-f]{32})?\b`)
	logTimestampRE  = regexp.MustCompile(`(?m)^\s*(?:\[?\d{1,2}:\d{2}:\d{2}\]?|\d{4}[-/]\d{1,2}[-/]\d{1,2})`)
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
	if text == "" && strings.TrimSpace(input.RunAt) == "" {
		return Plan{}, fmt.Errorf("text/task 或 run_at 至少提供一个")
	}

	task := Task{
		ID:        makeTaskID(text, now),
		Name:      shortName(firstNonEmpty(input.Prompt, input.Text, "定时任务"), 28),
		Prompt:    strings.TrimSpace(firstNonEmpty(input.Prompt, input.Text)),
		Kind:      normalizeKind(firstNonEmpty(input.Kind, inferKind(text))),
		Selector:  normalizeSelector(input.Selector, text),
		Timezone:  tzName,
		Repeat:    RepeatOnce,
		Report:    strings.Contains(text, "报告") || strings.Contains(strings.ToLower(text), "report"),
		Notify:    inferNotify(text),
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

	notes := []string{}
	repeat, weekday, interval := inferRepeat(text)
	if input.Repeat != "" {
		repeat, weekday, interval = parseRepeat(input.Repeat, text)
	}
	task.Repeat = repeat
	task.Weekday = weekday
	task.IntervalSec = interval

	runAt, timeNotes, err := parseRunTime(firstNonEmpty(input.RunAt, text), now, loc, task.Repeat, task.Weekday, task.IntervalSec)
	if err != nil {
		return Plan{}, err
	}
	task.RunAt = runAt
	notes = append(notes, timeNotes...)
	if task.Kind == KindInspection {
		task.Report = true
		notes = append(notes, "巡检类任务优先使用 inspection.devices 设备检查项生成 Word/Markdown；未配置时只做基础状态采集")
	}
	if task.Kind == KindAgent && !task.AllowBatch {
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

func ParseNaturalAt(text string, now time.Time, loc *time.Location) (Task, []string, error) {
	if loc == nil {
		loc = time.Local
	}
	plan, err := PlanTask(PlanInput{Text: text, Timezone: loc.String()}, now.In(loc))
	return plan.Task, plan.Notes, err
}

func LooksLikeSchedule(text string) bool {
	ok, _ := DetectScheduleIntent(text)
	return ok
}

// DetectScheduleIntent is deliberately conservative because a positive result
// takes the native mutation path and persists a job without an LLM round-trip.
// Time words plus generic verbs are not enough: the user must also express a
// scheduling request, a relative delay, or a recurring cadence.
func DetectScheduleIntent(text string) (bool, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, "空输入"
	}
	if looksLikeAnswerOrMaliciousArtifact(text) {
		return false, "更像答案、日志或取证材料"
	}
	hasTime := containsAny(text, "明天", "后天", "今天", "今晚", "每天", "每日", "每周", "每星期", "每礼拜", "每隔", "分钟后", "小时后", "天后")
	if !hasTime && (ymdRE.MatchString(text) || mdRE.MatchString(text)) {
		hasTime = true
	}
	if !hasTime {
		_, _, hasTime = extractClock(text)
	}
	if !hasTime {
		return false, "未识别到可靠的未来时间"
	}
	hasTask := containsAny(strings.ToLower(text), "提醒", "巡检", "检查", "执行", "运行", "跑", "生成", "备份", "汇总", "总结", "同步", "通知", "发送", "发钉钉", "钉钉", "飞书", "邮件", "邮箱", "email", "mail")
	if !hasTask {
		return false, "未识别到要调度的任务"
	}

	lower := strings.ToLower(text)
	explicit := containsAny(lower, "创建定时任务", "添加定时任务", "设置定时任务", "定时执行", "定时运行", "设置提醒", "设个提醒", "提醒我", "帮我提醒", "安排在", "计划在", "到点提醒", "schedule", "remind me")
	requested := containsAny(lower, "帮我", "请帮我", "请在", "请于", "请明天", "请后天", "麻烦在", "麻烦帮我", "我要你", "我想让你")
	relative := relativeAfterRE.MatchString(text) || strings.Contains(text, "半小时后")
	recurring := containsAny(text, "每天", "每日", "每周", "每星期", "每礼拜", "每隔")
	trimmed := strings.TrimSpace(text)
	directRelative := relative && startsWithRelativeTime(trimmed) && !containsAny(text, "结果", "显示", "记录", "日志", "会在", "已经")
	directRecurring := recurring && hasAnyPrefix(trimmed, "每天", "每日", "每周", "每星期", "每礼拜", "每隔") && !containsAny(text, "会在", "已经", "原本", "当前", "日志显示", "配置为", "脚本在", "程序在")
	if explicit || requested || directRelative || directRecurring {
		reason := "显式调度请求"
		switch {
		case recurring:
			reason = "显式重复周期"
		case relative:
			reason = "显式相对延时"
		}
		return true, reason
	}
	return false, "只出现了时间和动作词，没有明确要求创建调度"
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(text string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func startsWithRelativeTime(text string) bool {
	if strings.HasPrefix(text, "半小时后") {
		return true
	}
	idx := relativeAfterRE.FindStringIndex(text)
	return len(idx) == 2 && idx[0] == 0
}

func looksLikeAnswerOrMaliciousArtifact(text string) bool {
	lower := strings.ToLower(text)
	// Security challenge answers often contain labels such as "Q10：", an
	// answer marker and a hash.  They may also contain words like "执行", which
	// are schedule action verbs, so reject this shape before looking for time.
	if questionRE.MatchString(text) && (strings.Contains(text, "答案") || strings.Contains(lower, "answer")) {
		return true
	}
	if hashAnswerRE.MatchString(text) && (strings.Contains(text, "答案") || strings.Contains(lower, "md5") || strings.Contains(lower, "sha")) {
		return true
	}
	if strings.Count(text, "\n") >= 3 && !containsAny(lower, "创建定时任务", "设置定时任务", "设置提醒", "schedule", "remind me") {
		return true
	}
	if logTimestampRE.MatchString(text) || strings.Contains(text, "HTTP/1.") || strings.Contains(text, "```") {
		return true
	}
	if ipPortRE.MatchString(text) {
		for _, needle := range []string{"回连", "反连", "reverse shell", "callback", "connect back", "提交", "答案", "flag"} {
			if strings.Contains(lower, needle) || strings.Contains(text, needle) {
				return true
			}
		}
	}
	return false
}

func parseRunTime(raw string, now time.Time, loc *time.Location, repeat string, weekday, intervalSec int) (time.Time, []string, error) {
	raw = strings.TrimSpace(raw)
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
	if !ok {
		return time.Time{}, nil, fmt.Errorf("无法识别执行时间，请提供类似 明天9点 / 2026-06-27 09:00 / 10分钟后")
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

func inferRepeat(text string) (string, int, int) {
	switch {
	case strings.Contains(text, "每天") || strings.Contains(text, "每日"):
		return RepeatDaily, 0, 0
	case strings.Contains(text, "每周") || strings.Contains(text, "每星期") || strings.Contains(text, "每礼拜"):
		return RepeatWeekly, inferWeekday(text), 0
	}
	if m := intervalRE.FindStringSubmatch(text); len(m) == 3 {
		n, _ := strconv.Atoi(m[1])
		return RepeatInterval, 0, int(durationForUnit(n, m[2]).Seconds())
	}
	return RepeatOnce, 0, 0
}

func parseRepeat(raw, text string) (string, int, int) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", RepeatOnce:
		return RepeatOnce, 0, 0
	case RepeatDaily, "day", "每天", "每日":
		return RepeatDaily, 0, 0
	case RepeatWeekly, "week", "每周":
		return RepeatWeekly, inferWeekday(text), 0
	}
	if strings.HasPrefix(raw, "interval:") {
		sec, _ := strconv.Atoi(strings.TrimPrefix(raw, "interval:"))
		if sec > 0 {
			return RepeatInterval, 0, sec
		}
	}
	return inferRepeat(text + raw)
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

func inferKind(text string) string {
	for _, needle := range []string{"巡检", "健康检查", "服务器检查", "可用性检查", "体检"} {
		if strings.Contains(text, needle) {
			return KindInspection
		}
	}
	return KindAgent
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

func inferNotify(text string) string {
	lower := strings.ToLower(text)
	channels := []string{}
	if strings.Contains(text, "钉钉") || strings.Contains(lower, "dingtalk") {
		channels = append(channels, NotifyDingTalk)
	}
	if strings.Contains(text, "飞书") || strings.Contains(lower, "feishu") || strings.Contains(lower, "lark") {
		channels = append(channels, NotifyFeishu)
	}
	if strings.Contains(text, "邮件") || strings.Contains(text, "邮箱") || strings.Contains(lower, "email") || strings.Contains(lower, "mail") {
		channels = append(channels, NotifyEmail)
	}
	if len(channels) == 0 {
		return NotifyNone
	}
	return strings.Join(dedupeNotifyChannels(channels), ",")
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
