package chat

import (
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/harness/subagent"
	"ai-edr/internal/skills"
	"ai-edr/internal/tools"
	"ai-edr/internal/ui"
)

// FormatDashboard returns a phone-friendly plain-text snapshot of the TUI welcome panel.
func FormatDashboard(allowExec bool) string {
	cfg := config.GlobalConfig
	host, _ := os.Hostname()
	username := "-"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = u.Username
	}
	wd, _ := os.Getwd()
	maxSteps := cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}

	capability := fmt.Sprintf("%d 工具 · %d 子 Agent", tools.CountEnabled(), subagent.Count())
	if cfg.UseNativeTools {
		capability += " · Native Tools"
	}
	if n := skillCount(cfg); n > 0 {
		capability += fmt.Sprintf(" · %d Skills", n)
	}

	lines := []string{
		fmt.Sprintf("DeepSentry %s", ui.Version),
		"深海哨兵 · AI 驱动的安全应急与智能运维 Agent",
		fmt.Sprintf("Build · %s · %s", firstNonEmpty(ui.BuildTime, "dev"), ui.StudioName),
		ui.StudioSiteLine,
		"",
		"环境 · " + fmt.Sprintf("%s/%s · %s", runtime.GOOS, runtime.GOARCH, username),
		"主机 · " + firstNonEmpty(host, "-"),
		"连接 · " + connectionSummary(cfg),
		"模型 · " + firstNonEmpty(cfg.ModelDisplayInfo(), "-"),
		"能力 · " + capability,
	}
	if mcp := mcpSummary(cfg); mcp != "" {
		lines = append(lines, "MCP · "+mcp)
	}
	lines = append(lines, "聊天 · "+StartupSummary(cfg.Chat))
	steps := fmt.Sprintf("最多 %d 步/任务", maxSteps)
	if allowExec {
		steps += " · 聊天执行已授权"
	} else {
		steps += " · 仅查询"
	}
	lines = append(lines,
		"步数 · "+steps,
		"时间 · "+time.Now().Format("2006-01-02 15:04:05"),
		"目录 · "+firstNonEmpty(wd, "-"),
		"",
		"就绪 · 私聊直接发任务，或用 /ds 指令",
	)
	return strings.Join(lines, "\n")
}

func (s *Service) helpText(c config.ChatChannel) string {
	allow := s.cfg.AllowBatch || c.AllowBatch
	cfg := config.GlobalConfig
	var b strings.Builder
	b.WriteString("DeepSentry 已连上，直接发消息就行。\n\n")
	b.WriteString("模型 · " + firstNonEmpty(cfg.ModelDisplayInfo(), "-") + "\n")
	b.WriteString("连接 · " + connectionSummary(cfg) + "\n")
	if allow {
		b.WriteString("同一私聊会自动续聊；最多 5 路并发，会话互不打断。\n")
	} else {
		b.WriteString("当前只能查询，扫码窗口勾选执行权限后可对话。\n")
	}
	b.WriteString("\n说「重启会话」新开对话\n")
	b.WriteString("说「切换会话」查看并恢复近期对话\n")
	b.WriteString("/ds new 或 /new 新开对话\n")
	b.WriteString("/stop 停止当前对话的执行和排队任务\n")
	b.WriteString("/ds status 看进度\n")
	b.WriteString("/steer 补充内容 在执行中追加指令\n")
	b.WriteString("/delivery 看投递状态，/retry 投递ID 重发失败部分\n")
	b.WriteString("可发送图片或语音；原始语音需要配置转写服务。\n")
	b.WriteString("/ds result [任务ID] 看结果（默认最近任务）")
	return b.String()
}

func connectionSummary(cfg config.Config) string {
	if n := len(cfg.Targets); n > 0 {
		return fmt.Sprintf("Fleet 多目标 · %d 台", n)
	}
	proto := strings.ToLower(strings.TrimSpace(cfg.TargetProtocol))
	switch proto {
	case "ssh":
		return "SSH -> " + firstNonEmpty(cfg.SSHHost, "-")
	case "telnet":
		return "Telnet -> " + firstNonEmpty(cfg.TelnetHost, "-")
	case "ftp":
		return "FTP -> " + firstNonEmpty(cfg.FTPHost, "-")
	case "local", "":
		if cfg.SSHHost != "" {
			return "SSH -> " + cfg.SSHHost
		}
		return "本地模式 · 本地执行模式"
	default:
		return proto
	}
}

func mcpSummary(cfg config.Config) string {
	names := make([]string, 0, len(cfg.MCPServerConfigs))
	for _, s := range cfg.MCPServerConfigs {
		if s.Disabled || strings.TrimSpace(s.Name) == "" {
			continue
		}
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("%d 个已配置 · %s", len(names), strings.Join(names, " · "))
}

func skillCount(cfg config.Config) int {
	if cfg.SkillsDisabled || len(cfg.SkillSources) == 0 {
		return 0
	}
	catalog, err := skills.LoadCatalog(cfg.SkillSources)
	if err != nil || catalog == nil {
		return 0
	}
	return len(catalog.Skills)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
