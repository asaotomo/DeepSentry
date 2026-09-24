package scheduler

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type TaskEdit struct {
	Name        *string
	Prompt      *string
	IntervalSec *int
	TimeoutSec  *int
	Notify      *string
}

func Command(store *Store, line string, now time.Time) (string, error) {
	if store == nil {
		return "", fmt.Errorf("定时任务存储不可用")
	}
	if now.IsZero() {
		now = time.Now()
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return formatCommandList(store)
	}
	fields := strings.Fields(line)
	action := strings.ToLower(fields[0])
	rest := strings.TrimSpace(line[len(fields[0]):])
	switch action {
	case "list", "ls", "查看", "列表":
		return formatCommandList(store)
	case "help", "帮助", "?":
		return scheduleCommandHelp, nil
	case "show", "详情":
		task, err := store.find(rest)
		if err != nil {
			return "", err
		}
		return formatTaskDetail(task), nil
	case "edit", "modify", "update", "修改", "编辑":
		id, edit, err := parseEdit(rest)
		if err != nil {
			return "", err
		}
		task, err := store.Edit(id, edit, now)
		if err != nil {
			return "", err
		}
		return "已修改定时任务。\n\n" + formatTaskDetail(task), nil
	case "cancel", "pause", "取消":
		task, err := store.find(rest)
		if err != nil {
			return "", err
		}
		if err := store.SetStatus(task.ID, StatusDisabled); err != nil {
			return "", err
		}
		return fmt.Sprintf("已取消后续触发: %s (%s)。正在进行的这一轮不会被撤销。", task.ID, task.Name), nil
	case "resume", "恢复":
		task, err := store.find(rest)
		if err != nil {
			return "", err
		}
		if err := store.SetStatus(task.ID, StatusEnabled); err != nil {
			return "", err
		}
		return fmt.Sprintf("已恢复定时任务: %s (%s)", task.ID, task.Name), nil
	case "reset", "重置":
		task, err := store.Reset(rest, now)
		if err != nil {
			return "", err
		}
		note := "已清除失败次数，并重新安排下次执行。已经做过的事不会撤销。"
		if task.Repeat == RepeatOnce {
			note = "一次性任务已重新安排为到期。已经做过的事不会撤销，需要时请再核对结果。"
		}
		return note + "\n\n" + formatTaskDetail(task), nil
	case "delete", "rm", "remove", "删除":
		task, err := store.find(rest)
		if err != nil {
			return "", err
		}
		removed, ok, err := store.Remove(task.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("未找到任务: %s", task.ID)
		}
		return fmt.Sprintf("已删除定时任务: %s (%s)。如果这一轮已经开始，它不会被中断，但不会再安排下一次。", removed.ID, removed.Name), nil
	default:
		return "", fmt.Errorf("未知操作 %q\n%s", action, scheduleCommandHelp)
	}
}

const scheduleCommandHelp = `/schedule                         查看全部定时任务
/schedule show <id>               查看详情
/schedule edit <id> prompt=... every=1m name=... timeout=120 notify=none
/schedule cancel <id>             取消后续触发
/schedule resume <id>             恢复已暂停的周期任务
/schedule reset <id>              清除失败并重新安排
/schedule delete <id>             删除
id 可以是完整 ID，或能唯一匹配的前缀、名称。周期最短 60 秒。`

func (s *Store) Find(query string) (Task, error) {
	return s.find(query)
}

func FormatTask(task Task) string {
	return formatTaskDetail(task)
}

func (s *Store) find(query string) (Task, error) {
	tasks, err := s.Load()
	if err != nil {
		return Task{}, err
	}
	return resolveTask(tasks, query)
}

func resolveTask(tasks []Task, query string) (Task, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Task{}, fmt.Errorf("请提供任务 ID")
	}
	var exact, prefix, named []Task
	for _, task := range tasks {
		if task.ID == query {
			exact = append(exact, task)
		}
		if strings.HasPrefix(task.ID, query) {
			prefix = append(prefix, task)
		}
		if task.Name == query {
			named = append(named, task)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(prefix) == 1:
		return prefix[0], nil
	case len(prefix) > 1:
		return Task{}, fmt.Errorf("ID 前缀对应多个任务:\n%s", briefTasks(prefix))
	case len(named) == 1:
		return named[0], nil
	case len(named) > 1:
		return Task{}, fmt.Errorf("名称对应多个任务:\n%s", briefTasks(named))
	default:
		return Task{}, fmt.Errorf("未找到任务: %s", query)
	}
}

func (s *Store) Edit(query string, edit TaskEdit, now time.Time) (Task, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if edit.Name == nil && edit.Prompt == nil && edit.IntervalSec == nil && edit.TimeoutSec == nil && edit.Notify == nil {
		return Task{}, fmt.Errorf("请指定要修改的字段")
	}
	current, err := s.find(query)
	if err != nil {
		return Task{}, err
	}
	release, err := holdTaskRun(s.Path, current.ID)
	if err != nil {
		return Task{}, err
	}
	defer release()
	return s.mutate(current.ID, func(task *Task) error {
		if edit.Prompt != nil {
			prompt := strings.TrimSpace(*edit.Prompt)
			if prompt == "" {
				return fmt.Errorf("任务内容不能为空")
			}
			if utf8.RuneCountInString(prompt) > 8000 {
				return fmt.Errorf("任务内容超过 8000 字")
			}
			if task.Kind == KindAgent && task.AllowBatch {
				if reason := UnattendedPromptRisk(prompt); reason != "" {
					return fmt.Errorf("拒绝修改: %s", reason)
				}
			}
			task.Prompt = prompt
			if edit.Name == nil {
				task.Name = shortName(prompt, 28)
			}
		}
		if edit.Name != nil {
			name := strings.TrimSpace(*edit.Name)
			if name == "" || utf8.RuneCountInString(name) > 80 {
				return fmt.Errorf("名称需要 1 到 80 个字")
			}
			task.Name = name
		}
		if edit.IntervalSec != nil {
			sec := *edit.IntervalSec
			if sec < minIntervalSec || sec > maxIntervalSec {
				return fmt.Errorf("周期必须在 60 秒到 7 天之间")
			}
			task.Repeat = RepeatInterval
			task.IntervalSec = sec
			task.Weekday = 0
			task.RunAt = now.Add(time.Duration(sec) * time.Second)
		}
		if edit.TimeoutSec != nil {
			n := *edit.TimeoutSec
			if n < 1 || n > maxIntervalSec {
				return fmt.Errorf("timeout 必须是 1..604800 秒")
			}
			task.TimeoutSec = n
		}
		if edit.Notify != nil {
			task.Notify = normalizeNotify(*edit.Notify)
		}
		if task.Status == StatusRunning {
			task.Status = StatusEnabled
		}
		task.UpdatedAt = now
		return nil
	})
}

func (s *Store) Reset(query string, now time.Time) (Task, error) {
	if now.IsZero() {
		now = time.Now()
	}
	current, err := s.find(query)
	if err != nil {
		return Task{}, err
	}
	release, err := holdTaskRun(s.Path, current.ID)
	if err != nil {
		return Task{}, err
	}
	defer release()
	return s.mutate(current.ID, func(task *Task) error {
		task.FailureCount = 0
		task.Status = StatusEnabled
		task.UpdatedAt = now
		switch task.Repeat {
		case RepeatOnce:
			task.RunCount = 0
			task.LastRunAt = nil
			task.RunAt = now
		case RepeatInterval:
			task.RunAt = now
		default:
			if !task.RunAt.After(now) {
				task.RunAt = nextRun(*task, now)
			}
		}
		return nil
	})
}

func (s *Store) mutate(id string, fn func(*Task) error) (Task, error) {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockStore()
	if err != nil {
		return Task{}, err
	}
	defer release()
	tasks, err := s.loadUnlocked()
	if err != nil {
		return Task{}, err
	}
	for i := range tasks {
		if tasks[i].ID != id {
			continue
		}
		if err := fn(&tasks[i]); err != nil {
			return Task{}, err
		}
		if err := s.saveUnlocked(tasks); err != nil {
			return Task{}, err
		}
		return tasks[i], nil
	}
	return Task{}, fmt.Errorf("未找到任务: %s", id)
}

func holdTaskRun(storePath, id string) (func(), error) {
	sum := sha256.Sum256([]byte(id))
	release, ok, err := acquireRunLock(fmt.Sprintf("%s.%x", storePath, sum[:8]), time.Now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("任务仍在运行，不能修改或重置")
	}
	return release, nil
}

func parseEdit(rest string) (string, TaskEdit, error) {
	tokens := strings.Fields(rest)
	if len(tokens) == 0 {
		return "", TaskEdit{}, fmt.Errorf("用法: /schedule edit <id> prompt=... every=1m")
	}
	edit := TaskEdit{}
	for i := 1; i < len(tokens); i++ {
		key, val, ok := strings.Cut(tokens[i], "=")
		if !ok || strings.TrimSpace(key) == "" {
			return "", TaskEdit{}, fmt.Errorf("参数 %q 需要写成 key=value", tokens[i])
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "prompt" || key == "task" || key == "内容" {
			parts := []string{val}
			for i+1 < len(tokens) {
				nextKey, _, isAssign := strings.Cut(tokens[i+1], "=")
				if isAssign && knownEditKey(nextKey) {
					break
				}
				i++
				parts = append(parts, tokens[i])
			}
			joined := strings.TrimSpace(strings.Join(parts, " "))
			edit.Prompt = &joined
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "name", "名称":
			edit.Name = &val
		case "every", "interval", "interval_sec", "周期":
			sec, err := ParseIntervalSpec(val)
			if err != nil {
				return "", TaskEdit{}, err
			}
			edit.IntervalSec = &sec
		case "timeout", "timeout_sec", "时限":
			var n int
			if _, err := fmt.Sscan(val, &n); err != nil || n < 1 || n > maxIntervalSec {
				return "", TaskEdit{}, fmt.Errorf("timeout 必须是 1..604800 秒")
			}
			edit.TimeoutSec = &n
		case "notify", "通知":
			edit.Notify = &val
		default:
			return "", TaskEdit{}, fmt.Errorf("不支持的字段 %s", key)
		}
	}
	if edit.Name == nil && edit.Prompt == nil && edit.IntervalSec == nil && edit.TimeoutSec == nil && edit.Notify == nil {
		return "", TaskEdit{}, fmt.Errorf("请指定要修改的字段，例如 prompt=... every=1m")
	}
	return tokens[0], edit, nil
}

func knownEditKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "name", "名称", "prompt", "task", "内容", "every", "interval", "interval_sec", "周期", "timeout", "timeout_sec", "时限", "notify", "通知":
		return true
	default:
		return false
	}
}

func formatCommandList(store *Store) (string, error) {
	tasks, err := store.Load()
	if err != nil {
		return "", err
	}
	if len(tasks) == 0 {
		return "还没有定时任务。在对话里说明要做什么、多久做一次，模型会创建；之后用 /schedule 查看。", nil
	}
	var b strings.Builder
	b.WriteString("定时任务\n")
	for _, task := range tasks {
		b.WriteString(briefTaskLine(task))
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(scheduleCommandHelp)
	return b.String(), nil
}

func briefTasks(tasks []Task) string {
	var b strings.Builder
	for _, task := range tasks {
		b.WriteString(briefTaskLine(task))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func briefTaskLine(task Task) string {
	return fmt.Sprintf("- %s [%s] %s next=%s repeat=%s runs=%d fail=%d", task.ID, task.Status, task.Name, task.RunAt.Format("2006-01-02 15:04:05"), repeatLabel(task), task.RunCount, task.FailureCount)
}

func formatTaskDetail(task Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ID: %s\n", task.ID)
	fmt.Fprintf(&b, "名称: %s\n", task.Name)
	fmt.Fprintf(&b, "状态: %s\n", task.Status)
	fmt.Fprintf(&b, "类型: %s\n", task.Kind)
	fmt.Fprintf(&b, "下次: %s (%s)\n", task.RunAt.Format("2006-01-02 15:04:05"), task.Timezone)
	fmt.Fprintf(&b, "重复: %s\n", repeatLabel(task))
	fmt.Fprintf(&b, "已运行: %d  失败: %d\n", task.RunCount, task.FailureCount)
	if task.TimeoutSec > 0 {
		fmt.Fprintf(&b, "单次时限: %d 秒\n", task.TimeoutSec)
	}
	fmt.Fprintf(&b, "通知: %s\n", task.Notify)
	fmt.Fprintf(&b, "任务:\n%s\n", task.Prompt)
	if task.LastResult != "" {
		fmt.Fprintf(&b, "上次结果: %s\n", shortName(task.LastResult, 400))
	}
	return b.String()
}

func StatusBarText(tasks []Task, now time.Time) string {
	var enabled, running, paused, failed int
	var only *Task
	for i := range tasks {
		task := &tasks[i]
		switch task.Status {
		case StatusEnabled:
			enabled++
		case StatusRunning:
			running++
		case StatusDisabled:
			paused++
		case StatusFailed:
			failed++
		default:
			continue
		}
		only = task
	}
	total := enabled + running + paused + failed
	if total == 0 {
		return ""
	}
	var text string
	switch {
	case running == total:
		text = fmt.Sprintf("定时 %d 执行中", total)
	case enabled == total:
		text = fmt.Sprintf("定时 %d 启用", total)
	case paused == total:
		text = fmt.Sprintf("定时 %d 暂停", total)
	case failed == total:
		text = fmt.Sprintf("定时 %d 失败", total)
	default:
		parts := make([]string, 0, 4)
		if running > 0 {
			parts = append(parts, fmt.Sprintf("%d 执行中", running))
		}
		if enabled > 0 {
			parts = append(parts, fmt.Sprintf("%d 启用", enabled))
		}
		if paused > 0 {
			parts = append(parts, fmt.Sprintf("%d 暂停", paused))
		}
		if failed > 0 {
			parts = append(parts, fmt.Sprintf("%d 失败", failed))
		}
		text = fmt.Sprintf("定时 %d：%s", total, strings.Join(parts, " "))
	}
	if total == 1 {
		if next := formatStatusNext(only, now); next != "" {
			text += " · 下次 " + next
		}
	}
	return text
}

func formatStatusNext(task *Task, now time.Time) string {
	if task == nil || task.RunAt.IsZero() || !task.RunAt.After(now) {
		return ""
	}
	if task.Status != StatusEnabled && task.Status != StatusRunning {
		return ""
	}
	loc := now.Location()
	if name := strings.TrimSpace(task.Timezone); name != "" && !strings.EqualFold(name, "local") {
		if loaded, err := time.LoadLocation(name); err == nil {
			loc = loaded
		}
	}
	at := task.RunAt.In(loc)
	nowIn := now.In(loc)
	if at.Year() == nowIn.Year() && at.YearDay() == nowIn.YearDay() {
		return at.Format("15:04")
	}
	return at.Format("01-02 15:04")
}

func repeatLabel(task Task) string {
	switch task.Repeat {
	case RepeatInterval:
		if task.IntervalSec > 0 {
			return fmt.Sprintf("每 %d 秒", task.IntervalSec)
		}
		return "周期"
	case RepeatDaily:
		return "每天"
	case RepeatWeekly:
		return "每周"
	case RepeatOnce, "":
		return "一次"
	default:
		return task.Repeat
	}
}
