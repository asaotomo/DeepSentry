package scheduler

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScheduleCommandEditCancelResetDelete(t *testing.T) {
	store := NewStore(t.TempDir() + "/tasks.json")
	now := time.Date(2026, 9, 22, 9, 28, 0, 0, time.Local)
	plan, err := PlanTask(PlanInput{Text: "每分钟写笑话", Repeat: "每分钟", AllowBatch: true, Kind: KindAgent}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.AddUnique(plan.Task); err != nil || !created {
		t.Fatal(err)
	}
	id := plan.Task.ID[:16]
	out, err := Command(store, "edit "+id+" name=笑话 every=2m prompt=改成写一句英文", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已修改") || !strings.Contains(out, "改成写一句英文") {
		t.Fatalf("edit output: %s", out)
	}
	tasks, err := store.Load()
	if err != nil || len(tasks) != 1 {
		t.Fatal(err)
	}
	if tasks[0].Name != "笑话" || tasks[0].IntervalSec != 120 || tasks[0].Prompt != "改成写一句英文" {
		t.Fatalf("edited task: %+v", tasks[0])
	}
	if !tasks[0].RunAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("next run=%s", tasks[0].RunAt)
	}
	if _, err := Command(store, "cancel "+id, now); err != nil {
		t.Fatal(err)
	}
	tasks, _ = store.Load()
	if tasks[0].Status != StatusDisabled {
		t.Fatalf("status=%s", tasks[0].Status)
	}
	tasks[0].FailureCount = 2
	if err := store.Update(tasks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := Command(store, "reset "+tasks[0].ID, now); err != nil {
		t.Fatal(err)
	}
	tasks, _ = store.Load()
	if tasks[0].Status != StatusEnabled || tasks[0].FailureCount != 0 || !tasks[0].RunAt.Equal(now) {
		t.Fatalf("reset task: %+v", tasks[0])
	}
	if _, err := Command(store, "delete "+id, now); err != nil {
		t.Fatal(err)
	}
	tasks, _ = store.Load()
	if len(tasks) != 0 {
		t.Fatalf("still present: %+v", tasks)
	}
}

func TestResetRearmsFinishedOneShot(t *testing.T) {
	store := NewStore(t.TempDir() + "/tasks.json")
	ran := time.Date(2026, 9, 22, 8, 0, 0, 0, time.Local)
	task := Task{ID: "sched_once", Name: "打开文件", Prompt: "打开文件", Kind: KindAgent, Repeat: RepeatOnce, RunAt: ran, Status: StatusDisabled, RunCount: 1, LastRunAt: &ran}
	if err := store.Add(task); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(task.ID, StatusEnabled); err == nil {
		t.Fatal("resume replayed a finished one-shot")
	}
	now := ran.Add(time.Hour)
	out, err := Command(store, "reset "+task.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "一次性任务") {
		t.Fatalf("reset output: %s", out)
	}
	tasks, err := store.Load()
	if err != nil || len(tasks) != 1 {
		t.Fatal(err)
	}
	if tasks[0].Status != StatusEnabled || tasks[0].RunCount != 0 || tasks[0].LastRunAt != nil || !tasks[0].RunAt.Equal(now) {
		t.Fatalf("rearmed task: %+v", tasks[0])
	}
}

func TestEditRefusesRunningTask(t *testing.T) {
	store := NewStore(t.TempDir() + "/tasks.json")
	task := Task{ID: "sched_live", Name: "live", Prompt: "live", Kind: KindAgent, Repeat: RepeatInterval, IntervalSec: 60, RunAt: time.Now(), Status: StatusEnabled}
	if err := store.Add(task); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(task.ID))
	release, ok, err := acquireRunLock(fmt.Sprintf("%s.%x", store.Path, sum[:8]), time.Now())
	if err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}
	defer release()
	if _, err := store.Edit(task.ID, TaskEdit{Name: strPtr("next")}, time.Now()); err == nil || !strings.Contains(err.Error(), "仍在运行") {
		t.Fatalf("running edit err=%v", err)
	}
}

func TestStatusBarTextCountsLiveTasks(t *testing.T) {
	now := time.Date(2026, 9, 22, 13, 0, 0, 0, time.Local)
	if got := StatusBarText(nil, now); got != "" {
		t.Fatalf("empty=%q", got)
	}
	if got := StatusBarText([]Task{{Status: StatusCompleted}, {Status: StatusEnabled}}, now); got != "定时 1 启用" {
		t.Fatalf("enabled=%q", got)
	}
	got := StatusBarText([]Task{{Status: StatusRunning}, {Status: StatusEnabled}, {Status: StatusDisabled}, {Status: StatusFailed}}, now)
	if got != "定时 4：1 执行中 1 启用 1 暂停 1 失败" {
		t.Fatalf("mixed=%q", got)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	shanghai := time.Date(2026, 9, 22, 13, 0, 0, 0, loc)
	got = StatusBarText([]Task{{Status: StatusEnabled, RunAt: shanghai.Add(7 * time.Minute), Timezone: "Asia/Shanghai"}}, shanghai)
	if got != "定时 1 启用 · 下次 13:07" {
		t.Fatalf("next=%q", got)
	}
}

func strPtr(s string) *string { return &s }
