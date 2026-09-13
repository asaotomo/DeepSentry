package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/logger"
)

// ModelStepFunc is the model boundary owned by the runtime. Production uses
// analyzer.RunAgentStepWithOptions; deterministic fixtures inject a scripted
// provider without mutating global configuration or starting an HTTP server.
type ModelStepFunc func(analyzer.StepOptions) (analyzer.AgentResponse, error)

// ActionHandlerFunc is the tool/action execution boundary owned by the
// runtime. Production delegates to DeepAgent.HandleAction; controlled outcome
// fixtures inject deterministic target responses while exercising the same
// approvals, history, event and checkpoint lifecycle.
type ActionHandlerFunc func(*StepContext, *AgentAction) (*ActionResult, error)

// RunStatus describes why one RunLoop invocation returned.  Classic CLI and
// detached WebShell callers use it to produce a truthful process exit status
// instead of treating checkpoints, cancellation and max-step exhaustion as a
// successful finish.
type RunStatus string

const (
	RunStatusCompleted     RunStatus = "completed"
	RunStatusFailed        RunStatus = "failed"
	RunStatusCancelled     RunStatus = "cancelled"
	RunStatusAwaitingInput RunStatus = "awaiting_input"
	RunStatusMaxSteps      RunStatus = "max_steps"
)

// RunResult is the terminal state of a single Agent run.
type RunResult struct {
	Status     RunStatus
	Reason     string
	Step       int
	ReportPath string
}

func (r RunResult) Successful() bool { return r.Status == RunStatusCompleted }

// RunLoopConfig Agent 主循环配置
type RunLoopConfig struct {
	ExecutionBudget  *config.ExecutionBudgetConfig // nil inherits configured runtime policy
	DrainInput       func() []analyzer.Message     // IM supplements, consumed only at model boundaries
	SysCtx           collector.SystemContext
	History          *[]analyzer.Message
	Reporter         *logger.Reporter
	ReportPath       string
	BatchMode        bool
	NonInteractive   bool // WebShell/JSON/quiet 等无法稳定二次交互的 stdout 场景
	PauseOnAskUser   bool // 兼容旧 checkpoint 恢复逻辑；非交互主流程默认不允许 ask_user 阻塞
	MaxSteps         int
	SubAgentMaxSteps int
	MultiTurn        bool // TUI 等多轮会话：finish 后保留上下文，支持追问
	ChatReply        bool // 手机/IM 机器人续聊：更强的连续对话约束
	PlanMode         bool // 先澄清/规划，再按计划执行
	CompetitionMode  bool // 10 分钟运维比赛：快速取证、交叉验证、规范答题
	ConfirmFn        func(*AgentAction) bool
	AwaitUserFn      func(*AgentAction) (string, bool)
	SudoAuthFn       func() bool // TUI 暂停渲染并由系统 sudo 安全读取密码
	UI               UISink
	Stop             <-chan struct{}
	ModelStep        ModelStepFunc
	Executor         executor.Executor
	ActionHandler    ActionHandlerFunc
}
