package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/harness"
)

// ChannelSink 将 harness 事件转发到 TUI（线程安全）
type ChannelSink struct {
	mu            sync.Mutex
	sendMu        sync.Mutex
	events        chan harness.UIEvent
	done          chan struct{}
	closed        bool
	droppedStream int
}

func NewChannelSink(buf int) *ChannelSink {
	if buf <= 0 {
		buf = 256
	}
	return &ChannelSink{events: make(chan harness.UIEvent, buf), done: make(chan struct{})}
}

func (s *ChannelSink) Emit(e harness.UIEvent) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if e.Kind == harness.EventStreamDelta {
		// The final stream_end event carries the full response. Never hold up
		// the model's network reader for a preview chunk the UI cannot keep up
		// with; a full queue could otherwise add 50ms per token chunk.
		select {
		case s.events <- e:
			return
		default:
			s.mu.Lock()
			if !s.closed {
				s.droppedStream++
			}
			s.mu.Unlock()
			return
		}
	}
	if e.Kind == harness.EventCommandOutput {
		select {
		case s.events <- e:
			return
		default:
		}
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case s.events <- e:
			return
		case <-s.done:
			return
		case <-timer.C:
			s.mu.Lock()
			if !s.closed {
				s.droppedStream++
			}
			s.mu.Unlock()
			return
		}
	}

	// Semantic events (finish/error/ask/result/step...) must never disappear.
	// Serialize them so a dropped-output notice stays immediately before the
	// event that made room for it.
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	dropped := s.droppedStream
	s.droppedStream = 0
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	if dropped > 0 {
		notice := harness.UIEvent{
			Kind:    harness.EventInfo,
			Message: fmt.Sprintf("高频输出过快，已省略 %d 个显示片段；完整内容仍会进入模型上下文/审计结果。", dropped),
		}
		select {
		case s.events <- notice:
		case <-s.done:
			return
		}
	}
	select {
	case s.events <- e:
	case <-s.done:
	}
}

func (s *ChannelSink) Events() <-chan harness.UIEvent { return s.events }
func (s *ChannelSink) Done() <-chan struct{}          { return s.done }

func (s *ChannelSink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}

// FormatActionLine 格式化动作为 Claude Code 风格单行摘要
func FormatActionLine(a *harness.AgentAction) string {
	if a == nil {
		return ""
	}
	redacted := harness.RedactedAction(*a)
	a = &redacted
	switch a.Type {
	case harness.ActionTool:
		args := ""
		if len(a.ToolArgs) > 0 {
			parts := make([]string, 0, len(a.ToolArgs))
			for k, v := range a.ToolArgs {
				parts = append(parts, fmt.Sprintf("%s=%s", k, v))
			}
			args = " " + strings.Join(parts, " ")
		}
		return fmt.Sprintf("Tool · %s%s", a.ToolName, args)
	case harness.ActionExecute:
		return fmt.Sprintf("Shell · %s · %s", executionTargetLabel(a.TargetName, a.TargetProtocol, a.TargetHost), a.Command)
	case harness.ActionTask:
		if len(a.ParallelTasks) > 0 {
			return fmt.Sprintf("Sub-agent · 并行 %d 项 · %s", len(a.ParallelTasks), parallelTaskNames(a.ParallelTasks))
		}
		if strings.TrimSpace(a.TaskName) == "" || strings.TrimSpace(a.TaskPrompt) == "" {
			return "Sub-agent · 参数不完整"
		}
		target := ""
		if a.TargetSelector != "" {
			target = " selector=" + a.TargetSelector
		} else if a.TargetName != "" || a.TargetHost != "" {
			target = " target=" + firstNonEmpty(a.TargetName, a.TargetHost)
		}
		return fmt.Sprintf("Sub-agent · %s%s", a.TaskName, target)
	case harness.ActionLoadSkill:
		return fmt.Sprintf("Skill · %s", a.SkillName)
	case harness.ActionReadFile:
		return fmt.Sprintf("Read · %s", a.Path)
	case harness.ActionWriteFile:
		return fmt.Sprintf("Write · %s", a.Path)
	case harness.ActionEditFile:
		return fmt.Sprintf("Edit · %s", a.Path)
	case harness.ActionGrep:
		return fmt.Sprintf("Grep · %s in %s", a.Pattern, a.Path)
	case harness.ActionGlob:
		return fmt.Sprintf("Glob · %s/%s", a.Path, a.GlobPattern)
	case harness.ActionLS:
		return fmt.Sprintf("Ls · %s", a.Path)
	case harness.ActionTodo:
		return harness.FormatTodoList(a.Todos)
	case harness.ActionAskUser:
		return fmt.Sprintf("Ask · %s", a.Question)
	case harness.ActionRemember:
		return fmt.Sprintf("Memory · %s", a.MemoryKey)
	default:
		if a.Command != "" {
			return fmt.Sprintf("Shell · %s · %s", executionTargetLabel(a.TargetName, a.TargetProtocol, a.TargetHost), a.Command)
		}
		return string(a.Type)
	}
}

func parallelTaskNames(tasks []harness.SubAgentTaskAction) string {
	names := make([]string, 0, len(tasks))
	for _, task := range tasks {
		name := strings.TrimSpace(task.TaskName)
		if name == "" {
			name = "未指定"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func executionTargetLabel(name, proto, host string) string {
	proto = strings.ToLower(strings.TrimSpace(proto))
	name = strings.TrimSpace(name)
	host = strings.TrimSpace(host)
	switch proto {
	case "local", "":
		return joinExecutionTarget("控制端本机", name, host)
	case "ssh":
		return joinExecutionTarget("远端 SSH", name, host)
	case "telnet":
		return joinExecutionTarget("远端 Telnet", name, host)
	case "ftp":
		return joinExecutionTarget("远端 FTP", name, host)
	case "remote":
		return joinExecutionTarget("远端目标", name, host)
	}
	if proto != "" {
		return joinExecutionTarget("远端 "+proto, name, host)
	}
	return joinExecutionTarget("控制端本机", name, host)
}

func joinExecutionTarget(prefix, name, host string) string {
	if name != "" && host != "" {
		return fmt.Sprintf("%s %s(%s)", prefix, name, host)
	}
	if name != "" {
		return prefix + " " + name
	}
	if host != "" {
		return prefix + " " + host
	}
	return prefix
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
