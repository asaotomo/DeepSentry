package inspection

import (
	"ai-edr/internal/config"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureConfig(t *testing.T) config.Config {
	t.Helper()
	var cfg config.Config
	max := 80.0
	cfg.Inspection = config.InspectionConfig{OutputDir: t.TempDir(), Devices: []config.InspectionDevice{
		{Name: "firewall", URL: "http://device.test", Checks: []config.InspectionCheck{{Name: "cpu", Selector: "#cpu", NumericRegex: `CPU: (\d+)`, MaxValue: &max, Screenshot: true}, {Name: "alarms", Selector: "#alarms", AlertRegex: "CRITICAL"}}},
		{Name: "switch", URL: "http://switch.test", Checks: []config.InspectionCheck{{Name: "status", Selector: "#status", ExpectRegex: "UP"}}},
	}}
	return cfg
}
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestPartialInspectionReportEvidenceAndBaseline(t *testing.T) {
	cfg := fixtureConfig(t)
	t.Setenv("INSPECT_SECRET_TEST", "never-print-password")
	cfg.Inspection.Devices[0].PasswordEnv = "INSPECT_SECRET_TEST"
	cfg.Inspection.Devices[0].UsernameEnv = "INSPECT_USER_TEST"
	cfg.Inspection.Devices[0].UsernameSelector = "#user"
	cfg.Inspection.Devices[0].PasswordSelector = "#password"
	cfg.Inspection.Devices[0].SubmitSelector = "#submit"
	cfg.Inspection.Devices[0].ReadySelector = "#ready"
	png := pngBytes(t)
	collect := func(_ context.Context, d config.InspectionDevice, c config.InspectionCheck) (string, []byte, error) {
		if d.Name == "switch" {
			return "", nil, errors.New("unreachable")
		}
		if c.Name == "cpu" {
			return "CPU: 91 never-print-password", png, nil
		}
		return "CRITICAL interface down", nil, nil
	}
	report, err := RunWithCollector(context.Background(), cfg, "all", collect)
	if err == nil {
		t.Fatal("partial failure reported as success")
	}
	if len(report.Results) != 3 {
		t.Fatal("failed device prevented other checks")
	}
	for _, path := range []string{report.Markdown, report.Word, report.Manifest} {
		if _, e := os.Stat(path); e != nil {
			t.Fatal(e)
		}
	}
	md, _ := os.ReadFile(report.Markdown)
	if bytes.Contains(md, []byte("never-print-password")) {
		t.Fatal("credential leaked")
	}
	for _, r := range report.Results {
		for _, ev := range r.Evidence {
			b, e := os.ReadFile(filepath.Join(filepath.Dir(report.Manifest), ev.Path))
			if e != nil || digest(b) != ev.SHA256 {
				t.Fatal("evidence mismatch")
			}
		}
	}
	z, e := zip.OpenReader(report.Word)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	imageFound := false
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		if strings.HasSuffix(f.Name, ".png") {
			imageFound = true
		}
		if strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".rels") {
			d := xml.NewDecoder(bytes.NewReader(b))
			for {
				_, e := d.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatalf("invalid OOXML %s: %v", f.Name, e)
				}
			}
		}
	}
	if !imageFound {
		t.Fatal("Word did not embed evidence screenshot")
	}
	second, _ := RunWithCollector(context.Background(), cfg, "firewall", collect)
	for _, r := range second.Results {
		if !r.BaselineAvailable || r.Changed {
			t.Fatal("baseline not reused")
		}
	}
	var manifest Report
	raw, _ := os.ReadFile(second.Manifest)
	if json.Unmarshal(raw, &manifest) != nil || len(manifest.Results) != 2 {
		t.Fatal("invalid manifest")
	}
	cfg.Inspection.Devices[0].Checks[0].ExpectRegex = "changed rule"
	third, _ := RunWithCollector(context.Background(), cfg, "firewall", collect)
	for _, r := range third.Results {
		if r.Check == "cpu" && r.BaselineAvailable {
			t.Fatal("changed check reused incompatible baseline")
		}
	}
}
func TestInspectionValidatesRulesAndDoesNotInventHealthyState(t *testing.T) {
	cfg := fixtureConfig(t)
	cfg.Inspection.Devices[0].Checks[0].NumericRegex = "["
	if Validate(cfg) == nil {
		t.Fatal("invalid regex accepted")
	}
	for _, cmd := range []string{"show status; reboot", "display cpu | reboot", "get $(id)", "reboot", "cat /etc/shadow"} {
		if readCommand(cmd) {
			t.Fatal("unsafe command", cmd)
		}
	}
	if status, _ := evaluate(config.InspectionCheck{}, "some output"); status != "observed" {
		t.Fatal("unclassified result marked healthy")
	}
	if status, _ := evaluate(config.InspectionCheck{ExpectRegex: "UP"}, ""); status != "failed" {
		t.Fatal("empty result marked healthy")
	}
}
func TestInspectionExclusiveLockAndUnknownTarget(t *testing.T) {
	cfg := fixtureConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.Inspection.OutputDir, ".run.lock"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := RunWithCollector(context.Background(), cfg, "all", func(context.Context, config.InspectionDevice, config.InspectionCheck) (string, []byte, error) {
		t.Fatal("collector called while locked")
		return "", nil, nil
	})
	if err == nil {
		t.Fatal("lock ignored")
	}
	cfg = fixtureConfig(t)
	if _, err := Run(context.Background(), cfg, "missing"); err == nil {
		t.Fatal("empty selection claimed success")
	}
}

func TestAgentEvidenceReportUsesActualScreenshot(t *testing.T) {
	cfg := fixtureConfig(t)
	folder := t.TempDir()
	imagePath := filepath.Join(folder, "hawkeye.png")
	if err := os.WriteFile(imagePath, pngBytes(t), 0600); err != nil {
		t.Fatal(err)
	}
	input := Report{Results: []Result{{Device: "设备 A", Check: "状态", Status: "pass", CollectedAt: time.Now(), Text: "Online", Evidence: []Evidence{{Path: imagePath}}}, {Device: "设备 B", Check: "状态", Status: "pass", CollectedAt: time.Now(), Text: "无证据"}}}
	raw, _ := json.Marshal(input)
	path := filepath.Join(folder, "manifest.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Compile(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Evidence[0].SHA256 == "" || filepath.IsAbs(report.Results[0].Evidence[0].Path) {
		t.Fatal("evidence was not copied and verified")
	}
	if report.Results[1].Status != "failed" {
		t.Fatal("unsubstantiated pass accepted")
	}
	if _, err := os.Stat(report.Word); err != nil {
		t.Fatal(err)
	}
	assertProfessionalWord(t, report.Word)
}

func assertProfessionalWord(t *testing.T, path string) {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	seen := map[string]bool{}
	var document string
	for _, f := range z.File {
		seen[f.Name] = true
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		if strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".rels") {
			d := xml.NewDecoder(bytes.NewReader(b))
			for {
				_, e := d.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatalf("invalid OOXML %s: %v", f.Name, e)
				}
			}
		}
		if f.Name == "word/document.xml" {
			document = string(b)
		}
	}
	for _, name := range []string{"word/styles.xml", "word/header1.xml", "word/footer1.xml", "word/numbering.xml", "word/fontTable.xml"} {
		if !seen[name] {
			t.Fatalf("Word missing %s", name)
		}
	}
	if !strings.Contains(document, "w:tbl") || !strings.Contains(document, "Heading1") || !strings.Contains(document, "nextPage") || !strings.Contains(document, "DEEPSENTRY") || !strings.Contains(document, `w:pStyle w:val="Title"`) || strings.Contains(document, "0001 年") {
		t.Fatalf("Word missing cover page or professional layout marks")
	}
}
func TestCompileMarkdownFromSessionAuditEmbedsScreenshot(t *testing.T) {
	cfg := fixtureConfig(t)
	folder := t.TempDir()
	imagePath := filepath.Join(folder, "shot.png")
	if err := os.WriteFile(imagePath, pngBytes(t), 0600); err != nil {
		t.Fatal(err)
	}
	md := "# DeepSentry 安全排查报告\n\n- **启动时间**: 2026-09-13 13:25:48\n\n---\n\n### [13:26:00] AI Thought\nIdea: 先登录再看告警\nAction: tool\n\n### [13:31:53] Final Report\n## 巡检结论\nHx0Toolbox 登录成功。\n\n### 五、风险与建议（按优先级）\n- 默认口令仍可用。\n- 证据：`" + imagePath + "`\n已保存到 " + imagePath + "\n\n### [13:42:00] Final Report\n守卫还锁着，写不了文件。请 brew install pandoc 或写 inspection-evidence.json。\n"
	src := filepath.Join(folder, "report_20260913_132548.md")
	if err := os.WriteFile(src, []byte(md), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := CompileMarkdown(cfg, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(report.Word); err != nil {
		t.Fatal(err)
	}
	extracted, err := os.ReadFile(report.Markdown)
	if err != nil || !strings.Contains(string(extracted), "总体结论") || !strings.Contains(string(extracted), "风险与建议") || strings.Contains(string(extracted), "Idea:") || strings.Contains(string(extracted), "## 截图证据") {
		t.Fatalf("extracted markdown invalid: %s err=%v", extracted, err)
	}
	if i, j := strings.Index(string(extracted), "总体结论"), strings.Index(string(extracted), "巡检详情"); i < 0 || (j >= 0 && i > j) {
		t.Fatal("conclusions should appear before details")
	}
	z, err := zip.OpenReader(report.Word)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	imageFound := false
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, ".png") {
			imageFound = true
		}
	}
	if !imageFound {
		t.Fatal("Word did not embed session-audit screenshot")
	}
	assertProfessionalWord(t, report.Word)
}

func TestOrganizeInspectionMarkdownPutsImagesWithChapters(t *testing.T) {
	login := filepath.Join(t.TempDir(), "login.png")
	overview := filepath.Join(t.TempDir(), "overview.png")
	md := "# 可以安全排查报告\n\n- **启动时间**: 2026-09-13 13:25:48\n\n## 巡检报告：Hx0Toolbox 安全运营平台（https://127.0.0.1:65000/）\n\n### 三、登录验证（成功）\n登录页滑块通过。\n\n### 四、巡检结果\n**总览**：资产 74。\n\n### 五、风险与建议（按优先级）\n1. 弱口令。\n\n### 六、说明与下一步\n本次只读巡检。\n\n## 截图证据\n- `" + login + "`\n- 总览页截图 `" + overview + "`\n"
	out := organizeInspectionMarkdown(md, []string{login, overview})
	if !strings.Contains(out, "一、总体结论") || !strings.Contains(out, "二、巡检详情") || !strings.Contains(out, "三、总结与建议") {
		t.Fatalf("missing required sections:\n%s", out)
	}
	if strings.Contains(out, "## 截图证据") {
		t.Fatal("screenshot dump chapter should be redistributed")
	}
	iRisk := strings.Index(out, "风险与建议")
	iLogin := strings.Index(out, "登录验证")
	iSummary := strings.Index(out, "说明与下一步")
	if iRisk < 0 || iLogin < 0 || iSummary < 0 || !(iRisk < iLogin && iLogin < iSummary) {
		t.Fatalf("section order wrong: risk=%d login=%d summary=%d\n%s", iRisk, iLogin, iSummary, out)
	}
	iResult := strings.Index(out, "巡检结果")
	if iResult < 0 || iResult < iLogin {
		t.Fatalf("巡检结果 missing or out of order:\n%s", out)
	}
	loginBlock := out[iLogin:iResult]
	if !strings.Contains(loginBlock, login) || strings.Contains(loginBlock, overview) {
		t.Fatalf("login chapter should keep the login shot only:\n%s", loginBlock)
	}
	overviewBlock := out[iResult:iSummary]
	if !strings.Contains(overviewBlock, overview) || strings.Contains(overviewBlock, login) {
		t.Fatalf("overview shot should sit with 巡检结果:\n%s", overviewBlock)
	}
}

func TestRewriteExistingAgentWordIfRequested(t *testing.T) {
	dir := filepath.Join("..", "..", "build", "reports", "inspections", "20260913-132700-agent-2164434771")
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || os.Getenv("REWRITE_WORD") == "" {
		t.Skip("no operator Word to refresh")
	}
	var report Report
	if json.Unmarshal(raw, &report) != nil {
		t.Fatal("invalid operator manifest")
	}
	report.Word = filepath.Join(dir, "report.docx")
	report.Markdown = filepath.Join(dir, "report.md")
	if err := writeReports(dir, report); err != nil {
		t.Fatal(err)
	}
	assertProfessionalWord(t, report.Word)
	src := filepath.Join("..", "..", "build", "reports", "report_20260913_132548.md")
	if _, err := os.Stat(src); err == nil {
		cfg := fixtureConfig(t)
		cfg.Inspection.OutputDir = filepath.Join("..", "..", "build", "reports", "inspections")
		mdReport, err := CompileMarkdown(cfg, src)
		if err != nil {
			t.Fatal(err)
		}
		assertProfessionalWord(t, mdReport.Word)
	}
}

func TestCompileExistingHx0SessionIfPresent(t *testing.T) {
	src := filepath.Join("..", "..", "build", "reports", "report_20260913_132548.md")
	if _, err := os.Stat(src); err != nil {
		t.Skip("no local Hx0 session report")
	}
	cfg := fixtureConfig(t)
	if out := strings.TrimSpace(os.Getenv("WRITE_LIVE_WORD")); out != "" {
		cfg.Inspection.OutputDir = out
	}
	report, err := CompileSource(cfg, src)
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := os.ReadFile(report.Markdown)
	if err != nil {
		t.Fatal(err)
	}
	text := string(extracted)
	if !strings.Contains(text, "Hx0Toolbox") || !strings.Contains(text, "总体结论") || !strings.Contains(text, "风险与建议") || strings.Contains(text, "pandoc") || strings.Contains(text, "守卫还锁着") || strings.Contains(text, "## 截图证据") {
		t.Fatalf("did not extract inspection conclusion: %s", text[:min(len(text), 800)])
	}
	z, err := zip.OpenReader(report.Word)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	images := 0
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, ".png") || strings.HasSuffix(f.Name, ".jpg") {
			images++
		}
	}
	if images == 0 {
		t.Fatal("live session Word missing screenshots")
	}
	if os.Getenv("WRITE_LIVE_WORD") != "" {
		t.Logf("Word=%s images=%d", report.Word, images)
	}
}

func TestResolveReportSourcePrefersInspectionSession(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("reports", 0700); err != nil {
		t.Fatal(err)
	}
	noise := "# 新会话\n\n### [13:42:00] AI Thought\nIdea: 去源码里找 manifest\nAction: grep\n" + strings.Repeat("考古 Word 生成流程\n", 80)
	if err := os.WriteFile("reports/report_20260913_134243.md", []byte(noise), 0600); err != nil {
		t.Fatal(err)
	}
	good := "# 巡检\n\n### [13:25:00] AI Thought\n## 结论\n风险与建议如下。\n已保存到 reports/mcp-artifacts/demo.png\n" + strings.Repeat("巡检项通过\n", 200)
	if err := os.WriteFile("reports/report_20260913_132548.md", []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes("reports/report_20260913_132548.md", older, older); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveReportSource("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "report_20260913_132548.md") {
		t.Fatalf("chose %s, want inspection session", got)
	}
}

func TestInventoryDoesNotRequireCSSOrExposePasswords(t *testing.T) {
	var cfg config.Config
	cfg.Inspection.Devices = []config.InspectionDevice{{Name: "web", URL: "https://device.test", PasswordEnv: "FIXTURE_SECRET"}}
	t.Setenv("FIXTURE_SECRET", "super-private")
	text, err := Inventory(cfg, "all")
	if err != nil || !strings.Contains(text, "HawkEye") || strings.Contains(text, "super-private") {
		t.Fatalf("inventory invalid: %v", err)
	}
}
