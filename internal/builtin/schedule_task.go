package builtin

import (
	"fmt"
	"strings"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/scheduler"
)

func ScheduleTask(rt Runtime, args map[string]string) (string, error) {
	action := strings.ToLower(strings.TrimSpace(arg(args, "action")))
	if action == "" {
		action = "plan"
	}
	store := scheduler.NewStore(config.GlobalConfig.SchedulerStore)
	switch action {
	case "plan", "parse":
		plan, err := scheduler.PlanTask(schedulePlanInput(args), time.Now())
		if err != nil {
			return "", err
		}
		return formatSchedulePlan(plan, false), nil
	case "add", "create":
		plan, err := scheduler.PlanTask(schedulePlanInput(args), time.Now())
		if err != nil {
			return "", err
		}
		if err := validateScheduleCreate(plan, args); err != nil {
			return "", err
		}
		existing, created, err := store.AddUnique(plan.Task)
		if err != nil {
			return "", err
		}
		if !created {
			plan.Task = existing
			return "等价定时任务已存在，未重复创建。\n\n" + formatSchedulePlan(plan, true), nil
		}
		return formatSchedulePlan(plan, true), nil
	case "list":
		tasks, err := store.Load()
		if err != nil {
			return "", err
		}
		return formatScheduleList(tasks), nil
	case "show":
		id := arg(args, "id", "task_id")
		return scheduler.Command(store, "show "+id, time.Now())
	case "edit":
		edit, err := scheduleEdit(args)
		if err != nil {
			return "", err
		}
		task, err := store.Edit(arg(args, "id", "task_id"), edit, time.Now())
		if err != nil {
			return "", err
		}
		return "已修改定时任务。\n\n" + scheduler.FormatTask(task), nil
	case "pause", "cancel", "resume":
		id := arg(args, "id", "task_id")
		if id == "" {
			return "", fmt.Errorf("id 必填")
		}
		status := scheduler.StatusDisabled
		verb := "cancel"
		if action == "resume" {
			status = scheduler.StatusEnabled
			verb = "resume"
		}
		task, err := store.Find(id)
		if err != nil {
			return "", err
		}
		if err := store.SetStatus(task.ID, status); err != nil {
			return "", err
		}
		return "任务状态已更新：" + task.ID + " -> " + status + "；" + verb + " 停止或恢复后续触发，不撤销已开始的动作", nil
	case "reset":
		return scheduler.Command(store, "reset "+arg(args, "id", "task_id"), time.Now())
	case "remove", "delete":
		id := arg(args, "id", "task_id")
		if id == "" {
			return "", fmt.Errorf("id 必填")
		}
		task, err := store.Find(id)
		if err != nil {
			return "", err
		}
		removed, ok, err := store.Remove(task.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("未找到任务: %s", id)
		}
		return fmt.Sprintf("已删除定时任务: %s (%s)", removed.ID, removed.Name), nil
	case "run", "run-now":
		id := arg(args, "id", "task_id")
		if id == "" {
			return "", fmt.Errorf("id 必填")
		}
		r := scheduler.NewRunner(config.GlobalConfig)
		out, err := r.RunNow(id, time.Now())
		if err != nil {
			return "", err
		}
		return out, nil
	case "run-due", "tick":
		r := scheduler.NewRunner(config.GlobalConfig)
		out, err := r.RunDue(time.Now())
		if err != nil {
			return "", err
		}
		return out, nil
	default:
		return "", fmt.Errorf("action 仅支持 plan|add|list|show|edit|pause|cancel|resume|reset|remove|run|run-due")
	}
}

func schedulePlanInput(args map[string]string) scheduler.PlanInput {
	return scheduler.PlanInput{
		Text:         arg(args, "text", "natural", "request"),
		Prompt:       arg(args, "task", "prompt", "goal"),
		RunAt:        arg(args, "run_at", "time", "at"),
		Repeat:       arg(args, "repeat", "recurrence"),
		IntervalSec:  arg(args, "interval_sec", "every", "interval"),
		Notify:       arg(args, "notify", "notification"),
		Selector:     arg(args, "selector", "target", "targets"),
		Kind:         arg(args, "kind", "type"),
		Timezone:     firstNonEmptyLocal(arg(args, "timezone", "tz"), config.GlobalConfig.SchedulerTimezone),
		Report:       optionalBool(args, "report"),
		AllowBatch:   argBool(args, "allow_batch"),
		TimeoutSec:   args["timeout_sec"],
		ReplyText:    arg(args, "reply_text", "message", "say"),
		ReplySession: arg(args, "reply_session", "session_id"),
	}
}

func optionalBool(args map[string]string, key string) *bool {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	b := v == "1" || v == "true" || v == "yes" || v == "y" || v == "on" || v == "是"
	return &b
}

func validateScheduleCreate(plan scheduler.Plan, args map[string]string) error {
	task := plan.Task
	if !argBool(args, "confirm_create") {
		return fmt.Errorf("拒绝创建定时任务: add/create 必须显式提供 confirm_create=true；不确定时请先用 action=plan 预览")
	}
	if task.Kind != scheduler.KindAgent || task.ReplyText != "" {
		return nil
	}
	if reason := unsafeUnattendedAgentPrompt(task.Prompt); reason != "" {
		return fmt.Errorf("拒绝创建泛化 Agent 定时任务: %s。请改用 kind=inspection 做只读巡检，或先手动排查后再创建明确的维护任务", reason)
	}
	if !task.AllowBatch || !argBool(args, "confirm_unattended") {
		return fmt.Errorf("拒绝创建泛化 Agent 定时任务: 需要同时显式提供 allow_batch=true 和 confirm_unattended=true；巡检场景请使用 kind=inspection")
	}
	return nil
}

func unsafeUnattendedAgentPrompt(prompt string) string {
	return scheduler.UnattendedPromptRisk(prompt)
}

func scheduleEdit(args map[string]string) (scheduler.TaskEdit, error) {
	edit := scheduler.TaskEdit{}
	if v := arg(args, "task", "prompt"); v != "" {
		edit.Prompt = &v
	}
	if v := arg(args, "name"); v != "" {
		edit.Name = &v
	}
	if v := arg(args, "interval_sec", "every", "interval"); v != "" {
		sec, err := scheduler.ParseIntervalSpec(v)
		if err != nil {
			return scheduler.TaskEdit{}, err
		}
		edit.IntervalSec = &sec
	}
	if v := strings.TrimSpace(args["timeout_sec"]); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err != nil || n < 1 || n > 604800 {
			return scheduler.TaskEdit{}, fmt.Errorf("timeout_sec must be 1..604800")
		}
		edit.TimeoutSec = &n
	}
	if v := arg(args, "notify", "notification"); v != "" {
		edit.Notify = &v
	}
	return edit, nil
}

func firstNonEmptyLocal(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func formatSchedulePlan(plan scheduler.Plan, saved bool) string {
	task := plan.Task
	status := "解析完成，尚未写入"
	if saved {
		status = "已写入定时任务"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s\n", status))
	b.WriteString(fmt.Sprintf("- ID: %s\n", task.ID))
	b.WriteString(fmt.Sprintf("- 名称: %s\n", task.Name))
	b.WriteString(fmt.Sprintf("- 类型: %s\n", task.Kind))
	b.WriteString(fmt.Sprintf("- 执行时间: %s (%s)\n", task.RunAt.Format("2006-01-02 15:04:05"), task.Timezone))
	repeat := task.Repeat
	if task.Repeat == scheduler.RepeatInterval && task.IntervalSec > 0 {
		repeat = fmt.Sprintf("interval（每 %d 秒）", task.IntervalSec)
	}
	b.WriteString(fmt.Sprintf("- 重复: %s\n", repeat))
	b.WriteString(fmt.Sprintf("- 目标: %s\n", emptyDefault(task.Selector, "当前目标/all")))
	b.WriteString(fmt.Sprintf("- 报告: %v\n", task.Report))
	b.WriteString(fmt.Sprintf("- 通知: %s\n", task.Notify))
	if len(plan.Notes) > 0 {
		b.WriteString("\n说明:\n")
		for _, note := range plan.Notes {
			b.WriteString("- " + note + "\n")
		}
	}
	if saved {
		b.WriteString(fmt.Sprintf("\n管理: /schedule show %s\n", task.ID))
		b.WriteString("也可以 /schedule edit、cancel、reset、delete，ID 可用能唯一匹配的前缀。\n")
	}
	return b.String()
}

func formatScheduleList(tasks []scheduler.Task) string {
	if len(tasks) == 0 {
		return "无定时任务"
	}
	var b strings.Builder
	b.WriteString("定时任务列表\n")
	for _, task := range tasks {
		repeat := task.Repeat
		if task.Repeat == scheduler.RepeatInterval && task.IntervalSec > 0 {
			repeat = fmt.Sprintf("每 %d 秒", task.IntervalSec)
		}
		b.WriteString(fmt.Sprintf("- %s [%s] %s next=%s repeat=%s kind=%s notify=%s runs=%d fail=%d\n",
			task.ID, task.Status, task.Name, task.RunAt.Format("2006-01-02 15:04:05"), repeat, task.Kind, task.Notify, task.RunCount, task.FailureCount))
		if task.LastResult != "" {
			b.WriteString(fmt.Sprintf("  last: %s\n", task.LastResult))
		}
	}
	return b.String()
}
