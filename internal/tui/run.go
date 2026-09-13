package tui

import (
	"fmt"
	"os"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/harness"
	"ai-edr/internal/logger"
	"ai-edr/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
)

// SessionConfig TUI 会话参数
type SessionConfig struct {
	Agent            *harness.DeepAgent
	SysCtx           collector.SystemContext
	History          *[]analyzer.Message
	Reporter         *logger.Reporter
	ReportPath       string
	BatchMode        bool
	MaxSteps         int
	SubAgentMaxSteps int
	ConnInfo         string
	ModelInfo        string
	Startup          StartupInfo
	AwaitGoal        bool
	MultiTurn        bool // Claude Code 式多轮追问（默认开启）
	PlanMode         bool
	CompetitionMode  bool
}

// Run 启动全屏 Agent TUI（支持多轮 follow-up）
func Run(cfg SessionConfig) error {
	if err := prepareSessionConfig(&cfg); err != nil {
		return err
	}
	ctrl := newSessionController(cfg)
	defer ui.ResetTerminalState()
	defer cancelInputCursorAnchor()
	defer ctrl.Sink().Close()

	title := cfg.ModelInfo
	if title == "" {
		title = config.GlobalConfig.ModelName
	}
	status := cfg.ConnInfo
	if status == "" {
		status = "本地模式"
	}

	m := NewAgentModel(ctrl, title, status, cfg.MaxSteps, cfg.AwaitGoal, !cfg.AwaitGoal && len(*cfg.History) > 0, cfg.Startup)
	defer m.stopOwnedChat()
	defer m.cursorAnchor.release()
	if note := m.startConfiguredChatIfNeeded(); note != "" {
		m.startupInfo.Notices = append(m.startupInfo.Notices, note)
		m.appendLine("info", note, "chat autostart")
	}
	m.restoreConversationHistory(*cfg.History)
	m.refreshViewport()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithOutput(newInputCursorOutput(os.Stdout, m.cursorAnchor)))
	ctrl.SetProgram(p)
	defer startMacOSCmdVWatcher(p)()

	go ctrl.pumpEvents()

	_, err := p.Run()
	return err
}

func prepareSessionConfig(cfg *SessionConfig) error {
	if cfg == nil {
		return fmt.Errorf("TUI 会话配置不能为空")
	}
	if cfg.Agent == nil {
		return fmt.Errorf("TUI 会话缺少 Agent")
	}
	if cfg.History == nil {
		history := []analyzer.Message{}
		cfg.History = &history
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 30
	}
	return nil
}
