package main

import (
	"fmt"
	"os"
	"strings"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness"
)

func main() {
	cfgPath := "/tmp/deepsentry-smoke.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	if err := config.InitConfig(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Provider: %s | Model: %s | URL: %s\n",
		config.GlobalConfig.Provider,
		config.GlobalConfig.ModelName,
		config.GlobalConfig.ApiURL)

	// 1) LLM ping
	msgs := []analyzer.Message{{Role: "user", Content: `回复严格 JSON: {"thought":"ok","action":"finish","final_report":"llm ok","is_finished":true}`}}
	res, err := analyzer.CallLLMWithRetry(msgs, false, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "LLM FAIL: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("LLM OK: %s\n", truncate(res.Content, 120))

	// 2) SSH + executor
	if err := executor.Init(config.GlobalConfig); err != nil {
		fmt.Fprintf(os.Stderr, "SSH FAIL: %v\n", err)
		os.Exit(3)
	}
	defer executor.Current.Close()
	fmt.Printf("Executor: remote=%v\n", executor.Current.IsRemote())

	out, err := executor.Current.Run("uname -a")
	if err != nil {
		fmt.Fprintf(os.Stderr, "SSH RUN FAIL: %v\n", err)
		os.Exit(4)
	}
	fmt.Printf("SSH OK: %s\n", truncate(out, 120))

	// 3) Builtin tool via harness (port_listen)
	agent, err := harness.NewDeepAgent(harness.Config{BatchMode: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Agent FAIL: %v\n", err)
		os.Exit(5)
	}
	sysCtx := collector.GetSystemContext()
	history := []analyzer.Message{{Role: "user", Content: "冒烟测试：用 tool port_listen 查看监听端口，然后 finish"}}
	confirmAlways := func(_ *harness.AgentAction) bool { return true }
	agent.RunLoop(harness.RunLoopConfig{
		SysCtx:    sysCtx,
		History:   &history,
		BatchMode: true,
		MaxSteps:  5,
		ConfirmFn: confirmAlways,
	})
	fmt.Println("SMOKE PASS")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
