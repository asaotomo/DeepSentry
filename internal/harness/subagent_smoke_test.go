package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness/subagent"
	"fmt"
	"strings"
	"testing"
)

func TestSubAgentRegistrySmokeAllSpecsRunToFinish(t *testing.T) {
	parent := &DeepAgent{
		Middleware:     SubAgentMiddlewareStack(nil, nil),
		State:          NewAgentState(t.TempDir()),
		UseNativeTools: false,
	}
	for _, spec := range subagent.Registry {
		spec := spec
		t.Run(spec.Name, func(t *testing.T) {
			calls := 0
			runner := NewSubAgentRunner(parent)
			runner.Executor = &executor.LocalExecutor{}
			runner.MaxStepsCap = 5
			runner.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
				calls++
				if !strings.Contains(opts.ExtraPrompt, spec.SystemPrompt) {
					t.Fatalf("sub-agent prompt missing system prompt for %s", spec.Name)
				}
				if !strings.Contains(opts.ExtraPrompt, "禁止委派子 Agent") {
					t.Fatalf("sub-agent prompt should forbid nested task delegation")
				}
				if calls == 1 {
					return analyzer.AgentResponse{
						Thought: "smoke execute",
						Action:  string(ActionExecute),
						Command: fmt.Sprintf("printf 'SUBAGENT_SMOKE_%s\\n'", spec.Name),
					}, nil
				}
				return analyzer.AgentResponse{
					Thought:     "done",
					Action:      string(ActionFinish),
					FinalReport: "SMOKE_OK " + spec.Name,
				}, nil
			}
			out, err := runner.Run(spec, "冒烟测试：执行一个低风险命令后返回结论", collector.SystemContext{}, false)
			if err != nil {
				t.Fatalf("sub-agent smoke failed: %v", err)
			}
			if !strings.Contains(out, "SMOKE_OK "+spec.Name) {
				t.Fatalf("unexpected final report: %q", out)
			}
			if calls != 2 {
				t.Fatalf("expected execute then finish, got %d calls", calls)
			}
		})
	}
}

func TestSubAgentRegistrySmokeMetadataIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range subagent.Registry {
		if strings.TrimSpace(spec.Name) == "" {
			t.Fatal("sub-agent name must not be empty")
		}
		if seen[spec.Name] {
			t.Fatalf("duplicate sub-agent name: %s", spec.Name)
		}
		seen[spec.Name] = true
		if strings.TrimSpace(spec.Description) == "" {
			t.Fatalf("%s description must not be empty", spec.Name)
		}
		if strings.TrimSpace(spec.SystemPrompt) == "" {
			t.Fatalf("%s system prompt must not be empty", spec.Name)
		}
		if spec.MaxSteps <= 0 {
			t.Fatalf("%s max steps must be positive", spec.Name)
		}
		if _, ok := subagent.Find(spec.Name); !ok {
			t.Fatalf("Find should locate %s", spec.Name)
		}
		if _, ok := subagent.Find(" " + spec.Name + " "); !ok {
			t.Fatalf("Find should trim surrounding whitespace for %s", spec.Name)
		}
	}
	prompt := subagent.FormatRegistryPrompt()
	for _, spec := range subagent.Registry {
		if !strings.Contains(prompt, spec.Name) {
			t.Fatalf("registry prompt missing %s", spec.Name)
		}
	}
	for _, want := range []string{"task_max_steps", "parallel_tasks", "subagent_max_steps"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("registry prompt missing capability hint %q", want)
		}
	}
}

func TestBaselineHardeningAgentsAreDistinctAndDiscoverable(t *testing.T) {
	checks := []struct {
		name  string
		terms []string
	}{
		{"server-baseline-hardener", []string{"服务器", "Windows", "Linux", "回滚", "复验"}},
		{"endpoint-baseline-hardener", []string{"终端", "Windows", "macOS", "Linux", "恢复密钥", "复验"}},
	}
	catalog := subagent.FormatRegistryPrompt()
	for _, check := range checks {
		spec, ok := subagent.Find(check.name)
		if !ok || !strings.Contains(catalog, check.name) {
			t.Fatalf("baseline agent %q is not registered and discoverable", check.name)
		}
		for _, term := range check.terms {
			if !strings.Contains(spec.SystemPrompt, term) {
				t.Fatalf("%s prompt missing %q", check.name, term)
			}
		}
	}
}

func TestSpecialistRoutingAndNetworkDeviceExecutionProfile(t *testing.T) {
	catalog := subagent.FormatRegistryPrompt()
	for _, name := range []string{"host-incident-responder", "network-device-analyst"} {
		if _, ok := subagent.Find(name); !ok || !strings.Contains(catalog, name) {
			t.Fatalf("specialist %s is not discoverable", name)
		}
	}
	spec, _ := subagent.Find("network-device-analyst")
	parent := &DeepAgent{State: NewAgentState(t.TempDir()), SessionID: "session_device_prompt"}
	runner := NewSubAgentRunner(parent)
	runner.StepFn = func(opts analyzer.StepOptions) (analyzer.AgentResponse, error) {
		if !strings.Contains(opts.ExtraPrompt, "网络设备 CLI-first") || strings.Contains(opts.ExtraPrompt, "Shell-first") {
			t.Fatalf("network-device worker received shell guidance: %s", opts.ExtraPrompt)
		}
		return analyzer.AgentResponse{Action: "finish", FinalReport: "device checked"}, nil
	}
	if _, err := runner.Run(*spec, "检查交换机接口", collector.SystemContext{}, false); err != nil {
		t.Fatal(err)
	}
}
