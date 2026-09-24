package harness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/mcp"
	"ai-edr/internal/security"
)

var hasFofaMapMCP = mcp.HasFofaMapTools

const (
	identicalReadSkipAfter  = 2
	identicalProbeSkipAfter = 1
	maxStallWarnings        = 3
	maxFailStreak           = 3
	maxNoProgressStreak     = 3
	maxTimeoutStreak        = 2
	todoNudgeAfter          = 6
)

// LoopDecision is the pre-execution verdict for one agent action.
type LoopDecision struct {
	Allow    bool
	HardStop bool
	Warning  string
	Output   string
}

func actionFingerprint(action AgentAction) string {
	switch action.Type {
	case ActionTodo, ActionLoadSkill, ActionFinish, ActionAskUser, ActionRemember, ActionForget:
		return ""
	}
	// Hash the complete executable payload. Truncating commands or omitting
	// task/file fields made different work look identical and caused the loop
	// guard to reject valid delegations or edits. The digest also keeps secrets
	// in command arguments out of guard warnings and checkpoint metadata.
	payload := struct {
		Command        string
		TaskName       string
		TaskPrompt     string
		TargetSelector string
		TargetName     string
		TargetProtocol string
		TargetHost     string
		ParallelTasks  []SubAgentTaskAction
		Path           string
		Content        string
		Pattern        string
		OldString      string
		NewString      string
		ReplaceAll     bool
		GlobPattern    string
		Offset         int
		Limit          int
		ToolArgs       map[string]string
		ToolCalls      []ToolCallAction
	}{
		Command: normalizeShellCommand(action.Command), TaskName: action.TaskName,
		TaskPrompt: action.TaskPrompt, TargetSelector: action.TargetSelector,
		TargetName: action.TargetName, TargetProtocol: action.TargetProtocol,
		TargetHost: action.TargetHost, ParallelTasks: action.ParallelTasks,
		Path: action.Path, Content: action.Content, Pattern: action.Pattern,
		OldString: action.OldString, NewString: action.NewString,
		ReplaceAll: action.ReplaceAll, GlobPattern: action.GlobPattern,
		Offset: action.Offset, Limit: action.Limit,
		ToolArgs: action.ToolArgs, ToolCalls: action.ToolCalls,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%s|%s|%x", action.Type, strings.ToLower(strings.TrimSpace(action.ToolName)), sum[:16])
}

func compactFingerprintPart(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func normalizeShellCommand(command string) string {
	command = strings.TrimSpace(command)
	command = strings.TrimPrefix(command, "local_run ")
	return strings.Join(strings.Fields(command), " ")
}

func isWastefulProbe(action AgentAction) bool {
	if isNewBrowserTab(action) {
		return true
	}
	if action.Type == ActionLS {
		path := strings.TrimSpace(action.Path)
		return path == "" || path == "." || path == "./"
	}
	if action.Type != ActionExecute {
		return false
	}
	fields := strings.Fields(normalizeShellCommand(action.Command))
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "pwd", "dir":
		return len(fields) == 1
	case "ls":
		if len(fields) == 1 {
			return true
		}
		if len(fields) == 2 {
			switch fields[1] {
			case "-l", "-la", "-al", "-alh", "--color", "--color=auto":
				return true
			}
		}
	}
	return false
}

func isNewBrowserTab(action AgentAction) bool {
	if toolLooksLikeBrowserTabs(action.ToolName) && strings.EqualFold(strings.TrimSpace(action.ToolArgs["action"]), "new") {
		return true
	}
	for _, call := range action.ToolCalls {
		if toolLooksLikeBrowserTabs(call.Name) && strings.EqualFold(strings.TrimSpace(call.Args["action"]), "new") {
			return true
		}
	}
	return false
}

func toolLooksLikeBrowserTabs(name string) bool {
	return strings.Contains(strings.ToLower(name), "browser_tabs")
}

func isZIPNativeAction(action AgentAction) bool {
	if strings.EqualFold(strings.TrimSpace(action.ToolName), "zip_password_recover") {
		return true
	}
	for _, call := range action.ToolCalls {
		if strings.EqualFold(strings.TrimSpace(call.Name), "zip_password_recover") {
			return true
		}
	}
	return false
}

func isExternalZIPCrackAttempt(action AgentAction) bool {
	if isZIPNativeAction(action) {
		return false
	}
	if action.Type == ActionExecute && commandLooksLikeExternalZIPCrack(action.Command) {
		return true
	}
	if commandLooksLikeExternalZIPCrack(action.Command) {
		return true
	}
	if zipToolLooksExternal(action.ToolName, action.ToolArgs) {
		return true
	}
	for _, call := range action.ToolCalls {
		if zipToolLooksExternal(call.Name, call.Args) {
			return true
		}
	}
	return false
}

func zipToolLooksExternal(name string, args map[string]string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "archive_extract":
		source := strings.ToLower(strings.TrimSpace(firstMapValue(args, "source", "path", "file")))
		return strings.HasSuffix(source, ".zip") || strings.Contains(source, ".zip.")
	case "script_run":
		blob := strings.ToLower(strings.TrimSpace(firstMapValue(args, "content", "path", "script")))
		return strings.Contains(blob, "zipcracker") || strings.Contains(blob, "zipfile") || strings.Contains(blob, ".zip")
	default:
		return false
	}
}

func firstMapValue(args map[string]string, keys ...string) string {
	if args == nil {
		return ""
	}
	for _, key := range keys {
		if value := strings.TrimSpace(args[key]); value != "" {
			return value
		}
	}
	return ""
}

func commandLooksLikeExternalZIPCrack(command string) bool {
	cmd := strings.ToLower(normalizeShellCommand(command))
	if cmd == "" {
		return false
	}
	tokens := strings.FieldsFunc(cmd, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '.' && r != '_' && r != '-'
	})
	external := map[string]bool{
		"unzip": true, "7z": true, "7za": true, "john": true, "hashcat": true,
		"zip2john": true, "fcrackzip": true, "bkcrack": true, "rar": true, "unrar": true,
		"zipcracker": true, "zipcracker.py": true,
	}
	hasPython := false
	hasZipHint := false
	for _, token := range tokens {
		base := strings.ToLower(filepath.Base(token))
		if external[token] || external[base] {
			return true
		}
		if token == "python" || token == "python3" || token == "py" {
			hasPython = true
		}
		if token == "zip" || token == "zipfile" || strings.HasSuffix(token, ".zip") || strings.Contains(token, "zipcracker") {
			hasZipHint = true
		}
	}
	return hasPython && hasZipHint
}

func outputHash(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > 2048 {
		trimmed = trimmed[:2048]
	}
	sum := sha256.Sum256([]byte(trimmed))
	return fmt.Sprintf("%x", sum[:8])
}

func classifyLoopFailure(output string, failed bool) (isFail bool, class string) {
	if failed {
		return true, failureClass(output)
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "执行失败"), strings.Contains(lower, "timeout"), strings.Contains(lower, "超时"):
		return true, failureClass(output)
	default:
		return false, ""
	}
}

func failureClass(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "超时"):
		return "timeout"
	case strings.Contains(lower, "not found"), strings.Contains(lower, "未找到"), strings.Contains(lower, "no such"):
		return "not_found"
	default:
		return "error"
	}
}

func (s *AgentState) nonCommandPayload(action AgentAction) (bool, string) {
	if action.Type != ActionExecute {
		return false, ""
	}
	if security.LooksLikeFlagCommand(action.Command) {
		return true, "循环守卫：flag{...} 是解出的比赛答案或文件内容，不是 shell 命令。禁止 execute。请用 finish 提交该 flag。"
	}
	return false, ""
}

func (s *AgentState) skillForbidsProbe(action AgentAction) (bool, string) {
	if s == nil {
		return false, ""
	}
	zipTask := hasLoadedSkill(s.LoadedSkills, "zipcracker")
	if isNewBrowserTab(action) && loadedSkillHintsBrowser(s.LoadedSkills) {
		return true, "循环守卫：已加载浏览器/播放 Skill 时禁止 browser_tabs action=new。请按 playbook 用 browser_navigate 打开已知 URL，不要新开空白标签。"
	}
	if zipTask && isWastefulProbe(action) && !isNewBrowserTab(action) {
		return true, "循环守卫：ZIP 任务禁止 pwd/ls 探测。第一步调用 zip_password_recover（有掩码用 recover+mask，否则 auto）。"
	}
	if zipTask && !s.hasTriedToolLocked("zip_password_recover") && isExternalZIPCrackAttempt(action) {
		return true, "循环守卫：ZIP 解密必须先走原生 zip_password_recover。有掩码用 action=recover 且 mask 原样传递，否则 action=auto、extract=true。只有该工具明确失败后才允许 python/unzip/7z/john 等其他办法。"
	}
	if hasFofaMapMCP() && isFofaMapShellBypass(action) {
		return true, "循环守卫：本会话已连接 FofaMap MCP v2.0.1。禁止 execute scripts/fofa_recon.py 或 curl FOFA API。请调用 fofa_account / fofa_rules / fofa_validate_query / fofa_search。"
	}
	return false, ""
}

func isFofaMapShellBypass(action AgentAction) bool {
	if commandLooksLikeFofaMapShellBypass(action.Command) {
		return true
	}
	if commandLooksLikeFofaMapShellBypass(firstMapValue(action.ToolArgs, "content", "command", "script", "path")) {
		return true
	}
	for _, call := range action.ToolCalls {
		if commandLooksLikeFofaMapShellBypass(firstMapValue(call.Args, "content", "command", "script", "path")) {
			return true
		}
	}
	return false
}

func commandLooksLikeFofaMapShellBypass(command string) bool {
	cmd := strings.ToLower(normalizeShellCommand(command))
	if cmd == "" {
		return false
	}
	if strings.Contains(cmd, "fofa_recon.py") {
		return true
	}
	hitsAPI := strings.Contains(cmd, "fofa.so") ||
		strings.Contains(cmd, "fofa.info") ||
		strings.Contains(cmd, "api.fofa") ||
		strings.Contains(cmd, "fofaapi")
	if !hitsAPI {
		return false
	}
	return strings.Contains(cmd, "curl") ||
		strings.Contains(cmd, "wget") ||
		strings.Contains(cmd, "python") ||
		strings.Contains(cmd, "http")
}

func hasLoadedSkill(loaded map[string]string, name string) bool {
	for loadedName := range loaded {
		if strings.EqualFold(loadedName, name) {
			return true
		}
	}
	return false
}

func loadedSkillHintsBrowser(loaded map[string]string) bool {
	for name := range loaded {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "bilibili") || strings.Contains(lower, "browser") || strings.Contains(lower, "play") {
			return true
		}
	}
	return false
}

func (s *AgentState) LoopShouldHalt() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.StallCount >= maxStallWarnings
}

func (s *AgentState) noteStallLocked() {
	s.StallCount++
}

// LoopBeforeExecute rejects no-progress calls before they run. Codex-style
// identical tool-call loops and DSH skill-first probes are stopped here so
// side effects never happen a second time.
func (s *AgentState) LoopBeforeExecute(action AgentAction) LoopDecision {
	if s == nil {
		return LoopDecision{Allow: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.StallCount >= maxStallWarnings {
		return LoopDecision{
			HardStop: true,
			Warning:  fmt.Sprintf("循环守卫：本会话已拦截 %d 次空转，停止重复探测。请更换策略、按已加载 Skill 的下一步执行，或 finish 说明受阻原因。", s.StallCount),
		}
	}
	if forbid, msg := s.nonCommandPayload(action); forbid {
		s.noteStallLocked()
		if s.StallCount >= maxStallWarnings {
			return LoopDecision{HardStop: true, Warning: msg}
		}
		return LoopDecision{Warning: msg, Output: msg}
	}
	if forbid, msg := s.skillForbidsProbe(action); forbid {
		s.noteStallLocked()
		if s.StallCount >= maxStallWarnings {
			return LoopDecision{HardStop: true, Warning: msg}
		}
		return LoopDecision{Warning: msg, Output: msg}
	}
	fp := actionFingerprint(action)
	if fp == "" {
		return LoopDecision{Allow: true}
	}
	same := fp == s.LastActionFingerprint && s.LastActionRepeat >= 1
	if !same {
		return LoopDecision{Allow: true}
	}
	limit := identicalReadSkipAfter
	// A first failure may be transient. Idempotent work needs one bounded retry
	// so connection resets and similar recoverable faults do not become false
	// terminal failures. Wasteful probes remain single-shot, and a second
	// identical failure is still blocked before a third execution.
	if isWastefulProbe(action) {
		limit = identicalProbeSkipAfter
	}
	if s.LastActionRepeat < limit {
		return LoopDecision{Allow: true}
	}
	s.noteStallLocked()
	msg := fmt.Sprintf("循环守卫：拒绝再次执行相同动作（%s）。不要重复已完成或已失败的调用。请更换工具/参数，或按已加载 Skill 的下一步；证据已够则 finish。", compactFingerprintPart(fp, 160))
	if s.StallCount >= maxStallWarnings {
		return LoopDecision{HardStop: true, Warning: msg}
	}
	return LoopDecision{Warning: msg, Output: msg}
}

// LoopRecordSkip updates fingerprints after a blocked call so the next
// identical attempt stays blocked.
func (s *AgentState) LoopRecordSkip(action AgentAction) {
	if s == nil {
		return
	}
	fp := actionFingerprint(action)
	if fp == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LastActionFingerprint == fp {
		s.LastActionRepeat++
	} else {
		s.LastActionFingerprint = fp
		s.LastActionRepeat = 1
	}
	s.LastFailed = true
}

// LoopAfterExecute records outcome hashes and returns an extra warning when
// failures, timeouts, or identical outputs show no progress.
func (s *AgentState) LoopAfterExecute(action AgentAction, output string, failed bool) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if action.Type == ActionTodo {
		s.StepsSinceTodo = 0
	} else {
		s.StepsSinceTodo++
	}
	if strings.EqualFold(strings.TrimSpace(action.ToolName), "zip_password_recover") {
		s.markTriedToolLocked("zip_password_recover")
	}
	for _, call := range action.ToolCalls {
		if strings.EqualFold(strings.TrimSpace(call.Name), "zip_password_recover") {
			s.markTriedToolLocked("zip_password_recover")
		}
	}
	fp := actionFingerprint(action)
	if fp == "" {
		return s.todoNudgeLocked()
	}
	if s.LastActionFingerprint == fp {
		s.LastActionRepeat++
	} else {
		s.LastActionFingerprint = fp
		s.LastActionRepeat = 1
	}
	isFail, class := classifyLoopFailure(output, failed)
	hash := outputHash(output)
	if isFail {
		s.FailStreak++
		s.LastFailed = true
		s.LastErrorClass = class
		if class == "timeout" {
			s.TimeoutStreak++
		} else {
			s.TimeoutStreak = 0
		}
	} else {
		s.LastFailed = false
		s.LastErrorClass = ""
		if hash != "" && hash == s.LastOutputHash {
			s.NoProgressStreak++
		} else {
			s.NoProgressStreak = 0
			s.FailStreak = 0
			s.TimeoutStreak = 0
			s.StallCount = 0
		}
	}
	s.LastOutputHash = hash

	switch {
	case s.TimeoutStreak >= maxTimeoutStreak:
		s.noteStallLocked()
		return fmt.Sprintf("循环守卫：连续 %d 次超时。不要原样重试；缩小范围、换工具，或 finish 报告受阻。", s.TimeoutStreak)
	case s.FailStreak >= maxFailStreak:
		s.noteStallLocked()
		return fmt.Sprintf("循环守卫：连续 %d 次失败（%s）。必须改参数或换工具，禁止重复同一调用。", s.FailStreak, s.LastErrorClass)
	case s.NoProgressStreak >= maxNoProgressStreak:
		s.noteStallLocked()
		return fmt.Sprintf("循环守卫：连续 %d 次得到相同输出。这不是进展。请换策略或 finish。", s.NoProgressStreak)
	default:
		return s.todoNudgeLocked()
	}
}

func (s *AgentState) todoNudgeLocked() string {
	if s.StepsSinceTodo < todoNudgeAfter || len(s.Todos) == 0 {
		return ""
	}
	pending := 0
	for _, item := range s.Todos {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if status == "" || status == "pending" || status == "in_progress" {
			pending++
		}
	}
	if pending == 0 {
		return ""
	}
	s.StepsSinceTodo = 0
	return fmt.Sprintf("循环守卫：已连续 %d 步未更新 todo，仍有 %d 项未完成。请用 todo 记录进展，或 finish 已完成的部分。", todoNudgeAfter, pending)
}

func blockedActionResult(action AgentAction, message string) *ActionResult {
	result := &ActionResult{Output: message, SkipApproval: true}
	if len(action.ToolCalls) > 0 {
		result.ToolResults = make([]ToolCallResult, 0, len(action.ToolCalls))
		for _, call := range action.ToolCalls {
			result.ToolResults = append(result.ToolResults, ToolCallResult{ID: call.ID, Name: call.Name, Output: message})
		}
	}
	return result
}

func latestUserTask(history []analyzer.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if !analyzer.IsRealUserTurn(history[i]) {
			continue
		}
		content := strings.TrimSpace(history[i].Content)
		if content == "" || isInjectedControlMessage(content) {
			continue
		}
		return content
	}
	return ""
}

func isInjectedControlMessage(content string) bool {
	for _, prefix := range []string{"系统警告:", "循环守卫：", "Output:", "用户拒绝执行", "MCP 工具返回了图片证据", "上一步执行失败:"} {
		if strings.HasPrefix(content, prefix) {
			return true
		}
	}
	return false
}
