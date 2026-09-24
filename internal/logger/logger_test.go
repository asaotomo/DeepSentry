package logger

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTitleFromHistory(t *testing.T) {
	history := []analyzer.Message{
		{Role: "user", Content: "需求：检查 SSH 登录"},
		{Role: "user", Content: "Output:\nroot 1234"},
		{Role: "user", Content: "【系统】本轮已结束。"},
		{Role: "system", Content: "ignored"},
	}
	got := TitleFromHistory(history)
	if got != "检查 SSH 登录报告" {
		t.Fatalf("title=%q", got)
	}
}

func TestReporterRedactsSecretsInToolAndCommandLogs(t *testing.T) {
	tmp := t.TempDir()
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	old := config.GlobalConfig
	config.GlobalConfig.ApiKey = "api-secret-value-123"
	config.GlobalConfig.Targets = []config.TargetConfig{{Password: "ssh-secret-value-456"}}
	defer func() { config.GlobalConfig = old }()

	reporter, path, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	reporter.Log("tool", "api_key: api-secret-value-123 password: ssh-secret-value-456")
	reporter.LogCommand("sshpass -p 'ssh-secret-value-456' ssh host", "Authorization: Bearer api-secret-value-123")
	reporter.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"api-secret-value-123", "ssh-secret-value-456"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("report leaked %q:\n%s", secret, raw)
		}
	}
}

func TestReporterSetTitle(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	reporter, path, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()

	if err := reporter.SetTitle("查看当前系统服务"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "\xEF\xBB\xBF# 查看当前系统服务报告\n") {
		t.Fatalf("unexpected report header: %q", string(data[:80]))
	}
}

func TestReporterUsesPrivateUniqueFiles(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	r1, p1, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer r1.Close()
	r2, p2, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if p1 == p2 {
		t.Fatalf("concurrent reporters collided at %s", p1)
	}
	for _, path := range []string{p1, p2} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("report %s mode=%o want 600", path, got)
		}
	}
}

func TestMarkdownFenceSurvivesEmbeddedCodeFence(t *testing.T) {
	if got := markdownFence("before ``` nested ```` block"); got != "`````" {
		t.Fatalf("fence=%q", got)
	}
}

func TestEvidenceJournalArchivesDialogueAndCompleteRedactedOutput(t *testing.T) {
	tmp := t.TempDir()
	oldwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	old := config.GlobalConfig
	config.GlobalConfig.ApiKey = "evidence-secret-123"
	t.Cleanup(func() { config.GlobalConfig = old })
	reporter, reportPath, err := NewReporterWithTitle("检查登录日志")
	if err != nil {
		t.Fatal(err)
	}
	history := []analyzer.Message{{Role: "user", Content: "检查登录日志"}, {Role: "user", Content: "检查登录日志"}, {Role: "user", Content: "Output: synthetic", Synthetic: true}}
	if err := reporter.RecordUserHistory(&history); err != nil {
		t.Fatal(err)
	}
	if err := reporter.RecordUserHistory(&history); err != nil {
		t.Fatal(err)
	}
	fullOutput := strings.Repeat("证据行\n", 500) + "api_key: evidence-secret-123"
	if _, err := reporter.RecordEvidence(EvidenceRecord{Kind: "action", Step: 2, Action: "execute", Status: "success", Input: "journalctl -n 100", Output: fullOutput, Risk: "low", Approval: "auto"}); err != nil {
		t.Fatal(err)
	}
	reporter.Close()
	count, finalHash, err := VerifyEvidenceJournal(reporter.EvidencePath())
	if err != nil || count != 3 || finalHash == "" {
		t.Fatalf("verify count=%d hash=%q err=%v", count, finalHash, err)
	}
	raw, err := os.ReadFile(reporter.EvidencePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "evidence-secret-123") || !strings.Contains(string(raw), strings.Repeat("证据行\\n", 500)) {
		t.Fatalf("archive lost output or leaked secret: %s", raw)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "E-000003") || !strings.Contains(string(report), finalHash) || strings.Contains(string(report), "evidence-secret-123") {
		t.Fatalf("report missing reference/anchor or leaked secret: %s", report)
	}
	if !strings.Contains(string(report), "风险与溯源") || !strings.Contains(string(report), "用户原话 2 条") {
		t.Fatalf("report missing risk summary: %s", report)
	}
}

func TestSessionOutcomeStaysDistinctFromFinalReport(t *testing.T) {
	tmp := t.TempDir()
	oldwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	reporter, reportPath, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.RecordSessionOutcome("cancelled", "cancelled_before_step", ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.LogFinal("这是结论"); err != nil {
		t.Fatal(err)
	}
	if err := reporter.RecordSessionOutcome("failed", "should_not_append", ""); err != nil {
		t.Fatal(err)
	}
	reporter.Close()
	raw, err := os.ReadFile(reporter.EvidencePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "session_outcome") != 1 || !strings.Contains(string(raw), "final_report") {
		t.Fatalf("outcome journal=%s", raw)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "不能把中断前的中间结果当成最终结论") || !strings.Contains(string(report), "## 最终结论") || strings.Contains(string(report), "should_not_append") {
		t.Fatalf("report=%s", report)
	}
}

func TestEvidenceJournalDetectsTampering(t *testing.T) {
	tmp := t.TempDir()
	oldwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	reporter, _, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reporter.RecordEvidence(EvidenceRecord{Kind: "action", Action: "read_file", Output: "safe evidence"}); err != nil {
		t.Fatal(err)
	}
	reporter.Close()
	path := reporter.EvidencePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), "safe evidence", "fake evidence", 1)
	if err := os.WriteFile(path, []byte(tampered), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyEvidenceJournal(path); err == nil {
		t.Fatal("modified evidence passed verification")
	}
}

func TestNilReporterSetTitle(t *testing.T) {
	var r *Reporter
	if err := r.SetTitle("title"); err != nil {
		t.Fatal(err)
	}
}
