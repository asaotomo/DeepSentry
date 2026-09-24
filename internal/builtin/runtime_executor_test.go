package builtin

import (
	"ai-edr/internal/executor"
	"strings"
	"testing"
)

type recordingExecutor struct {
	remote   bool
	commands []string
}

func (r *recordingExecutor) Run(command string) (string, error) {
	r.commands = append(r.commands, command)
	return "ok", nil
}
func (r *recordingExecutor) ReadTargetFile(string) ([]byte, error)  { return nil, nil }
func (r *recordingExecutor) ListTargetDir(string) ([]string, error) { return nil, nil }
func (r *recordingExecutor) IsRemote() bool                         { return r.remote }
func (r *recordingExecutor) Close()                                 {}

func TestWorkflowToolsUseInjectedRuntimeExecutor(t *testing.T) {
	t.Chdir(t.TempDir())
	global := &recordingExecutor{remote: true}
	injected := &recordingExecutor{remote: true}
	old := executor.Current
	executor.Current = global
	t.Cleanup(func() { executor.Current = old })
	rt := WithExecutor(NewRuntime("linux", true), injected)

	if _, err := FileDownload(rt, "/remote/a", "/local/a", 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := FileUpload(rt, "/local/b", "/remote/b", 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchivePack(rt, "tar.gz", "/remote/src", "/remote/a.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if _, err := ScriptRun(rt, "shell", "", "/remote/check.sh", "--mode inspect", 30); err != nil {
		t.Fatal(err)
	}
	if len(global.commands) != 0 {
		t.Fatalf("workflow tool used global executor: %#v", global.commands)
	}
	joined := strings.Join(injected.commands, "\n")
	for _, want := range []string{"download ", "upload ", "tar ", "/remote/check.sh"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("injected executor did not receive %q: %s", want, joined)
		}
	}
}

func TestRemoteArchiveExtractFailsClosedBeforeRunningCommand(t *testing.T) {
	injected := &recordingExecutor{remote: true}
	rt := WithExecutor(NewRuntime("linux", true), injected)
	_, err := ArchiveExtract(rt, "zip", "/remote/untrusted.zip", "/remote/dest")
	if err == nil || !strings.Contains(err.Error(), "远程直接解压已禁用") {
		t.Fatalf("expected safe remote extraction rejection, got %v", err)
	}
	if len(injected.commands) != 0 {
		t.Fatalf("remote archive command unexpectedly ran: %#v", injected.commands)
	}
}
