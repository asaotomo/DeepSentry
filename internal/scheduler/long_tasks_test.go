package scheduler

import (
	"ai-edr/internal/config"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRelativeAndIntervalExamples(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.Local)
	for _, test := range []struct {
		text   string
		runAt  string
		every  string
		delay  time.Duration
		repeat string
	}{
		{"两小时后打开文件", "2小时后", "", 2 * time.Hour, RepeatOnce},
		{"两小时后打开文件", "两小时后", "", 2 * time.Hour, RepeatOnce},
		{"查一个数据", "", "30m", 30 * time.Minute, RepeatInterval},
		{"检查状态", "", "2h", 2 * time.Hour, RepeatInterval},
	} {
		p, e := PlanTask(PlanInput{Prompt: test.text, RunAt: test.runAt, IntervalSec: test.every}, now)
		if e != nil || p.Task.Repeat != test.repeat || !p.Task.RunAt.Equal(now.Add(test.delay)) {
			t.Fatalf("%s: %+v %v", test.text, p, e)
		}
	}
}
func TestLongJobDoesNotBlockNewDueJob(t *testing.T) {
	store := NewStore(t.TempDir() + "/tasks.json")
	now := time.Now()
	entered := make(chan struct{})
	release := make(chan struct{})
	r := NewRunner(config.Config{})
	r.Store = store
	r.execute = func(ctx context.Context, task Task, _ time.Time) (string, string, error) {
		if task.ID == "slow" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return "", "done", nil
	}
	store.Add(Task{ID: "slow", Prompt: "slow", Kind: KindAgent, Repeat: RepeatOnce, RunAt: now, Status: StatusEnabled})
	finished := make(chan struct{})
	go func() { r.RunDue(now); close(finished) }()
	<-entered
	if err := store.SetStatus("slow", StatusEnabled); err == nil {
		t.Fatal("active job allowed resume")
	}
	store.Add(Task{ID: "fast", Prompt: "fast", Kind: KindAgent, Repeat: RepeatOnce, RunAt: now, Status: StatusEnabled})
	out, e := r.RunDue(now)
	if e != nil || !strings.Contains(out, "done") {
		t.Fatalf("new task blocked: %s %v", out, e)
	}
	store.SetStatus("slow", StatusDisabled)
	close(release)
	<-finished
	tasks, _ := store.Load()
	for _, task := range tasks {
		if task.ID == "slow" && task.Status != StatusDisabled {
			t.Fatal("completion overwrote pause")
		}
		if task.RunCount != 1 {
			t.Fatal("duplicate run")
		}
	}
}
func TestResumeDoesNotReplayOneShot(t *testing.T) {
	store := NewStore(t.TempDir() + "/tasks.json")
	ran := time.Now()
	if err := store.Add(Task{ID: "once", Prompt: "open file", Kind: KindAgent, Repeat: RepeatOnce, RunAt: ran, Status: StatusDisabled, RunCount: 1, LastRunAt: &ran}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus("once", StatusEnabled); err == nil {
		t.Fatal("resume replayed a one-shot that already started")
	}
}
func TestRepeatSkipsMissedSlotsAndFailureIsVisible(t *testing.T) {
	now := time.Now()
	task := Task{RunAt: now.Add(-100 * time.Hour), Repeat: RepeatInterval, IntervalSec: 60}
	if got := nextRun(task, now); !got.After(now) || got.After(now.Add(time.Minute)) {
		t.Fatal("bad next run")
	}
	r := NewRunner(config.Config{})
	r.Store = NewStore(t.TempDir() + "/tasks.json")
	r.execute = func(context.Context, Task, time.Time) (string, string, error) {
		return "", "", context.DeadlineExceeded
	}
	task.Repeat = RepeatOnce
	if got := r.runOne(context.Background(), task, now); got.Status != StatusFailed {
		t.Fatal("failed one shot marked complete")
	}
}
