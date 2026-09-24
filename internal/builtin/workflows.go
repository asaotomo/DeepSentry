package builtin

import (
	"fmt"
	"strings"
	"sync"
)

type evidenceStep struct {
	name string
	run  func() (string, error)
}

type evidenceResult struct {
	name   string
	output string
	err    error
}

// runEvidenceWorkflow is the internal workflow contract used for deterministic
// evidence collection. It intentionally stays independent of an orchestration
// implementation so the orchestration backend can change without modifying
// the public tool protocol.
func runEvidenceWorkflow(title string, steps []evidenceStep, concurrency int) string {
	if concurrency <= 0 {
		concurrency = 3
	}
	results := make([]evidenceResult, len(steps))
	limit := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range steps {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			out, err := steps[index].run()
			results[index] = evidenceResult{name: steps[index].name, output: out, err: err}
		}(i)
	}
	wg.Wait()
	var b strings.Builder
	b.WriteString("【确定性工作流】" + title + "\n")
	b.WriteString("证据采集完成后请由模型交叉研判；单项失败不代表整体无异常。\n")
	for _, result := range results {
		b.WriteString("\n=== " + result.name + " ===\n")
		if result.err != nil {
			b.WriteString("采集失败: " + result.err.Error() + "\n")
			continue
		}
		b.WriteString(result.output)
		if !strings.HasSuffix(result.output, "\n") {
			b.WriteByte('\n')
		}
	}
	return truncate(b.String(), 120000)
}

func HostIncidentBaseline(rt Runtime, args map[string]string) (string, error) {
	limit := argInt(args, "limit", 100, 300)
	lines := argInt(args, "lines", 300, 2000)
	steps := []evidenceStep{
		{name: "主机健康", run: func() (string, error) { return TargetHealthSummary(rt) }},
		{name: "进程", run: func() (string, error) { return ProcessList(rt, limit) }},
		{name: "监听端口", run: func() (string, error) { return PortListen(rt) }},
		{name: "网络连接", run: func() (string, error) { return NetConnections(rt, "all") }},
		{name: "登录审计", run: func() (string, error) { return LoginAudit(rt, lines) }},
		{name: "服务与启动项", run: func() (string, error) { return ServiceUnits(rt, "", limit) }},
	}
	return runEvidenceWorkflow("主机应急基线", steps, argInt(args, "concurrency", 3, 6)), nil
}

func WebShellHunt(rt Runtime, args map[string]string) (string, error) {
	root := arg(args, "root", "path")
	if root == "" {
		root = "/var/www"
	}
	limit := argInt(args, "limit", 120, 500)
	pattern := arg(args, "pattern")
	if pattern == "" {
		pattern = `(?i)(eval\s*\(|assert\s*\(|base64_decode\s*\(|system\s*\(|shell_exec\s*\(|passthru\s*\(|Runtime\.getRuntime\(\)\.exec|ProcessBuilder\s*\(|cmd\.exe|/bin/(ba)?sh)`
	}
	steps := []evidenceStep{
		{name: "Web 目录可疑代码", run: func() (string, error) { return SecretScan(rt, root, pattern, limit) }},
		{name: "进程", run: func() (string, error) { return ProcessList(rt, limit) }},
		{name: "外联与监听", run: func() (string, error) { return NetConnections(rt, "all") }},
		{name: "Web/脚本启动项", run: func() (string, error) { return ServiceUnitAudit(rt, "php", limit) }},
	}
	out := runEvidenceWorkflow(fmt.Sprintf("WebShell 排查 root=%s", root), steps, argInt(args, "concurrency", 3, 4))
	return out, nil
}
