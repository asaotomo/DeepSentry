package scheduler

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxReplyRunes = 200
	maxInboxRunes = 4000
)

var (
	quotedReplyRE = regexp.MustCompile(`[「“"]([^」”"]{1,200})[」”"]`)
	ansiRE        = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	inboxMu       sync.Mutex
	replyCues     = []string{"给我发一句", "跟我说", "发给我", "发到聊天", "回一句", "发一句", "说一句"}
)

type ChatInboxLine struct {
	TaskID  string    `json:"task_id"`
	Session string    `json:"session,omitempty"`
	Kind    string    `json:"kind,omitempty"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
}

func directChatReply(task Task) (string, bool) {
	if task.Kind == KindInspection {
		return "", false
	}
	if text := clipReply(task.ReplyText); text != "" {
		return text, true
	}
	prompt := strings.TrimSpace(task.Prompt)
	if !wantsCurrentChatReply(prompt) {
		return "", false
	}
	if m := quotedReplyRE.FindStringSubmatch(prompt); len(m) == 2 {
		if text := clipReply(m[1]); text != "" {
			return text, true
		}
	}
	if text := clipReply(utteranceAfterCue(prompt)); text != "" && utf8.RuneCountInString(text) <= 80 {
		return text, true
	}
	return "", false
}

func utteranceAfterCue(prompt string) string {
	last, cue := -1, ""
	for _, item := range replyCues {
		if i := strings.LastIndex(prompt, item); i > last {
			last, cue = i, item
		}
	}
	if last < 0 {
		return ""
	}
	utterance := strings.TrimSpace(prompt[last+len(cue):])
	return strings.Trim(utterance, "。.!！ ")
}

func wantsCurrentChatReply(text string) bool {
	return strings.Contains(text, "当前聊天") || strings.Contains(text, "聊天框") || strings.Contains(text, "聊天页") ||
		strings.Contains(text, "回一句") || strings.Contains(text, "发一句") || strings.Contains(text, "说一句") ||
		strings.Contains(text, "跟我说") || strings.Contains(text, "发给我") || strings.Contains(text, "发到聊天")
}

func clipReply(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) <= maxReplyRunes {
		return text
	}
	return string([]rune(text)[:maxReplyRunes])
}

func chatInboxPath(store string) string {
	store = strings.TrimSpace(store)
	if store == "" {
		store = DefaultStorePath
	}
	return filepath.Join(filepath.Dir(store), "chat-inbox.jsonl")
}

// PostChatInbox appends a scheduler notice to the shared chat inbox. It exists so
// other packages (and tests) can enqueue lines the same way the runner does.
func PostChatInbox(store string, line ChatInboxLine) error {
	return postChatInbox(store, line)
}

func postChatInbox(store string, line ChatInboxLine) error {
	line.Text = clipInbox(line.Text)
	if line.Text == "" {
		return nil
	}
	if strings.TrimSpace(line.Kind) == "" {
		line.Kind = "info"
	}
	if line.At.IsZero() {
		line.At = time.Now()
	}
	if err := savePendingChatReply(store, line); err != nil {
		return err
	}
	payload, err := json.Marshal(line)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	path := chatInboxPath(store)
	inboxMu.Lock()
	defer inboxMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 256<<10 {
		// Rotate instead of deleting: a reader behind the rotation finishes
		// the old file before continuing, so unread replies survive.
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(payload)
	return err
}

// ReadChatInbox returns new messages after offset. The first call with primed
// false skips history already in the file so an open chat does not replay old lines.
func ReadChatInbox(store string, offset int64, session string, primed bool) (lines []ChatInboxLine, next int64, primedOut bool) {
	path := chatInboxPath(store)
	inboxMu.Lock()
	defer inboxMu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, true
	}
	if !primed {
		return nil, info.Size(), true
	}
	var buf []byte
	if offset > info.Size() {
		if old, readErr := readInboxFrom(path+".1", offset); readErr == nil {
			buf = old
		}
		offset = 0
	}
	if offset == info.Size() && len(buf) == 0 {
		return nil, offset, true
	}
	current, err := readInboxFrom(path, offset)
	if err != nil && len(buf) == 0 {
		return nil, offset, true
	}
	next = offset + int64(len(current))
	buf = append(buf, current...)
	for _, raw := range strings.Split(string(buf), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var line ChatInboxLine
		if json.Unmarshal([]byte(raw), &line) != nil || strings.TrimSpace(line.Text) == "" {
			continue
		}
		if line.Session != "" && session != "" && line.Session != session {
			continue
		}
		lines = append(lines, line)
	}
	return lines, next, true
}

func readInboxFrom(path string, offset int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if offset >= info.Size() {
		return nil, nil
	}
	if _, err = f.Seek(offset, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func clipInbox(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) <= maxInboxRunes {
		return text
	}
	return string([]rune(text)[:maxInboxRunes]) + "…"
}

func formatTaskRunNotice(title string, task Task, result string) string {
	name := clipRunField(oneLine(firstNonEmpty(task.Name, task.Prompt, "定时任务")), 36)
	line := title + "：" + name
	result = clipRunField(oneLine(result), 120)
	if result != "" {
		line += " — " + result
	}
	return line
}

func formatRunClock(task Task, finished time.Time) string {
	loc := time.Local
	if name := strings.TrimSpace(task.Timezone); name != "" && !strings.EqualFold(name, "local") {
		if loaded, err := time.LoadLocation(name); err == nil {
			loc = loaded
		}
	}
	if finished.IsZero() {
		finished = time.Now()
	}
	line := "完成于 " + finished.In(loc).Format("2006-01-02 15:04:05")
	if task.Status == StatusEnabled && task.Repeat != RepeatOnce && !task.RunAt.IsZero() {
		line += "，下次 " + task.RunAt.In(loc).Format("2006-01-02 15:04:05")
	}
	return line
}

func presentRunResult(result, reportPath string) string {
	if body := readDeliveredText(reportPath); body != "" {
		return clipDelivery(body, reportPath)
	}
	result = strings.TrimSpace(stripANSI(result))
	if result == "" || isBareReportPointer(result) {
		if path := strings.TrimSpace(reportPath); path != "" {
			return "完成，但没有可展示的正文。报告: " + path
		}
		return result
	}
	return clipDelivery(result, reportPath)
}

func readDeliveredText(reportPath string) string {
	reportPath = strings.TrimSpace(reportPath)
	if reportPath == "" {
		return ""
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return ""
	}
	return extractDeliveredText(string(data))
}

func extractDeliveredText(raw string) string {
	raw = stripANSI(raw)
	idx := strings.LastIndex(raw, "最终报告:")
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len("最终报告:"):]
	var b strings.Builder
	started := false
	for _, line := range strings.Split(rest, "\n") {
		trim := strings.TrimSpace(line)
		if isReportRule(trim) {
			if started {
				break
			}
			continue
		}
		if strings.HasPrefix(trim, "日志:") || strings.HasPrefix(trim, "📂") {
			break
		}
		if !started && trim == "" {
			continue
		}
		started = true
		b.WriteString(strings.TrimRight(line, " \t"))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func isReportRule(line string) bool {
	if len(line) < 8 || !strings.Contains(line, "=") {
		return false
	}
	return strings.Trim(line, "= ") == ""
}

func isBareReportPointer(result string) bool {
	result = strings.TrimSpace(result)
	if strings.Contains(result, "\n") {
		return false
	}
	return strings.HasPrefix(result, "Agent 任务完成") || strings.HasPrefix(result, "基础状态采集完成")
}

func clipDelivery(text, reportPath string) string {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) <= 1800 {
		return text
	}
	text = string([]rune(text)[:1800]) + "…"
	if path := strings.TrimSpace(reportPath); path != "" {
		text += "\n完整报告: " + path
	}
	return text
}

func stripANSI(text string) string {
	return ansiRE.ReplaceAllString(text, "")
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func clipRunField(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}
