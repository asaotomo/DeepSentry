package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteReports 写入 JSON + Markdown 报告
func WriteReports(r *SuiteReport, dir string) (jsonPath, mdPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", err
	}
	ts := r.Timestamp.Format("20060102_150405")
	jsonPath = filepath.Join(dir, fmt.Sprintf("benchmark_%s.json", ts))
	mdPath = filepath.Join(dir, fmt.Sprintf("benchmark_%s.md", ts))

	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(r)), 0o600); err != nil {
		return "", "", err
	}
	return jsonPath, mdPath, nil
}

func renderMarkdown(r *SuiteReport) string {
	var b strings.Builder
	b.WriteString("# DeepSentry Agent Benchmark Report\n\n")
	b.WriteString(fmt.Sprintf("- **时间**: %s\n", r.Timestamp.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- **Provider**: %s\n", r.Provider))
	b.WriteString(fmt.Sprintf("- **Model**: %s\n", r.Model))
	b.WriteString(fmt.Sprintf("- **远程模式**: %v (%s)\n", r.RemoteMode, r.SSHHost))
	b.WriteString(fmt.Sprintf("- **总耗时**: %v\n", r.Duration.Round(time.Millisecond)))
	b.WriteString(fmt.Sprintf("- **综合得分**: **%.1f / 100** — %s\n\n", r.OverallScore, r.Grade))

	b.WriteString("## 维度评分\n\n")
	b.WriteString("| 维度 | 权重 | 得分 | 评级 |\n|------|------|------|------|\n")
	order := []Category{CatLLM, CatLocalTool, CatRemoteTool, CatLinkage, CatFilesystem, CatForensics, CatIncident, CatAgent, CatHarness, CatResilience, CatSecurity}
	for _, cat := range order {
		meta := CategoryMeta[cat]
		avg := r.CategoryAvg[cat]
		b.WriteString(fmt.Sprintf("| %s | %.0f%% | %.1f | %s |\n", meta.DisplayName, meta.Weight, avg, scoreToGrade(avg)))
	}

	b.WriteString("\n## 场景明细\n\n")
	b.WriteString("| ID | 维度 | 场景 | 结果 | 得分 | 耗时 | 说明 |\n")
	b.WriteString("|----|------|------|------|------|------|------|\n")
	for _, s := range r.Scenarios {
		status := "❌"
		if strings.HasPrefix(s.Message, "跳过") {
			status = "⏭"
		} else if s.Passed {
			status = "✅"
		} else if s.Partial {
			status = "⚠️"
		}
		lat := "-"
		if s.Latency > 0 {
			lat = s.Latency.Round(time.Millisecond).String()
		}
		meta := CategoryMeta[s.Category]
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %.0f | %s | %s |\n",
			s.ID, meta.DisplayName, s.Name, status, s.Score, lat, escapeMD(s.Message)))
	}

	b.WriteString("\n## 指标说明\n\n")
	b.WriteString("- **LLM 接入**: 连通性、JSON 协议、tool calling\n")
	b.WriteString("- **控制端工具**: ping/dns 等从 Controller 发起\n")
	b.WriteString("- **目标机 BusyBox**: /proc SFTP 直读，不依赖目标 CLI\n")
	b.WriteString("- **本地-远程联动**: 同会话双视角 + local_run + http_probe\n")
	b.WriteString("- **Agent 编排**: Harness 多步调度、skill/todo/subagent 注入\n")
	b.WriteString("- **取证分析**: file_ident/hash/strings/read_log/flow_snapshot\n")
	b.WriteString("- **安全应急**: 内存/网络/暴露面/Skill 导向排查链\n")
	b.WriteString("- **安全边界**: 受保护路径拦截、视角隔离\n")
	b.WriteString("- **高可用**: 重试/超时/URL 规范化配置\n")
	return b.String()
}

func escapeMD(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}
