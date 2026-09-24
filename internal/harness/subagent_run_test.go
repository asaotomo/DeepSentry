package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/harness/subagent"
	"ai-edr/internal/memory"
	"ai-edr/internal/skills"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSubAgentsShareBoundedCoreClueBoardAcrossRuns(t *testing.T) {
	parent := &DeepAgent{State: NewAgentState(t.TempDir()), SessionID: "session_test"}
	spec := subagent.Spec{Name: "worker", SystemPrompt: "test", MaxSteps: 2}
	producer := NewSubAgentRunner(parent)
	producer.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		return analyzer.AgentResponse{Action: string(ActionFinish), FinalReport: "关键结论：已验证攻击源 203.0.113.9"}, nil
	}
	if _, err := producer.Run(spec, "确认攻击源", collector.SystemContext{}, false); err != nil {
		t.Fatal(err)
	}

	consumer := NewSubAgentRunner(parent)
	seen := false
	consumer.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
		seen = strings.Contains(opts.ExtraPrompt, "203.0.113.9")
		return analyzer.AgentResponse{Action: string(ActionFinish), FinalReport: "任务状态：完成"}, nil
	}
	if _, err := consumer.Run(spec, "关联后续证据", collector.SystemContext{}, false); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("consumer sub-agent did not receive producer's core clue")
	}
}

func TestSubAgentAutoLoadsSkillForItsAssignment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("# probe-skill\nRun the precise probe workflow."), 0600); err != nil {
		t.Fatal(err)
	}
	parent := &DeepAgent{
		State: NewAgentState(dir), SessionID: "skill_auto",
		Catalog: &skills.SkillCatalog{Skills: []skills.SkillMeta{{Name: "probe-skill", Description: "probe workflow", Path: path, AllowImplicit: true}}},
	}
	runner := NewSubAgentRunner(parent)
	seen := false
	runner.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
		seen = strings.Contains(opts.ExtraPrompt, "Run the precise probe workflow.")
		return analyzer.AgentResponse{Action: string(ActionFinish), FinalReport: "done"}, nil
	}
	_, err := runner.Run(subagent.Spec{Name: "worker", SystemPrompt: "test", MaxSteps: 1}, "【主流程协作简报】\n其他工作\n【你的唯一分工】\n使用 probe-skill 进行检查\n不要替其他子 Agent 扩大范围。", collector.SystemContext{}, false)
	if err != nil || !seen {
		t.Fatalf("sub-agent did not auto-load assignment skill: seen=%v err=%v", seen, err)
	}
}

func TestSubAgentRemembersInAssignedTargetScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := memory.NewStore(memory.ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	parent := &DeepAgent{State: NewAgentState(t.TempDir()), SessionID: "scoped_memory", MemoryStore: store}
	runner := NewSubAgentRunner(parent)
	runner.Target = config.TargetConfig{Name: "host-a", Protocol: "ssh", Host: "host-a"}
	calls := 0
	runner.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		calls++
		if calls == 1 {
			return analyzer.AgentResponse{Action: string(ActionRemember), MemoryKey: "os", MemoryValue: "Windows Server", MemoryScope: "target"}, nil
		}
		return analyzer.AgentResponse{Action: string(ActionFinish), FinalReport: "done"}, nil
	}
	_, err = runner.Run(subagent.Spec{Name: "worker", SystemPrompt: "test", MaxSteps: 2}, "检查目标系统", collector.SystemContext{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if entries := store.ActiveEntries(); len(entries) != 0 {
		t.Fatalf("child memory leaked into parent scope: %#v", entries)
	}
	entries := store.ActiveEntriesForScope("ssh:host-a")
	if len(entries) != 1 || entries[0].Value != "Windows Server" {
		t.Fatalf("child target memory=%#v", entries)
	}
}

func TestSubAgentStepEstimateIgnoresSharedBriefNoise(t *testing.T) {
	brief := "【主流程协作简报】\n" + strings.Repeat("完整日志异常证据链 ", 100) + "\n【你的唯一分工】\n简单确认文件存在\n\n不要替其他子 Agent 扩大范围。"
	assignment := subAgentAssignmentForEstimate(brief)
	if assignment != "简单确认文件存在" {
		t.Fatalf("assignment extraction=%q", assignment)
	}
	if got := estimateSubAgentSteps(assignment, 5); got > 7 {
		t.Fatalf("shared brief noise inflated step estimate: %d", got)
	}
}

func TestSubAgentRunnersUseIsolatedOutputSessions(t *testing.T) {
	parent := &DeepAgent{State: NewAgentState(t.TempDir()), SessionID: "session_parent"}
	left := NewSubAgentRunner(parent)
	right := NewSubAgentRunner(parent)
	if left.State.SessionID == right.State.SessionID {
		t.Fatalf("parallel sub-agents share output session %q", left.State.SessionID)
	}
	for _, id := range []string{left.State.SessionID, right.State.SessionID} {
		if !strings.HasPrefix(id, "session_parent-sub-") {
			t.Fatalf("unexpected sub-agent session id %q", id)
		}
	}
}

func TestConcurrentSubAgentReadsPeerClueOnNextTurn(t *testing.T) {
	parent := &DeepAgent{State: NewAgentState(t.TempDir()), SessionID: "session_parallel_board"}
	spec := subagent.Spec{Name: "worker", SystemPrompt: "test", MaxSteps: 3}
	consumerStarted := make(chan struct{})
	producerDone := make(chan struct{})
	seen := make(chan bool, 1)

	consumer := NewSubAgentRunner(parent)
	consumerCalls := 0
	consumer.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
		consumerCalls++
		if consumerCalls == 1 {
			close(consumerStarted)
			<-producerDone
			return analyzer.AgentResponse{}, nil
		}
		seen <- strings.Contains(opts.ExtraPrompt, "203.0.113.77")
		return analyzer.AgentResponse{Action: string(ActionFinish), FinalReport: "任务状态：完成"}, nil
	}

	producer := NewSubAgentRunner(parent)
	producer.StepFn = func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
		<-consumerStarted
		return analyzer.AgentResponse{Action: string(ActionFinish), FinalReport: "关键结论：已验证横向来源 203.0.113.77"}, nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := consumer.Run(spec, "消费共享线索", collector.SystemContext{}, false); err != nil {
			t.Errorf("consumer: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := producer.Run(spec, "产生共享线索", collector.SystemContext{}, false); err != nil {
			t.Errorf("producer: %v", err)
		}
		close(producerDone)
	}()
	wg.Wait()
	if !<-seen {
		t.Fatal("concurrent consumer did not pull peer clue on its next model turn")
	}
}

func TestAuthorizeSubAgentExecuteAllowsAIReviewedLowRisk(t *testing.T) {
	for _, command := range []string{"echo $(pwd)", "rm -rf /tmp/deepsentry-ai-reviewed-safe"} {
		action := &AgentAction{Type: ActionExecute, Command: command}
		confirmCalled := false
		reviewer := func(collector.SystemContext, string, string) (string, string, bool) {
			return "low", "AI 复核确认无副作用", true
		}

		allowed, feedback := authorizeSubAgentExecute(action, collector.SystemContext{}, false, nil, func(*AgentAction) bool {
			confirmCalled = true
			return false
		}, reviewer)
		if !allowed {
			t.Fatalf("expected AI-reviewed low risk command to run, command=%q feedback=%q", command, feedback)
		}
		if confirmCalled {
			t.Fatalf("confirm should not be called after AI review marks command low risk: %q", command)
		}
		if action.RiskLevel != "low" || !strings.Contains(action.Reason, "无副作用") {
			t.Fatalf("unexpected reviewed risk: risk=%q reason=%q", action.RiskLevel, action.Reason)
		}
	}
}

func TestAuthorizeSubAgentExecuteConfirmsHighRiskCommand(t *testing.T) {
	action := &AgentAction{Type: ActionExecute, Command: "rm -rf /tmp/deepsentry-risk-test"}
	confirmCalled := false

	allowed, feedback := authorizeSubAgentExecute(action, collector.SystemContext{}, false, nil, func(a *AgentAction) bool {
		confirmCalled = true
		if a.RiskLevel != "high" {
			t.Fatalf("confirm should receive high risk action, got %q", a.RiskLevel)
		}
		return true
	}, func(collector.SystemContext, string, string) (string, string, bool) {
		return "high", "会删除文件", true
	})
	if !allowed {
		t.Fatalf("expected approved high risk command to run, feedback=%q", feedback)
	}
	if !confirmCalled {
		t.Fatal("expected high risk command to request confirmation")
	}
}

func TestAuthorizeSubAgentExecuteBlocksFlagPayloadWithoutConfirm(t *testing.T) {
	action := &AgentAction{Type: ActionExecute, Command: "flag{Adm1N-B2G-kU-SZIP}"}
	confirmCalled := false
	allowed, feedback := authorizeSubAgentExecute(action, collector.SystemContext{}, false, nil, func(*AgentAction) bool {
		confirmCalled = true
		return true
	}, func(collector.SystemContext, string, string) (string, string, bool) {
		t.Fatal("AI review should not run for flag payloads")
		return "", "", false
	})
	if allowed || confirmCalled {
		t.Fatalf("flag payload must be auto-denied: allowed=%v confirm=%v feedback=%q", allowed, confirmCalled, feedback)
	}
	if !strings.Contains(feedback, "不是可执行命令") {
		t.Fatalf("feedback=%q", feedback)
	}
}

func TestAuthorizeSubAgentExecuteDeniesWhenConfirmationRejected(t *testing.T) {
	action := &AgentAction{Type: ActionExecute, Command: "rm -rf /tmp/deepsentry-risk-deny"}

	allowed, feedback := authorizeSubAgentExecute(action, collector.SystemContext{}, false, nil, func(*AgentAction) bool {
		return false
	}, func(collector.SystemContext, string, string) (string, string, bool) {
		return "high", "会删除文件", true
	})
	if allowed {
		t.Fatal("expected rejected high risk command to be denied")
	}
	if !strings.Contains(feedback, "请改用只读、低风险方式继续") {
		t.Fatalf("feedback should guide sub-agent to a safer plan, got %q", feedback)
	}
}

func TestAuthorizeSubAgentExecuteFailsClosedWhenAIReviewUnavailable(t *testing.T) {
	action := &AgentAction{Type: ActionExecute, Command: "unknown-mutator --apply"}
	confirmCalled := false
	allowed, feedback := authorizeSubAgentExecute(action, collector.SystemContext{}, false, nil, func(*AgentAction) bool {
		confirmCalled = true
		return true
	}, func(collector.SystemContext, string, string) (string, string, bool) {
		return "", "", false
	})
	if !allowed || !confirmCalled {
		t.Fatalf("AI review failure must fall back to human confirmation: allowed=%v confirm=%v feedback=%q", allowed, confirmCalled, feedback)
	}
}

func TestAuthorizeSubAgentMutationConfirmsFileWritesAndRiskyTools(t *testing.T) {
	for _, action := range []*AgentAction{
		{Type: ActionWriteFile, Path: "/tmp/example", Content: "data"},
		{Type: ActionEditFile, Path: "/tmp/example", OldString: "a", NewString: "b"},
		{Type: ActionTool, ToolName: "nmap_scan", ToolArgs: map[string]string{"host": "127.0.0.1"}},
		{Type: ActionTool, ToolName: "unknown_external_tool"},
	} {
		called := false
		allowed, feedback := authorizeSubAgentMutation(action, false, nil, func(*AgentAction) bool {
			called = true
			return false
		})
		if allowed || !called {
			t.Fatalf("action %s should require and honor confirmation rejection", action.Type)
		}
		if !strings.Contains(feedback, "低风险替代") {
			t.Fatalf("unsafe-action feedback=%q", feedback)
		}
	}
}

func TestAuthorizeSubAgentMutationAllowsReadOnlyToolWithoutPrompt(t *testing.T) {
	called := false
	action := &AgentAction{Type: ActionTool, ToolName: "mem_info"}
	allowed, feedback := authorizeSubAgentMutation(action, false, nil, func(*AgentAction) bool {
		called = true
		return false
	})
	if !allowed || called || feedback != "" {
		t.Fatalf("read-only tool should run without prompt: allowed=%v called=%v feedback=%q", allowed, called, feedback)
	}
}

func TestResolveSubAgentMaxStepsCapsRequestedAndEstimate(t *testing.T) {
	spec := subagent.Spec{Name: "log-analyst", MaxSteps: 15}
	got := resolveSubAgentMaxSteps(spec, "完整分析 auth.log/syslog 登录失败、提权、异常 IP、时间线和证据链", 40, 24)
	if got != 24 {
		t.Fatalf("max steps should be capped by user limit, got %d", got)
	}

	got = resolveSubAgentMaxSteps(spec, "简单确认文件存在", 0, 15)
	if got != 15 {
		t.Fatalf("default cap/base should keep 15, got %d", got)
	}
}

func TestSubAgentHonorsStopBeforeCallingModel(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	called := false
	r := &SubAgentRunner{
		State: NewAgentState(t.TempDir()),
		Stop:  stop,
		StepFn: func(analyzer.StepOptions) (analyzer.AgentResponse, error) {
			called = true
			return analyzer.AgentResponse{}, nil
		},
	}
	out, err := r.Run(subagent.Spec{Name: "stoppable", MaxSteps: 3}, "inspect", collector.SystemContext{}, false)
	if err != nil || called || !strings.Contains(out, "已按用户请求停止") {
		t.Fatalf("stop result=%q err=%v modelCalled=%v", out, err, called)
	}
}
