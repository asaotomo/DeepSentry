package analyzer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractJSONPayload_MarkdownBlock(t *testing.T) {
	raw := "The lastb output is too massive for SSH pipe processing.\n\n```json\n" +
		`{"action":"tool","tool_name":"read_log","tool_args":{"path":"/var/log/auth.log","lines":500,"pattern":"Failed"}}` +
		"\n```"

	jsonPart, prose := extractJSONPayload(raw)
	if jsonPart == "" {
		t.Fatal("expected JSON extracted")
	}
	if !strings.Contains(jsonPart, `"action":"tool"`) {
		t.Fatalf("unexpected json: %s", jsonPart)
	}
	if prose == "" {
		t.Fatal("expected prose before json block")
	}
}

func TestExtractBalancedJSONObject(t *testing.T) {
	s := `prefix {"action":"execute","command":"ls"} suffix`
	obj := extractBalancedJSONObject(s)
	if obj != `{"action":"execute","command":"ls"}` {
		t.Fatalf("got %q", obj)
	}
}

func TestCleanJSON_MixedResponse(t *testing.T) {
	raw := "Let me read auth.log\n\n```json\n{\"action\":\"tool\",\"tool_name\":\"grep\"}\n```"
	out, prose := cleanJSON(raw)
	if !strings.Contains(out, `"tool_name"`) {
		t.Fatalf("cleanJSON failed: %q prose=%q", out, prose)
	}
	if prose == "" {
		t.Fatal("expected prose")
	}
}

func TestCleanJSONPreservesValidEscapedPipesAndPaths(t *testing.T) {
	for _, raw := range []string{
		`{"action":"execute","command":"grep a\\|b C:\\logs\\n.txt"}`,
		`{"action":"tool","tool_args":{"path":"C:\\Users\\示例用户\\report.txt","pattern":"a\\|b"}}`,
		`{"action":"tool","tool_args":{"path":"\\\\server\\share\\report.txt"}}`,
	} {
		got, _ := cleanJSON(raw)
		if got != raw || !json.Valid([]byte(got)) {
			t.Fatalf("valid JSON changed: got %q, want %q", got, raw)
		}
	}
}

func TestCleanJSONRepairsOnlyInvalidEscapesInStrings(t *testing.T) {
	raw := `{"action":"execute","command":"grep a\|b C:\Users\示例用户\report.txt"}`
	got, _ := cleanJSON(raw)
	var decoded struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("repaired JSON invalid: %q: %v", got, err)
	}
	if want := `grep a\|b C:\Users\示例用户\report.txt`; decoded.Command != want {
		t.Fatalf("command changed: got %q, want %q", decoded.Command, want)
	}
}

func TestCleanJSONKeepsWindowsCommandPathsLiteral(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"command":"type C:\new\test.txt"}`, `type C:\new\test.txt`},
		{`{"command":"Get-Content 'C:\Program Files\test.txt'; echo ok"}`, `Get-Content 'C:\Program Files\test.txt'; echo ok`},
		{`{"command":"Get-Content \"C:\Program Files\test.txt\"; echo ok"}`, `Get-Content "C:\Program Files\test.txt"; echo ok`},
		{`{"command":"type C:\\new\\test.txt"}`, `type C:\new\test.txt`},
		{`{"command":"echo \"quoted\"; grep \d+"}`, `echo "quoted"; grep \d+`},
	} {
		got, _ := cleanJSON(tc.raw)
		var decoded struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(got), &decoded); err != nil || decoded.Command != tc.want {
			t.Fatalf("raw=%q clean=%q command=%q want=%q err=%v", tc.raw, got, decoded.Command, tc.want, err)
		}
	}
}

func TestExtractClarificationQuestion(t *testing.T) {
	raw := "您好！为了设置每10分钟监控CPU使用率并发送到钉钉机器人，请提供 Webhook URL。"
	q := extractClarificationQuestion(raw)
	if q == "" {
		t.Fatal("expected clarification question to be detected")
	}
}

func TestRecoverPlainTextResponseExtractsPendingShellBlock(t *testing.T) {
	raw := "让我重新来，先从硬件和存储开始。\n\n```bash\n$ system_profiler SPHardwareDataType | head -80\n```\n"
	resp, ok := recoverPlainTextResponse(raw)
	if !ok {
		t.Fatal("expected plain text response to be recovered")
	}
	if resp.Action != "execute" || resp.Command != "system_profiler SPHardwareDataType | head -80" {
		t.Fatalf("unexpected recovered response: %#v", resp)
	}
	if resp.IsFinished {
		t.Fatal("pending shell step must not be treated as a final report")
	}
}

func TestRecoverPlainTextResponsePresentsNaturalLanguageConclusion(t *testing.T) {
	raw := "检查完成：系统运行正常。\n\n- CPU 正常\n- 磁盘正常"
	resp, ok := recoverPlainTextResponse(raw)
	if !ok || !resp.IsFinished || resp.Action != "finish" {
		t.Fatalf("plain conclusion should finish cleanly: %#v", resp)
	}
	if resp.FinalReport != raw || strings.Contains(resp.FinalReport, "解析失败") {
		t.Fatalf("plain conclusion was not preserved: %q", resp.FinalReport)
	}
}

func TestRecoverPlainTextResponseRetriesTruncatedJSON(t *testing.T) {
	raw := `{"action":"execute","command":"uname -a"`
	resp, ok := recoverPlainTextResponse(raw)
	if !ok || resp.Action != "" || resp.IsFinished {
		t.Fatalf("truncated JSON should request a retry: %#v", resp)
	}
	if !strings.Contains(resp.Thought, "自动") {
		t.Fatalf("retry thought should explain recovery: %q", resp.Thought)
	}
}

func TestRecoverPlainTextResponseKeepsClarificationAsQuestion(t *testing.T) {
	raw := "继续连接前，请提供服务器地址和用户名。"
	resp, ok := recoverPlainTextResponse(raw)
	if !ok || resp.Action != "ask_user" || resp.Question != raw {
		t.Fatalf("clarification should remain interactive: %#v", resp)
	}
}

func TestRecoverPlainTextSummaryWithRhetoricalQuestionsFinishes(t *testing.T) {
	raw := `让我想想这个问题：用户输入和独立验证到底应该怎么平衡？

分析结果如下：
1. 用户提供的信息是输入，不是无需验证的结论。
2. 善意提供的信息也可能包含基于经验的推测。
3. 工具的价值在于可靠地核验证据。

所以总结成一条原则：尊重用户输入，同时独立核验证据链。`
	resp, ok := recoverPlainTextResponse(raw)
	if !ok || resp.Action != "finish" || !resp.IsFinished {
		t.Fatalf("rhetorical summary must finish instead of asking user: %#v", resp)
	}
	if resp.FinalReport != raw || resp.Question != "" {
		t.Fatalf("summary should be preserved as final report: %#v", resp)
	}
}

func TestFinalizeResponseExpandsEscapedReportNewlines(t *testing.T) {
	raw := "授权边界先说清楚。\\n\\n## 下一步\\n- 核对书面授权\\n- 再决定能不能测\\n路径 C:\\notes 保持原样"
	resp := finalizeResponse(AgentResponse{Action: "finish", IsFinished: true, FinalReport: raw})
	if strings.Count(resp.FinalReport, `\n`) != 1 {
		t.Fatalf("report newlines stayed escaped: %q", resp.FinalReport)
	}
	if !strings.Contains(resp.FinalReport, "\n\n## 下一步\n- 核对书面授权\n- 再决定能不能测\n") {
		t.Fatalf("report was not split into paragraphs: %q", resp.FinalReport)
	}
	if !strings.Contains(resp.FinalReport, `C:\notes`) {
		t.Fatalf("path was rewritten: %q", resp.FinalReport)
	}
}

func TestUnescapeModelLineBreaksPreservesWindowsPathsAndCodeEscapes(t *testing.T) {
	raw := "## 排查\\n- 查看 C:\\n1\\logs 和 C:\\t1\\logs\\n- 命令 `printf '\\n'`，保留 \\r 与 \\\" 引号"
	got := UnescapeModelLineBreaks(raw)
	if !strings.Contains(got, "## 排查\n- 查看 C:\\n1\\logs 和 C:\\t1\\logs\n- 命令") {
		t.Fatalf("layout or paths changed unexpectedly: %q", got)
	}
	for _, literal := range []string{`printf '\n'`, `\r`, `\"`} {
		if !strings.Contains(got, literal) {
			t.Fatalf("literal %q was rewritten: %q", literal, got)
		}
	}
}

func TestUnescapeModelLineBreaksSingleMarkdownBoundary(t *testing.T) {
	if got := UnescapeModelLineBreaks(`## 结论\n- 已完成`); got != "## 结论\n- 已完成" {
		t.Fatalf("single escaped markdown boundary not expanded: %q", got)
	}
	if got := UnescapeModelLineBreaks(`C:\n1\logs`); got != `C:\n1\logs` {
		t.Fatalf("single path segment changed: %q", got)
	}
	if got := UnescapeModelLineBreaks(`C:\n- report`); got != `C:\n- report` {
		t.Fatalf("path resembling a list boundary changed: %q", got)
	}
}

func TestUnescapeModelLineBreaksPreservesInlineCode(t *testing.T) {
	raw := "## 示例\\n- 正则 `\\n- ` 应保持原样\\n- 下一项"
	got := UnescapeModelLineBreaks(raw)
	if got != "## 示例\n- 正则 `\\n- ` 应保持原样\n- 下一项" {
		t.Fatalf("inline code escape changed: %q", got)
	}
}

func TestFinalizeResponseRejectsFakeAskUserSummary(t *testing.T) {
	summary := `让我想想这个哲学问题：我们到底怎么平衡？

这其实是一个永恒难题。
1. 用户提供的是输入，不是无需验证的结论。
2. Agent 应当独立核验证据链。
3. 最后输出可靠结论。

所以总结：信任输入，但保持独立判断。`
	resp := finalizeResponse(AgentResponse{Action: "ask_user", Question: summary})
	if resp.Action != "finish" || !resp.IsFinished || resp.FinalReport != summary {
		t.Fatalf("fake ask_user summary should become final report: %#v", resp)
	}
}

func TestFinalizeResponseKeepsRealShortAskUser(t *testing.T) {
	resp := finalizeResponse(AgentResponse{Action: "ask_user", Question: "你希望使用可见 Chrome 还是无头浏览器？"})
	if resp.Action != "ask_user" || resp.IsFinished {
		t.Fatalf("real direct question should remain ask_user: %#v", resp)
	}
}

func TestFinalizeResponseKeepsLongMenuAskUserInteractive(t *testing.T) {
	question := `欢迎使用 DeepSentry！

请选择本次需要执行的任务：

🔍 FOFA 资产测绘 — 对已授权目标进行资产发现
🛡️ 安全巡检 — 检查服务器状态与风险项
📋 日志分析 — 汇总异常登录与关键时间线
☁️ 服务器管理 — 查看已配置主机的运行状态
🧰 其他任务 — 使用自然语言描述具体目标

请输入选项名称，或直接说明希望完成的任务。`
	resp := finalizeResponse(AgentResponse{Action: "ask_user", Question: question})
	if resp.Action != "ask_user" || resp.IsFinished || resp.Question != question || resp.FinalReport != "" {
		t.Fatalf("menu question must remain interactive: %#v", resp)
	}
}

func TestFinalizeResponseAlwaysKeepsStructuredChoiceInteractive(t *testing.T) {
	question := strings.Repeat("这是较长的背景说明。", 40)
	resp := finalizeResponse(AgentResponse{
		Action:      "ask_user",
		Question:    question,
		Options:     []string{"执行方案 A", "执行方案 B"},
		IsFinished:  true,
		FinalReport: "不应显示为报告",
	})
	if resp.Action != "ask_user" || resp.IsFinished || resp.FinalReport != "" || len(resp.Options) != 2 {
		t.Fatalf("structured options must take precedence over finish fields: %#v", resp)
	}
}

func TestFinalizeResponseKeepsLongDirectQuestionInteractive(t *testing.T) {
	question := strings.Repeat("这里是执行前必须了解的较长背景。", 40) + "你希望我采用保守模式还是完整模式？"
	resp := finalizeResponse(AgentResponse{Action: "ask_user", Question: question})
	if resp.Action != "ask_user" || resp.IsFinished || resp.Question != question {
		t.Fatalf("long direct question must remain interactive: %#v", resp)
	}
}

func TestFinalizeResponseNormalizesThinkAlias(t *testing.T) {
	short := finalizeResponse(AgentResponse{Action: "think", Thought: "我再思考一下"})
	if short.Action != "" || short.IsFinished {
		t.Fatalf("short think alias should request a corrected action: %#v", short)
	}
	long := finalizeResponse(AgentResponse{Action: "think", Thought: strings.Repeat("完整总结。", 60)})
	if long.Action != "finish" || !long.IsFinished || long.FinalReport == "" {
		t.Fatalf("long think alias should become a final report: %#v", long)
	}
}

func TestExtractCommandStringDecodesJSONOnce(t *testing.T) {
	raw := `{"action":"execute","command":"echo C:\\u0041 \u0026 \uD83D\uDE00",`
	got, ok := extractCommandString(raw)
	if !ok || got != `echo C:\u0041 & 😀` {
		t.Fatalf("fallback command decoded incorrectly: %q ok=%t", got, ok)
	}
}

func TestParseToolCallResponsePreservesLiteralUnicodeEscapeInCommand(t *testing.T) {
	raw := `{"action":"execute","command":"echo C:\\Users\\u0041 \\u0026"}`
	resp, err := ParseToolCallResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Command != `echo C:\Users\u0041 \u0026` {
		t.Fatalf("valid JSON command was decoded twice: %q", resp.Command)
	}
}

func TestNativeToolCallsPreserveRawWindowsPaths(t *testing.T) {
	response, err := ParseToolCallResponse(`{"action":"execute","command":"type C:\new\test.txt"}`)
	if err != nil || response.Command != `type C:\new\test.txt` {
		t.Fatalf("agent action path changed: %q %v", response.Command, err)
	}
	tool, err := ParseNamedToolCall("file_tail", `{"path":"C:\Users\示例用户\report.txt","lines":5}`)
	if err != nil || tool.ToolArgs["path"] != `C:\Users\示例用户\report.txt` {
		t.Fatalf("named tool path changed: %#v %v", tool.ToolArgs, err)
	}
}

func TestTodoItemAcceptsNumericIDAndTitleDetail(t *testing.T) {
	raw := `{"id":1,"title":"修复签名逻辑","status":"in_progress","detail":"将 printf '%s' 改为 printf '%s\n'"}`
	var item TodoItem
	if err := item.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if item.ID != "1" {
		t.Fatalf("id=%q", item.ID)
	}
	if !strings.Contains(item.Content, "修复签名逻辑") || !strings.Contains(item.Content, "printf") {
		t.Fatalf("content not normalized: %q", item.Content)
	}
	if item.Status != "in_progress" {
		t.Fatalf("status=%q", item.Status)
	}
}

func TestAgentToolSchemaConstrainsSubAgentTasks(t *testing.T) {
	defs := AgentToolDefinitions()
	if len(defs) == 0 {
		t.Fatal("expected agent tool definitions")
	}
	props, ok := defs[0].Function.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties missing or wrong type: %#v", defs[0].Function.Parameters["properties"])
	}
	action, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatalf("action schema missing enum-friendly shape: %#v", props["action"])
	}
	if _, ok := action["enum"].([]string); !ok {
		t.Fatalf("action enum missing: %#v", action)
	}
	parallel, ok := props["parallel_tasks"].(map[string]interface{})
	if !ok {
		t.Fatalf("parallel_tasks schema missing: %#v", props["parallel_tasks"])
	}
	items, ok := parallel["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("parallel_tasks items missing: %#v", parallel["items"])
	}
	required, ok := items["required"].([]string)
	if !ok {
		t.Fatalf("parallel_tasks items required missing: %#v", items["required"])
	}
	if len(required) != 2 || required[0] != "task_name" || required[1] != "task_prompt" {
		t.Fatalf("unexpected parallel task required fields: %#v", required)
	}
}

func TestAgentToolDefinitionsExposeBuiltinsDirectly(t *testing.T) {
	defs := AgentToolDefinitions()
	byName := map[string]FunctionDef{}
	for _, def := range defs {
		byName[def.Function.Name] = def.Function
	}
	for _, name := range []string{"agent_action", "tool_catalog", "skill", "config_manage", "fleet_inventory", "fleet_exec", "fleet_file"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("native tool %s missing; got %d definitions", name, len(defs))
		}
	}
	configProps, ok := byName["config_manage"].Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("config_manage properties missing: %#v", byName["config_manage"].Parameters)
	}
	if _, ok := configProps["port"]; !ok {
		t.Fatalf("config_manage native schema must teach host+port: %#v", configProps)
	}
	for _, def := range defs {
		if got := len([]rune(def.Function.Description)); got > 1024 {
			t.Fatalf("native description for %s too large for compatible providers: %d runes", def.Function.Name, got)
		}
	}
}

func TestParseNamedToolCallMapsToHarnessAction(t *testing.T) {
	resp, err := ParseNamedToolCall("fleet_exec", `{"selector":"prod","command":"uptime","concurrency":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Action != "tool" || resp.ToolName != "fleet_exec" || resp.ToolArgs["concurrency"] != "3" {
		t.Fatalf("unexpected mapped response: %#v", resp)
	}
}

func TestParseNamedToolCallMapsSkillToLoadSkill(t *testing.T) {
	resp, err := ParseNamedToolCall("skill", `{"name":"bilibili-play"}`)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Action != "load_skill" || resp.SkillName != "bilibili-play" {
		t.Fatalf("skill native tool should map to load_skill: %#v", resp)
	}
	alias, err := ParseNamedToolCall("load_skill", `{"skill_name":"zipcracker"}`)
	if err != nil {
		t.Fatal(err)
	}
	if alias.Action != "load_skill" || alias.SkillName != "zipcracker" {
		t.Fatalf("load_skill alias should map: %#v", alias)
	}
}
