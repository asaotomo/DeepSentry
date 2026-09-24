package executor

import (
	"ai-edr/internal/config"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateOutputPreservesUTF8(t *testing.T) {
	got := truncateOutput(strings.Repeat("你", 10), 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "你") {
		t.Fatalf("truncated output lost readable prefix: %q", got)
	}
}

func TestLocalExecutorStopTerminatesShellProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep to verify process-group cancellation")
	}
	stop := make(chan struct{})
	time.AfterFunc(100*time.Millisecond, func() { close(stop) })
	start := time.Now()
	out, err := (&LocalExecutor{}).RunWithStreamingAndStop("printf 'started\\n'; sleep 10; printf 'finished\\n'", nil, stop)
	if err == nil || !strings.Contains(err.Error(), "中断") {
		t.Fatalf("stopped command err=%v output=%q", err, out)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("stop took too long: %s", time.Since(start))
	}
	if !strings.Contains(out, "started") || strings.Contains(out, "finished") {
		t.Fatalf("unexpected stopped output: %q", out)
	}
}

func TestOutputCollectorTruncatesAndContinues(t *testing.T) {
	c := newOutputCollector(10)
	for _, line := range []string{"abcdefghij\n", "klmnopqrst\n"} {
		c.appendLine(line)
	}
	if !c.truncated {
		t.Fatal("expected truncation")
	}
	got := c.result()
	if !strings.Contains(got, "输出已截断") {
		t.Fatalf("expected truncation notice, got %q", got)
	}
	if len(strings.TrimSpace(strings.Split(got, "\n")[0])) > 20 {
		t.Fatalf("stored output too long: %q", got)
	}
}

func TestTruncateOutput(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := truncateOutput(s, 50)
	if !strings.Contains(got, "输出已截断") {
		t.Fatalf("expected truncation notice")
	}
	if len(got) < 50 {
		t.Fatalf("truncated output too short")
	}
}

func TestLocalExecutorRunWithStreamingEmitsLines(t *testing.T) {
	for i := 0; i < 50; i++ {
		var lines []string
		out, err := (&LocalExecutor{}).RunWithStreaming("printf 'one\\n'; printf 'two\\n'", func(line string) {
			lines = append(lines, line)
		})
		if err != nil {
			t.Fatalf("iteration %d: RunWithStreaming failed: %v", i, err)
		}
		if !strings.Contains(out, "one") || !strings.Contains(out, "two") {
			t.Fatalf("iteration %d: expected collected output to include both lines, got %q", i, out)
		}
		if len(lines) != 2 || lines[0] != "one\n" || lines[1] != "two\n" {
			t.Fatalf("iteration %d: expected two streamed lines, got %#v", i, lines)
		}
	}
}

func TestLocalExecutorReturnsAfterForegroundShellWhenBackgroundServiceKeepsPipeOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX background process semantics")
	}
	start := time.Now()
	out, err := (&LocalExecutor{}).RunWithStreaming(`sleep 10 & child=$!; printf 'READY %s\n' "$child"`, nil)
	if err != nil {
		t.Fatalf("background service launch failed: %v (output=%q)", err, out)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("executor waited for background service pipe EOF: %s", elapsed)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != "READY" {
		t.Fatalf("unexpected launch output: %q", out)
	}
	pid, parseErr := strconv.Atoi(fields[1])
	if parseErr != nil || pid <= 0 {
		t.Fatalf("invalid background pid in %q", out)
	}
	process, findErr := os.FindProcess(pid)
	if findErr == nil {
		_ = process.Kill()
	}
}

func TestLocalExecutorBlocksRawSSHLikeCommands(t *testing.T) {
	origTargets := config.GlobalConfig.Targets
	config.GlobalConfig.Targets = []config.TargetConfig{{
		Name:     "target-01",
		Protocol: "ssh",
		Host:     "192.0.2.49:2222",
		User:     "root",
		Password: "secret",
		Tags:     []string{"toolbox"},
	}}
	t.Cleanup(func() { config.GlobalConfig.Targets = origTargets })

	out, err := (&LocalExecutor{}).RunWithStreaming(`ssh -p 2222 root@192.0.2.49 "echo ok"`, nil)
	if err != nil {
		t.Fatalf("raw ssh should return guidance, not hard fail: %v", err)
	}
	for _, want := range []string{"已拦截控制端裸 ssh 命令", "不会读取 DeepSentry config.yaml", "fleet_exec", "target-01"} {
		if !strings.Contains(out, want) {
			t.Fatalf("raw ssh guidance missing %q:\n%s", want, out)
		}
	}
}

func TestLocalExecutorBlocksRawSCPCommand(t *testing.T) {
	out, err := (&LocalExecutor{}).RunWithStreaming(`scp root@10.0.0.1:/tmp/a ~/a`, nil)
	if err != nil {
		t.Fatalf("raw scp should return guidance, not hard fail: %v", err)
	}
	for _, want := range []string{"已拦截控制端裸 scp 命令", "fleet_file", "原始命令未执行"} {
		if !strings.Contains(out, want) {
			t.Fatalf("raw scp guidance missing %q:\n%s", want, out)
		}
	}
}

func TestRawSSHLikeCommandBlocksOnlyRemoteConnections(t *testing.T) {
	blockedCases := []string{
		`ssh -p 2222 root@192.0.2.49 "echo ok"`,
		`timeout 5 ssh root@192.0.2.49 true`,
		`env SSHPASS=x sshpass -e ssh root@192.0.2.49 true`,
		`scp ./flag.zip root@192.0.2.49:/tmp/flag.zip`,
		`scp -- ./flag.zip root@192.0.2.49:/tmp/flag.zip`,
		`sftp root@192.0.2.49`,
		`ssh root@192.0.2.49 echo -G`,
	}
	for _, cmd := range blockedCases {
		if _, blocked := blockRawSSHLikeCommand(cmd); !blocked {
			t.Fatalf("expected raw remote command to be blocked: %s", cmd)
		}
	}

	allowedCases := []string{
		`ssh -V`,
		`ssh -G root@192.0.2.49`,
		`scp -h`,
		`sftp -h`,
		`sftp -P 2222`,
	}
	for _, cmd := range allowedCases {
		_, blocked := blockRawSSHLikeCommand(cmd)
		if blocked {
			t.Fatalf("expected local/help command to be allowed: %s", cmd)
		}
	}
}

func TestParsePowerShellCommandKeepsRequestedShell(t *testing.T) {
	shell, script := parsePowerShellCommand(`pwsh -c "Write-Output ok"`)
	if shell != "pwsh" || script != "Write-Output ok" {
		t.Fatalf("expected pwsh command, got shell=%q script=%q", shell, script)
	}

	shell, script = parsePowerShellCommand(`powershell -Command "Get-Date"`)
	if shell != "powershell" || script != "Get-Date" {
		t.Fatalf("expected powershell command, got shell=%q script=%q", shell, script)
	}
	shell, script = parsePowerShellCommand(`powershell -NoProfile -Command "$p=@('C:\Program Files (x86)\Tencent')"`)
	if shell != "powershell" || script != `$p=@('C:\Program Files (x86)\Tencent')` {
		t.Fatalf("trailing script quote was stripped: %q", script)
	}
	for _, test := range []struct{ command, shell, script string }{
		{`powershell.exe -Command "Write-Output 'C:\Temp\a b'"`, "powershell", `Write-Output 'C:\Temp\a b'`},
		{`pwsh.exe -c "Get-Item 'C:\Temp'"`, "pwsh", `Get-Item 'C:\Temp'`},
	} {
		gotShell, gotScript := parsePowerShellCommand(test.command)
		if gotShell != test.shell || gotScript != test.script {
			t.Fatalf("parsePowerShellCommand(%q)=(%q,%q)", test.command, gotShell, gotScript)
		}
	}
	if _, _, ok := splitPowerShellLauncher(`powershell-extra -Command whoami`); ok {
		t.Fatal("non-PowerShell executable misclassified")
	}
}

func TestParseTransferCommandKeepsQuotedPaths(t *testing.T) {
	action, src, dst, ok := parseTransferCommand(`upload '/tmp/a b/source.txt' "/tmp/c d/dest.txt"`)
	if !ok {
		t.Fatal("expected command to parse")
	}
	if action != "upload" || src != "/tmp/a b/source.txt" || dst != "/tmp/c d/dest.txt" {
		t.Fatalf("unexpected parse: action=%q src=%q dst=%q", action, src, dst)
	}

	_, _, _, ok = parseTransferCommand(`download '/tmp/open`)
	if ok {
		t.Fatal("expected unterminated quote to fail")
	}
}

func TestParseTransferCommandKeepsWindowsBackslashes(t *testing.T) {
	for _, test := range []struct {
		command string
		src     string
		dst     string
	}{
		{`upload "C:\Users\测试 用户\report.txt" "D:\Backups\report.txt"`, `C:\Users\测试 用户\report.txt`, `D:\Backups\report.txt`},
		{`download \\server\share\report.txt "C:\Temp\"`, `\\server\share\report.txt`, `C:\Temp\`},
		{`upload ".\reports\" "D:\Backups\"`, `.\reports\`, `D:\Backups\`},
		{`upload 'C:\Users\O'\''Brien\report & final.txt' "D:\Backups\report.txt"`, `C:\Users\O'Brien\report & final.txt`, `D:\Backups\report.txt`},
	} {
		_, src, dst, ok := parseTransferCommand(test.command)
		if !ok || src != test.src || dst != test.dst {
			t.Fatalf("parseTransferCommand(%q)=(%q,%q,%t)", test.command, src, dst, ok)
		}
	}
}

func TestLocalExecutorPreservesEmbeddedLocalRunText(t *testing.T) {
	out, err := (&LocalExecutor{}).Run(`echo "literal local_run text"`)
	if err != nil || !strings.Contains(out, "literal local_run text") {
		t.Fatalf("embedded local_run changed command: %q %v", out, err)
	}
}

func TestExpandLocalPathExpandsHomePrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := expandLocalPath("~/Downloads/test_flag.zip")
	want := filepath.Join(home, "Downloads", "test_flag.zip")
	if got != want {
		t.Fatalf("expanded path=%q want %q", got, want)
	}
	if got := expandLocalPath("~literal/file"); got != "~literal/file" {
		t.Fatalf("non-home tilde should not expand, got %q", got)
	}
}

func TestCopyLocalFileExpandsTildeDestination(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(src, []byte("flag-data"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := copyLocalFile(src, "~/Downloads/test_flag.txt")
	if err != nil {
		t.Fatalf("copyLocalFile failed: %v", err)
	}
	want := filepath.Join(home, "Downloads", "test_flag.txt")
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("expected expanded destination to exist: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("downloaded evidence mode=%o want 600", got)
	}
	if strings.Contains(out, "~/Downloads") {
		t.Fatalf("result should report expanded destination, got %q", out)
	}
}
