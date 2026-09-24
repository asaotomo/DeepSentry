package builtin

import (
	"ai-edr/internal/executor"
	"ai-edr/internal/security"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func ScriptRun(rt Runtime, language, content, path, args string, timeoutSec int) (string, error) {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		language = "python"
	}
	if language != "python" && language != "shell" && language != "sh" {
		return "", fmt.Errorf("script_run 仅支持 language=python|shell")
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}

	isWindows := rt.IsWindows || (!rt.Exec.IsRemote() && runtime.GOOS == "windows")
	if isWindows && rt.Exec.IsRemote() {
		return "", fmt.Errorf("script_run 暂不支持远程 Windows 执行器")
	}
	scriptPath := strings.TrimSpace(path)
	cleanup := false
	if strings.TrimSpace(content) != "" {
		ext := ".py"
		if language == "shell" || language == "sh" {
			ext = ".sh"
		}
		scriptPath = fmt.Sprintf("/tmp/deepsentry_script_%d%s", time.Now().UnixNano(), ext)
		if !rt.Exec.IsRemote() {
			scriptPath = filepath.Join(os.TempDir(), filepath.Base(scriptPath))
		}
		if err := executor.WriteFileWithExecutor(rt.Exec, scriptPath, []byte(content)); err != nil {
			return "", err
		}
		cleanup = true
	}
	if scriptPath == "" {
		return "", fmt.Errorf("必须提供 content 或 path")
	}

	if cleanup {
		defer func() {
			if !rt.Exec.IsRemote() {
				_ = os.Remove(scriptPath)
			} else {
				_, _ = rt.Exec.Run("rm -f " + shellQuote(scriptPath))
			}
		}()
	}
	cmd, err := scriptRunCommand(isWindows, language, scriptPath, args)
	if err != nil {
		return "", err
	}
	start := time.Now()
	var out string
	if stoppable, ok := rt.Exec.(executor.StoppableStreamingExecutor); ok {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		defer cancel()
		out, err = stoppable.RunWithStreamingAndStop(cmd, nil, ctx.Done())
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("脚本执行超过 %d 秒，已中止", timeoutSec)
		}
	} else if isWindows {
		return "", fmt.Errorf("Windows 执行器不支持受控超时，未执行脚本")
	} else {
		// Legacy remote executors use the target's POSIX timeout utility.
		out, err = rt.Exec.Run(fmt.Sprintf("timeout %d sh -c %s", timeoutSec, shellQuote(cmd)))
	}
	elapsed := time.Since(start).Round(time.Millisecond)

	logPath := writeToolExecLog("script_run", fmt.Sprintf("language=%s path=%s args=%s timeout=%d", language, scriptPath, args, timeoutSec), out, err)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 受控脚本执行\n", rt.tag()))
	b.WriteString(fmt.Sprintf("language=%s path=%s elapsed=%v\n", language, scriptPath, elapsed))
	if logPath != "" {
		b.WriteString("执行日志: " + logPath + "\n")
	}
	if err != nil {
		b.WriteString("状态: 失败: " + err.Error() + "\n")
	} else {
		b.WriteString("状态: 完成\n")
	}
	b.WriteString("\n输出:\n" + truncate(out, 30000))
	return b.String(), err
}

func writeToolExecLog(tool, meta, output string, runErr error) string {
	if err := os.MkdirAll("reports", 0o700); err != nil {
		return ""
	}
	if err := os.Chmod("reports", 0o700); err != nil {
		return ""
	}
	meta = security.RedactSensitiveText(meta)
	output = security.RedactSensitiveText(output)
	path := filepath.Join("reports", fmt.Sprintf("tool_exec_%s_%d.log", tool, time.Now().UnixNano()))
	var b strings.Builder
	b.WriteString("tool: " + tool + "\n")
	b.WriteString("time: " + time.Now().Format(time.RFC3339) + "\n")
	b.WriteString("meta: " + meta + "\n")
	if runErr != nil {
		b.WriteString("error: " + security.RedactSensitiveText(runErr.Error()) + "\n")
	}
	b.WriteString("\noutput:\n")
	b.WriteString(output)
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		return ""
	}
	return path
}

// Resolve an interpreter before running; script failures must never trigger a replay.
func scriptRunCommand(windows bool, language, path, args string) (string, error) {
	parsedArgs, err := executor.SplitCommandArguments(args)
	if err != nil {
		return "", fmt.Errorf("script_run args 解析失败: %w", err)
	}
	if windows {
		quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
		names := "python,py,python3"
		missing := "Python interpreter not found"
		if language != "python" {
			names = "sh"
			missing = "POSIX sh not found; install Git Bash or use language=python"
		}
		for i := range parsedArgs {
			parsedArgs[i] = powerShellLiteral(parsedArgs[i])
		}
		return "powershell -NoProfile -NonInteractive -Command $ErrorActionPreference='Stop'; $p=Get-Command " + names + " -CommandType Application -ErrorAction SilentlyContinue | Where-Object { $_.Source -notlike '*\\Microsoft\\WindowsApps\\*' } | Select-Object -First 1; if (!$p) { throw '" + missing + "' }; & $p.Source " + quoted + " " + strings.Join(parsedArgs, " ") + "; exit $LASTEXITCODE", nil
	}
	for i := range parsedArgs {
		parsedArgs[i] = shellQuote(parsedArgs[i])
	}
	quotedArgs := strings.Join(parsedArgs, " ")
	if language == "python" {
		return "if command -v python3 >/dev/null 2>&1; then p=python3; elif command -v python >/dev/null 2>&1; then p=python; else echo 'Python interpreter not found' >&2; exit 127; fi; exec \"$p\" " + shellQuote(path) + " " + quotedArgs, nil
	}
	return "exec sh " + shellQuote(path) + " " + quotedArgs, nil
}
