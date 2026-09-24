package harness

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestStdoutSinkShowsStreamSummaryWithoutRawJSON(t *testing.T) {
	out := captureStdout(func() {
		s := NewStdoutSink()
		s.Emit(UIEvent{Kind: EventThinking})
		s.Emit(UIEvent{Kind: EventStreamDelta, Detail: `{"thought":"hello`})
		s.Emit(UIEvent{Kind: EventStreamDelta, Detail: `{"thought":"hello world`})
		s.Emit(UIEvent{Kind: EventStreamEnd, Detail: `{"thought":"hello world"}`})
		s.Emit(UIEvent{Kind: EventThought, Message: "hello world"})
	})
	if strings.Contains(out, "思考: hello world") {
		t.Fatalf("stream deltas should not print incremental thought summaries:\n%s", out)
	}
	if strings.Contains(out, `{"thought"`) {
		t.Fatalf("raw JSON should not appear in stdout sink output:\n%s", out)
	}
	if !strings.Contains(out, "想法: hello world") {
		t.Fatalf("expected final thought line, got:\n%s", out)
	}
}

func TestQuietSinkKeepsFullResultDetail(t *testing.T) {
	full := "line1\nline2\nline3"
	out := captureStdout(func() {
		s := NewQuietSink(NewStdoutSink())
		s.Emit(UIEvent{Kind: EventThinking})
		s.Emit(UIEvent{Kind: EventResult, Message: "line1...", Detail: full})
	})
	if strings.Contains(out, "AI 正在思考") {
		t.Fatalf("quiet sink should suppress thinking output:\n%s", out)
	}
	if !strings.Contains(out, full) {
		t.Fatalf("quiet sink should keep full result detail, got:\n%s", out)
	}
}

func TestWebShellSinkKeepsExecutionLog(t *testing.T) {
	full := "uid=33(www-data)\ngid=33(www-data)"
	out := captureStdout(func() {
		s := NewWebShellSink(NewStdoutSink())
		s.Emit(UIEvent{Kind: EventThinking})
		s.Emit(UIEvent{Kind: EventStepStart, Step: 1, MaxSteps: 30})
		s.Emit(UIEvent{Kind: EventThought, Message: "inspect target"})
		s.Emit(UIEvent{Kind: EventAction, Action: &AgentAction{Type: ActionExecute, Command: "\033[36mid\033[0m"}})
		s.Emit(UIEvent{Kind: EventCommandOutput, Message: "\033[32muid=33(www-data)\033[0m\n"})
		s.Emit(UIEvent{Kind: EventCommandOutput, Message: "gid=33(www-data)\n"})
		s.Emit(UIEvent{Kind: EventResult, Message: "uid=33...", Detail: full})
	})
	if strings.Contains(out, "AI 正在思考") {
		t.Fatalf("webshell sink should suppress thinking output:\n%s", out)
	}
	for _, want := range []string{"Step 1 / 30", "想法: inspect target", "命令[控制端本机]: id", full} {
		if !strings.Contains(out, want) {
			t.Fatalf("webshell sink should keep %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033[") {
		t.Fatalf("webshell sink must not emit ANSI control sequences:\n%q", out)
	}
}

func TestQuietSinkSuppressesCommandOutputStream(t *testing.T) {
	out := captureStdout(func() {
		s := NewQuietSink(NewStdoutSink())
		s.Emit(UIEvent{Kind: EventCommandOutput, Message: "live line\n"})
		s.Emit(UIEvent{Kind: EventResult, Message: "done"})
	})
	if strings.Contains(out, "live line") {
		t.Fatalf("quiet sink should suppress live command output, got:\n%s", out)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("quiet sink should keep final result, got:\n%s", out)
	}
}

func TestFinalReplySinkOnlyPrintsConclusion(t *testing.T) {
	out := captureStdout(func() {
		s := NewFinalReplySink()
		s.Emit(UIEvent{Kind: EventStepStart, Step: 1, MaxSteps: 30})
		s.Emit(UIEvent{Kind: EventThought, Message: "检查磁盘"})
		s.Emit(UIEvent{Kind: EventAction, Action: &AgentAction{Type: ActionExecute, Command: "df -h"}})
		s.Emit(UIEvent{Kind: EventBatchAuto})
		s.Emit(UIEvent{Kind: EventResult, Message: "命令执行完成", Detail: "/dev/disk3s1 460Gi"})
		s.Emit(UIEvent{Kind: EventFinish, Message: "磁盘使用率正常，根分区约 35%。"})
	})
	for _, ban := range []string{"想法", "命令", "df -h", "Batch", "Step", "结果", "最终报告", "==="} {
		if strings.Contains(out, ban) {
			t.Fatalf("final reply leaked %q:\n%s", ban, out)
		}
	}
	if !strings.Contains(out, "磁盘使用率正常") {
		t.Fatalf("missing conclusion:\n%s", out)
	}
}
