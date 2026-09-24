package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/harness"
)

type progressKey struct{}
type noticeKey struct{}
type Progress struct {
	Seq  int64
	Text string
}

func withProgress(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// ControlSink exports public execution state, never raw model deltas, thoughts or tool output.
type ControlSink struct {
	Base harness.UISink
	Dir  string
	mu   sync.Mutex
}

func (s *ControlSink) Emit(e harness.UIEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Base.Emit(e)
	text := ""
	switch e.Kind {
	case harness.EventStepStart:
		text = fmt.Sprintf("正在处理第 %d 步。", e.Step)
	case harness.EventAction:
		if e.Action != nil {
			text = "正在执行工具：" + string(e.Action.Type)
			if e.Action.ToolName != "" {
				text = "正在执行工具：" + e.Action.ToolName
			}
		}
	case harness.EventAwaitUser:
		text = "正在等待补充信息。"
	}
	if text != "" {
		_ = writeConfirmJSON(filepath.Join(s.Dir, "progress.json"), Progress{Seq: time.Now().UnixNano(), Text: text})
	}
}

// DrainControlInput runs only at model boundaries on the agent goroutine.
// Rename to .used before returning so the parent can distinguish consumed inputs.
func DrainControlInput(dir string) []analyzer.Message {
	files, _ := filepath.Glob(filepath.Join(dir, "input-*.json"))
	sort.Strings(files)
	var out []analyzer.Message
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) > 64000 {
			continue
		}
		var m analyzer.Message
		if json.Unmarshal(raw, &m) != nil || m.Role != "user" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		if os.Rename(p, strings.TrimSuffix(p, ".json")+".used") != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}
func (s *Service) steerLocked(cName string, own string, target *Job, key string) {
	for id, p := range s.pending {
		j := s.jobs[id]
		if j == nil || j.Event != key || j.Owner != own {
			continue
		}
		dir := filepath.Join(filepath.Dir(s.cfg.Store), "confirm", target.ID)
		if os.MkdirAll(dir, 0700) != nil {
			return
		}
		name := fmt.Sprintf("input-%020d-%s", j.Created.UnixNano(), id)
		if writeConfirmJSON(filepath.Join(dir, name+".json"), analyzer.Message{Role: "user", Content: p.prompt}) != nil {
			return
		}
		p.steerTarget = target.ID
		p.steerFile = name
		s.pending[id] = p
	}
}
func (s *Service) settleSteeringLocked(target string, dir string) {
	for id, p := range s.pending {
		if p.steerTarget != target {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, p.steerFile+".used")); err == nil {
			if j := s.jobs[id]; j != nil {
				j.Status = "completed"
				j.Result = "补充指令已由任务 " + target + " 读取；请查看该任务结果。"
				j.Updated = time.Now()
			}
			delete(s.pending, id)
		}
	}
}

// Compete atomically with the child's rename-to-used before cancelling a supplement.
func (s *Service) cancelPendingLocked(id string, stopAll bool) bool {
	p, ok := s.pending[id]
	if ok && p.steerTarget != "" {
		base := filepath.Join(filepath.Dir(s.cfg.Store), "confirm", p.steerTarget, p.steerFile)
		if err := os.Rename(base+".json", base+".cancelled"); err == nil {
			_ = os.Remove(base + ".cancelled")
		} else if _, err := os.Stat(base + ".used"); err == nil && !stopAll {
			return false
		}
	}
	delete(s.pending, id)
	return true
}
