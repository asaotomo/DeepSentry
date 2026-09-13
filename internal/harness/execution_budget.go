package harness

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"

	"ai-edr/internal/config"
)

// A shared model-call ledger bounds parallel delegation as well as the parent.
// Reserving two parent turns allows workers to hand back partial evidence.
type executionLedger struct {
	mu          sync.Mutex
	used, limit int
}

func (l *executionLedger) take(worker bool) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	limit := l.limit
	if worker {
		limit = max(0, limit-2)
	}
	if l.used >= limit {
		return false
	}
	l.used++
	return true
}
func (l *executionLedger) snapshot() (int, int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used, l.limit
}

type executionLease struct {
	policy                         config.ExecutionBudgetConfig
	limit, hard, stalled, progress int
	seen                           map[[32]byte]bool
}

func newExecutionLease(initial int, worker bool, policy config.ExecutionBudgetConfig) *executionLease {
	policy = policy.Normalized()
	hard := policy.MaxSteps
	if worker {
		hard = policy.SubagentMaxSteps
	}
	return &executionLease{policy: policy, limit: min(max(1, initial), hard), hard: hard, seen: map[[32]byte]bool{}}
}
func (l *executionLease) allow(used int) (bool, string) {
	if l.stalled >= l.policy.NoProgressSteps {
		return false, "no_progress"
	}
	if used < l.limit {
		return true, ""
	}
	if used >= l.hard {
		return false, "hard_step_budget"
	}
	if l.progress == 0 {
		return false, "no_progress"
	}
	l.limit = min(l.hard, l.limit+l.policy.ExtensionSteps)
	l.progress = 0
	return true, "extended"
}
func (l *executionLease) observe(action AgentAction, result *ActionResult, err error) {
	if err != nil || result == nil {
		return
	}
	for _, tool := range result.ToolResults {
		if tool.Error != "" {
			return
		}
	}
	// Planning chatter and memory edits do not buy more execution. The model is
	// free to finish at any point; only actual nonempty new tool output renews a lease.
	switch action.Type {
	case ActionTodo, ActionRemember, ActionForget, ActionLoadSkill:
		return
	}
	out := strings.Join(strings.Fields(result.Output), " ")
	if out == "" {
		return
	}
	key := sha256.Sum256([]byte(out))
	if l.seen[key] {
		return
	}
	l.seen[key] = true
	l.progress++
	l.stalled = 0
}
func (l *executionLease) prompt(used int, ledger *executionLedger) string {
	total, ceiling := ledger.snapshot()
	return fmt.Sprintf("\n【执行预算】当前已用 %d 步，规划窗口 %d 步，单 Agent 上限 %d 步；整次任务（含子 Agent）已用 %d/%d 次主/子 Agent 推理轮次。取得新的工具结果会自动延长规划窗口；连续无进展会暂停。按实际任务决定下一步，目标已验证即 finish，不要为了用完预算继续。接近边界时交接已验证结果、未完成项和恢复建议，不能声称未验证的任务已完成。\n", used, l.limit, l.hard, total, ceiling)
}

// Preserve partial evidence while making incomplete delegation machine-readable.
type SubAgentIncompleteError struct{ Reason, Summary string }

func (e *SubAgentIncompleteError) Error() string {
	return fmt.Sprintf("子任务未完成（%s）；部分结果：\n%s", e.Reason, e.Summary)
}
