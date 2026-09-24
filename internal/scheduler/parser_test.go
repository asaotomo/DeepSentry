package scheduler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanTaskParsesTomorrowInspectionDingTalk(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 26, 15, 30, 0, 0, loc)
	plan, err := PlanTask(PlanInput{
		Prompt:   "巡检服务器并生成巡检报告",
		RunAt:    "明天9点",
		Kind:     KindInspection,
		Notify:   NotifyDingTalk,
		Timezone: "Asia/Shanghai",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 27, 9, 0, 0, 0, loc)
	if !plan.Task.RunAt.Equal(want) {
		t.Fatalf("run_at mismatch: got %s want %s", plan.Task.RunAt, want)
	}
	if plan.Task.Kind != KindInspection {
		t.Fatalf("kind=%s", plan.Task.Kind)
	}
	if !plan.Task.Report {
		t.Fatal("inspection should enable report")
	}
	if plan.Task.Notify != NotifyDingTalk {
		t.Fatalf("notify=%s", plan.Task.Notify)
	}
}

func TestPlanTaskParsesMultiChannelNotify(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 26, 15, 30, 0, 0, loc)
	plan, err := PlanTask(PlanInput{
		Prompt:   "巡检服务器并生成报告",
		RunAt:    "明天9点",
		Notify:   "dingtalk,feishu,email",
		Timezone: "Asia/Shanghai",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Task.Notify != "dingtalk,feishu,email" {
		t.Fatalf("notify=%s", plan.Task.Notify)
	}
	channels := NotifyChannels(plan.Task.Notify)
	if len(channels) != 3 || channels[0] != NotifyDingTalk || channels[1] != NotifyFeishu || channels[2] != NotifyEmail {
		t.Fatalf("channels=%#v", channels)
	}
}

func TestNormalizeNotifyAliases(t *testing.T) {
	got := normalizeNotify("ding + lark + mail + ding")
	if got != "dingtalk,feishu,email" {
		t.Fatalf("notify=%s", got)
	}
}

func TestPlanTaskParsesRelativeTime(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	now := time.Date(2026, 6, 26, 15, 0, 0, 0, loc)
	plan, err := PlanTask(PlanInput{RunAt: "10分钟后", Timezone: "Local"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Task.RunAt.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("unexpected relative run_at: %s", plan.Task.RunAt)
	}
}

func TestTaskSentenceDoesNotBecomeASchedule(t *testing.T) {
	now := time.Date(2026, 6, 26, 15, 30, 0, 0, time.Local)
	for _, input := range []string{
		"明天9点帮我巡检服务器并生成报告发钉钉通知",
		"每分钟在当前聊天框发一句 hello",
		"10分钟后提醒我检查备份",
		"攻击者似乎篡改了系统命令，导致该命令一旦运行就会执行恶意回连的动作。提交回连的IP和端口，例如：1.1.1.1:1111",
	} {
		_, err := PlanTask(PlanInput{Text: input}, now)
		if err == nil || !strings.Contains(err.Error(), "不会从任务正文猜测执行时间") {
			t.Fatalf("%q err=%v", input, err)
		}
	}
}

func TestExtractClockRejectsLabelsAndRequiresColonMinutes(t *testing.T) {
	for _, input := range []string{"Q10：", "Step 20: result", "CVE-2026-10: 执行"} {
		if h, m, ok := extractClock(input); ok {
			t.Errorf("%q parsed as %02d:%02d", input, h, m)
		}
	}
	if h, m, ok := extractClock("明天 10:30 巡检"); !ok || h != 10 || m != 30 {
		t.Fatalf("valid clock parse = %02d:%02d ok=%v", h, m, ok)
	}
}

func TestPlanTaskDoesNotParseIPPortAsClock(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 2, 17, 25, 0, 0, loc)
	_, err = PlanTask(PlanInput{
		Prompt:   "攻击者似乎篡改了系统命令，导致该命令一旦运行就会执行恶意回连的动作。提交回连的IP和端口，例如：1.1.1.1:1111",
		RunAt:    "2099-01-01 09:00",
		Timezone: "Asia/Shanghai",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPlanTaskRejectsPastExplicitDateAndInvalidCalendarDate(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	now := time.Date(2026, 7, 17, 15, 0, 0, 0, loc)
	for _, input := range []string{"今天9点", "2026-02-30 09:00"} {
		if _, err := PlanTask(PlanInput{RunAt: input, Timezone: "Asia/Shanghai"}, now); err == nil {
			t.Errorf("expected invalid schedule to fail: %q", input)
		}
	}
}

func TestStoreAddRemove(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "tasks.json"))
	now := time.Now()
	task := Task{ID: "sched_test", Name: "test", RunAt: now, Status: StatusEnabled, Repeat: RepeatOnce}
	if err := store.Add(task); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
	removed, ok, err := store.Remove(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || removed.ID != task.ID {
		t.Fatalf("remove failed: ok=%v removed=%#v", ok, removed)
	}
	tasks, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected empty store, got %#v", tasks)
	}
}

func TestStoreAddUniquePreventsEquivalentDuplicates(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "tasks.json"))
	runAt := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	first := Task{ID: "sched_first", Prompt: "  每天 巡检服务器  ", Kind: KindInspection, RunAt: runAt, Timezone: "UTC", Repeat: RepeatDaily, Status: StatusEnabled}
	second := first
	second.ID = "sched_second"
	second.Prompt = "每天 巡检服务器"
	if _, created, err := store.AddUnique(first); err != nil || !created {
		t.Fatalf("first add: created=%v err=%v", created, err)
	}
	existing, created, err := store.AddUnique(second)
	if err != nil || created || existing.ID != first.ID {
		t.Fatalf("duplicate add: existing=%#v created=%v err=%v", existing, created, err)
	}
	tasks, err := store.Load()
	if err != nil || len(tasks) != 1 {
		t.Fatalf("stored tasks=%#v err=%v", tasks, err)
	}
}

func TestPlanEveryMinuteWithoutClock(t *testing.T) {
	now := time.Date(2026, 9, 22, 9, 28, 0, 0, time.Local)
	text := "每分钟在桌面创建一个记事本文件并写入一个笑话"
	plan, err := PlanTask(PlanInput{Text: text, Repeat: "每分钟"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Task.Repeat != RepeatInterval || plan.Task.IntervalSec != 60 {
		t.Fatalf("repeat=%s interval=%d", plan.Task.Repeat, plan.Task.IntervalSec)
	}
	if !plan.Task.RunAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("run_at=%s", plan.Task.RunAt)
	}
	bare, err := PlanTask(PlanInput{Text: text}, now)
	if err == nil || !strings.Contains(err.Error(), "不会从任务正文猜测执行时间") {
		t.Fatalf("task sentence was still parsed: %+v %v", bare.Task, err)
	}
}

func TestStructuredIntervalIgnoresClockInsideTask(t *testing.T) {
	now := time.Date(2026, 9, 22, 9, 28, 0, 0, time.Local)
	plan, err := PlanTask(PlanInput{
		Prompt:      "写入笑话，文件名 joke-2026-09-22-09:00.txt",
		IntervalSec: "60",
		Repeat:      "interval",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Task.RunAt.Equal(now.Add(time.Minute)) || plan.Task.IntervalSec != 60 {
		t.Fatalf("structured interval used the prose clock: %+v", plan.Task)
	}
	if _, err := ParseIntervalSpec("30"); err == nil {
		t.Fatal("30 seconds must be rejected")
	}
	if sec, err := ParseIntervalSpec("每小时"); err != nil || sec != 3600 {
		t.Fatalf("每小时=%d %v", sec, err)
	}
}
