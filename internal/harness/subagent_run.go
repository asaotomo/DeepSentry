package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness/subagent"
	"ai-edr/internal/logger"
	"ai-edr/internal/memory"
	"ai-edr/internal/security"
	"ai-edr/internal/skills"
	"ai-edr/internal/tools"
	termui "ai-edr/internal/ui"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// SubAgentRunner 子 Agent 运行器（复用 harness middleware，无 sub-sub-agent）
type SubAgentRunner struct {
	Reporter         *logger.Reporter
	budgetPolicy     config.ExecutionBudgetConfig
	budgetLedger     *executionLedger
	Middleware       []Middleware
	State            *AgentState
	SharedState      *AgentState // 主 Agent 会话线索板；并发子 Agent 只共享高信号线索，不共享原始对话
	Catalog          *skills.SkillCatalog
	MemoryStore      *memory.Store
	UseNative        bool
	UI               UISink
	ConfirmFn        func(*AgentAction) bool
	Stop             <-chan struct{}
	MaxStepsCap      int
	TaskMaxSteps     int
	StepFn           func(analyzer.StepOptions) (analyzer.AgentResponse, error)
	Executor         executor.Executor
	Target           config.TargetConfig
	CheckpointParent *CheckpointStore
	CheckpointKey    string // stable key supplied when resuming a saved child
	Parent           *DeepAgent
}

type commandRiskReviewer func(collector.SystemContext, string, string) (string, string, bool)

var makeSubAgentRunner = func(parent *DeepAgent) *SubAgentRunner {
	return NewSubAgentRunner(parent)
}

var subAgentRunSequence atomic.Uint64

// NewSubAgentRunner 创建子 Agent 运行器
func NewSubAgentRunner(parent *DeepAgent) *SubAgentRunner {
	state := NewAgentStateWithSession(parent.State.WorkspaceDir,
		fmt.Sprintf("%s-sub-%d", parent.SessionID, subAgentRunSequence.Add(1)))
	state.ReplaceCoreClues(parent.State.CoreCluesSnapshot())
	return &SubAgentRunner{
		Reporter:         parent.reporter,
		budgetPolicy:     parent.budgetPolicy,
		budgetLedger:     parent.budgetLedger,
		Middleware:       SubAgentMiddlewareStack(parent.Catalog, parent.MemoryStore),
		State:            state,
		SharedState:      parent.State,
		Catalog:          parent.Catalog,
		MemoryStore:      parent.MemoryStore,
		UseNative:        parent.UseNativeTools,
		CheckpointParent: parent.Checkpoint,
		Parent:           parent,
	}
}

// Run 在隔离上下文中运行子 Agent
func (r *SubAgentRunner) Run(spec subagent.Spec, taskPrompt string, sysCtx collector.SystemContext, batchMode bool) (string, error) {
	targetScope := ""
	if strings.TrimSpace(r.Target.Host) != "" {
		protocol := strings.ToLower(strings.TrimSpace(r.Target.Protocol))
		targetScope = memory.ScopeForTarget(true, strings.ToLower(strings.TrimSpace(r.Target.Host)))
		if protocol != "" && protocol != "ssh" {
			targetScope = protocol + ":" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(r.Target.Host)), ":", "_")
		}
	}
	for _, mw := range r.Middleware {
		if memoryMW, ok := mw.(*MemoryMiddleware); ok {
			memoryMW.TargetScope = targetScope
		}
	}
	subHistory := []analyzer.Message{
		{Role: "user", Content: taskPrompt},
	}
	if r.Target.Name != "" || r.Target.Host != "" {
		targetPrompt := fmt.Sprintf("【当前子 Agent 目标】name=%s protocol=%s host=%s user=%s\n所有 execute/read_file/grep/ls/tool 的 target 视角都限定在这台机器；不要操作其他目标。共享线索中来自其他 target 的事实只能用于关联分析，不得未经本目标复核就当作本机事实。",
			r.Target.Name, r.Target.Protocol, r.Target.Host, r.Target.User)
		subHistory = append([]analyzer.Message{{Role: "system", Content: targetPrompt}}, subHistory...)
	}
	store, restored, checkpoint, err := r.openCheckpoint(spec, taskPrompt)
	if err != nil {
		return "", fmt.Errorf("加载子 Agent checkpoint: %w", err)
	}
	startStep := 0
	var results []string
	if restored != nil {
		if restored.State == nil || len(restored.History) == 0 {
			return "", fmt.Errorf("子 Agent checkpoint 缺少状态或历史")
		}
		r.State = restored.State
		subHistory = restored.History
		startStep = restored.StepNum
		results = append(results, checkpoint.Results...)
		if checkpoint.Phase == "complete" {
			return checkpoint.Report, nil
		}
		if checkpoint.Phase == "action_pending" {
			// The process may have stopped after the external side effect. Never
			// replay an in-flight action automatically, even without a native ID.
			subHistory = append(subHistory, analyzer.Message{Role: "user", Content: "恢复提示：上一动作可能已经执行，但结果未能确认。动作：" + checkpoint.PendingAction + "。请先用只读方式核验实际状态；不要直接重放修改。", Synthetic: true})
			checkpoint.Phase = "ready"
			checkpoint.PendingAction = ""
			if err := r.saveCheckpoint(store, checkpoint, startStep, subHistory); err != nil {
				return "", fmt.Errorf("保存子 Agent 恢复边界: %w", err)
			}
		} else if checkpoint.Phase != "ready" {
			return "", fmt.Errorf("未知子 Agent checkpoint 阶段 %q", checkpoint.Phase)
		}
	} else {
		// Match only this worker's assignment, not the shared briefing from
		// other workers. Persist the loaded playbook in its first checkpoint.
		NewSkillsMiddleware(r.Catalog).AutoLoadForQuery(r.State, subAgentAssignmentForEstimate(taskPrompt))
		if store != nil {
			if err := r.saveCheckpoint(store, checkpoint, 0, subHistory); err != nil {
				return "", fmt.Errorf("初始化子 Agent checkpoint: %w", err)
			}
		}
	}

	maxSteps := resolveSubAgentMaxSteps(spec, subAgentAssignmentForEstimate(taskPrompt), r.TaskMaxSteps, r.MaxStepsCap)

	var lease *executionLease
	if r.budgetPolicy.Enabled {
		lease = newExecutionLease(maxSteps, true, r.budgetPolicy)
		maxSteps = lease.limit
	}
	stopReason := "max_steps"

	executionGuidance := "- Shell-first：优先使用目标机原生 Shell 直接排查；命令不适合或需结构化解析时再选内置工具\n"
	if spec.ExecutionProfile == "network_device" {
		executionGuidance = "- 网络设备 CLI-first：使用厂商 display/show 或 network_device_diagnose/network_device_baseline；设备 CLI 不是 Shell，禁止拼接管道、分号或文件系统命令\n"
	}
	extraBase := spec.SystemPrompt + "\n" + `
【子 Agent 模式】
` + executionGuidance + `- 主 Agent 负责跨分工汇总、最终决策和对用户交付；你只负责唯一分工，不能扩展目标或替其他角色下结论
- 主机文件证据可用 read_file/grep/ls；网络设备优先设备专用工具和原生 CLI
- 禁止委派子 Agent (task)
- 已知核心线索会随上下文提供。不要重复无意义探测；需要复核时说明复核目的
- 完成后 action="finish"，final_report 必须按以下顺序交接：任务状态、核心结论、关键证据、冲突/不确定项、建议主 Agent 下一步
- 结论必须区分“已验证事实”和“推断”，证据注明命令、目标、路径或原始指标
`

	for step := startStep; ; step++ {
		if lease != nil {
			allowed, reason := lease.allow(step)
			if !allowed {
				stopReason = reason
				break
			}
			maxSteps = lease.limit
		} else if step >= maxSteps {
			break
		}

		if shouldStop(r.Stop) {
			return fmt.Sprintf("子 Agent [%s] 已按用户请求停止。", spec.Name), nil
		}
		if r.UI != nil {
			r.UI.Emit(UIEvent{
				Kind:           EventSubAgentStep,
				Message:        fmt.Sprintf("%s step %d/%d", spec.Name, step+1, maxSteps),
				Detail:         taskPrompt,
				TargetName:     r.Target.Name,
				TargetProtocol: r.Target.Protocol,
				TargetHost:     r.Target.Host,
			})
		}
		// Pull the latest bounded clue board before every model turn. This gives
		// parallel workers useful cross-agent evidence without merging raw histories.
		if r.SharedState != nil {
			r.State.ReplaceCoreClues(r.SharedState.CoreCluesSnapshot())
		}
		extraPrompt := extraBase
		if lease != nil {
			extraPrompt += lease.prompt(step, r.budgetLedger)
		}
		for _, mw := range r.Middleware {
			extraPrompt = mw.EnhancePrompt(extraPrompt, r.State)
		}

		stepFn := r.StepFn
		if stepFn == nil {
			stepFn = analyzer.RunAgentStepWithOptions
		}
		if lease != nil {
			if !r.budgetLedger.take(true) {
				stopReason = "shared_step_budget"
				break
			}
			lease.stalled++
		}
		llmCtx, cancelLLM := contextFromStop(r.Stop)
		resp, err := stepFn(analyzer.StepOptions{
			Context:        llmCtx,
			SysCtx:         sysCtx,
			History:        &subHistory,
			ExtraPrompt:    extraPrompt,
			UseNativeTools: r.UseNative,
		})
		cancelLLM()
		if shouldStop(r.Stop) {
			return fmt.Sprintf("子 Agent [%s] 已按用户请求停止。", spec.Name), nil
		}
		if err != nil {
			return "", fmt.Errorf("子 Agent [%s] 第 %d 步失败: %s", spec.Name, step+1, security.RedactSensitiveText(err.Error()))
		}

		action := ParseAction(resp)
		auditAction := action
		auditAction.TargetName = r.Target.Name
		auditAction.TargetProtocol = r.Target.Protocol
		auditAction.TargetHost = r.Target.Host

		if action.Type == ActionFinish || action.IsFinished {
			report := "子 Agent 任务完成"
			if action.FinalReport != "" {
				report = action.FinalReport
			} else if action.Thought != "" {
				report = action.Thought
			}
			r.observeCoreClues(report, "subagent/"+spec.Name)
			if checkpoint != nil {
				checkpoint.Phase = "complete"
				checkpoint.Report = report
				if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
					return "", fmt.Errorf("保存子 Agent 完成状态: %w", err)
				}
			}
			return report, nil
		}

		if isEmptyAction(action) {
			subHistory = append(subHistory, analyzer.Message{
				Role:    "assistant",
				Content: actionToJSON(action),
			})
			subHistory = append(subHistory, analyzer.Message{
				Role:      "user",
				Content:   "请执行具体 action 或 finish 返回结论。",
				Synthetic: true,
			})
			if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
				return "", err
			}
			continue
		}
		if r.UI != nil {
			actCopy := RedactedAction(action)
			enrichActionExecutionTargetFor(&actCopy, r.Target)
			r.UI.Emit(UIEvent{Kind: EventSubAgentAction, Message: spec.Name, Action: &actCopy, TargetName: r.Target.Name, TargetProtocol: r.Target.Protocol, TargetHost: r.Target.Host})
		}

		if decision := r.State.LoopBeforeExecute(action); !decision.Allow {
			warning := strings.TrimSpace(decision.Warning)
			if warning == "" {
				warning = strings.TrimSpace(decision.Output)
			}
			if err := recordActionEvidence(r.Reporter, auditAction, &ActionResult{Output: decision.Output}, nil, step+1, "blocked", "loop_guard", spec.Name, 0); err != nil {
				return "", err
			}
			if decision.HardStop {
				return warning, nil
			}
			result := blockedActionResult(action, decision.Output)
			appendActionResultHistory(&subHistory, action, result)
			if warning != "" {
				subHistory = append(subHistory, analyzer.Message{Role: "user", Content: warning, Synthetic: true})
			}
			r.State.LoopRecordSkip(action)
			if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
				return "", err
			}
			continue
		}

		if action.Type == ActionTask {
			subHistory = append(subHistory, analyzer.Message{
				Role: "user", Content: "子 Agent 不能委派 task，请直接执行。", Synthetic: true,
			})
			if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
				return "", err
			}
			continue
		}
		if action.Type == ActionAskUser {
			question := strings.TrimSpace(action.Question)
			if question == "" {
				question = strings.TrimSpace(action.Thought)
			}
			if question == "" {
				question = "缺少必要信息"
			}
			out := fmt.Sprintf("子 Agent [%s] 需要主流程补充信息后才能继续: %s", spec.Name, security.RedactSensitiveText(question))
			if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
				return "", err
			}
			return out, nil
		}

		// 子 Agent execute 风控：与主 Agent 一致，先 AI 复核，再按需人工确认。
		if action.Type == ActionExecute || action.Command != "" {
			allowed, feedback := authorizeSubAgentExecute(&action, sysCtx, batchMode, r.UI, r.ConfirmFn, reviewCommandRiskWithAI)
			if !allowed {
				if err := recordActionEvidence(r.Reporter, auditAction, nil, nil, step+1, "denied", "subagent_policy", spec.Name, 0); err != nil {
					return "", err
				}
				msg := fmt.Sprintf("[步骤 %d] 高危命令未执行: %s", step+1, action.Command)
				results = append(results, msg)
				subHistory = append(subHistory, analyzer.Message{
					Role: "user", Content: feedback, Synthetic: true,
				})
				if checkpoint != nil {
					checkpoint.Results = append([]string(nil), results...)
				}
				if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
					return "", err
				}
				continue
			}
		} else if allowed, feedback := authorizeSubAgentMutation(&action, batchMode, r.UI, r.ConfirmFn); !allowed {
			if err := recordActionEvidence(r.Reporter, auditAction, nil, nil, step+1, "denied", "subagent_policy", spec.Name, 0); err != nil {
				return "", err
			}
			msg := fmt.Sprintf("[步骤 %d] 中高风险动作未执行: %s", step+1, action.Type)
			results = append(results, msg)
			subHistory = append(subHistory, analyzer.Message{Role: "user", Content: feedback, Synthetic: true})
			if checkpoint != nil {
				checkpoint.Results = append([]string(nil), results...)
			}
			if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
				return "", err
			}
			continue
		}

		stepCtx := &StepContext{
			SysCtx:      sysCtx,
			State:       r.State,
			History:     &subHistory,
			BatchMode:   batchMode,
			StepNum:     step + 1,
			MaxSteps:    maxSteps,
			MemoryStore: r.MemoryStore,
			UI:          r.UI,
			ConfirmFn:   r.ConfirmFn,
			Stop:        r.Stop,
			Executor:    firstExecutor(r.Executor),
			TargetName:  r.Target.Name,
			TargetProto: r.Target.Protocol,
			TargetHost:  r.Target.Host,
		}

		agent := &DeepAgent{Middleware: r.Middleware, State: r.State, MemoryStore: r.MemoryStore}
		prepareToolCallExecution(r.State, &action)
		if checkpoint != nil {
			checkpoint.Phase = "action_pending"
			checkpoint.PendingAction = truncate(security.RedactSensitiveText(actionToJSON(RedactedAction(action))), 700)
			if err := r.saveCheckpoint(store, checkpoint, step, subHistory); err != nil {
				return "", fmt.Errorf("保存子 Agent 工具执行边界: %w", err)
			}
		}
		started := time.Now()
		result, err := agent.HandleAction(stepCtx, &action)
		status := "success"
		if err != nil {
			status = "error"
		} else if result == nil {
			status = "empty_result"
		}
		if journalErr := recordActionEvidence(r.Reporter, auditAction, result, err, step+1, status, "subagent_policy", spec.Name, time.Since(started)); journalErr != nil {
			return "", journalErr
		}
		if lease != nil {
			lease.observe(action, result, err)
		}
		if err != nil {
			failActionToolCalls(r.State, action)
			return "", err
		}
		if result != nil && action.Type == ActionExecute && shouldStop(r.Stop) && strings.HasPrefix(strings.TrimSpace(result.Output), "执行错误:") {
			// Some executors report interruption in their text output while
			// HandleAction returns nil error. Keep the pre-action checkpoint so
			// resume verifies the side effect instead of replaying the command.
			return fmt.Sprintf("子 Agent [%s] 已按用户请求停止；命令结果未确认。", spec.Name), nil
		}
		completeActionToolCalls(r.State, action)

		if result.ShouldStop {
			if checkpoint != nil {
				checkpoint.Phase = "complete"
				checkpoint.PendingAction = ""
				checkpoint.Report = result.FinalReport
				if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
					return "", err
				}
			}
			return result.FinalReport, nil
		}

		out := security.RedactSensitiveText(result.Output)
		r.observeCoreClues(out, "subagent/"+spec.Name+"/"+string(action.Type))
		if len(out) > 4000 {
			out = safeUTF8BytePrefix(out, 4000) + "\n...(输出已截断)..."
		}
		results = append(results, fmt.Sprintf("[步骤 %d] %s\n%s", step+1, action.Type, out))

		result.Output = out
		appendActionResultHistory(&subHistory, action, result)
		halt := false
		haltMessage := ""
		if warning := r.State.LoopAfterExecute(action, out, false); warning != "" {
			subHistory = append(subHistory, analyzer.Message{Role: "user", Content: warning, Synthetic: true})
			if r.State.LoopShouldHalt() {
				halt, haltMessage = true, warning
			}
		}
		if checkpoint != nil {
			checkpoint.Phase = "ready"
			checkpoint.PendingAction = ""
			checkpoint.Results = append([]string(nil), results...)
			if err := r.saveCheckpoint(store, checkpoint, step+1, subHistory); err != nil {
				return "", fmt.Errorf("保存子 Agent 工具结果: %w", err)
			}
		}
		if halt {
			return haltMessage, nil
		}
		if shouldStop(r.Stop) {
			return fmt.Sprintf("子 Agent [%s] 已按用户请求停止。", spec.Name), nil
		}
	}

	summary := strings.Join(results, "\n---\n")
	if len(summary) > 6000 {
		summary = safeUTF8BytePrefix(summary, 6000) + "\n...(子 Agent 输出已截断)..."
	}
	if lease != nil {
		return summary, &SubAgentIncompleteError{Reason: stopReason, Summary: summary}
	}
	return fmt.Sprintf("子 Agent [%s] 未完成，执行暂停（%s），部分结果:\n%s", spec.Name, stopReason, summary), nil
}

func (r *SubAgentRunner) observeCoreClues(text, source string) {
	if r.Target.Name != "" || r.Target.Host != "" {
		source += "@" + executor.TargetDisplayName(r.Target)
	}
	if r.State != nil {
		r.State.ObserveCoreClues(text, source)
	}
	if r.SharedState != nil && r.SharedState != r.State {
		r.SharedState.ObserveCoreClues(text, source)
	}
}

func authorizeSubAgentMutation(action *AgentAction, batchMode bool, ui UISink, confirmFn func(*AgentAction) bool) (bool, string) {
	if action == nil {
		return false, "动作为空，请重新规划。"
	}
	needsConfirm := false
	switch action.Type {
	case ActionWriteFile, ActionEditFile:
		action.RiskLevel = tools.RiskHigh
		action.Reason = "子 Agent 将修改文件"
		needsConfirm = true
	case ActionTool:
		action.RiskLevel, action.Reason = classifyToolRisk(*action)
		needsConfirm = action.RiskLevel == tools.RiskHigh || action.RiskLevel == tools.RiskMedium
		if !needsConfirm && ui != nil {
			ui.Emit(UIEvent{Kind: EventRiskAuto, Message: fmt.Sprintf("%s子 Agent 工具 [%s] 低风险 -> 自动执行", termui.Prefix("🟢", "[LOW]"), action.ToolName)})
		}
	case ActionToolBatch:
		for _, call := range action.ToolCalls {
			risk, reason := classifyToolRisk(AgentAction{Type: ActionTool, ToolName: call.Name, ToolArgs: call.Args})
			if risk == tools.RiskHigh || risk == tools.RiskMedium {
				action.RiskLevel = risk
				action.Reason = fmt.Sprintf("批量工具 %s: %s", call.Name, reason)
				needsConfirm = true
				break
			}
		}
	default:
		return true, ""
	}
	if !needsConfirm {
		return true, ""
	}
	if batchMode {
		if ui != nil {
			ui.Emit(UIEvent{Kind: EventBatchAuto, Message: "Batch 模式已启用：子 Agent 动作自动批准"})
		}
		return true, ""
	}
	confirmAction := RedactedAction(*action)
	if confirmFn != nil && confirmFn(&confirmAction) {
		return true, ""
	}
	return false, "用户拒绝该中高风险动作；请采用只读或低风险替代方案。"
}

func resolveSubAgentMaxSteps(spec subagent.Spec, taskPrompt string, requested, cap int) int {
	if cap <= 0 {
		cap = 15
	}
	base := spec.MaxSteps
	if base <= 0 {
		base = 15
	}
	estimated := estimateSubAgentSteps(taskPrompt, base)
	maxSteps := max(base, estimated)
	if requested > 0 {
		maxSteps = max(maxSteps, requested)
	}
	if maxSteps > cap {
		maxSteps = cap
	}
	if maxSteps < 1 {
		maxSteps = 1
	}
	return maxSteps
}

func estimateSubAgentSteps(taskPrompt string, base int) int {
	text := strings.ToLower(taskPrompt)
	estimate := base
	complexHints := []string{"完整", "综合", "所有", "全部", "多", "日志", "时间线", "证据链", "关联", "异常", "webshell", "漏洞", "基线", "横向", "提权", "登录", "auth", "syslog", "nginx", "apache", "access", "error"}
	for _, hint := range complexHints {
		if strings.Contains(text, strings.ToLower(hint)) {
			estimate += 2
		}
	}
	if n := strings.Count(taskPrompt, "\n") + strings.Count(taskPrompt, "；") + strings.Count(taskPrompt, ";"); n > 2 {
		estimate += n
	}
	if len([]rune(taskPrompt)) > 240 {
		estimate += 4
	}
	if estimate > 35 {
		estimate = 35
	}
	return estimate
}

func subAgentAssignmentForEstimate(prompt string) string {
	const marker = "【你的唯一分工】"
	if idx := strings.Index(prompt, marker); idx >= 0 {
		prompt = prompt[idx+len(marker):]
		if end := strings.Index(prompt, "不要替其他子 Agent"); end >= 0 {
			prompt = prompt[:end]
		}
	}
	return strings.TrimSpace(prompt)
}

func authorizeSubAgentExecute(action *AgentAction, sysCtx collector.SystemContext, batchMode bool, ui UISink, confirmFn func(*AgentAction) bool, reviewer commandRiskReviewer) (bool, string) {
	if action == nil || strings.TrimSpace(action.Command) == "" {
		return true, ""
	}
	if security.LooksLikeFlagCommand(action.Command) {
		action.RiskLevel = "blocked"
		action.Reason = "比赛 flag/文件内容不是 shell 命令"
		return false, "flag{...} 是解出的比赛答案或文件内容，不是可执行命令。不要 execute，请用 finish 提交该 flag。"
	}
	if batchMode {
		if ui != nil {
			ui.Emit(UIEvent{Kind: EventBatchAuto, Message: "Batch 模式已启用：子 Agent 命令自动批准"})
		}
		return true, ""
	}

	risk, reason := security.CheckRisk(action.Command)
	action.RiskLevel = risk
	action.Reason = reason
	if risk != "high" {
		if ui != nil {
			ui.Emit(UIEvent{Kind: EventRiskAuto, Message: "子 Agent 风险: 低 -> 自动执行"})
		}
		return true, ""
	}

	if security.CanReviewHighRiskWithAI(action.Command, reason) {
		if ui != nil {
			ui.Emit(UIEvent{Kind: EventInfo, Message: "子 Agent 规则判高，正在进行 AI 风险复核..."})
		}
		if reviewer == nil {
			reviewer = reviewCommandRiskWithAI
		}
		if reviewedRisk, reviewedReason, ok := reviewer(sysCtx, action.Command, reason); ok {
			action.RiskLevel = reviewedRisk
			action.Reason = reviewedReason
			if reviewedRisk == "low" {
				if ui != nil {
					ui.Emit(UIEvent{Kind: EventRiskAuto, Message: "子 Agent AI 复核: 低风险 -> 自动执行 (" + reviewedReason + ")"})
				}
				return true, ""
			}
		}
	}

	confirmAction := RedactedAction(*action)
	if confirmFn != nil && confirmFn(&confirmAction) {
		return true, ""
	}
	if ui != nil {
		ui.Emit(UIEvent{Kind: EventDenied, Message: "子 Agent 高危命令未获授权"})
	}
	return false, fmt.Sprintf("用户未批准子 Agent 高危命令: %s。请改用只读、低风险方式继续。", action.Command)
}

// RunSubAgentLoop 便捷入口
func RunSubAgentLoop(parent *DeepAgent, spec subagent.Spec, taskPrompt string, sysCtx collector.SystemContext, batchMode bool) (string, error) {
	return makeSubAgentRunner(parent).Run(spec, taskPrompt, sysCtx, batchMode)
}

// RunSubAgentLoopWithUI 将子 Agent 内部步骤透传给父级 UI。
func RunSubAgentLoopWithUI(parent *DeepAgent, spec subagent.Spec, taskPrompt string, sysCtx collector.SystemContext, batchMode bool, ui UISink, confirmFn func(*AgentAction) bool, maxStepsCap, taskMaxSteps int) (string, error) {
	return RunSubAgentLoopWithUIAndStop(parent, spec, taskPrompt, sysCtx, batchMode, ui, confirmFn, maxStepsCap, taskMaxSteps, nil)
}

func RunSubAgentLoopWithUIAndStop(parent *DeepAgent, spec subagent.Spec, taskPrompt string, sysCtx collector.SystemContext, batchMode bool, ui UISink, confirmFn func(*AgentAction) bool, maxStepsCap, taskMaxSteps int, stop <-chan struct{}) (string, error) {
	r := makeSubAgentRunner(parent)
	r.UI = ui
	r.ConfirmFn = confirmFn
	r.MaxStepsCap = maxStepsCap
	r.TaskMaxSteps = taskMaxSteps
	r.Stop = stop
	return r.Run(spec, taskPrompt, sysCtx, batchMode)
}

func RunSubAgentLoopForTarget(parent *DeepAgent, spec subagent.Spec, taskPrompt string, sysCtx collector.SystemContext, batchMode bool, ui UISink, confirmFn func(*AgentAction) bool, maxStepsCap, taskMaxSteps int, target config.TargetConfig, stop <-chan struct{}) (string, error) {
	ex, err := executor.NewEphemeralExecutor(target)
	if err != nil {
		return "", err
	}
	defer ex.Close()
	r := makeSubAgentRunner(parent)
	r.UI = ui
	r.ConfirmFn = confirmFn
	r.MaxStepsCap = maxStepsCap
	r.TaskMaxSteps = taskMaxSteps
	r.Stop = stop
	r.Executor = ex
	r.Target = target
	return r.Run(spec, taskPrompt, sysCtx, batchMode)
}

func firstExecutor(ex executor.Executor) executor.Executor {
	if ex != nil {
		return ex
	}
	return executor.Current
}

func isEmptyAction(action AgentAction) bool {
	if action.Type != "" {
		return false
	}
	if action.Command != "" || action.ToolName != "" {
		return false
	}
	if action.Path != "" || action.TaskName != "" || action.SkillName != "" {
		return false
	}
	if action.MemoryKey != "" || len(action.Todos) > 0 {
		return false
	}
	if action.Question != "" {
		return false
	}
	return !action.IsFinished
}
