package tui

import (
	"strings"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/scheduler"

	tea "github.com/charmbracelet/bubbletea"
)

type scheduleInboxMsg struct {
	lines          []scheduler.ChatInboxLine
	offset         int64
	primed         bool
	scheduleStatus string
}

func (m AgentModel) schedulePollCmd(delay time.Duration) tea.Cmd {
	offset, primed := m.inboxOffset, m.inboxPrimed
	session := ""
	if m.ctrl != nil && m.ctrl.cfg.Agent != nil {
		session = m.ctrl.cfg.Agent.SessionID
	}
	store := config.GlobalConfig.SchedulerStore
	fire := func(time.Time) tea.Msg {
		lines, next, nextPrimed := scheduler.ReadChatInbox(store, offset, session, primed)
		return scheduleInboxMsg{lines: lines, offset: next, primed: nextPrimed, scheduleStatus: currentScheduleStatus(store)}
	}
	if delay <= 0 {
		return func() tea.Msg { return fire(time.Now()) }
	}
	return tea.Tick(delay, fire)
}

func currentScheduleStatus(store string) string {
	tasks, err := scheduler.NewStore(store).Load()
	if err != nil {
		return ""
	}
	return scheduler.StatusBarText(tasks, time.Now())
}

func (m *AgentModel) handleScheduleSlash(arg string) {
	store := scheduler.NewStore(config.GlobalConfig.SchedulerStore)
	out, err := scheduler.Command(store, strings.TrimSpace(arg), time.Now())
	if err != nil {
		m.appendLine("error", err.Error(), arg)
		return
	}
	m.appendLine("result", out, out)
	m.scheduleStatus = currentScheduleStatus(config.GlobalConfig.SchedulerStore)
}
