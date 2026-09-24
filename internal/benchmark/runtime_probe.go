package benchmark

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/tools"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RuntimeProbeReport is a real-model, non-executing tool-selection probe. It
// intentionally does not claim evidence coverage or Runtime v3 acceptance:
// controlled target fixtures still need to execute before those gates apply.
type RuntimeProbeReport struct {
	Mode                     string                    `json:"mode"`
	Provider                 string                    `json:"provider"`
	Model                    string                    `json:"model"`
	Repetitions              int                       `json:"repetitions"`
	TaskCount                int                       `json:"task_count"`
	Observations             []RuntimeProbeObservation `json:"observations"`
	TaskSuccessRate          float64                   `json:"task_success_rate"`
	CorrectToolSelectionRate float64                   `json:"correct_tool_selection_rate"`
	ValidToolCallRate        float64                   `json:"valid_tool_call_rate"`
	P95Tokens                int                       `json:"p95_tokens"`
	P95Latency               time.Duration             `json:"p95_latency"`
}

type RuntimeProbeObservation struct {
	TaskID        string        `json:"task_id"`
	Run           int           `json:"run"`
	ExpectedTools []string      `json:"expected_tools"`
	SelectedTools []string      `json:"selected_tools"`
	CalledTools   []string      `json:"called_tools,omitempty"`
	Success       bool          `json:"success"`
	ValidCalls    int           `json:"valid_calls"`
	InvalidCalls  int           `json:"invalid_calls"`
	PromptTokens  int           `json:"prompt_tokens"`
	Tokens        int           `json:"tokens"`
	Latency       time.Duration `json:"latency"`
	Error         string        `json:"error,omitempty"`
}

func RunRuntimeProbe(ctx context.Context, cfgPath string, repetitions, maxTasks int) (*RuntimeProbeReport, error) {
	return runRuntimeProbe(ctx, cfgPath, "", repetitions, maxTasks, nil)
}

// RunRuntimeProbeMode loads the same on-disk model configuration while
// overriding only the runtime under test. This prevents an A/B gate from
// accidentally comparing different endpoints, model IDs, credentials, or
// capability settings through hand-edited config copies.
func RunRuntimeProbeMode(ctx context.Context, cfgPath, mode string, repetitions, maxTasks int) (*RuntimeProbeReport, error) {
	return runRuntimeProbe(ctx, cfgPath, mode, repetitions, maxTasks, nil)
}

type RuntimeProbeProgress func(completed, total int, observation RuntimeProbeObservation)

func RunRuntimeProbeModeWithProgress(ctx context.Context, cfgPath, mode string, repetitions, maxTasks int, progress RuntimeProbeProgress) (*RuntimeProbeReport, error) {
	return runRuntimeProbe(ctx, cfgPath, mode, repetitions, maxTasks, progress)
}

func runRuntimeProbe(ctx context.Context, cfgPath, mode string, repetitions, maxTasks int, progress RuntimeProbeProgress) (*RuntimeProbeReport, error) {
	if err := config.InitConfig(cfgPath); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if normalized := strings.ToLower(strings.TrimSpace(mode)); normalized != "" {
		if normalized != "legacy" && normalized != "v3" {
			return nil, fmt.Errorf("unsupported runtime probe mode %q", mode)
		}
		config.GlobalConfig.AgentRuntime = normalized
		if err := config.ValidateRuntimeConfig(config.GlobalConfig); err != nil {
			return nil, fmt.Errorf("runtime override: %w", err)
		}
	}
	tools.ConfigureEnabled(config.GlobalConfig.EnabledTools, config.GlobalConfig.DisabledTools)
	if repetitions <= 0 {
		repetitions = 3
	}
	tasks := RuntimeSecurityTasks()
	if maxTasks > 0 && maxTasks < len(tasks) {
		tasks = evenlySampleProbeTasks(tasks, maxTasks)
	}
	report := &RuntimeProbeReport{
		Mode: config.GlobalConfig.AgentRuntime, Provider: config.GlobalConfig.Provider,
		Model: config.GlobalConfig.ModelName, Repetitions: repetitions, TaskCount: len(tasks),
	}
	total := len(tasks) * repetitions
	for run := 1; run <= repetitions; run++ {
		for _, task := range tasks {
			observation := runRuntimeProbeTask(ctx, task, run)
			report.Observations = append(report.Observations, observation)
			if progress != nil {
				progress(len(report.Observations), total, observation)
			}
		}
	}
	summarizeRuntimeProbe(report)
	return report, nil
}

func evenlySampleProbeTasks(tasks []SecurityTaskFixture, limit int) []SecurityTaskFixture {
	if limit <= 0 || limit >= len(tasks) {
		return append([]SecurityTaskFixture(nil), tasks...)
	}
	out := make([]SecurityTaskFixture, 0, limit)
	for index := 0; index < limit; index++ {
		position := index * len(tasks) / limit
		out = append(out, tasks[position])
	}
	return out
}

func runRuntimeProbeTask(ctx context.Context, task SecurityTaskFixture, run int) RuntimeProbeObservation {
	started := time.Now()
	observation := RuntimeProbeObservation{TaskID: task.ID, Run: run, ExpectedTools: append([]string(nil), task.ExpectedTools...)}
	messages := []analyzer.Message{
		{Role: "system", Content: "你是 DeepSentry 工具选择评测器。针对受控 fixture，只调用完成全部证据字段所需的专用原生工具；不得使用 agent_action、Shell、finish，不得解释。已经直接展示的专用原生工具必须直接调用，不能用 agent_action 包装。多目标/Fleet 任务通常先 fleet_inventory 确认目标，再用 fleet_exec 批量巡检或用 fleet_file 取证。仅当当前没有任何合适的直接工具时才可调用一次 tool_catalog；目录返回后必须调用具体工具，不得重复搜索。工具选择不代表实际执行，风险审批由运行时另行处理。"},
		{Role: "user", Content: fmt.Sprintf("任务: %s\n受控 fixture: %s\n必须产出的证据字段: %s\n请选择并调用覆盖全部证据字段的最小工具集合；参数可使用 fixture 路径或安全占位值。", task.Goal, task.Fixture, strings.Join(task.RequiredEvidence, ", "))},
	}
	selected := map[string]bool{}
	for turn := 0; turn < 2; turn++ {
		result, err := analyzer.CallLLMWithRetryContext(ctx, messages, true, nil)
		observation.PromptTokens += result.Usage.PromptTokens
		observation.Tokens += result.Usage.TotalTokens
		if err != nil {
			observation.Error = err.Error()
			break
		}
		calls := result.ToolCalls
		if len(calls) == 0 && result.ToolCallName != "" {
			calls = []analyzer.LLMToolCall{{ID: result.ToolCallID, Name: result.ToolCallName, Arguments: result.ToolCallArgs}}
		}
		if len(calls) == 0 {
			observation.Error = "model returned no tool call"
			break
		}
		assistantCalls := make([]analyzer.ToolCall, 0, len(calls))
		catalogUsed := false
		for _, call := range calls {
			observation.CalledTools = append(observation.CalledTools, call.Name)
			if strings.TrimSpace(call.Arguments) == "" {
				call.Arguments = "{}"
			}
			if call.ID == "" {
				call.ID = fmt.Sprintf("probe_%s_%d_%d", task.ID, run, turn)
			}
			assistantCall := analyzer.ToolCall{ID: call.ID, Type: "function"}
			assistantCall.Function.Name = call.Name
			assistantCall.Function.Arguments = call.Arguments
			assistantCalls = append(assistantCalls, assistantCall)
			classification := classifyProbeCall(call)
			if !classification.Valid {
				observation.InvalidCalls++
				continue
			}
			observation.ValidCalls++
			catalogUsed = catalogUsed || classification.Catalog
			if classification.SelectedTool != "" {
				selected[classification.SelectedTool] = true
			}
		}
		selectedNow := make([]string, 0, len(selected))
		for name := range selected {
			selectedNow = append(selectedNow, name)
		}
		needAnother := turn == 0 && overlap(task.ExpectedTools, selectedNow) < len(task.ExpectedTools)
		if !catalogUsed && !needAnother {
			break
		}
		messages = append(messages, analyzer.Message{Role: "assistant", ReasoningContent: result.ReasoningContent, ToolCalls: assistantCalls})
		for _, call := range calls {
			if call.Name != "tool_catalog" {
				content := "受控 fixture 已接受该只读工具调用；如仍需其他证据，请继续调用下一个专用工具。"
				effectiveName := call.Name
				if call.Name == "agent_action" {
					if action, parseErr := analyzer.ParseToolCallResponse(call.Arguments); parseErr == nil && action.ToolName != "" {
						effectiveName = action.ToolName
					}
				}
				if effectiveName == "fleet_inventory" {
					content = "Fleet inventory 已确认存在多个受控目标，但尚未采集任何健康、日志或中断恢复证据；现在必须继续调用 fleet_exec(selector=all, command=<与任务匹配的只读巡检命令>)。"
				}
				messages = append(messages, analyzer.Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: content})
				continue
			}
			var args map[string]string
			_ = json.Unmarshal([]byte(call.Arguments), &args)
			category := args["category"]
			if category == "" {
				category = "all"
			}
			output := tools.FormatCatalogDetail(category, firstNonEmptyProbe(args["name"], args["query"], task.Goal))
			messages = append(messages, analyzer.Message{Role: "tool", ToolCallID: call.ID, Name: "tool_catalog", Content: output})
		}
	}
	for name := range selected {
		observation.SelectedTools = append(observation.SelectedTools, name)
	}
	sort.Strings(observation.SelectedTools)
	observation.Success = overlap(task.ExpectedTools, observation.SelectedTools) == len(task.ExpectedTools)
	observation.Latency = time.Since(started)
	return observation
}

type probeCallClassification struct {
	Valid        bool
	Catalog      bool
	SelectedTool string
}

// classifyProbeCall separates protocol validity from task correctness. A
// schema-valid agent_action that chooses execute/finish is the wrong choice for
// this tool-only benchmark, but it is not a malformed tool call; task success
// and selection rate already penalize that behavior. Likewise, a turn with no
// tool call is a task miss, not a failed parse/execution attempt.
func classifyProbeCall(call analyzer.LLMToolCall) probeCallClassification {
	if strings.TrimSpace(call.Arguments) == "" {
		call.Arguments = "{}"
	}
	if !json.Valid([]byte(call.Arguments)) {
		return probeCallClassification{}
	}
	if call.Name == "tool_catalog" {
		return probeCallClassification{Valid: true, Catalog: true}
	}
	if call.Name == "agent_action" {
		action, err := analyzer.ParseToolCallResponse(call.Arguments)
		if err != nil {
			return probeCallClassification{}
		}
		if action.Action != "tool" {
			return probeCallClassification{Valid: true}
		}
		if _, ok := tools.Get(action.ToolName); !ok || tools.ValidateCall(action.ToolName, action.ToolArgs) != nil {
			return probeCallClassification{}
		}
		return probeCallClassification{Valid: true, SelectedTool: action.ToolName}
	}
	if _, ok := tools.Get(call.Name); !ok {
		return probeCallClassification{}
	}
	args, err := probeToolArgs(call.Arguments)
	if err != nil || tools.ValidateCall(call.Name, args) != nil {
		return probeCallClassification{}
	}
	return probeCallClassification{Valid: true, SelectedTool: call.Name}
}

func summarizeRuntimeProbe(report *RuntimeProbeReport) {
	if report == nil || len(report.Observations) == 0 {
		return
	}
	var successes, expected, selected, valid, invalid int
	var tokens []int
	var latencies []time.Duration
	for _, observation := range report.Observations {
		if observation.Success {
			successes++
		}
		expected += len(observation.ExpectedTools)
		selected += overlap(observation.ExpectedTools, observation.SelectedTools)
		valid += observation.ValidCalls
		invalid += observation.InvalidCalls
		tokens = append(tokens, observation.Tokens)
		latencies = append(latencies, observation.Latency)
	}
	report.TaskSuccessRate = ratio(successes, len(report.Observations))
	report.CorrectToolSelectionRate = ratio(selected, expected)
	report.ValidToolCallRate = ratio(valid, valid+invalid)
	sort.Ints(tokens)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	index := p95Index(len(tokens))
	report.P95Tokens = tokens[index]
	report.P95Latency = latencies[index]
}

func firstNonEmptyProbe(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "all"
}

func probeToolArgs(raw string) (map[string]string, error) {
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if text, ok := value.(string); ok {
			out[key] = text
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		out[key] = string(encoded)
	}
	return out, nil
}
