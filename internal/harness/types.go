package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/executor"
	"ai-edr/internal/logger"
	"ai-edr/internal/memory"
	"ai-edr/internal/security"
	"strings"
)

// ActionType 定义 Agent 可执行的动作类型（对标 deepagents 内置工具）
type ActionType string

const (
	ActionExecute   ActionType = "execute"    // 执行 Shell 命令
	ActionTask      ActionType = "task"       // 委派子 Agent
	ActionLoadSkill ActionType = "load_skill" // 按需加载 Skill
	ActionReadFile  ActionType = "read_file"
	ActionWriteFile ActionType = "write_file"
	ActionEditFile  ActionType = "edit_file"
	ActionGlob      ActionType = "glob"
	ActionGrep      ActionType = "grep"
	ActionLS        ActionType = "ls"
	ActionTodo      ActionType = "todo"
	ActionRemember  ActionType = "remember" // 跨会话保存记忆
	ActionForget    ActionType = "forget"   // 删除记忆
	ActionTool      ActionType = "tool"     // 内置场景工具
	ActionToolBatch ActionType = "tool_batch"
	ActionAskUser   ActionType = "ask_user" // 暂停并等待用户补充信息
	ActionFinish    ActionType = "finish"
)

// AgentAction 统一的 Agent 动作描述
type AgentAction struct {
	Type ActionType `json:"action"`

	// execute
	Command string `json:"command"`

	// task (sub-agent)
	TaskName       string               `json:"task_name"`
	TaskPrompt     string               `json:"task_prompt"`
	TaskMaxSteps   int                  `json:"task_max_steps"`
	TargetSelector string               `json:"target_selector"`
	TargetName     string               `json:"target_name"`
	TargetProtocol string               `json:"target_protocol"`
	TargetHost     string               `json:"target_host"`
	ParallelTasks  []SubAgentTaskAction `json:"parallel_tasks"`

	// load_skill
	SkillName string `json:"skill_name"`

	// filesystem
	Path        string `json:"path"`
	Content     string `json:"content"`
	Pattern     string `json:"pattern"`
	OldString   string `json:"old_string"`
	NewString   string `json:"new_string"`
	ReplaceAll  bool   `json:"replace_all"`
	GlobPattern string `json:"glob_pattern"`
	Offset      int    `json:"offset,omitempty"`
	Limit       int    `json:"limit,omitempty"`

	// todo
	Todos []TodoItem `json:"todos"`

	// finish
	FinalReport string `json:"final_report"`

	// ask_user
	Question string   `json:"question"`
	Options  []string `json:"options"`

	// memory (跨会话持久化)
	MemoryKey   string `json:"memory_key"`
	MemoryValue string `json:"memory_value"`
	MemoryScope string `json:"memory_scope"` // target | global

	// tool (内置场景工具)
	ToolName         string            `json:"tool_name"`
	ToolArgs         map[string]string `json:"tool_args"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	NativeCallName   string            `json:"-"`
	ReasoningContent string            `json:"-"`
	ToolCalls        []ToolCallAction  `json:"tool_calls,omitempty"`
	SkipToolCallIDs  map[string]bool   `json:"-"`

	// common
	Thought    string `json:"thought"`
	RiskLevel  string `json:"risk_level"`
	Reason     string `json:"reason"`
	IsFinished bool   `json:"is_finished"`

	// Approval scope is derived from the original action before UI redaction.
	// It is process-local metadata and is never sent to the model/checkpoint.
	ApprovalScopeKey   string `json:"-"`
	ApprovalScopeLabel string `json:"-"`
}

// RedactedAction returns a deep-enough copy that is safe for UI, reports and
// confirmation dialogs while leaving the executable action untouched.
func RedactedAction(action AgentAction) AgentAction {
	scopeKey, scopeLabel := SessionApprovalScope(&action)
	out := action
	out.ApprovalScopeKey = scopeKey
	out.ApprovalScopeLabel = scopeLabel
	out.Command = security.RedactSensitiveText(out.Command)
	out.TaskPrompt = security.RedactSensitiveText(out.TaskPrompt)
	out.Content = security.RedactSensitiveText(out.Content)
	out.Pattern = security.RedactSensitiveText(out.Pattern)
	out.OldString = security.RedactSensitiveText(out.OldString)
	out.NewString = security.RedactSensitiveText(out.NewString)
	out.FinalReport = security.RedactSensitiveText(out.FinalReport)
	out.Question = security.RedactSensitiveText(out.Question)
	out.MemoryValue = security.RedactSensitiveText(out.MemoryValue)
	out.Thought = security.RedactSensitiveText(out.Thought)
	out.ReasoningContent = security.RedactSensitiveText(out.ReasoningContent)
	out.ToolArgs = make(map[string]string, len(action.ToolArgs))
	for key, value := range action.ToolArgs {
		tagged := key + "=" + value
		redacted := security.RedactSensitiveText(tagged)
		if redacted != tagged {
			if i := strings.IndexByte(redacted, '='); i >= 0 {
				out.ToolArgs[key] = redacted[i+1:]
			} else {
				out.ToolArgs[key] = "***"
			}
			continue
		}
		out.ToolArgs[key] = security.RedactSensitiveText(value)
	}
	out.ToolCalls = append([]ToolCallAction(nil), action.ToolCalls...)
	for i := range out.ToolCalls {
		out.ToolCalls[i].Args = redactToolArgs(out.ToolCalls[i].Args)
	}
	out.ParallelTasks = append([]SubAgentTaskAction(nil), action.ParallelTasks...)
	for i := range out.ParallelTasks {
		out.ParallelTasks[i].TaskPrompt = security.RedactSensitiveText(out.ParallelTasks[i].TaskPrompt)
	}
	out.Options = append([]string(nil), action.Options...)
	for i := range out.Options {
		out.Options[i] = security.RedactSensitiveText(out.Options[i])
	}
	return out
}

type ToolCallAction struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Args map[string]string `json:"args"`
}

func redactToolArgs(args map[string]string) map[string]string {
	out := make(map[string]string, len(args))
	for key, value := range args {
		tagged := key + "=" + value
		redacted := security.RedactSensitiveText(tagged)
		if redacted != tagged {
			if i := strings.IndexByte(redacted, '='); i >= 0 {
				out[key] = redacted[i+1:]
			} else {
				out[key] = "***"
			}
			continue
		}
		out[key] = security.RedactSensitiveText(value)
	}
	return out
}

// TodoItem 任务清单项（对标 deepagents write_todos）
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

// StepContext 单步执行上下文，供中间件读写
type StepContext struct {
	SysCtx           collector.SystemContext
	State            *AgentState
	History          *[]analyzer.Message
	Reporter         *logger.Reporter
	BatchMode        bool
	StepNum          int
	MaxSteps         int
	SubAgentMaxSteps int
	MemoryStore      *memory.Store
	SessionID        string
	Checkpoint       *CheckpointStore
	UI               UISink // 可选：子 Agent 等中间件回传 UI 事件
	ConfirmFn        func(*AgentAction) bool
	SudoAuthFn       func() bool
	Stop             <-chan struct{}
	Executor         executor.Executor
	TargetName       string
	TargetProto      string
	TargetHost       string
}

type SubAgentTaskAction struct {
	TaskName       string `json:"task_name"`
	TaskPrompt     string `json:"task_prompt"`
	TargetSelector string `json:"target_selector,omitempty"`
	TaskMaxSteps   int    `json:"task_max_steps,omitempty"`
}

// ActionResult 动作执行结果
type ActionResult struct {
	Output       string
	ShouldStop   bool
	FinalReport  string
	SkipApproval bool // filesystem read/ls 等低风险操作
	Streamed     bool // execute 已经实时输出过，经典 stdout 结束时只显示摘要
	ToolResults  []ToolCallResult
	Attachments  []analyzer.ImageAttachment
}

type ToolCallResult struct {
	ID          string
	Name        string
	Output      string
	Error       string
	Attachments []analyzer.ImageAttachment
}

// Middleware 中间件接口（对标 deepagents middleware stack）
type Middleware interface {
	Name() string
	EnhancePrompt(base string, state *AgentState) string
	HandleAction(ctx *StepContext, action *AgentAction) (*ActionResult, bool, error)
}
