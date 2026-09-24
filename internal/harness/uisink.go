package harness

import (
	"ai-edr/internal/ui"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EventKind Agent UI 事件类型（供 TUI / stdout 统一渲染）
type EventKind string

const (
	EventInfo           EventKind = "info"
	EventStepStart      EventKind = "step_start"
	EventThinking       EventKind = "thinking"
	EventThought        EventKind = "thought"
	EventAction         EventKind = "action"
	EventRiskAuto       EventKind = "risk_auto"
	EventBatchAuto      EventKind = "batch_auto"
	EventDenied         EventKind = "denied"
	EventResult         EventKind = "result"
	EventError          EventKind = "error"
	EventFinish         EventKind = "finish"
	EventCheckpoint     EventKind = "checkpoint"
	EventSubAgentStart  EventKind = "subagent_start"
	EventSubAgentStep   EventKind = "subagent_step"
	EventSubAgentAction EventKind = "subagent_action"
	EventSubAgentResult EventKind = "subagent_result"
	EventTargetStatus   EventKind = "target_status"
	EventCommandOutput  EventKind = "command_output"
	// Stream deltas carry only the new bytes in Message. StreamEnd carries the
	// complete response in Detail, including chunks a UI may have dropped.
	EventStreamDelta EventKind = "stream_delta"
	EventStreamEnd   EventKind = "stream_end"
	EventAwaitUser   EventKind = "await_user"
	EventTokenUsage  EventKind = "token_usage"
)

// UIEvent 单条 UI 事件
type UIEvent struct {
	Kind             EventKind
	Step             int
	MaxSteps         int
	Message          string
	Detail           string
	Action           *AgentAction
	TargetName       string
	TargetProtocol   string
	TargetHost       string
	Status           string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// UISink Agent 循环输出接口（stdout / TUI 共用）
type UISink interface {
	Emit(e UIEvent)
}

// ANSI 样式（经典 CLI）
const (
	ansiThought = "\033[90m" // 亮灰，比 dim 更易读
	ansiReset   = "\033[0m"
)

// 经典 CLI 只显示 AI 思考/动作摘要，不直接打印原始 JSON delta。
type StdoutSink struct {
	streamLast string
	forcePlain bool
}

func NewStdoutSink() *StdoutSink { return &StdoutSink{} }

func (s *StdoutSink) text(value string) string {
	if s.forcePlain {
		return ui.StripANSI(value)
	}
	return ui.TerminalText(value)
}

func (s *StdoutSink) Emit(e UIEvent) {
	switch e.Kind {
	case EventInfo:
		fmt.Println(ui.TerminalText(e.Message))
	case EventStepStart:
		fmt.Printf("\n--- [Step %d / %d] -----------------\n", e.Step, e.MaxSteps)
	case EventThinking:
		fmt.Println(s.text(ansiThought + ui.Prefix("🧠", "[AI]") + "AI 正在思考..." + ansiReset))
	case EventThought:
		if e.Message != "" {
			fmt.Println(s.text(ansiThought + ui.Prefix("💡", "[IDEA]") + "想法: " + e.Message + ansiReset))
		}
	case EventTokenUsage:
		if e.TotalTokens > 0 {
			fmt.Println(s.text(fmt.Sprintf("%s%sToken: prompt=%d completion=%d total=%d%s", ansiThought, ui.Prefix("📊", "[TOK]"), e.PromptTokens, e.CompletionTokens, e.TotalTokens, ansiReset)))
		}
	case EventAction:
		if e.Action != nil {
			printActionStdout(RedactedAction(*e.Action))
		} else if e.Message != "" {
			fmt.Println(ui.TerminalText(e.Message))
		}
	case EventRiskAuto:
		fmt.Println(ui.TerminalText(e.Message))
	case EventBatchAuto:
		fmt.Println(ui.Prefix("⚡", "[BATCH]") + "[Batch] 自动执行")
	case EventDenied:
		fmt.Println(ui.Prefix("🚫", "[DENY]") + "已拒绝执行")
	case EventResult:
		body := strings.TrimSpace(e.Detail)
		if body == "" {
			body = strings.TrimSpace(e.Message)
		}
		body = ui.TerminalText(body)
		if strings.Contains(body, "\n") {
			fmt.Printf("%s结果:\n%s\n", ui.Prefix("✅", "[OK]"), body)
		} else {
			fmt.Printf("%s结果: %s\n", ui.Prefix("✅", "[OK]"), body)
		}
	case EventCommandOutput:
		if e.Message != "" {
			line := ui.TerminalText(e.Message)
			fmt.Print(line)
			if !strings.HasSuffix(line, "\n") {
				fmt.Println()
			}
		}
	case EventError:
		fmt.Println(ui.TerminalText(e.Message))
	case EventFinish:
		fmt.Println("\n" + ui.Prefix("📝", "[REPORT]") + "最终报告:\n" + repeatChar('=', 40))
		fmt.Println(ui.TerminalText(e.Message))
		fmt.Println(repeatChar('=', 40))
		if e.Detail != "" {
			fmt.Printf("\n%s日志: %s\n", ui.Prefix("📂", "[LOG]"), ui.TerminalText(e.Detail))
		}
	case EventCheckpoint:
		fmt.Printf("%s%s\n", ui.Prefix("⚠️", "[WARN]"), e.Message)
	case EventAwaitUser:
		fmt.Println("\n" + ui.Prefix("❓", "[ASK]") + "需要用户补充信息:")
		fmt.Println(ui.TerminalText(e.Message))
	case EventSubAgentStart:
		fmt.Printf("%s子 Agent 启动: %s%s\n", ui.Prefix("🔀", "[SUB]"), e.Message, formatTargetSuffix(e.TargetName, e.TargetProtocol, e.TargetHost))
		if e.Detail != "" {
			fmt.Printf("   任务: %s\n", ui.TerminalText(truncate(e.Detail, 80)))
		}
	case EventSubAgentStep:
		fmt.Printf("   子 Agent 步骤%s: %s\n", formatTargetSuffix(e.TargetName, e.TargetProtocol, e.TargetHost), e.Message)
	case EventSubAgentAction:
		if e.Action != nil {
			fmt.Print("   子 Agent ")
			printActionStdout(RedactedAction(*e.Action))
		} else if e.Message != "" {
			fmt.Printf("   子 Agent 动作: %s\n", ui.TerminalText(e.Message))
		}
	case EventSubAgentResult:
		fmt.Printf("%s子 Agent [%s]%s 返回:\n%s\n", ui.Prefix("📦", "[SUB]"), e.Message, formatTargetSuffix(e.TargetName, e.TargetProtocol, e.TargetHost), truncate(e.Detail, 400))
	case EventTargetStatus:
		fmt.Printf("%s[%s] %s%s %s\n", ui.Prefix("📡", "[TARGET]"), e.Status, e.Message, formatTargetSuffix(e.TargetName, e.TargetProtocol, e.TargetHost), e.Detail)
	case EventStreamDelta:
		// no-tui should stay readable: keep the single "AI 正在思考..." line
		// from EventThinking, then print the final thought once via EventThought.
	case EventStreamEnd:
		s.streamLast = ""
	}
}

func formatTargetSuffix(name, proto, host string) string {
	if name == "" && proto == "" && host == "" {
		return ""
	}
	label := name
	if label == "" {
		label = host
	}
	if proto != "" && host != "" {
		return fmt.Sprintf(" @ %s (%s %s)", label, proto, host)
	}
	if proto != "" {
		return fmt.Sprintf(" @ %s (%s)", label, proto)
	}
	return fmt.Sprintf(" @ %s", label)
}

// JSONSink 输出 JSONL 事件，便于 agent/CI 消费。
type JSONSink struct {
	enc *json.Encoder
}

func NewJSONSink() *JSONSink {
	return &JSONSink{enc: json.NewEncoder(os.Stdout)}
}

func (s *JSONSink) Emit(e UIEvent) {
	_ = s.enc.Encode(e)
}

// QuietSink 保留关键结果/错误，抑制思考流和启动噪音。
type QuietSink struct {
	inner UISink
}

func NewQuietSink(inner UISink) *QuietSink {
	if inner == nil {
		inner = NewStdoutSink()
	}
	return &QuietSink{inner: inner}
}

func (s *QuietSink) Emit(e UIEvent) {
	switch e.Kind {
	case EventInfo, EventThinking, EventStreamDelta, EventStreamEnd, EventCommandOutput, EventRiskAuto, EventTokenUsage:
		return
	default:
		s.inner.Emit(e)
	}
}

// FinalReplySink 仅输出最终结论/错误，供聊天机器人手机回复使用。
type FinalReplySink struct{}

func NewFinalReplySink() *FinalReplySink { return &FinalReplySink{} }

func (s *FinalReplySink) Emit(e UIEvent) {
	switch e.Kind {
	case EventFinish:
		msg := strings.TrimSpace(ui.StripANSI(e.Message))
		if msg != "" {
			fmt.Println(msg)
		}
	case EventError, EventDenied:
		msg := strings.TrimSpace(ui.StripANSI(e.Message))
		if msg != "" {
			fmt.Println(msg)
		}
	case EventAwaitUser:
		msg := strings.TrimSpace(ui.StripANSI(e.Message))
		if msg == "" {
			return
		}
		fmt.Println("需要补充信息：")
		fmt.Println(msg)
	}
}

// WebShellSink 面向 WebShell/非 TTY 的低噪音执行日志输出。
// 与 QuietSink 不同，它明确保留 step/action/result/finish，方便像 fscan 一样在终端直接看执行过程。
type WebShellSink struct {
	inner UISink
}

func NewWebShellSink(inner UISink) *WebShellSink {
	if inner == nil {
		inner = NewStdoutSink()
	}
	if stdout, ok := inner.(*StdoutSink); ok {
		clone := *stdout
		clone.forcePlain = true
		inner = &clone
	}
	return &WebShellSink{inner: inner}
}

func (s *WebShellSink) Emit(e UIEvent) {
	switch e.Kind {
	case EventInfo, EventThinking, EventStreamDelta, EventStreamEnd, EventRiskAuto, EventBatchAuto, EventTokenUsage:
		return
	default:
		e.Message = ui.StripANSI(e.Message)
		e.Detail = ui.StripANSI(e.Detail)
		if e.Action != nil {
			action := *e.Action
			action.Command = ui.StripANSI(action.Command)
			action.TaskPrompt = ui.StripANSI(action.TaskPrompt)
			action.Content = ui.StripANSI(action.Content)
			action.FinalReport = ui.StripANSI(action.FinalReport)
			action.Question = ui.StripANSI(action.Question)
			e.Action = &action
		}
		s.inner.Emit(e)
	}
}

func printActionStdout(action AgentAction) {
	switch action.Type {
	case ActionTask:
		if len(action.ParallelTasks) > 0 {
			fmt.Printf("%s并行委派: %d 项\n", ui.Prefix("🔀", "[SUB]"), len(action.ParallelTasks))
			return
		}
		if strings.TrimSpace(action.TaskName) == "" || strings.TrimSpace(action.TaskPrompt) == "" {
			fmt.Printf("%s委派参数不完整\n", ui.Prefix("🔀", "[SUB]"))
			return
		}
		target := action.TargetSelector
		if target == "" {
			target = firstNonEmpty(action.TargetName, action.TargetHost)
		}
		if target != "" {
			fmt.Printf("%s委派: %s @ %s -> %s\n", ui.Prefix("🔀", "[SUB]"), action.TaskName, target, truncate(action.TaskPrompt, 60))
		} else {
			fmt.Printf("%s委派: %s -> %s\n", ui.Prefix("🔀", "[SUB]"), action.TaskName, truncate(action.TaskPrompt, 60))
		}
	case ActionLoadSkill:
		fmt.Printf("%s加载 Skill: %s\n", ui.Prefix("📚", "[SKILL]"), action.SkillName)
	case ActionTodo:
		fmt.Println(FormatTodoList(action.Todos))
	case ActionAskUser:
		fmt.Printf("%s询问用户: %s\n", ui.Prefix("❓", "[ASK]"), action.Question)
	case ActionRemember:
		fmt.Printf("%s保存记忆: %s\n", ui.Prefix("🧠", "[MEM]"), action.MemoryKey)
	case ActionForget:
		fmt.Printf("%s删除记忆: %s\n", ui.Prefix("🧠", "[MEM]"), action.MemoryKey)
	case ActionTool:
		fmt.Printf("%s工具: %s %v\n", ui.Prefix("🔧", "[TOOL]"), action.ToolName, action.ToolArgs)
	case ActionToolBatch:
		fmt.Printf("%s并行工具: %d 项\n", ui.Prefix("🔧", "[TOOL]"), len(action.ToolCalls))
	case ActionReadFile:
		fmt.Printf("%s读取: %s\n", ui.Prefix("📄", "[READ]"), action.Path)
	case ActionWriteFile:
		fmt.Printf("%s写入: %s\n", ui.Prefix("✏️", "[WRITE]"), action.Path)
	case ActionEditFile:
		fmt.Printf("%s编辑: %s\n", ui.Prefix("✏️", "[EDIT]"), action.Path)
	case ActionGlob:
		fmt.Printf("%sGlob: %s / %s\n", ui.Prefix("🔎", "[GLOB]"), action.Path, action.GlobPattern)
	case ActionGrep:
		fmt.Printf("%s搜索: %s in %s\n", ui.Prefix("🔍", "[GREP]"), action.Pattern, action.Path)
	case ActionLS:
		fmt.Printf("%s列出: %s\n", ui.Prefix("📁", "[LS]"), action.Path)
	default:
		if action.Command != "" {
			fmt.Printf("%s命令[%s]: %s\n", ui.Prefix("💻", "[CMD]"), stdoutExecutionTargetLabel(action), ui.TerminalText(action.Command))
		}
	}
}

func stdoutExecutionTargetLabel(action AgentAction) string {
	proto := strings.ToLower(strings.TrimSpace(action.TargetProtocol))
	host := strings.TrimSpace(action.TargetHost)
	name := strings.TrimSpace(action.TargetName)
	switch proto {
	case "ssh":
		return stdoutJoinTarget("远端SSH", name, host)
	case "telnet":
		return stdoutJoinTarget("远端Telnet", name, host)
	case "ftp":
		return stdoutJoinTarget("远端FTP", name, host)
	case "remote":
		return stdoutJoinTarget("远端目标", name, host)
	case "local", "":
		return stdoutJoinTarget("控制端本机", name, host)
	default:
		return stdoutJoinTarget("远端"+proto, name, host)
	}
}

func stdoutJoinTarget(prefix, name, host string) string {
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

func repeatChar(c rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = c
	}
	return string(out)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
