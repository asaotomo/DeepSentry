package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"ai-edr/internal/mcp"
)

// SessionApprovalScope returns a conservative fingerprint for "allow similar
// actions for this session". File mutations intentionally ignore the changed
// content but retain both the operation and exact path. Commands stay exact.
// Tool scopes use the tool, operation discriminator and target identity so the
// UI's "same class for this session" choice survives ordinary payload changes.
// Secrets remain part of the fingerprint and RedactedAction derives the key
// before redaction, so a credential change always asks again.
func SessionApprovalScope(action *AgentAction) (key, label string) {
	if action == nil {
		return "", ""
	}
	target := strings.Join([]string{
		strings.TrimSpace(action.TargetProtocol),
		strings.TrimSpace(action.TargetName),
		strings.TrimSpace(action.TargetHost),
		strings.TrimSpace(action.TargetSelector),
	}, "|")

	switch action.Type {
	case ActionEditFile, ActionWriteFile:
		path := filepath.Clean(strings.TrimSpace(action.Path))
		if path == "." || path == "" {
			path = "<unspecified>"
		}
		return approvalScopeKey(string(action.Type), target, path),
			fmt.Sprintf("后续 `%s` 同一文件 `%s`", action.Type, path)
	case ActionExecute:
		return approvalScopeKey(string(action.Type), target, strings.TrimSpace(action.Command)),
			"后续在同一目标执行完全相同的命令"
	case ActionTool:
		fingerprint, semantic, summary := toolApprovalFingerprint(action.ToolName, action.ToolArgs)
		invocation := action.ToolName + "|" + fingerprint
		if semantic {
			return approvalScopeKey(string(action.Type), target, invocation),
				fmt.Sprintf("后续在同一目标调用工具 `%s` 的同类操作（%s；普通参数可变化）", action.ToolName, summary)
		}
		return approvalScopeKey(string(action.Type), target, invocation),
			fmt.Sprintf("后续使用相同参数调用工具 `%s`", action.ToolName)
	case ActionToolBatch:
		parts := make([]string, 0, len(action.ToolCalls))
		for _, call := range action.ToolCalls {
			fingerprint, _, _ := toolApprovalFingerprint(call.Name, call.Args)
			parts = append(parts, call.Name+"|"+fingerprint)
		}
		return approvalScopeKey(string(action.Type), target, strings.Join(parts, "\n")),
			"后续在同一目标执行相同工具与操作类型的批量调用"
	default:
		identity := strings.Join([]string{action.TaskName, action.TaskPrompt, action.Path, action.Reason}, "|")
		return approvalScopeKey(string(action.Type), target, identity),
			fmt.Sprintf("后续执行相同的 `%s` 操作", action.Type)
	}
}

func toolApprovalFingerprint(toolName string, args map[string]string) (fingerprint string, semantic bool, summary string) {
	if len(args) == 0 {
		return "", false, ""
	}
	operationKeys := []string{"action", "operation", "op", "method", "mode"}
	identityKeys := []string{
		"host", "target", "target_host", "hostname", "server", "server_name", "url", "endpoint",
		"port", "protocol", "path", "file_path", "resource", "database", "db", "namespace", "scope", "tab_id",
	}
	selected := make(map[string]string)
	var operationParts []string
	for _, key := range operationKeys {
		if value, ok := lookupApprovalArg(args, key); ok && strings.TrimSpace(value) != "" {
			selected[key] = value
			operationParts = append(operationParts, key+"="+value)
		}
	}
	if len(operationParts) == 0 {
		// MCP servers commonly expose one operation per tool (for example
		// browser_click or hawkeye_capture_start), so requiring an action/op
		// field made "A 本会话同类" behave exactly like one-shot approval. Treat
		// the registered MCP tool name itself as the operation discriminator.
		// Arbitrary-code/file-write style tools deliberately remain exact.
		if !isSemanticMCPApprovalTool(toolName) {
			return stableApprovalArgs(args), false, ""
		}
		operationParts = append(operationParts, "tool="+strings.TrimSpace(toolName))
	}
	// config_manage intentionally groups one operation (for example
	// add_mcp_server) across different names/values. Other tools remain bound to
	// their explicit resource/host identity.
	if !strings.EqualFold(strings.TrimSpace(toolName), "config_manage") {
		for _, key := range identityKeys {
			if value, ok := lookupApprovalArg(args, key); ok && strings.TrimSpace(value) != "" {
				selected[key] = value
			}
		}
	}
	for key, value := range args {
		if sensitiveApprovalKey(key) {
			selected[key] = value
		}
	}
	return stableApprovalArgs(selected), true, strings.Join(operationParts, ", ")
}

func isSemanticMCPApprovalTool(toolName string) bool {
	name := strings.TrimSpace(strings.TrimPrefix(toolName, "mcp:"))
	if name == "" {
		return false
	}
	if _, _, ok := mcp.Global().Get(name); !ok {
		return false
	}
	lower := strings.ToLower(name)
	for _, marker := range []string{"evaluate", "execute", "exec", "shell", "terminal", "script", "file_write", "upload"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func lookupApprovalArg(args map[string]string, want string) (string, bool) {
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.NewReplacer("_", "", "-", "", ".", "").Replace(value)
		return value
	}
	want = normalize(want)
	for key, value := range args {
		if normalize(key) == want {
			return value, true
		}
	}
	return "", false
}

func sensitiveApprovalKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"password", "passwd", "token", "secret", "api_key", "apikey", "credential", "private_key", "cookie", "authorization", "session"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func approvalScopeKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func stableApprovalArgs(args map[string]string) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(args[key])
		b.WriteByte('\n')
	}
	return b.String()
}
