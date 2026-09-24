package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ai-edr/internal/benchmark"
	"ai-edr/internal/tui"
	"ai-edr/internal/ui"
)

func main() {
	cfgPath := flag.String("c", "/tmp/deepsentry-smoke.yaml", "配置文件路径")
	outDir := flag.String("o", "reports/benchmark", "报告输出目录")
	skipLLM := flag.Bool("skip-llm", false, "跳过 LLM 相关场景")
	skipRemote := flag.Bool("skip-remote", false, "跳过远程/SSH 场景")
	benchTUI := flag.Bool("tui", false, "TUI 可视化报告")
	runtimeProbe := flag.Bool("runtime-probe", false, "运行真实模型 Runtime 工具选择 A/B 探针（不执行目标工具）")
	runtimeABGate := flag.Bool("runtime-ab-gate", false, "运行完整 24 题×3 次运行时质量与恢复门禁")
	runtimeABRecheck := flag.String("runtime-ab-recheck", "", "复用既有 A/B 报告中的 legacy/受控基线，只重跑修复后的 v3 全量门禁")
	runtimeABEvaluate := flag.String("runtime-ab-evaluate", "", "离线重新评估既有完整 A/B 报告，不发起模型请求")
	controlledGate := flag.Bool("controlled-runtime-gate", false, "运行 24 题受控端到端 Runtime 门禁（模拟目标，不连接生产环境）")
	repetitions := flag.Int("repetitions", 3, "runtime-probe 每题重复次数")
	maxProbeTasks := flag.Int("max-probe-tasks", 0, "runtime-probe 最多任务数，0 表示全部 24 题")
	runtimeMode := flag.String("runtime-mode", "", "runtime-probe 覆盖配置中的运行时：legacy|v3")
	flag.Parse()

	if len(flag.Args()) > 0 {
		*cfgPath = flag.Args()[0]
	}
	if *runtimeABEvaluate != "" {
		runRuntimeABEvaluate(*outDir, *runtimeABEvaluate)
		return
	}
	if *runtimeABRecheck != "" {
		runRuntimeABRecheck(*cfgPath, *outDir, *repetitions, *runtimeABRecheck)
		return
	}
	if *runtimeABGate {
		runRuntimeABGate(*cfgPath, *outDir, *repetitions)
		return
	}
	if *runtimeProbe {
		runRuntimeProbe(*cfgPath, *outDir, *repetitions, *maxProbeTasks, *runtimeMode)
		return
	}
	if *controlledGate {
		runControlledRuntimeGate(*outDir, *repetitions)
		return
	}

	if *benchTUI {
		if err := tui.RunBenchmark(*cfgPath, *skipLLM, *skipRemote); err != nil {
			fmt.Fprintf(os.Stderr, "%s%v\n", ui.Prefix("❌", "[ERR]"), err)
			os.Exit(1)
		}
		return
	}

	fmt.Println(ui.Prefix("🚀", "[RUN]") + "DeepSentry Agent Benchmark 启动...")
	report, err := benchmark.RunSuite(*cfgPath, *skipLLM, *skipRemote)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s%v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}

	benchmark.PrintSummary(report)

	jsonPath, mdPath, err := benchmark.WriteReports(report, *outDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s报告写入失败: %v\n", ui.Prefix("⚠️", "[WARN]"), err)
	} else {
		abs, _ := filepath.Abs(mdPath)
		fmt.Printf("\n%s报告已保存:\n   JSON: %s\n   MD:   %s\n", ui.Prefix("📊", "[STAT]"), jsonPath, abs)
	}

	if report.OverallScore < 60 {
		os.Exit(1)
	}
}

func runRuntimeABGate(cfgPath, outDir string, repetitions int) {
	if repetitions < 3 {
		repetitions = 3
	}
	fmt.Println(ui.Prefix("🧪", "[GATE]") + "Runtime v3 完整发布门禁启动：24 题、同模型、legacy/v3 各至少 3 次...")
	controlledLegacy, err := benchmark.RunControlledRuntimeFixtures("legacy", repetitions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%scontrolled legacy: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	controlledV3, err := benchmark.RunControlledRuntimeFixtures("v3", repetitions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%scontrolled v3: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	controlledDecision := benchmark.EvaluateControlledRuntimeV3Gate(controlledLegacy.Score, controlledV3.Score)
	fmt.Println("[1/3] 受控执行/恢复门禁完成；开始 legacy 真实模型探针")
	legacy, err := benchmark.RunRuntimeProbeMode(context.Background(), cfgPath, "legacy", repetitions, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%slegacy probe: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	fmt.Printf("[2/3] legacy 完成：success=%.1f%% valid=%.1f%%；开始 v3\n", legacy.TaskSuccessRate*100, legacy.ValidToolCallRate*100)
	v3, err := benchmark.RunRuntimeProbeMode(context.Background(), cfgPath, "v3", repetitions, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sv3 probe: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	realDecision := benchmark.EvaluateRuntimeProbeAB(legacy, v3)
	decision := benchmark.CombineAcceptanceDecisions(controlledDecision, realDecision)
	report := benchmark.RuntimeABGateReport{
		GeneratedAt: time.Now().UTC(), Legacy: legacy, V3: v3,
		Controlled: benchmark.ControlledABGate{Legacy: controlledLegacy, V3: controlledV3, Decision: controlledDecision},
		Decision:   decision,
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "%s创建报告目录失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s序列化报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	path := filepath.Join(outDir, fmt.Sprintf("runtime_ab_gate_%s.json", time.Now().Format("20060102_150405")))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s写入报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("[3/3] v3 完成：success=%.1f%% valid=%.1f%% p95_tokens=%d p95_latency=%s\n", v3.TaskSuccessRate*100, v3.ValidToolCallRate*100, v3.P95Tokens, v3.P95Latency.Round(time.Millisecond))
	fmt.Printf("passed=%v report=%s\n", decision.Passed, abs)
	if !decision.Passed {
		for _, reason := range decision.Reasons {
			fmt.Printf("- %s\n", reason)
		}
		os.Exit(1)
	}
}

func runRuntimeABEvaluate(outDir, reportPath string) {
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s读取 A/B 报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	var report benchmark.RuntimeABGateReport
	if err := json.Unmarshal(raw, &report); err != nil {
		fmt.Fprintf(os.Stderr, "%s解析 A/B 报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	controlled := benchmark.EvaluateControlledRuntimeV3Gate(report.Controlled.Legacy.Score, report.Controlled.V3.Score)
	real := benchmark.EvaluateRuntimeProbeAB(report.Legacy, report.V3)
	report.Controlled.Decision = controlled
	report.Decision = benchmark.CombineAcceptanceDecisions(controlled, real)
	report.GeneratedAt = time.Now().UTC()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "%s创建报告目录失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s序列化报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	path := filepath.Join(outDir, fmt.Sprintf("runtime_ab_evaluated_%s.json", time.Now().Format("20060102_150405")))
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s写入报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("passed=%v report=%s\n", report.Decision.Passed, abs)
	for _, reason := range report.Decision.Reasons {
		fmt.Printf("- %s\n", reason)
	}
	if !report.Decision.Passed {
		os.Exit(1)
	}
}

func runRuntimeABRecheck(cfgPath, outDir string, repetitions int, previousPath string) {
	if repetitions < 3 {
		repetitions = 3
	}
	raw, err := os.ReadFile(previousPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s读取既有 A/B 报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	var previous benchmark.RuntimeABGateReport
	if err := json.Unmarshal(raw, &previous); err != nil {
		fmt.Fprintf(os.Stderr, "%s解析既有 A/B 报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	if previous.Legacy == nil || !previous.Controlled.Decision.Passed {
		fmt.Fprintf(os.Stderr, "%s既有报告缺少已通过的 legacy/受控基线\n", ui.Prefix("❌", "[ERR]"))
		os.Exit(1)
	}
	fmt.Println(ui.Prefix("🧪", "[RECHECK]") + "复用已完成 legacy/受控基线，只重跑修复后的 v3 24题×3次...")
	progress := func(completed, total int, observation benchmark.RuntimeProbeObservation) {
		if completed == total || completed%6 == 0 {
			status := "ok"
			if observation.Error != "" {
				status = "error"
			}
			fmt.Printf("v3 progress=%d/%d last=%s status=%s\n", completed, total, observation.TaskID, status)
		}
	}
	v3, err := benchmark.RunRuntimeProbeModeWithProgress(context.Background(), cfgPath, "v3", repetitions, 0, progress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sv3 probe: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	controlledDecision := benchmark.EvaluateControlledRuntimeV3Gate(previous.Controlled.Legacy.Score, previous.Controlled.V3.Score)
	realDecision := benchmark.EvaluateRuntimeProbeAB(previous.Legacy, v3)
	decision := benchmark.CombineAcceptanceDecisions(controlledDecision, realDecision)
	report := benchmark.RuntimeABGateReport{
		GeneratedAt: time.Now().UTC(), Legacy: previous.Legacy, V3: v3,
		Controlled: benchmark.ControlledABGate{Legacy: previous.Controlled.Legacy, V3: previous.Controlled.V3, Decision: controlledDecision}, Decision: decision,
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "%s创建报告目录失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s序列化报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	path := filepath.Join(outDir, fmt.Sprintf("runtime_ab_recheck_%s.json", time.Now().Format("20060102_150405")))
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s写入报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("v3 success=%.1f%% selection=%.1f%% valid=%.1f%% p95_tokens=%d p95_latency=%s\n", v3.TaskSuccessRate*100, v3.CorrectToolSelectionRate*100, v3.ValidToolCallRate*100, v3.P95Tokens, v3.P95Latency.Round(time.Millisecond))
	fmt.Printf("passed=%v report=%s\n", decision.Passed, abs)
	if !decision.Passed {
		for _, reason := range decision.Reasons {
			fmt.Printf("- %s\n", reason)
		}
		os.Exit(1)
	}
}

func runControlledRuntimeGate(outDir string, repetitions int) {
	fmt.Println(ui.Prefix("🧪", "[GATE]") + "24 题受控端到端 Runtime 门禁启动（模拟目标，不连接生产环境）...")
	legacy, err := benchmark.RunControlledRuntimeFixtures("legacy", repetitions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%slegacy: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	v3, err := benchmark.RunControlledRuntimeFixtures("v3", repetitions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sv3: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	decision := benchmark.EvaluateControlledRuntimeV3Gate(legacy.Score, v3.Score)
	payload := struct {
		GeneratedAt time.Time                         `json:"generated_at"`
		Legacy      benchmark.ControlledRuntimeReport `json:"legacy"`
		V3          benchmark.ControlledRuntimeReport `json:"v3"`
		Decision    benchmark.AcceptanceDecision      `json:"decision"`
	}{GeneratedAt: time.Now().UTC(), Legacy: legacy, V3: v3, Decision: decision}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "%s创建报告目录失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s序列化报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	path := filepath.Join(outDir, fmt.Sprintf("controlled_runtime_gate_%s.json", time.Now().Format("20060102_150405")))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s写入报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	fmt.Printf("legacy success=%.1f%% evidence=%.1f%% recovery=%.1f%%\n", legacy.Score.TaskSuccessRate*100, legacy.Score.EvidenceCoverageRate*100, legacy.Score.RecoverableFailureRate*100)
	fmt.Printf("v3     success=%.1f%% evidence=%.1f%% recovery=%.1f%% valid_calls=%.1f%% duplicates=%d\n", v3.Score.TaskSuccessRate*100, v3.Score.EvidenceCoverageRate*100, v3.Score.RecoverableFailureRate*100, v3.Score.ValidToolCallRate*100, v3.Score.ModifyingDuplicates)
	abs, _ := filepath.Abs(path)
	fmt.Printf("passed=%v report=%s\n", decision.Passed, abs)
	if !decision.Passed {
		for _, reason := range decision.Reasons {
			fmt.Printf("- %s\n", reason)
		}
		os.Exit(1)
	}
}

func runRuntimeProbe(cfgPath, outDir string, repetitions, maxTasks int, mode string) {
	fmt.Println(ui.Prefix("🧪", "[PROBE]") + "Runtime 真实模型工具选择探针启动（不会执行目标工具）...")
	progress := func(completed, total int, observation benchmark.RuntimeProbeObservation) {
		if completed == total || completed%6 == 0 {
			status := "ok"
			if observation.Error != "" {
				status = "error"
			}
			fmt.Printf("progress=%d/%d last=%s status=%s\n", completed, total, observation.TaskID, status)
		}
	}
	report, err := benchmark.RunRuntimeProbeModeWithProgress(context.Background(), cfgPath, mode, repetitions, maxTasks, progress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s%v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	fmt.Printf("mode=%s model=%s/%s observations=%d\n", report.Mode, report.Provider, report.Model, len(report.Observations))
	fmt.Printf("task_success=%.1f%% tool_selection=%.1f%% valid_calls=%.1f%% p95_tokens=%d p95_latency=%s\n",
		report.TaskSuccessRate*100, report.CorrectToolSelectionRate*100, report.ValidToolCallRate*100,
		report.P95Tokens, report.P95Latency.Round(time.Millisecond))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "%s创建报告目录失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s序列化报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	path := filepath.Join(outDir, fmt.Sprintf("runtime_probe_%s_%s.json", report.Mode, time.Now().Format("20060102_150405")))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "%s写入报告失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("报告: %s\n", abs)
}
