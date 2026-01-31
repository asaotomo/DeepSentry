package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Reporter 负责生成审计报告
type Reporter struct {
	file *os.File
	path string
}

// NewReporter 创建一个新的审计报告文件
func NewReporter() (*Reporter, string, error) {
	// 1. 确保 reports 目录存在
	reportDir := "reports"
	if _, err := os.Stat(reportDir); os.IsNotExist(err) {
		if err := os.MkdirAll(reportDir, 0755); err != nil {
			return nil, "", fmt.Errorf("无法创建日志目录: %v", err)
		}
	}

	// 2. 生成文件名 (按时间戳)
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("report_%s.md", timestamp)
	fullPath := filepath.Join(reportDir, filename)

	// 3. 创建文件
	file, err := os.Create(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("无法创建报告文件: %v", err)
	}

	// 🟢 [核心修复] 写入 UTF-8 BOM (Byte Order Mark)
	// Windows 的记事本和部分编辑器在打开没有 BOM 的 UTF-8 文件时，
	// 可能会错误地将其识别为 GBK 编码，导致中文显示为乱码。
	// 写入这三个字节 (\xEF\xBB\xBF) 可以显式声明文件为 UTF-8 编码。
	file.WriteString("\xEF\xBB\xBF")

	// 4. 写入报告头部信息
	header := fmt.Sprintf("# DeepSentry 安全排查报告\n\n"+
		"- **启动时间**: %s\n"+
		"- **操作员**: %s\n"+
		"- **工具版本**: v1.0 Ultimate\n\n"+
		"---\n\n",
		time.Now().Format("2006-01-27 15:04:05"),
		os.Getenv("USER"), // 获取当前用户名，Windows下通常也能获取到
	)
	file.WriteString(header)

	return &Reporter{
		file: file,
		path: fullPath,
	}, fullPath, nil
}

// Log 记录常规思考和日志
func (r *Reporter) Log(title, content string) {
	if r.file == nil {
		return
	}
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
	if r.file == nil {
		return
	}

	// 对超长输出进行截断，避免报告体积过大导致阅读困难
	if len(output) > 2000 {
		output = output[:2000] + "\n... (输出过长已截断) ..."
	}

	// 格式化为代码块
	entry := fmt.Sprintf("```bash\n> %s\n```\n**执行结果**:\n```text\n%s\n```\n\n", cmd, output)

	if _, err := r.file.WriteString(entry); err == nil {
		r.file.Sync()
	}
}

// Close 关闭文件句柄
func (r *Reporter) Close() {
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
}
