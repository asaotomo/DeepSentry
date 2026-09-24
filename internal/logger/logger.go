package logger

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/security"
	"ai-edr/internal/ui"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Reporter 负责生成审计报告
type Reporter struct {
	mu           sync.Mutex
	file         *os.File
	evidence     *os.File
	path         string
	evidencePath string
	title        string
	sequence     int
	previousHash string
	seenUserIDs  map[string]bool
	sawFinal     bool
	userTurns    int
	actions      int
	highRisk     int
	mediumRisk   int
	lowRisk      int
	denied       int
	blocked      int
	failures     int
}

// NewReporter 创建一个新的审计报告文件
func NewReporter() (*Reporter, string, error) {
	return NewReporterWithTitle(DefaultReportTitle)
}

const DefaultReportTitle = "DeepSentry 安全排查报告"

// NewReporterWithTitle 创建一个新的审计报告文件，并使用任务相关标题。
func NewReporterWithTitle(title string) (*Reporter, string, error) {
	fullPath := strings.TrimSpace(os.Getenv("DEEPSENTRY_REPORT_PATH"))
	autoPath := fullPath == ""
	if autoPath {
		fullPath = filepath.Join("reports", fmt.Sprintf("report_%s.md", time.Now().Format("20060102_150405")))
	}
	// #nosec G703 -- DEEPSENTRY_REPORT_PATH 只由本机操作员/受控父进程指定，设计上允许选择报告目录；目录和文件权限在此函数中收紧。
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		return nil, "", fmt.Errorf("无法创建日志目录: %v", err)
	}
	// #nosec G703 -- 与上述操作员指定的报告目录相同，此处只用于把其权限收紧为 0700。
	if err := os.Chmod(filepath.Dir(fullPath), 0o700); err != nil {
		return nil, "", fmt.Errorf("无法收紧日志目录权限: %v", err)
	}

	// 3. 创建文件。默认路径使用 O_EXCL，避免同一秒启动的多个实例
	// 互相截断报告；报告可能包含敏感取证信息，固定为 0600。
	// #nosec G703 -- 路径信任边界同上，createReportFile 固定使用 0600 且默认 O_EXCL。
	file, actualPath, err := createReportFile(fullPath, autoPath)
	if err != nil {
		return nil, "", fmt.Errorf("无法创建报告文件: %v", err)
	}
	fullPath = actualPath
	evidencePath := strings.TrimSuffix(fullPath, filepath.Ext(fullPath)) + ".evidence.jsonl"
	// The evidence journal is part of the report, not optional telemetry.
	// Never claim a report was created if its append-only archive is unavailable.
	// #nosec G703 -- Same operator-selected report path as above; the evidence suffix cannot introduce a new directory and permissions are fixed to 0600.
	evidenceFile, err := os.OpenFile(evidencePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		_ = file.Close()
		return nil, "", fmt.Errorf("无法创建证据档案: %w", err)
	}
	if err := evidenceFile.Chmod(0o600); err != nil {
		_ = evidenceFile.Close()
		_ = file.Close()
		return nil, "", fmt.Errorf("无法收紧证据档案权限: %w", err)
	}

	// 🟢 [核心修复] 写入 UTF-8 BOM (Byte Order Mark)
	// Windows 的记事本和部分编辑器在打开没有 BOM 的 UTF-8 文件时，
	// 可能会错误地将其识别为 GBK 编码，导致中文显示为乱码。
	// 写入这三个字节 (\xEF\xBB\xBF) 可以显式声明文件为 UTF-8 编码。
	if _, err := file.WriteString("\xEF\xBB\xBF"); err != nil {
		_ = evidenceFile.Close()
		_ = file.Close()
		return nil, "", fmt.Errorf("写入报告 BOM 失败: %v", err)
	}

	// 4. 写入报告头部信息
	title = NormalizeReportTitle(title)
	header := fmt.Sprintf("# %s\n\n"+
		"- **启动时间**: %s\n"+
		"- **操作员**: %s\n"+
		"- **工具版本**: v%s Ultimate\n"+
		"- **证据档案**: `%s`（脱敏后的逐步记录；SHA256 链校验）\n"+
		"- **证据边界**: 保存运行器实际返回的内容；上游截断、未采集和脱敏信息不能当作完整原始证据。\n\n"+
		"---\n\n",
		title,
		time.Now().Format("2006-01-02 15:04:05"),
		currentUser(),
		ui.Version,
		filepath.Base(evidencePath),
	)
	if _, err := file.WriteString(header); err != nil {
		_ = evidenceFile.Close()
		_ = file.Close()
		return nil, "", fmt.Errorf("写入报告头失败: %v", err)
	}

	return &Reporter{
		file: file, evidence: evidenceFile, path: fullPath,
		evidencePath: evidencePath, title: title,
		seenUserIDs: make(map[string]bool),
	}, fullPath, nil
}

func createReportFile(path string, avoidCollision bool) (*os.File, string, error) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if !avoidCollision {
		// #nosec G703 -- path 已由 NewReporterWithTitle 按本机操作员边界接受，不来自 LLM 或远程目标。
		file, err := os.OpenFile(path, flags, 0o600)
		if err == nil {
			_ = file.Chmod(0o600)
		}
		return file, path, err
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 0; i < 1000; i++ {
		candidate := path
		if i > 0 {
			candidate = fmt.Sprintf("%s_%d%s", base, i+1, ext)
		}
		// #nosec G703 -- candidate 仅在受信路径后追加数字后缀，不引入新的路径分量。
		file, err := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return file, candidate, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("同名报告过多: %s", path)
}

func currentUser() string {
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		return user
	}
	return strings.TrimSpace(os.Getenv("USERNAME"))
}

// TitleFromHistory 从当前会话里提取适合作为报告标题的任务名。
func TitleFromHistory(history []analyzer.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if !isReportTitleUserMessage(history[i]) {
			continue
		}
		if title := NormalizeReportTitle(history[i].Content); title != DefaultReportTitle {
			return title
		}
	}
	return DefaultReportTitle
}

func isReportTitleUserMessage(message analyzer.Message) bool {
	if message.Role != "user" {
		return false
	}
	content := strings.TrimSpace(message.Content)
	for _, prefix := range []string{"Output:", "系统警告:", "【系统】", "上一步执行失败:", "用户拒绝执行"} {
		if strings.HasPrefix(content, prefix) {
			return false
		}
	}
	return content != ""
}

// NormalizeReportTitle 将用户需求整理成简洁的 Markdown 报告标题。
func NormalizeReportTitle(raw string) string {
	s := strings.TrimSpace(security.RedactSensitiveText(raw))
	if s == "" {
		return DefaultReportTitle
	}
	replacers := []string{
		"【用户中途打断/改写目标】", "",
		"需求：", "", "需求:", "",
		"用户补充：", "", "用户补充:", "",
		"--task", "", "-q", "",
		"\"", "", "'", "", "`", "",
	}
	r := strings.NewReplacer(replacers...)
	s = r.Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, " ，。,.!！?？:：;；-_")
	if s == "" {
		return DefaultReportTitle
	}

	runes := []rune(s)
	const maxRunes = 36
	if len(runes) > maxRunes {
		s = string(runes[:maxRunes])
		s = strings.Trim(s, " ，。,.!！?？:：;；-_") + "..."
	}
	if !hasReportLikeSuffix(s) {
		if len([]rune(s)) <= 6 {
			s += "安全排查报告"
		} else {
			s += "报告"
		}
	}
	return s
}

func hasReportLikeSuffix(s string) bool {
	return strings.Contains(s, "报告") || strings.Contains(s, "排查") ||
		strings.Contains(s, "巡检") || strings.Contains(s, "审计") ||
		strings.Contains(s, "分析")
}

// SetTitle 更新报告第一行标题。TUI 首屏等待用户输入任务时，报告可在任务开始后再重命名。
func (r *Reporter) SetTitle(title string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil || r.path == "" {
		return nil
	}
	title = NormalizeReportTitle(title)
	if title == "" || title == r.title {
		return nil
	}
	if err := r.file.Sync(); err != nil {
		return err
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return err
	}
	const bom = "\xEF\xBB\xBF"
	content := string(data)
	prefix := ""
	if strings.HasPrefix(content, bom) {
		prefix = bom
		content = strings.TrimPrefix(content, bom)
	}
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) == 0 {
		return nil
	}
	if !strings.HasPrefix(lines[0], "# ") {
		return nil
	}
	rest := ""
	if len(lines) > 1 {
		rest = "\n" + lines[1]
	}
	updated := prefix + "# " + title + rest
	if err := r.file.Truncate(0); err != nil {
		return err
	}
	if _, err := r.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := r.file.WriteString(updated); err != nil {
		return err
	}
	if _, err := r.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	r.title = title
	return r.file.Sync()
}

// Log 记录常规思考和日志
func (r *Reporter) Log(title, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return
	}
	title = security.RedactSensitiveText(title)
	content = security.RedactSensitiveText(content)
	timestamp := time.Now().Format("15:04:05")
	// 使用 Markdown 格式记录
	entry := fmt.Sprintf("### [%s] %s\n%s\n\n", timestamp, title, content)

	if _, err := r.file.WriteString(entry); err == nil {
		// 强制刷入磁盘，防止程序意外崩溃导致日志未保存
		r.file.Sync()
	}
}

// LogCommand 专门记录命令执行
func (r *Reporter) LogCommand(cmd, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return
	}

	cmd = security.RedactSensitiveText(cmd)
	output = security.RedactSensitiveText(output)
	// 对超长输出进行截断，避免报告体积过大导致阅读困难
	if len(output) > 2000 {
		output = safeUTF8Prefix(output, 2000) + "\n... (输出过长已截断) ..."
	}

	// Fence must be longer than any backtick run in command/output, otherwise
	// shell heredocs or Markdown snippets can break the rest of the report.
	cmdFence := markdownFence(cmd)
	outFence := markdownFence(output)
	entry := fmt.Sprintf("%sbash\n> %s\n%s\n**执行结果**:\n%stext\n%s\n%s\n\n", cmdFence, cmd, cmdFence, outFence, output, outFence)

	if _, err := r.file.WriteString(entry); err == nil {
		r.file.Sync()
	}
}

func markdownFence(content string) string {
	longest := 0
	current := 0
	for _, r := range content {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return strings.Repeat("`", maxInt(3, longest+1))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func safeUTF8Prefix(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end]
}

// Close 关闭文件句柄
func (r *Reporter) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil && r.evidence != nil {
		_, _ = fmt.Fprintf(r.file, "\n---\n\n## 风险与溯源\n\n"+
			"- 用户原话 %d 条，执行或询问 %d 次。高风险 %d，中风险 %d，低风险 %d。拒绝 %d，守卫拦截 %d，失败或空结果 %d。\n"+
			"- 证据档案 `%s` 共 %d 条。末条 SHA256 `%s`。删除或改写任一条都会使后续链式校验失败。\n"+
			"- 报告只保留可读摘要。完整脱敏返回在证据档案；上游工具自己截断的内容、未执行的步骤和已脱敏字段都不能回推出原文。\n"+
			"- 公开转发前再人工核对高风险步骤、目标地址和截图。\n",
			r.userTurns, r.actions, r.highRisk, r.mediumRisk, r.lowRisk, r.denied, r.blocked, r.failures,
			filepath.Base(r.evidencePath), r.sequence, r.previousHash)
		_ = r.file.Sync()
	}
	if r.evidence != nil {
		_ = r.evidence.Sync()
		_ = r.evidence.Close()
		r.evidence = nil
	}
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
}
