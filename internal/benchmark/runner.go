package benchmark

import (
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/ui"
	"fmt"
	"os"
	"strings"
	"time"
)

// ProgressCallback benchmark 场景进度回调（供 TUI）
type ProgressCallback func(id, name string, score float64, passed bool, current, total int)

// RunSuite 执行完整 benchmark
func RunSuite(cfgPath string, skipLLM bool, skipRemote bool) (*SuiteReport, error) {
	return RunSuiteWithProgress(cfgPath, skipLLM, skipRemote, nil)
}

// RunSuiteWithProgress 执行 benchmark 并回调进度
func RunSuiteWithProgress(cfgPath string, skipLLM bool, skipRemote bool, onProgress ProgressCallback) (*SuiteReport, error) {
	start := time.Now()

	if err := config.InitConfig(cfgPath); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	remoteAvailable := false
	if !skipRemote && config.GlobalConfig.SSHHost != "" {
		if err := executor.Init(config.GlobalConfig); err != nil {
			fmt.Fprintf(os.Stderr, "%sSSH 不可用，跳过远程场景: %v\n", ui.Prefix("⚠️", "[WARN]"), err)
		} else {
			remoteAvailable = executor.Current != nil && executor.Current.IsRemote()
			defer executor.Current.Close()
		}
	}

	ctx := &Context{
		RemoteAvailable: remoteAvailable,
		UseNativeTools:  config.GlobalConfig.UseNativeTools,
	}

	report := &SuiteReport{
		Timestamp:   time.Now(),
		Provider:    config.GlobalConfig.Provider,
		Model:       config.GlobalConfig.ModelName,
		RemoteMode:  remoteAvailable,
		SSHHost:     config.GlobalConfig.SSHHost,
		CategoryAvg: make(map[Category]float64),
	}

	catScores := make(map[Category][]float64)
	catWeights := make(map[Category]float64)
	all := AllScenarios()
	total := len(all)
	cur := 0

	for _, sc := range all {
		cur++
		if skipLLM && sc.RequiresLLM {
			sr := skippedReport(sc, "跳过 LLM")
			report.Scenarios = append(report.Scenarios, sr)
			if onProgress != nil {
				onProgress(sc.ID, sc.Name, 0, false, cur, total)
			}
			continue
		}
		if sc.RequiresRemote && !remoteAvailable {
			sr := skippedReport(sc, "跳过 (无 SSH)")
			report.Scenarios = append(report.Scenarios, sr)
			if onProgress != nil {
				onProgress(sc.ID, sc.Name, 0, false, cur, total)
			}
			continue
		}

		if onProgress == nil {
			fmt.Printf("  %s %s %s...\n", ui.Sym("▶", ">"), sc.ID, sc.Name)
		}
		res := sc.Run(ctx)
		sr := ScenarioReport{
			ID: sc.ID, Category: sc.Category, Name: sc.Name,
			Passed: res.Passed, Partial: res.Partial, Score: res.Score,
			Latency: res.Latency, Message: res.Message, Evidence: res.Evidence, Metrics: res.Metrics,
		}
		report.Scenarios = append(report.Scenarios, sr)

		if onProgress != nil {
			onProgress(sc.ID, sc.Name, res.Score, res.Passed, cur, total)
		} else {
			icon := ui.Prefix("❌", "[ERR]")
			if res.Passed {
				icon = ui.Prefix("✅", "[OK]")
			} else if res.Partial {
				icon = ui.Prefix("⚠️", "[WARN]")
			}
			fmt.Printf("    %s %.0f分 %v %s\n", icon, res.Score, res.Latency.Round(time.Millisecond), res.Message)
		}

		meta := CategoryMeta[sc.Category]
		catScores[sc.Category] = append(catScores[sc.Category], res.Score)
		catWeights[sc.Category] += meta.Weight
	}

	// 维度均分
	var weightedSum, totalWeight float64
	for cat, meta := range CategoryMeta {
		scores := catScores[cat]
		if len(scores) == 0 {
			report.CategoryAvg[cat] = 0
			continue
		}
		var sum float64
		for _, s := range scores {
			sum += s
		}
		avg := sum / float64(len(scores))
		report.CategoryAvg[cat] = avg
		weightedSum += avg * meta.Weight
		totalWeight += meta.Weight
	}
	if totalWeight > 0 {
		report.OverallScore = weightedSum / totalWeight
	}
	report.Grade = scoreToGrade(report.OverallScore)
	report.Duration = time.Since(start)
	return report, nil
}

func skippedReport(sc Scenario, reason string) ScenarioReport {
	return ScenarioReport{
		ID: sc.ID, Category: sc.Category, Name: sc.Name,
		Score: 0, Message: reason,
	}
}

func scoreToGrade(s float64) string {
	switch {
	case s >= 90:
		return "A (优秀)"
	case s >= 80:
		return "B (良好)"
	case s >= 70:
		return "C (合格)"
	case s >= 60:
		return "D (待改进)"
	default:
		return "F (不合格)"
	}
}

// PrintSummary 控制台摘要
func PrintSummary(r *SuiteReport) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("DeepSentry Agent Benchmark  %s\n", r.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Provider: %s | Model: %s | Remote: %v | SSH: %s\n", r.Provider, r.Model, r.RemoteMode, r.SSHHost)
	fmt.Printf("总耗时: %v\n\n", r.Duration.Round(time.Millisecond))

	fmt.Println("【维度得分】")
	order := []Category{CatLLM, CatLocalTool, CatRemoteTool, CatLinkage, CatFilesystem, CatForensics, CatIncident, CatAgent, CatHarness, CatResilience, CatSecurity}
	for _, cat := range order {
		meta := CategoryMeta[cat]
		avg, ok := r.CategoryAvg[cat]
		if !ok {
			continue
		}
		bar := scoreBar(avg)
		fmt.Printf("  %-22s %5.1f/100  (权重 %.0f%%)  %s\n", meta.DisplayName, avg, meta.Weight, bar)
	}
	fmt.Printf("\n【综合得分】 %.1f / 100  等级: %s\n", r.OverallScore, r.Grade)

	passed, partial, failed, skipped := 0, 0, 0, 0
	for _, s := range r.Scenarios {
		if strings.HasPrefix(s.Message, "跳过") {
			skipped++
		} else if s.Passed {
			passed++
		} else if s.Partial {
			partial++
		} else {
			failed++
		}
	}
	fmt.Printf("【场景统计】 %s%d  %s%d  %s%d  %s%d  (共 %d)\n",
		ui.Prefix("✅", "[OK]"), passed,
		ui.Prefix("⚠️", "[WARN]"), partial,
		ui.Prefix("❌", "[ERR]"), failed,
		ui.Prefix("⏭", "[SKIP]"), skipped,
		len(r.Scenarios))
	fmt.Println(strings.Repeat("=", 60))
}

func scoreBar(score float64) string {
	n := int(score / 10)
	if n > 10 {
		n = 10
	}
	if ui.PlainTextMode() {
		return strings.Repeat("#", n) + strings.Repeat(".", 10-n)
	}
	return strings.Repeat("█", n) + strings.Repeat("░", 10-n)
}
