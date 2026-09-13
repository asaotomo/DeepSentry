package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/harness"
)

type discardSink struct{}

func (discardSink) Emit(harness.UIEvent) {}
func TestProgressExportsPublicStateOnly(t *testing.T) {
	dir := t.TempDir()
	sink := &ControlSink{Base: discardSink{}, Dir: dir}
	for _, kind := range []harness.EventKind{harness.EventThought, harness.EventStreamDelta, harness.EventCommandOutput, harness.EventResult} {
		sink.Emit(harness.UIEvent{Kind: kind, Message: "secret raw text"})
	}
	if _, err := os.Stat(filepath.Join(dir, "progress.json")); !os.IsNotExist(err) {
		t.Fatal("internal output exported")
	}
	sink.Emit(harness.UIEvent{Kind: harness.EventStepStart, Step: 2})
	raw, err := os.ReadFile(filepath.Join(dir, "progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p Progress
	_ = json.Unmarshal(raw, &p)
	if !strings.Contains(p.Text, "第 2 步") || strings.Contains(string(raw), "secret") {
		t.Fatal(string(raw))
	}
}
func TestSteerConsumedByActiveTaskDoesNotRunTwice(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	received := make(chan string, 1)
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		dir := ConfirmDirFromContext(ctx)
		started <- dir
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		inputs := DrainControlInput(dir)
		if len(inputs) != 1 {
			received <- "missing"
		} else {
			received <- inputs[0].Content
		}
		return "done", nil
	})
	c := testChannel()
	_, _ = s.command(c, testMessage("first", "first"))
	<-started
	out, err := s.command(c, testMessage("follow", "/steer 只检查日志"))
	if err != nil || !strings.Contains(out, "补充") {
		t.Fatalf("%q %v", out, err)
	}
	close(release)
	select {
	case got := <-received:
		if got != "只检查日志" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing supplement")
	}
	waitJob(t, s, "first", "completed")
	j := waitJob(t, s, "只检查日志", "completed")
	if !strings.Contains(j.Result, "读取") {
		t.Fatal(j.Result)
	}
	select {
	case <-started:
		t.Fatal("supplement ran twice")
	default:
	}
}
func TestSteerFinishingRaceFallsBackToQueuedTurn(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	second := make(chan string, 1)
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		if prompt == "first" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		} else {
			second <- prompt
		}
		return "done", nil
	})
	c := testChannel()
	_, _ = s.command(c, testMessage("first", "first"))
	<-started
	_, _ = s.command(c, testMessage("steer", "/steer 补充条件"))
	close(release)
	select {
	case got := <-second:
		if got != "补充条件" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("lost late supplement")
	}
	waitJob(t, s, "补充条件", "completed")
}

func TestCancelSupplementBeforeOrAfterConsumption(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "consumed"}[consumed], func(t *testing.T) {
			started := make(chan string, 1)
			s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
				started <- ConfirmDirFromContext(ctx)
				<-ctx.Done()
				return "", ctx.Err()
			})
			c := testChannel()
			_, _ = s.command(c, testMessage("first", "first"))
			dir := <-started
			_, _ = s.command(c, testMessage("steer", "/steer later"))
			j := jobByPrompt(t, s, "later")
			if consumed {
				if len(DrainControlInput(dir)) != 1 {
					t.Fatal("not consumed")
				}
			}
			out, err := s.command(c, testMessage("cancel", "/ds cancel "+j.ID))
			if err != nil {
				t.Fatal(err)
			}
			if consumed {
				if !strings.Contains(out, "已被当前任务读取") {
					t.Fatal(out)
				}
			} else {
				if !strings.Contains(out, "已取消") || len(DrainControlInput(dir)) != 0 {
					t.Fatal("cancelled supplement still readable", out)
				}
			}
		})
	}
}
