package builtin

import (
	"ai-edr/internal/executor"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScriptRunWindowsCommand(t *testing.T) {
	cmd, err := scriptRunCommand(true, "python", `C:\Users\测试 用户\a'b.py`, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"timeout ", "||", "rm -f"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("unsafe Windows command: %s", cmd)
		}
	}
	if !strings.Contains(cmd, `C:\Users\测试 用户\a''b.py`) || !strings.Contains(cmd, "exit $LASTEXITCODE") {
		t.Fatal(cmd)
	}
}

func TestScriptRunArgumentsAreLiteralOnBothShells(t *testing.T) {
	args := `--output "C:\Users\测试 用户\out.txt" "; Write-Output injected"`
	windows, err := scriptRunCommand(true, "python", `C:\work\script.py`, args)
	if err != nil || !strings.Contains(windows, `'C:\Users\测试 用户\out.txt'`) || !strings.Contains(windows, `'; Write-Output injected'`) {
		t.Fatalf("Windows args not quoted: %q %v", windows, err)
	}
	posix, err := scriptRunCommand(false, "python", "/tmp/script.py", args)
	if err != nil || !strings.Contains(posix, `'C:\Users\测试 用户\out.txt'`) || !strings.Contains(posix, `'; Write-Output injected'`) {
		t.Fatalf("POSIX args not quoted: %q %v", posix, err)
	}
	if _, err := scriptRunCommand(true, "python", `C:\work\script.py`, `"unterminated`); err == nil {
		t.Fatal("unterminated args should be rejected")
	}
}

func TestScriptRunPassesLiteralArgumentsToShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell integration test")
	}
	out, err := ScriptRun(
		Runtime{Exec: &executor.LocalExecutor{}},
		"shell", "printf '<%s>\\n' \"$1\" \"$2\"", "",
		`"C:\Users\测试 用户\report.txt" "; echo INJECTED"`, 5,
	)
	if err != nil || !strings.Contains(out, "<C:\\Users\\测试 用户\\report.txt>\n<; echo INJECTED>") {
		t.Fatalf("script arguments changed: %q %v", out, err)
	}
}

func TestScriptRunFailureDoesNotReplay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX integration test")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "count")
	script := filepath.Join(dir, "script with spaces.sh")
	if err := os.WriteFile(script, []byte("echo run >> "+shellQuote(marker)+"\nexit 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ScriptRun(Runtime{Exec: &executor.LocalExecutor{}}, "shell", "", script, "", 5)
	if err == nil {
		t.Fatal("expected script failure")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "run\n" {
		t.Fatalf("script replayed or not run: %q %v", data, err)
	}
}

func TestScriptRunTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX integration test")
	}
	out, err := ScriptRun(Runtime{Exec: &executor.LocalExecutor{}}, "shell", "sleep 10", "", "", 1)
	if err == nil || !strings.Contains(out, "超过 1 秒") {
		t.Fatalf("timeout not enforced: %s %v", out, err)
	}
}

func TestPythonFailureDoesNotTrySecondInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX interpreter fixture")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "runs")
	for _, name := range []string{"python3", "python"} {
		body := "#!/bin/sh\necho " + name + " >> " + shellQuote(marker) + "\nexit 7\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := ScriptRun(Runtime{Exec: &executor.LocalExecutor{}}, "python", "print('ignored')", "", "", 5)
	if err == nil {
		t.Fatal("expected failure")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "python3\n" {
		t.Fatalf("interpreter fallback replayed script: %q %v", data, err)
	}
}
