package builtin

import (
	"strings"
	"testing"

	"ai-edr/internal/config"
	"ai-edr/internal/scheduler"
)

func TestScheduleTaskPlan(t *testing.T) {
	oldStore := config.GlobalConfig.SchedulerStore
	oldTZ := config.GlobalConfig.SchedulerTimezone
	config.GlobalConfig.SchedulerStore = t.TempDir() + "/tasks.json"
	config.GlobalConfig.SchedulerTimezone = "Asia/Shanghai"
	defer func() {
		config.GlobalConfig.SchedulerStore = oldStore
		config.GlobalConfig.SchedulerTimezone = oldTZ
	}()

	out, err := ScheduleTask(NewRuntime("linux", false), map[string]string{
		"action": "plan",
		"task":   "巡检服务器并生成报告",
		"run_at": "明天9点",
		"kind":   "inspection",
		"notify": "dingtalk",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "解析完成") || !strings.Contains(out, "dingtalk") || !strings.Contains(out, "inspection") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestScheduleTaskRejectsUnsafeAgentCreate(t *testing.T) {
	oldStore := config.GlobalConfig.SchedulerStore
	oldTZ := config.GlobalConfig.SchedulerTimezone
	config.GlobalConfig.SchedulerStore = t.TempDir() + "/tasks.json"
	config.GlobalConfig.SchedulerTimezone = "Asia/Shanghai"
	defer func() {
		config.GlobalConfig.SchedulerStore = oldStore
		config.GlobalConfig.SchedulerTimezone = oldTZ
	}()

	_, err := ScheduleTask(NewRuntime("linux", false), map[string]string{
		"action":         "add",
		"text":           "攻击者似乎篡改了系统命令，导致该命令一旦运行就会执行恶意回连的动作。提交回连的IP和端口，例如：1.1.1.1:1111",
		"run_at":         "2099-01-01 09:00",
		"confirm_create": "true",
	})
	if err == nil || !strings.Contains(err.Error(), "回连") {
		t.Fatalf("expected unsafe callback prompt to be rejected, got %v", err)
	}
	tasks, loadErr := scheduler.NewStore(config.GlobalConfig.SchedulerStore).Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("unsafe prompt should not be persisted: %#v", tasks)
	}
}

func TestScheduleTaskRejectsAgentCreateWithoutUnattendedConfirmation(t *testing.T) {
	oldStore := config.GlobalConfig.SchedulerStore
	oldTZ := config.GlobalConfig.SchedulerTimezone
	config.GlobalConfig.SchedulerStore = t.TempDir() + "/tasks.json"
	config.GlobalConfig.SchedulerTimezone = "Asia/Shanghai"
	defer func() {
		config.GlobalConfig.SchedulerStore = oldStore
		config.GlobalConfig.SchedulerTimezone = oldTZ
	}()

	_, err := ScheduleTask(NewRuntime("linux", false), map[string]string{
		"action":         "add",
		"text":           "整理今天的日志并总结异常",
		"run_at":         "2099-01-01 09:00",
		"kind":           "agent",
		"allow_batch":    "true",
		"confirm_create": "true",
	})
	if err == nil || !strings.Contains(err.Error(), "confirm_unattended") {
		t.Fatalf("expected missing confirm_unattended rejection, got %v", err)
	}
}

func TestScheduleTaskAllowsInspectionCreate(t *testing.T) {
	oldStore := config.GlobalConfig.SchedulerStore
	oldTZ := config.GlobalConfig.SchedulerTimezone
	config.GlobalConfig.SchedulerStore = t.TempDir() + "/tasks.json"
	config.GlobalConfig.SchedulerTimezone = "Asia/Shanghai"
	defer func() {
		config.GlobalConfig.SchedulerStore = oldStore
		config.GlobalConfig.SchedulerTimezone = oldTZ
	}()

	out, err := ScheduleTask(NewRuntime("linux", false), map[string]string{
		"action":         "add",
		"text":           "巡检服务器并生成报告",
		"run_at":         "2099-01-01 09:00",
		"kind":           "inspection",
		"confirm_create": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已写入定时任务") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestScheduleTaskChatReplyDoesNotNeedUnattendedAgent(t *testing.T) {
	oldStore := config.GlobalConfig.SchedulerStore
	oldTZ := config.GlobalConfig.SchedulerTimezone
	config.GlobalConfig.SchedulerStore = t.TempDir() + "/tasks.json"
	config.GlobalConfig.SchedulerTimezone = "Asia/Shanghai"
	defer func() {
		config.GlobalConfig.SchedulerStore = oldStore
		config.GlobalConfig.SchedulerTimezone = oldTZ
	}()

	out, err := ScheduleTask(NewRuntime("linux", false), map[string]string{
		"action":         "add",
		"task":           "每1分钟在当前聊天框发一句 hello world",
		"interval_sec":   "60",
		"repeat":         "interval",
		"reply_text":     "hello world",
		"confirm_create": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已写入定时任务") || !strings.Contains(out, "hello world") {
		t.Fatalf("unexpected output: %s", out)
	}
}
