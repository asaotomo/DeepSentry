package harness

import (
	"testing"

	"ai-edr/internal/mcp"
)

func TestSessionApprovalScopeFileMutationIsBoundToTypeAndExactPath(t *testing.T) {
	first, label := SessionApprovalScope(&AgentAction{Type: ActionEditFile, Path: "/tmp/site.html", OldString: "a", NewString: "b"})
	second, _ := SessionApprovalScope(&AgentAction{Type: ActionEditFile, Path: "/tmp/site.html", OldString: "b", NewString: "c"})
	otherPath, _ := SessionApprovalScope(&AgentAction{Type: ActionEditFile, Path: "/tmp/other.html", OldString: "a", NewString: "b"})
	write, _ := SessionApprovalScope(&AgentAction{Type: ActionWriteFile, Path: "/tmp/site.html", Content: "new"})

	if first == "" || label == "" || first != second {
		t.Fatalf("repeated edits of one file must share a useful scope: %q %q %q", first, second, label)
	}
	if first == otherPath || first == write {
		t.Fatal("file approval scope broadened across another path or mutation type")
	}
}

func TestSessionApprovalScopeCommandsRemainExactAndToolsReuseSemanticClass(t *testing.T) {
	command, _ := SessionApprovalScope(&AgentAction{Type: ActionExecute, Command: "rm /tmp/a", TargetHost: "host-a"})
	otherCommand, _ := SessionApprovalScope(&AgentAction{Type: ActionExecute, Command: "rm /tmp/b", TargetHost: "host-a"})
	otherTarget, _ := SessionApprovalScope(&AgentAction{Type: ActionExecute, Command: "rm /tmp/a", TargetHost: "host-b"})
	if command == otherCommand || command == otherTarget {
		t.Fatal("command session approval broadened across command or target")
	}

	tool, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: "config_manage", ToolArgs: map[string]string{"action": "set", "key": "a"}})
	otherArgs, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: "config_manage", ToolArgs: map[string]string{"action": "set", "key": "b"}})
	otherAction, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: "config_manage", ToolArgs: map[string]string{"action": "delete", "key": "b"}})
	if tool != otherArgs {
		t.Fatal("same config_manage operation should reuse the session approval when ordinary payload changes")
	}
	if tool == otherAction {
		t.Fatal("tool session approval broadened across a different operation")
	}

	firstHost, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: "hawkeye_intercept", ToolArgs: map[string]string{"action": "release", "host": "a.test", "id": "1"}})
	secondID, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: "hawkeye_intercept", ToolArgs: map[string]string{"action": "release", "host": "a.test", "id": "2"}})
	otherHost, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: "hawkeye_intercept", ToolArgs: map[string]string{"action": "release", "host": "b.test", "id": "2"}})
	if firstHost != secondID || firstHost == otherHost {
		t.Fatal("semantic tool approval must reuse payload changes but remain bound to target identity")
	}
}

func TestRedactedActionCarriesScopeDerivedBeforeSecretRedaction(t *testing.T) {
	action := AgentAction{Type: ActionTool, ToolName: "redis_probe", ToolArgs: map[string]string{
		"host": "127.0.0.1", "password": "first-secret",
	}}
	wantKey, wantLabel := SessionApprovalScope(&action)
	redacted := RedactedAction(action)
	if redacted.ApprovalScopeKey != wantKey || redacted.ApprovalScopeLabel != wantLabel {
		t.Fatal("redacted confirmation lost its original approval fingerprint")
	}

	other := action
	other.ToolArgs = map[string]string{"host": "127.0.0.1", "password": "second-secret"}
	otherRedacted := RedactedAction(other)
	if redacted.ApprovalScopeKey == otherRedacted.ApprovalScopeKey {
		t.Fatal("different original secret-bearing invocations shared one approval scope")
	}
}

func TestSessionApprovalScopeUsesRegisteredMCPToolAsSemanticOperation(t *testing.T) {
	name := "approval_test__browser_click"
	mcp.Global().RegisterHandler(name, mcp.ExternalTool{Name: name, OriginalName: "browser_click", Server: "approval_test"}, func(map[string]string) (string, error) {
		return "ok", nil
	})
	first, label := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: name, ToolArgs: map[string]string{"ref": "button-1"}})
	second, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: name, ToolArgs: map[string]string{"ref": "button-2"}})
	if first == "" || first != second || label == "" {
		t.Fatalf("registered MCP operation did not reuse its session scope: first=%q second=%q label=%q", first, second, label)
	}

	evalName := "approval_test__hawkeye_evaluate"
	mcp.Global().RegisterHandler(evalName, mcp.ExternalTool{Name: evalName, OriginalName: "hawkeye_evaluate", Server: "approval_test"}, func(map[string]string) (string, error) {
		return "ok", nil
	})
	one, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: evalName, ToolArgs: map[string]string{"expression": "1+1"}})
	two, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: evalName, ToolArgs: map[string]string{"expression": "dangerous()"}})
	if one == two {
		t.Fatal("arbitrary-code MCP tool approval was broadened across expressions")
	}
}

func TestSessionApprovalScopeUnderstandsHawkEyeCamelCaseTargetsAndFilePaths(t *testing.T) {
	name := "approval_test__hawkeye_capture_history"
	mcp.Global().RegisterHandler(name, mcp.ExternalTool{Name: name, OriginalName: "hawkeye_capture_history", Server: "hawkeye"}, func(map[string]string) (string, error) {
		return "ok", nil
	})
	first, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: name, ToolArgs: map[string]string{"targetHost": "a.test", "limit": "20"}})
	second, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: name, ToolArgs: map[string]string{"targetHost": "a.test", "limit": "50"}})
	other, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: name, ToolArgs: map[string]string{"targetHost": "b.test", "limit": "50"}})
	if first != second || first == other {
		t.Fatal("HawkEye targetHost should retain host identity while allowing ordinary pagination changes")
	}

	shot := "approval_test__browser_screenshot"
	mcp.Global().RegisterHandler(shot, mcp.ExternalTool{Name: shot, OriginalName: "browser_screenshot", Server: "hawkeye"}, func(map[string]string) (string, error) {
		return "ok", nil
	})
	pathA, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: shot, ToolArgs: map[string]string{"save_to_file": "true", "file_path": "/tmp/a.png"}})
	pathB, _ := SessionApprovalScope(&AgentAction{Type: ActionTool, ToolName: shot, ToolArgs: map[string]string{"save_to_file": "true", "file_path": "/tmp/b.png"}})
	if pathA == pathB {
		t.Fatal("HawkEye screenshot write approval broadened across file_path")
	}
}
