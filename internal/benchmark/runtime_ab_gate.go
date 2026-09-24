package benchmark

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RuntimeABGateReport is the auditable release artifact used before changing
// the product default. Controlled fixtures prove execution/recovery semantics;
// real probes prove tool-selection quality and token/latency guardrails with
// the exact same configured model.
type RuntimeABGateReport struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Legacy      *RuntimeProbeReport `json:"legacy"`
	V3          *RuntimeProbeReport `json:"v3"`
	Controlled  ControlledABGate    `json:"controlled"`
	Decision    AcceptanceDecision  `json:"decision"`
}

type ControlledABGate struct {
	Legacy   ControlledRuntimeReport `json:"legacy"`
	V3       ControlledRuntimeReport `json:"v3"`
	Decision AcceptanceDecision      `json:"decision"`
}

// EvaluateRuntimeProbeAB enforces the real-model part of the published gate.
// It deliberately rejects sampled reports: release evidence must contain all
// 24 distinct security tasks and at least three observations per task/mode.
func EvaluateRuntimeProbeAB(legacy, v3 *RuntimeProbeReport) AcceptanceDecision {
	decision := AcceptanceDecision{Passed: true}
	require := func(ok bool, reason string) {
		if !ok {
			decision.Passed = false
			decision.Reasons = append(decision.Reasons, reason)
		}
	}
	if legacy == nil || v3 == nil {
		require(false, "真实模型 legacy/v3 报告不完整")
		return decision
	}
	require(strings.EqualFold(legacy.Mode, "legacy"), "legacy 报告运行时标识错误")
	require(strings.EqualFold(v3.Mode, "v3"), "v3 报告运行时标识错误")
	require(legacy.Provider == v3.Provider && legacy.Model == v3.Model, "A/B 未使用同一 provider/model")
	require(legacy.Repetitions >= 3 && v3.Repetitions >= 3, "每种运行时必须至少重复 3 次")
	require(legacy.TaskCount == len(RuntimeSecurityTasks()) && v3.TaskCount == len(RuntimeSecurityTasks()), "真实模型门禁必须覆盖全部 24 个不同安全任务")
	legacyCoverage, legacyErr := validateProbeCoverage(legacy)
	v3Coverage, v3Err := validateProbeCoverage(v3)
	require(legacyErr == nil, "legacy 任务覆盖不完整: "+errorText(legacyErr))
	require(v3Err == nil, "v3 任务覆盖不完整: "+errorText(v3Err))
	require(equalStringSlices(legacyCoverage, v3Coverage), "legacy/v3 任务集合不一致")
	require(v3.TaskSuccessRate >= 0.85 || v3.TaskSuccessRate-legacy.TaskSuccessRate >= 0.15, "真实模型任务成功率未达到 85% 或相对提升 15 个百分点")
	require(v3.CorrectToolSelectionRate >= 0.95, "真实模型正确工具选择率低于 95%")
	require(v3.ValidToolCallRate >= 0.99, "真实模型有效工具调用率低于 99%")
	if legacy.P95Tokens > 0 {
		require(float64(v3.P95Tokens) <= float64(legacy.P95Tokens)*1.10, "真实模型 p95 Token 超过 legacy 110%")
	}
	if legacy.P95Latency > 0 {
		require(v3.P95Latency <= time.Duration(float64(legacy.P95Latency)*1.15), "真实模型 p95 延迟超过 legacy 115%")
	}
	return decision
}

func validateProbeCoverage(report *RuntimeProbeReport) ([]string, error) {
	if report == nil {
		return nil, fmt.Errorf("report is nil")
	}
	want := make(map[string]bool, len(RuntimeSecurityTasks()))
	for _, task := range RuntimeSecurityTasks() {
		want[task.ID] = true
	}
	counts := make(map[string]int, len(want))
	for _, observation := range report.Observations {
		if !want[observation.TaskID] {
			return nil, fmt.Errorf("unexpected task %s", observation.TaskID)
		}
		counts[observation.TaskID]++
	}
	ids := make([]string, 0, len(counts))
	for id := range want {
		if counts[id] != report.Repetitions {
			return nil, fmt.Errorf("%s observations=%d want=%d", id, counts[id], report.Repetitions)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func CombineAcceptanceDecisions(decisions ...AcceptanceDecision) AcceptanceDecision {
	combined := AcceptanceDecision{Passed: true}
	for _, decision := range decisions {
		if decision.Passed {
			continue
		}
		combined.Passed = false
		combined.Reasons = append(combined.Reasons, decision.Reasons...)
	}
	return combined
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
