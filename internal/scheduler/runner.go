package scheduler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/inspection"
)

type Runner struct {
	Store      *Store
	Config     config.Config
	ConfigPath string
	Logf       func(string, ...any)
	mu         sync.Mutex
	slots      chan struct{}
	execute    func(context.Context, Task, time.Time) (string, string, error)
}

func NewRunner(cfg config.Config) *Runner {
	return &Runner{
		Store:  NewStore(cfg.SchedulerStore),
		Config: cfg,
	}
}

func (r *Runner) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		go r.RunDueContext(ctx, time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				go r.RunDueContext(ctx, now)
			}
		}
	}()
}
func (r *Runner) prepare() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Store == nil {
		r.Store = NewStore(r.Config.SchedulerStore)
	}
	if r.slots == nil {
		r.slots = make(chan struct{}, 3)
	}
}
func (r *Runner) RunDue(now time.Time) (string, error) {
	return r.RunDueContext(context.Background(), now)
}
func (r *Runner) RunDueContext(ctx context.Context, now time.Time) (string, error) {
	r.prepare()
	if now.IsZero() {
		now = time.Now()
	}
	due, err := r.Store.Due(now)
	if err != nil {
		return "", err
	}
	results := make(chan string, len(due))
	var wg sync.WaitGroup
	for _, task := range due {
		if ctx.Err() != nil {
			break
		}
		select {
		case r.slots <- struct{}{}:
		default:
			continue
		}
		wg.Add(1)
		go func(task Task) {
			defer wg.Done()
			defer func() { <-r.slots }()
			out, e := r.runClaimed(ctx, task.ID, now, false)
			if e != nil {
				out = e.Error()
			}
			results <- out
		}(task)
	}
	wg.Wait()
	close(results)
	var lines []string
	for out := range results {
		if out != "" {
			lines = append(lines, out)
		}
	}
	if len(lines) == 0 {
		return "无可执行到期任务（未到期、已运行或执行槽占用）", nil
	}
	return strings.Join(lines, "\n"), nil
}
func (r *Runner) RunNow(id string, now time.Time) (string, error) {
	r.prepare()
	if now.IsZero() {
		now = time.Now()
	}
	return r.runClaimed(context.Background(), id, now, true)
}
func (r *Runner) runClaimed(ctx context.Context, id string, now time.Time, manual bool) (string, error) {
	// Hash IDs instead of using user-controlled IDs as path components.
	sum := sha256.Sum256([]byte(id))
	release, ok, err := acquireRunLock(fmt.Sprintf("%s.%x", r.Store.Path, sum[:8]), now)
	if err != nil {
		return "", err
	}
	if !ok {
		return "任务已在另一执行器运行", nil
	}
	defer release()

	if err := ctx.Err(); err != nil {
		return "", err
	}
	task, claimed, err := r.Store.Claim(id, now, manual)
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", nil
	}
	updated := r.runOne(ctx, task, now)
	if err = r.Store.CompleteRun(updated); err != nil {
		return "", err
	}
	return updated.LastResult, nil
}

const staleRunLockAfter = 6 * time.Hour

func acquireRunLock(storePath string, now time.Time) (func(), bool, error) {
	if strings.TrimSpace(storePath) == "" {
		storePath = DefaultStorePath
	}
	path := storePath + ".run.lock"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err = tryScheduleLock(f); err != nil {
		f.Close()
		return func() {}, false, nil
	}
	var once sync.Once
	return func() { once.Do(func() { releaseScheduleLock(f); f.Close() }) }, true, nil
}

func (r *Runner) runOne(ctx context.Context, task Task, now time.Time) Task {
	timeout := task.TimeoutSec
	if timeout <= 0 {
		timeout = 7200
	}
	if timeout > 604800 {
		timeout = 604800
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if now.IsZero() {
		now = time.Now()
	}
	task.UpdatedAt = now
	reportPath := ""
	result := ""
	var err error
	replyText, isReply := directChatReply(task)
	if isReply {
		if postErr := postChatInbox(r.Store.Path, ChatInboxLine{TaskID: task.ID, Session: task.ReplySession, Kind: "result", Text: replyText, At: now}); postErr != nil {
			err = postErr
		} else {
			result = "已发送到当前聊天: " + replyText
		}
	} else if r.execute != nil {
		reportPath, result, err = r.execute(ctx, task, now)
	} else {
		switch task.Kind {
		case KindInspection:
			reportPath, result, err = r.executeInspection(ctx, task, now)
		case KindAgent:
			reportPath, result, err = r.executeAgent(ctx, task, now)
		default:
			err = fmt.Errorf("未知任务类型: %s", task.Kind)
		}
	}
	if reportPath != "" {
		task.LastReportPath = reportPath
	}
	if err != nil {
		task.LastResult = "失败: " + err.Error()
	} else {
		task.LastResult = result
	}
	if task.Notify != NotifyNone && reportPath != "" {
		task.LastResult += r.notifyTask(task, reportPath)
	}
	task.Status = StatusEnabled
	if err != nil {
		task.FailureCount++
	} else {
		task.FailureCount = 0
	}
	finished := time.Now()
	if finished.Before(now) {
		finished = now
	}
	task.RunAt = nextRun(task, finished)
	if task.Repeat == RepeatOnce {
		task.Status = StatusCompleted
		if err != nil {
			task.Status = StatusFailed
		}
	}
	if task.FailureCount >= 3 && task.Repeat != RepeatOnce {
		task.Status = StatusDisabled
		task.LastResult += "；连续失败三次，已暂停"
	}
	if !isReply {
		if err != nil {
			r.postRunNotice(task, "error", formatTaskRunNotice("未完成", task, task.LastResult))
		} else if body := presentRunResult(result, reportPath); body != "" {
			r.postRunNotice(task, "result", body)
		}
	}
	r.postRunNotice(task, "info", formatRunClock(task, finished))
	return task
}

func (r *Runner) postRunNotice(task Task, kind, text string) {
	if r == nil || r.Store == nil || strings.TrimSpace(text) == "" {
		return
	}
	err := postChatInbox(r.Store.Path, ChatInboxLine{
		TaskID:  task.ID,
		Session: task.ReplySession,
		Kind:    kind,
		Text:    text,
		At:      time.Now(),
	})
	if err != nil && r.Logf != nil {
		r.Logf("schedule chat notice: %v", err)
	}
}

func (r *Runner) executeInspection(ctx context.Context, task Task, now time.Time) (string, string, error) {
	if len(r.Config.Inspection.Devices) > 0 {
		report, err := inspection.Run(ctx, r.Config, task.Selector)
		return report.Markdown, "巡检报告：" + report.Markdown + "；Word：" + report.Word, err
	}
	command := inspectionCommand()
	failures := 0
	var b strings.Builder
	b.WriteString("# DeepSentry 定时巡检报告\n\n")
	b.WriteString(fmt.Sprintf("- 任务: %s\n", task.Name))
	b.WriteString(fmt.Sprintf("- ID: %s\n", task.ID))
	b.WriteString(fmt.Sprintf("- 时间: %s\n", now.Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("- 类型: %s\n", task.Kind))
	b.WriteString(fmt.Sprintf("- 目标选择器: %s\n", emptyDefault(task.Selector, "当前目标/all")))
	b.WriteString("\n---\n\n")

	if len(r.Config.Targets) > 0 {
		selector := task.Selector
		if strings.TrimSpace(selector) == "" {
			selector = "all"
		}
		selected := executor.MatchTargets(r.Config.Targets, selector)
		if len(selected) == 0 {
			return "", "", fmt.Errorf("没有匹配的巡检目标")
		}
		for _, t := range selected {
			if t.DeviceType != "" && t.DeviceType != "linux" {
				return "", "", fmt.Errorf("设备 %s 需要 inspection.devices 检查项；禁止将 Linux 命令用于网络设备", t.Name)
			}
		}
		results := executor.RunFleet(selected, "all", command, 3)
		for _, item := range results {
			if !item.Success || strings.TrimSpace(item.Output) == "" {
				failures++
			}
		}
		b.WriteString(executor.FormatFleetResults(results))
	} else if executor.Current != nil {
		out, err := executor.Current.Run(command)
		b.WriteString("## 当前目标\n\n")
		b.WriteString("```text\n")
		b.WriteString(truncateReport(out, 24000))
		b.WriteString("\n```\n")
		if err != nil {
			failures++
			b.WriteString(fmt.Sprintf("\n执行错误: %v\n", err))
		}
	} else {
		return "", "", fmt.Errorf("执行器未初始化")
	}

	reportPath, err := writeScheduleReport(task.ID, now, b.String())
	if err != nil {
		return "", "", err
	}
	if failures > 0 {
		return reportPath, "", fmt.Errorf("%d 个目标采集失败；报告保留失败详情", failures)
	}
	return reportPath, "基础状态采集完成（未配置设备判定规则，需复核），报告: " + reportPath, nil
}

func (r *Runner) executeAgent(ctx context.Context, task Task, now time.Time) (string, string, error) {
	if !task.AllowBatch {
		return "", "", fmt.Errorf("泛化 Agent 定时任务需要 allow_batch=true；巡检场景请使用 kind=inspection")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	args := []string{"--no-tui", "--quiet", "--batch", "-y", "--task", "这是已到期调度的本次执行。直接执行下面任务，时间规则仅为原始计划；不要重复创建定时任务。最终报告只写要给用户看的正文，这段会直接出现在聊天里，不要写报告路径或过程说明。\n" + task.Prompt}
	if strings.TrimSpace(r.ConfigPath) != "" {
		args = append([]string{"-c", r.ConfigPath}, args...)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	configureScheduleCommand(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = os.Environ()
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	content := fmt.Sprintf("# DeepSentry 定时 Agent 任务报告\n\n- 任务: %s\n- ID: %s\n- 时间: %s\n\n```text\n%s\n```\n",
		task.Name, task.ID, now.Format("2006-01-02 15:04:05"), truncateReport(out.String(), 30000))
	reportPath, writeErr := writeScheduleReport(task.ID, now, content)
	if writeErr != nil {
		return "", "", writeErr
	}
	body := extractDeliveredText(out.String())
	if err != nil {
		if body == "" {
			return reportPath, "", fmt.Errorf("agent batch 执行失败: %w", err)
		}
		return reportPath, body, fmt.Errorf("agent batch 执行失败: %w", err)
	}
	if body == "" {
		return reportPath, "", fmt.Errorf("agent 已结束，但最终报告没有可展示的正文。报告: %s", reportPath)
	}
	return reportPath, body, nil
}

func (r *Runner) notifyTask(task Task, reportPath string) string {
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return "；通知失败: " + err.Error()
	}
	title := "DeepSentry 定时任务: " + task.Name
	text := string(data)
	if strings.TrimSpace(text) == "" {
		text = title
	}
	channels := NotifyChannels(task.Notify)
	if len(channels) == 0 {
		return ""
	}
	results := make([]string, 0, len(channels))
	for _, ch := range channels {
		label, notifyErr := r.sendNotification(ch, title, text)
		if notifyErr != nil {
			results = append(results, fmt.Sprintf("%s通知失败: %v", label, notifyErr))
		} else {
			results = append(results, label+"已通知")
		}
	}
	if len(results) == 0 {
		return ""
	}
	return "；" + strings.Join(results, "；")
}

func (r *Runner) sendNotification(channel, title, text string) (string, error) {
	switch channel {
	case NotifyDingTalk:
		return "钉钉", SendDingTalkMarkdown(r.Config.DingTalkWebhook, r.Config.DingTalkSecret, title, text)
	case NotifyFeishu:
		return "飞书", SendFeishuMarkdown(r.Config.FeishuWebhook, r.Config.FeishuSecret, title, text)
	case NotifyEmail:
		return "邮件", SendEmailGatewayMarkdown(r.Config.EmailGatewayURL, r.Config.EmailGatewayToken, r.Config.EmailGatewayHeader, r.Config.EmailTo, r.Config.EmailFrom, title, text)
	default:
		return channel, fmt.Errorf("未知通知通道: %s", channel)
	}
}

func nextRun(task Task, now time.Time) time.Time {
	next := task.RunAt
	switch task.Repeat {
	case RepeatDaily:
		for !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
	case RepeatWeekly:
		for !next.After(now) {
			next = next.AddDate(0, 0, 7)
		}
	case RepeatInterval:
		sec := task.IntervalSec
		if sec <= 0 {
			sec = 3600
		}
		d := time.Duration(sec) * time.Second
		if !next.After(now) {
			next = next.Add((now.Sub(next)/d + 1) * d)
		}
	}
	return next
}

func writeScheduleReport(id string, now time.Time, content string) (string, error) {
	dir := filepath.Join("reports", "schedules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("report_%s_%s.md", id, now.Format("20060102_150405")))
	return path, os.WriteFile(path, []byte(content), 0600)
}

func inspectionCommand() string {
	return `printf '== HOST ==\n'; (hostname 2>/dev/null || uname -n 2>/dev/null || true); date 2>/dev/null || true; uptime 2>/dev/null || true; printf '\n== DISK ==\n'; df -h 2>/dev/null || true; printf '\n== MEMORY ==\n'; (free -m 2>/dev/null || cat /proc/meminfo 2>/dev/null | head -40 || true); printf '\n== LISTEN ==\n'; (ss -lntup 2>/dev/null || netstat -lntup 2>/dev/null || cat /proc/net/tcp 2>/dev/null | head -30 || true); printf '\n== TOP PROCESS ==\n'; (ps aux --sort=-%cpu 2>/dev/null | head -15 || ps -ef 2>/dev/null | head -15 || true)`
}

func truncateReport(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return safeTextPrefix(s, max) + "\n...(报告内容过长已截断)..."
}

func safeTextPrefix(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end]
}

func emptyDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

type boundedOutput struct {
	data []byte
	mu   sync.Mutex
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	const limit = 65536
	if len(p) >= limit {
		b.data = append(b.data[:0], p[n-limit:]...)
	} else {
		b.data = append(b.data, p...)
		if len(b.data) > limit {
			b.data = append([]byte(nil), b.data[len(b.data)-limit:]...)
		}
	}
	return n, nil
}
func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.data), "�")
}
