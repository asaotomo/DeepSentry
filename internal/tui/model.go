package tui

import (
	"ai-edr/internal/analyzer"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ai-edr/internal/chat"
	"ai-edr/internal/config"
	"ai-edr/internal/harness"
	"ai-edr/internal/mcp"
	"ai-edr/internal/memory"
	"ai-edr/internal/skills"
	"ai-edr/internal/ui"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/viper"
)

type chatSetupResultMsg struct{ err error }

type logLine struct {
	kind      string
	content   string
	raw       string
	step      int
	complete  bool
	settled   bool
	collapsed bool
	group     int
	groupHead bool
	id        int
	at        time.Time
}

type confirmState struct {
	action       *harness.AgentAction
	prompt       string
	respCh       chan approvalDecision
	restoreInput bool
}

type approvalDecision uint8

const (
	approvalDeny approvalDecision = iota
	approvalAllowOnce
	approvalAllowSession
	approvalAllowAllSession
)

type askState struct {
	action  *harness.AgentAction
	prompt  string
	options []string
	respCh  chan string
}

type slashCommand struct {
	Name        string
	Description string
}

type tokenStats struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Calls            int
}

// bubbles/textinput is single-line and rewrites newlines to spaces. A private
// one-rune sentinel preserves real multiline drafts and stable cursor indexes.
const inputLineBreak = '\uE000'

// Claude Code collapses a paste only when it would crowd the prompt:
// more than 2 lines or more than 800 characters. A 2-line, 16-character
// mask (often copied with a trailing newline) stays visible inline.
const (
	largePasteMaxLines = 2
	largePasteMaxRunes = 800
)

var slashCommands = []slashCommand{
	{Name: "help", Description: "显示快捷键和斜杠命令"},
	{Name: "new", Description: "开启全新任务/会话；可直接 /new 任务"},
	{Name: "restart", Description: "等同 /new，重新开始一个会话"},
	{Name: "clear", Description: "清空当前屏幕日志"},
	{Name: "status", Description: "显示运行状态、步骤和连接"},
	{Name: "cost", Description: "显示会话轮次、消息数和估算 token"},
	{Name: "model", Description: "显示当前模型"},
	{Name: "image", Description: "附加图片：/image [路径]；省略路径读取剪贴板"},
	{Name: "compact", Description: "折叠长输出并整理上下文"},
	{Name: "memory", Description: "Memory 管理：/memory list|clues|clear [all|target|global]"},
	{Name: "agents", Description: "AGENTS.md 管理：/agents status|clear"},
	{Name: "sessions", Description: "列出可恢复 checkpoint"},
	{Name: "resume", Description: "恢复 checkpoint：/resume <session_id> [补充说明]"},
	{Name: "tsecbench", Description: "进入 TSecBench 跑分模式；可追加题目或目标说明"},
	{Name: "config", Description: "显示连接与模型配置"},
	{Name: "sudo", Description: "由系统安全验证/刷新本机 sudo 授权（密码不进入程序）"},
	{Name: "connect", Description: "连接通讯工具：扫码绑定飞书/QQ/微信/企微，钉钉可手动配置；Ctrl+C 返回终端并保持后台聊天"},
	{Name: "mcp", Description: "MCP 管理：/mcp status|reconnect|add|import|resources|prompts"},
	{Name: "skill", Description: "Skill 管理：list 查看；on/off [name] 启停；only <name> 仅启用一个"},
	{Name: "exit", Description: "退出 TUI"},
	{Name: "quit", Description: "退出 TUI"},
}

type agentDoneMsg struct{}
type confirmMsg confirmState
type askMsg askState
type sudoAuthMsg struct{ respCh chan bool }
type sudoAuthResultMsg struct {
	respCh chan bool
	err    error
}
type sudoTerminalRestoredMsg struct {
	respCh chan bool
	ok     bool
}
type uiEventMsg harness.UIEvent
type userMsgEvent struct{ text string }
type agentStartMsg struct{ followUp bool }
type streamRefreshMsg struct{}
type commandOutputRefreshMsg struct{}
type streamCollapseMsg struct{ id int }
type cmdOutputCollapseMsg struct{ group int }
type copyToastMsg struct {
	chars int
	err   string
}
type copyToastClearMsg struct{}
type skillMarketResultMsg struct {
	action string
	out    string
	err    error
}
type mcpResultMsg struct {
	action string
	out    string
	err    error
}

type inputDraftPart struct {
	text   string
	pasted bool
}

// AgentModel 主 Agent TUI（多轮对话 + 子 Agent 面板）
type AgentModel struct {
	ctrl *SessionController

	width, height    int
	viewport         viewport.Model
	spinner          spinner.Model
	input            textinput.Model
	inputAllSelected bool

	title, statusLine string
	currentTarget     string
	maxSteps          int

	lines          []logLine
	lineID         int
	streamIdx      int // 当前流式行索引，-1 表示无
	cmdOutputGroup int
	activeCmdGroup int
	streamTick     bool
	commandTick    bool
	running        bool
	thinking       bool
	currentStep    int
	done           bool
	awaitGoal      bool
	sessionLive    bool
	autoStart      bool // 带 history 启动时由 Init 触发首轮
	autoScroll     bool
	stopping       bool
	inputHistory   []string
	historyIdx     int
	draftParts     []inputDraftPart
	draftImages    []analyzer.ImageAttachment
	slashSelected  int
	cursorAnchor   *inputCursorAnchorState
	footerVersion  uint64
	frameVersion   uint64

	pendingConfirm *confirmState
	pendingAsk     *askState
	quitting       bool
	chatOwner      *chat.Owner
	startupInfo    StartupInfo
	bannerCache    string
	bannerCacheW   int

	viewportPlain string
	selecting     bool
	selRow1       int
	selCol1       int
	selRow2       int
	selCol2       int
	selActive     bool
	copyToast     string
	tokenUsage    tokenStats
	trimmedLines  int
}

func (m *AgentModel) stopOwnedChat() {
	if m == nil || m.chatOwner == nil {
		return
	}
	_ = m.chatOwner.Stop(config.GlobalConfig.Chat)
}

func (m *AgentModel) ensureOwnedChat() error {
	if m.chatOwner == nil {
		m.chatOwner = &chat.Owner{}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cfgPath, err := filepath.Abs(viper.ConfigFileUsed())
	if err != nil {
		return err
	}
	return m.chatOwner.Ensure(config.GlobalConfig.Chat, chat.Handoff{
		Executable: exe,
		ConfigPath: cfgPath,
		Proxy:      config.GlobalConfig.ControllerProxy,
	})
}

func (m *AgentModel) startConfiguredChatIfNeeded() string {
	ok, names := chat.ConfiguredChat(config.GlobalConfig.Chat)
	if !ok {
		return ""
	}
	if err := m.ensureOwnedChat(); err != nil {
		return "已配置聊天绑定，但自动启动失败: " + err.Error()
	}
	label := strings.Join(names, "、")
	if label == "" {
		label = "已绑定通道"
	}
	return "已自动启动聊天服务（" + label + "）。退出 DeepSentry 时会一起停止。"
}

func NewAgentModel(ctrl *SessionController, title, status string, maxSteps int, awaitGoal, autoStart bool, startup StartupInfo) AgentModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)

	ti := textinput.New()
	ti.Placeholder = "task, Enter to start..."
	ti.Prompt = ""
	ti.CharLimit = 256 * 1024
	ti.Width = 70
	_ = ti.Cursor.SetMode(cursor.CursorStatic)
	if awaitGoal {
		ti.Focus()
	} else {
		ti.Blur()
	}

	vp := viewport.New(80, 18)
	if strings.TrimSpace(startup.Tip) == "" {
		startup.Tip = randomUsageTip()
	}
	if strings.TrimSpace(startup.StartedAt) == "" {
		startup.StartedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	return AgentModel{
		ctrl:         ctrl,
		title:        title,
		statusLine:   status,
		maxSteps:     maxSteps,
		awaitGoal:    awaitGoal,
		autoStart:    autoStart,
		spinner:      sp,
		viewport:     vp,
		input:        ti,
		cursorAnchor: newInputCursorAnchorState(),
		chatOwner:    &chat.Owner{},
		lines:        []logLine{},
		lineID:       0,
		streamIdx:    -1,
		autoScroll:   true,
		historyIdx:   -1,
		startupInfo:  startup,
	}
}

func (m AgentModel) Init() tea.Cmd {
	// Explicitly restore paste framing even if a previous child process left
	// the terminal mode altered.
	cmds := []tea.Cmd{m.spinner.Tick, tea.EnableBracketedPaste}
	m.scheduleInputCursorAnchor()
	if m.autoStart && m.ctrl != nil && m.ctrl.beginRun() {
		cmds = append(cmds, agentStartCmd(false))
	}
	return tea.Batch(cmds...)
}

func agentStartCmd(followUp bool) tea.Cmd {
	return func() tea.Msg { return agentStartMsg{followUp: followUp} }
}

func isSelectAllKey(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyCtrlA {
		return true
	}
	switch msg.String() {
	case "ctrl+a", "cmd+a", "meta+a":
		return true
	default:
		return false
	}
}

func isSubmitKey(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyEnter {
		return true
	}
	switch msg.String() {
	case "enter", "return":
		return true
	default:
		return false
	}
}

func (m AgentModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.bannerCache = ""
		m.bannerCacheW = 0
		m.recalcLayout()
		m.refreshViewport()
		m.scheduleInputCursorAnchor()
		return m, nil

	case uiEventMsg:
		e := harness.UIEvent(msg)
		if settlesCompletedStream(e.Kind) {
			m.settleCompletedStreams()
		}
		m.applyEvent(e)
		if msg.Kind != harness.EventThinking {
			m.thinking = false
		}
		var cmds []tea.Cmd
		if e.Kind == harness.EventStreamDelta {
			if !m.streamTick {
				m.streamTick = true
				cmds = append(cmds, streamRefreshCmd())
			}
		} else if e.Kind == harness.EventCommandOutput {
			// External tools can produce hundreds of lines per second. Rebuilding
			// and repainting the entire viewport for every line causes visible
			// tearing and can starve Bubble Tea's renderer, so coalesce repaints.
			if !m.commandTick {
				m.commandTick = true
				cmds = append(cmds, commandOutputRefreshCmd())
			}
		} else {
			m.refreshViewport()
			if m.inputFocused() {
				m.scheduleInputCursorAnchor()
			}
			if e.Kind == harness.EventStreamEnd {
				if id := m.lastStreamLineID(); id > 0 {
					cmds = append(cmds, streamCollapseCmd(id))
				}
			}
			if e.Kind == harness.EventResult && isCommandCompletionEvent(e) {
				if group := m.activeCmdGroup; group > 0 {
					cmds = append(cmds, cmdOutputCollapseCmd(group))
				}
				m.activeCmdGroup = 0
			}
		}
		if m.thinking {
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(cmds...)

	case streamRefreshMsg:
		m.streamTick = false
		m.refreshViewport()
		if m.inputFocused() {
			m.scheduleInputCursorAnchor()
		}
		return m, nil

	case commandOutputRefreshMsg:
		m.commandTick = false
		m.refreshViewport()
		return m, nil

	case streamCollapseMsg:
		if !m.autoScroll {
			return m, nil
		}
		m.collapseStreamLine(msg.id)
		m.refreshViewport()
		if m.inputFocused() {
			m.scheduleInputCursorAnchor()
		}
		return m, nil

	case cmdOutputCollapseMsg:
		if !m.autoScroll {
			return m, nil
		}
		lines, chars, _ := m.commandOutputGroupStats(msg.group)
		if lines <= 8 && chars <= 1000 {
			return m, nil
		}
		m.collapseCommandOutputGroup(msg.group)
		m.refreshViewport()
		if m.inputFocused() {
			m.scheduleInputCursorAnchor()
		}
		return m, nil

	case copyToastMsg:
		m.copyToast = m.copyToastText(msg)
		return m, nil

	case copyToastClearMsg:
		m.copyToast = ""
		return m, nil

	case skillMarketResultMsg:
		if msg.err != nil {
			m.appendLine("error", "Skill 市场操作失败: "+msg.err.Error(), msg.err.Error())
		} else {
			m.appendLine("result", msg.out, msg.out)
			if msg.action == "install" || msg.action == "update" || msg.action == "uninstall" || msg.action == "rollback" {
				m.reloadSkillCatalog()
			}
		}
		m.recalcLayout()
		m.refreshViewport()
		if m.inputFocused() {
			m.scheduleInputCursorAnchor()
		}
		return m, nil

	case chatSetupResultMsg:
		if msg.err != nil {
			m.appendLine("error", "通讯工具窗口已退出: "+msg.err.Error(), "chat setup")
		} else {
			m.appendLine("info", "已返回终端。聊天服务由本进程托管，退出 DeepSentry 时会一起停止。再次 /connect 可管理绑定。", "chat setup")
		}
		m.recalcLayout()
		m.refreshViewport()
		return m, nil
	case mcpResultMsg:
		if msg.err != nil {
			m.appendLine("error", "MCP "+msg.action+" 失败: "+msg.err.Error(), msg.err.Error())
		} else {
			m.appendLine("result", msg.out, msg.out)
		}
		m.recalcLayout()
		m.refreshViewport()
		if m.inputFocused() {
			m.scheduleInputCursorAnchor()
		}
		return m, nil

	case imageAttachResultMsg:
		if msg.err != nil {
			m.appendLine("error", "附加图片失败: "+msg.err.Error(), msg.err.Error())
		} else if !m.inputAllSelected && len(m.draftImages) >= analyzer.MaxImagesPerMessage {
			m.appendLine("error", fmt.Sprintf("单条消息最多允许 %d 张图片", analyzer.MaxImagesPerMessage), "too many images")
		} else if (!m.inputAllSelected && draftImageBytes(m.draftImages)+msg.attachment.Size > analyzer.MaxImageBatchBytes) || msg.attachment.Size > analyzer.MaxImageBatchBytes {
			m.appendLine("error", fmt.Sprintf("单条消息图片总大小不能超过 %d MiB", analyzer.MaxImageBatchBytes>>20), "image batch too large")
		} else {
			if m.inputAllSelected {
				m.clearInputDraft()
			}
			m.draftImages = append(m.draftImages, msg.attachment)
			m.appendLine("info", fmt.Sprintf("✓ 已从%s附加图片：%s · %s · %.1f KiB", msg.source, msg.attachment.Name, msg.attachment.MediaType, float64(msg.attachment.Size)/1024), msg.attachment.Path)
		}
		m.recalcLayout()
		m.refreshViewport()
		if m.inputFocused() {
			m.scheduleInputCursorAnchor()
		}
		return m, nil

	case macosCmdVMsg:
		if !m.inputFocused() || m.pendingConfirm != nil {
			return m, nil
		}
		sessionID := ""
		if m.ctrl != nil {
			sessionID = m.ctrl.Stats().SessionID
		}
		return m, pasteClipboardImageOnlyCmd(sessionID)

	case macosCmdVIgnoredMsg:
		return m, nil

	case clipboardPasteResultMsg:
		if msg.err != nil {
			m.appendLine("error", "粘贴剪贴板失败: "+msg.err.Error(), msg.err.Error())
		} else if msg.attachment.Path != "" {
			return m.Update(imageAttachResultMsg{attachment: msg.attachment, source: "剪贴板"})
		} else {
			m.acceptPaste(msg.text)
		}
		m.recalcLayout()
		m.refreshViewport()
		if m.inputFocused() {
			m.scheduleInputCursorAnchor()
		}
		return m, nil

	case confirmMsg:
		restoreInput := m.inputFocused()
		m.pendingConfirm = &confirmState{action: msg.action, prompt: msg.prompt, respCh: msg.respCh, restoreInput: restoreInput}
		m.input.Blur()
		cancelInputCursorAnchor()
		m.appendConfirmLine(msg.prompt)
		m.recalcLayout()
		m.refreshViewport()
		return m, nil

	case askMsg:
		m.pendingAsk = &askState{action: msg.action, prompt: msg.prompt, options: msg.options, respCh: msg.respCh}
		m.input.Focus()
		m.input.SetValue("")
		m.draftParts = nil
		m.draftImages = nil
		m.appendAskLine(msg.prompt, msg.options)
		m.recalcLayout()
		m.refreshViewport()
		m.scheduleInputCursorAnchor()
		return m, nil

	case sudoAuthMsg:
		m.appendLine("info", "sudo 需要本机管理员授权：即将暂时退出全屏，由系统 sudo 隐藏读取密码；DeepSentry 不会接收或记录密码。", "sudo system validation")
		m.refreshViewport()
		m.cursorAnchor.release()
		return m, sudoValidationCmd(msg.respCh)

	case sudoAuthResultMsg:
		ok := msg.err == nil
		if ok {
			m.appendLine("info", "✓ sudo 授权验证成功；后续命令将使用非交互模式执行。", "sudo validated")
		} else {
			m.appendLine("error", "sudo 授权未完成，命令不会执行: "+msg.err.Error(), "sudo validation failed")
		}
		m.refreshViewport()
		return m, restoreTerminalAfterSudoCmd(msg.respCh, ok)

	case sudoTerminalRestoredMsg:
		// tea.ExecProcess restores the alternate screen, but Bubble Tea v1.3.4
		// does not restore the mouse mode enabled by WithMouseAllMotion. Finish
		// the repaint first, then let the waiting Agent resume its command.
		m.selecting = false
		m.selActive = false
		m.recalcLayout()
		m.refreshViewport()
		if msg.respCh != nil {
			msg.respCh <- msg.ok
		}
		m.syncInputCursorAnchor()
		return m, nil

	case agentDoneMsg:
		m.done = true
		m.running = false
		m.thinking = false
		m.stopping = false
		m.awaitGoal = false
		m.sessionLive = true
		m.pendingConfirm = nil
		m.pendingAsk = nil
		m.input.SetValue("")
		m.draftParts = nil
		m.draftImages = nil
		m.input.Width = ChromeContentWidth(m.width) - 2
		m.input.Focus()
		m.recalcLayout()
		m.refreshViewport()
		m.scheduleInputCursorAnchor()
		return m, nil

	case userMsgEvent:
		m.returnToLiveTail()
		m.appendLine("user", "You: "+msg.text, msg.text)
		m.refreshViewport()
		return m, nil

	case agentStartMsg:
		m.done = false
		m.running = true
		m.sessionLive = true
		m.input.Blur()
		cancelInputCursorAnchor()
		m.input.SetValue("")
		if msg.followUp {
			m.appendLine("info", "▶ 追问处理中…", "followup")
		} else if !m.autoStart {
			m.appendLine("info", "▶ 开始执行...", "start")
		}
		m.autoStart = false
		m.recalcLayout()
		m.refreshViewport()
		return m, nil

	case spinner.TickMsg:
		if m.thinking {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			m.refreshViewport()
			return m, cmd
		}
		return m, nil

	case tea.MouseMsg:
		if !m.inputFocused() {
			switch msg.Action {
			case tea.MouseActionPress:
				if msg.Button == tea.MouseButtonLeft {
					if row, col, ok := m.mouseToContentCoord(msg.X, msg.Y); ok {
						m.selecting = true
						m.selActive = true
						m.selRow1, m.selCol1 = row, col
						m.selRow2, m.selCol2 = row, col
					}
				}
			case tea.MouseActionMotion:
				if m.selecting {
					if row, col, ok := m.mouseToContentCoord(msg.X, msg.Y); ok {
						m.selRow2, m.selCol2 = row, col
					}
				}
			case tea.MouseActionRelease:
				if msg.Button == tea.MouseButtonLeft && m.selecting {
					m.selecting = false
					text := m.selectedPlainText()
					if cmd := m.copyTextCmd(text); cmd != nil {
						return m, cmd
					}
				}
			}
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.autoScroll = false
			m.viewport.LineUp(3)
			m.invalidateFooter()
			return m, nil
		case tea.MouseButtonWheelDown:
			m.viewport.LineDown(3)
			if m.viewport.AtBottom() {
				m.autoScroll = true
			}
			m.invalidateFooter()
			return m, nil
		}

	case tea.KeyMsg:
		// Ignore complete leaked SGR mouse reports, but preserve literal pasted text.
		if !msg.Paste && msg.Type == tea.KeyRunes {
			msg.Runes = []rune(leakedMouseReport.ReplaceAllString(string(msg.Runes), ""))
			if len(msg.Runes) == 0 {
				return m, nil
			}
		}
		key := msg.String()

		if m.pendingConfirm != nil {
			switch {
			case key == "y" || key == "Y":
				ch := m.pendingConfirm.respCh
				restoreInput := m.pendingConfirm.restoreInput
				m.pendingConfirm = nil
				m.resolveLastConfirm(approvalAllowOnce)
				if ch != nil {
					ch <- approvalAllowOnce
				}
				if restoreInput {
					m.input.Focus()
				}
				m.recalcLayout()
				m.refreshViewport()
				if restoreInput {
					m.scheduleInputCursorAnchor()
				}
			case key == "a" || key == "A":
				ch := m.pendingConfirm.respCh
				restoreInput := m.pendingConfirm.restoreInput
				m.pendingConfirm = nil
				m.resolveLastConfirm(approvalAllowSession)
				if ch != nil {
					ch <- approvalAllowSession
				}
				if restoreInput {
					m.input.Focus()
				}
				m.recalcLayout()
				m.refreshViewport()
				if restoreInput {
					m.scheduleInputCursorAnchor()
				}
			case key == "s" || key == "S":
				ch := m.pendingConfirm.respCh
				restoreInput := m.pendingConfirm.restoreInput
				m.pendingConfirm = nil
				m.resolveLastConfirm(approvalAllowAllSession)
				if ch != nil {
					ch <- approvalAllowAllSession
				}
				if restoreInput {
					m.input.Focus()
				}
				m.recalcLayout()
				m.refreshViewport()
				if restoreInput {
					m.scheduleInputCursorAnchor()
				}
			case key == "n" || key == "N" || key == "esc" || isSubmitKey(msg):
				ch := m.pendingConfirm.respCh
				restoreInput := m.pendingConfirm.restoreInput
				m.pendingConfirm = nil
				m.resolveLastConfirm(approvalDeny)
				if ch != nil {
					ch <- approvalDeny
				}
				if restoreInput {
					m.input.Focus()
				}
				m.recalcLayout()
				m.refreshViewport()
				if restoreInput {
					m.scheduleInputCursorAnchor()
				}
			case key == "ctrl+c":
				if ch := m.pendingConfirm.respCh; ch != nil {
					ch <- approvalDeny
				}
				m.pendingConfirm = nil
				if m.ctrl != nil {
					m.ctrl.RequestStop()
				}
				m.quitting = true
				cancelInputCursorAnchor()
				return m, tea.Quit
			}
			return m, nil
		}

		if m.inputFocused() && m.inputAllSelected && key == "esc" {
			m.inputAllSelected = false
			return m, nil
		}

		if m.pendingAsk != nil && (key == "esc" || key == "ctrl+c") {
			ch := m.pendingAsk.respCh
			m.pendingAsk = nil
			if ch != nil {
				ch <- ""
			}
			m.appendLine("info", "已暂停等待补充；可稍后直接输入追问继续。", "ask-cancel")
			m.refreshViewport()
			if key == "ctrl+c" {
				if m.ctrl != nil {
					m.ctrl.RequestStop()
				}
				m.quitting = true
				cancelInputCursorAnchor()
				return m, tea.Quit
			}
			return m, nil
		}

		// Ctrl+C：有选区时复制；否则停止任务 / 退出
		if key == "ctrl+c" || key == "cmd+c" {
			if !m.inputFocused() && m.hasCopySelection() {
				return m, m.copyTextCmd(m.selectedPlainText())
			}
			if m.running && !m.stopping && m.ctrl != nil {
				m.stopping = true
				m.ctrl.RequestStop()
				m.appendLine("info", "正在停止当前任务，会在当前 LLM/命令返回后保存 checkpoint。再次 Ctrl+C 强制退出。", "stopping")
				m.refreshViewport()
				return m, nil
			}
			m.quitting = true
			cancelInputCursorAnchor()
			return m, tea.Quit
		}

		// Esc：运行中中断任务；输入聚焦时退出输入模式便于滚动
		if key == "esc" {
			if m.running && !m.stopping && m.ctrl != nil {
				m.stopping = true
				m.ctrl.RequestStop()
				m.appendLine("info", "正在停止当前任务，会在当前 LLM/命令返回后保存 checkpoint。", "stopping")
				m.refreshViewport()
				return m, nil
			}
			if m.inputFocused() {
				m.input.Blur()
				cancelInputCursorAnchor()
				m.recalcLayout()
				m.refreshViewport()
				return m, nil
			}
			if m.selActive {
				m.selActive = false
				m.selecting = false
				return m, nil
			}
			return m, nil
		}

		// Handle the raw control-key type before bracketed paste. Some terminal
		// stacks mark Ctrl+V as an empty paste event; letting that event reach the
		// generic paste branch silently inserts nothing and makes image paste look
		// broken. A real Ctrl+V always means "read the OS clipboard" here.
		if m.inputFocused() && isClipboardPasteShortcut(msg) {
			sessionID := ""
			if m.ctrl != nil {
				sessionID = m.ctrl.Stats().SessionID
			}
			m.appendLine("info", "正在读取剪贴板（图片优先）…", "clipboard paste")
			m.refreshViewport()
			return m, pasteClipboardCmd(sessionID)
		}

		if m.inputFocused() && msg.Paste {
			// An empty bracketed-paste payload is what a few terminals emit when
			// their paste command sees an image clipboard. Use the same native
			// clipboard reader instead of treating it as an empty text insertion.
			sessionID := ""
			if m.ctrl != nil {
				sessionID = m.ctrl.Stats().SessionID
			}
			if len(msg.Runes) == 0 {
				m.appendLine("info", "正在读取剪贴板（图片优先）…", "clipboard paste")
				m.refreshViewport()
				return m, pasteClipboardCmd(sessionID)
			}
			if path, ok := looksLikeLocalImagePath(string(msg.Runes)); ok {
				m.appendLine("info", "正在附加粘贴的图片文件…", "clipboard paste")
				m.refreshViewport()
				return m, attachImageCmd(path, sessionID)
			}
			m.acceptPaste(string(msg.Runes))
			m.recalcLayout()
			m.refreshViewport()
			m.scheduleInputCursorAnchor()
			return m, nil
		}

		// 输入聚焦：仅处理输入相关快捷键，其余字符交给 textinput（避免 q/e/j/k 等全局键抢输入）
		if m.inputFocused() {
			if isSelectAllKey(msg) {
				m.inputAllSelected = m.input.Value() != "" || len(m.draftParts) > 0 || len(m.draftImages) > 0
				m.scheduleInputCursorAnchor()
				return m, nil
			}
			if m.inputAllSelected {
				switch key {
				case "backspace", "delete":
					m.clearInputDraft()
					m.recalcLayout()
					m.scheduleInputCursorAnchor()
					return m, nil
				case "left", "right", "home", "end", "up", "down", "ctrl+b", "ctrl+f", "ctrl+e", "tab":
					m.inputAllSelected = false
				default:
					if msg.Type == tea.KeyRunes || key == "space" || key == "alt+enter" || key == "shift+enter" || key == "ctrl+j" {
						m.clearInputDraft()
					}
				}
			}
			if isSubmitKey(msg) && m.pendingAsk != nil {
				if cmd := m.submitAskResponse(); cmd != nil {
					return m, cmd
				}
				return m, nil
			}
			if isSubmitKey(msg) && m.running && m.pendingConfirm == nil {
				if cmd := m.tryInterruptSubmit(); cmd != nil {
					return m, cmd
				}
				return m, nil
			}
			if isSubmitKey(msg) && !m.running && m.pendingConfirm == nil {
				if cmd := m.trySubmit(); cmd != nil {
					return m, cmd
				}
				return m, nil
			}
			switch key {
			case "alt+enter", "shift+enter", "ctrl+j":
				m.appendInputNewline()
				m.recalcLayout()
				m.scheduleInputCursorAnchor()
				return m, nil
			case "ctrl+l":
				m.clearView()
				m.scheduleInputCursorAnchor()
				return m, nil
			case "ctrl+u":
				m.clearInputDraft()
				m.recalcLayout()
				m.scheduleInputCursorAnchor()
				return m, nil
			case "backspace", "delete":
				if (len(m.draftParts) > 0 || len(m.draftImages) > 0) && m.input.Value() == "" {
					if key == "backspace" {
						m.removeLastDraftPart()
					}
					m.recalcLayout()
					m.scheduleInputCursorAnchor()
					return m, nil
				}
			case "up":
				if m.hasSlashSuggestions() {
					m.moveSlashSelection(-1)
					m.scheduleInputCursorAnchor()
					return m, nil
				}
				if m.moveInputCursorLine(-1) {
					m.scheduleInputCursorAnchor()
					return m, nil
				}
				m.recallInputHistory(-1)
				m.scheduleInputCursorAnchor()
				return m, nil
			case "down":
				if m.hasSlashSuggestions() {
					m.moveSlashSelection(1)
					m.scheduleInputCursorAnchor()
					return m, nil
				}
				if m.moveInputCursorLine(1) {
					m.scheduleInputCursorAnchor()
					return m, nil
				}
				m.recallInputHistory(1)
				m.scheduleInputCursorAnchor()
				return m, nil
			case "tab":
				if m.acceptSlashSuggestion() {
					m.recalcLayout()
					m.scheduleInputCursorAnchor()
					return m, nil
				}
			case "pgup":
				m.autoScroll = false
				m.viewport.ViewUp()
				m.invalidateFooter()
				m.scheduleInputCursorAnchor()
				return m, nil
			case "pgdown":
				m.viewport.ViewDown()
				if m.viewport.AtBottom() {
					m.autoScroll = true
				}
				m.invalidateFooter()
				m.scheduleInputCursorAnchor()
				return m, nil
			case "ctrl+home":
				m.autoScroll = false
				m.viewport.GotoTop()
				m.invalidateFooter()
				return m, nil
			case "ctrl+end":
				m.autoScroll = true
				m.viewport.GotoBottom()
				m.invalidateFooter()
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.clampSlashSelection()
			m.recalcLayout()
			m.scheduleInputCursorAnchor()
			return m, cmd
		}

		// 提交：Enter（必须用 tea.Cmd 更新状态，禁止 program.Send 防死锁）
		if isSubmitKey(msg) && m.pendingAsk != nil {
			if cmd := m.submitAskResponse(); cmd != nil {
				return m, cmd
			}
			return m, nil
		}
		if isSubmitKey(msg) && m.running && m.pendingConfirm == nil {
			if cmd := m.tryInterruptSubmit(); cmd != nil {
				return m, cmd
			}
			return m, nil
		}
		if isSubmitKey(msg) && !m.running && m.pendingConfirm == nil {
			if cmd := m.trySubmit(); cmd != nil {
				return m, cmd
			}
			return m, nil
		}

		// 以下全局快捷键仅在输入未聚焦时生效
		switch key {
		case "ctrl+l":
			m.clearView()
			return m, nil
		case "q":
			if !m.running {
				m.quitting = true
				cancelInputCursorAnchor()
				return m, tea.Quit
			}
		case "e":
			oldOffset := m.viewport.YOffset
			_, collapsed := m.toggleAllCollapsible()
			// Expansion is a reading action. Keeping the live-tail lock would
			// jump straight to </html> and make the preceding paste look lost.
			if !collapsed {
				m.autoScroll = false
			}
			m.refreshViewport()
			if !collapsed {
				m.viewport.SetYOffset(oldOffset)
			}
			return m, nil
		case "up", "k", "pgup":
			m.autoScroll = false
			if key == "pgup" {
				m.viewport.ViewUp()
			} else {
				m.viewport.LineUp(1)
			}
			m.invalidateFooter()
			return m, nil
		case "down", "j", "pgdown":
			if key == "pgdown" {
				m.viewport.ViewDown()
			} else {
				m.viewport.LineDown(1)
			}
			if m.viewport.AtBottom() {
				m.autoScroll = true
			}
			m.invalidateFooter()
			return m, nil
		case "G":
			m.autoScroll = true
			m.viewport.GotoBottom()
			m.invalidateFooter()
			return m, nil
		case "g", "home", "ctrl+home":
			m.autoScroll = false
			m.viewport.GotoTop()
			m.invalidateFooter()
			return m, nil
		case "tab":
			if m.done || m.awaitGoal || m.sessionLive {
				m.input.Focus()
				m.recalcLayout()
				m.refreshViewport()
				m.scheduleInputCursorAnchor()
				return m, nil
			}
		}

		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	if m.inputFocused() {
		m.scheduleInputCursorAnchor()
	}
	return m, nil
}

func (m *AgentModel) submitAskResponse() tea.Cmd {
	if m.pendingAsk == nil {
		return nil
	}
	pasted := m.hasPasteBlocks()
	draftSummary := m.submittedDraftSummary()
	text := strings.TrimSpace(m.currentInputValue())
	if text == "" {
		return nil
	}
	text = normalizeAskAnswer(text, m.pendingAsk.options)
	if runewidth.StringWidth(text) <= 4000 {
		m.inputHistory = append(m.inputHistory, text)
	}
	ch := m.pendingAsk.respCh
	m.pendingAsk = nil
	m.clearInputDraft()
	m.input.Blur()
	cancelInputCursorAnchor()
	m.returnToLiveTail()
	m.appendSubmittedUserLine(text, pasted, draftSummary, nil)
	m.recalcLayout()
	m.refreshViewport()
	if ch != nil {
		ch <- text
	}
	return nil
}

func (m *AgentModel) tryInterruptSubmit() tea.Cmd {
	pasted := m.hasPasteBlocks()
	draftSummary := m.submittedDraftSummary()
	images := append([]analyzer.ImageAttachment(nil), m.draftImages...)
	text := strings.TrimSpace(m.currentInputValue())
	if (text == "" && len(images) == 0) || m.ctrl == nil {
		return nil
	}
	if text == "" {
		text = "请分析所附图片，并结合当前任务继续。"
	}
	if !m.ctrl.InterruptWithAttachments(text, images) {
		return nil
	}
	if runewidth.StringWidth(text) <= 4000 {
		m.inputHistory = append(m.inputHistory, text)
	}
	m.clearInputDraft()
	m.input.Blur()
	cancelInputCursorAnchor()
	m.returnToLiveTail()
	m.appendSubmittedUserLine(text, pasted, draftSummary, images)
	m.appendLine("info", "↳ 已注入新指令，当前轮停止后会按最新目标继续。", text)
	m.recalcLayout()
	m.refreshViewport()
	m.scheduleInputCursorAnchor()
	return nil
}

func summarizeIfNeeded(text string) string {
	if strings.Count(text, "\n") >= 3 || runewidth.StringWidth(text) > 400 {
		return summarizeUserText(text)
	}
	return text
}

func (m *AgentModel) appendSubmittedUserLine(text string, pasted bool, draftSummary string, images []analyzer.ImageAttachment) {
	text = normalizeInputNewlines(text)
	draftSummary = normalizeInputNewlines(draftSummary)
	content := "You: " + summarizeIfNeeded(text)
	if pasted {
		content = "You:\n" + strings.TrimSpace(draftSummary)
	}
	if len(images) > 0 {
		names := make([]string, 0, len(images))
		for _, image := range images {
			names = append(names, image.Name)
		}
		content += fmt.Sprintf("\n[图片 %d 张：%s]", len(images), strings.Join(names, ", "))
	}
	m.appendLine("user", content, text)
	if pasted && len(m.lines) > 0 {
		m.lines[len(m.lines)-1].collapsed = true
	}
}

func normalizeAskAnswer(text string, options []string) string {
	idx, err := strconv.Atoi(strings.TrimSpace(text))
	if err == nil && idx > 0 && idx <= len(options) {
		if opt := strings.TrimSpace(options[idx-1]); opt != "" {
			return opt
		}
	}
	return text
}

func slashCommandNames() string {
	names := make([]string, 0, len(slashCommands))
	for _, cmd := range slashCommands {
		names = append(names, "/"+cmd.Name)
	}
	return strings.Join(names, " ")
}

func splitSlashCommand(text string) (string, string) {
	text = strings.TrimSpace(strings.TrimPrefix(text, "/"))
	if text == "" {
		return "", ""
	}
	for i, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return strings.ToLower(strings.TrimSpace(text[:i])), strings.TrimSpace(text[i:])
		}
	}
	return strings.ToLower(text), ""
}

func (m AgentModel) slashQuery() (string, bool) {
	if !m.inputFocused() || m.hasPasteBlocks() || m.pendingAsk != nil {
		return "", false
	}
	value := strings.TrimSpace(m.input.Value())
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \t\r\n") {
		return "", false
	}
	return strings.TrimPrefix(value, "/"), true
}

func (m AgentModel) slashSuggestions() []slashCommand {
	query, ok := m.slashQuery()
	if !ok {
		return nil
	}
	query = strings.ToLower(query)
	if query != "" {
		for _, cmd := range slashCommands {
			if strings.ToLower(cmd.Name) == query {
				return nil
			}
		}
	}
	out := make([]slashCommand, 0, len(slashCommands))
	for _, cmd := range slashCommands {
		if query == "" || strings.HasPrefix(strings.ToLower(cmd.Name), query) {
			out = append(out, cmd)
		}
	}
	return out
}

func (m AgentModel) hasSlashSuggestions() bool {
	return len(m.slashSuggestions()) > 0
}

func (m *AgentModel) clampSlashSelection() {
	suggestions := m.slashSuggestions()
	if len(suggestions) == 0 {
		m.slashSelected = 0
		return
	}
	if m.slashSelected < 0 {
		m.slashSelected = 0
	}
	if m.slashSelected >= len(suggestions) {
		m.slashSelected = len(suggestions) - 1
	}
}

func (m *AgentModel) moveSlashSelection(delta int) {
	suggestions := m.slashSuggestions()
	if len(suggestions) == 0 {
		m.slashSelected = 0
		return
	}
	m.slashSelected = (m.slashSelected + delta + len(suggestions)) % len(suggestions)
}

func (m *AgentModel) acceptSlashSuggestion() bool {
	suggestions := m.slashSuggestions()
	if len(suggestions) == 0 {
		return false
	}
	m.clampSlashSelection()
	m.input.SetValue("/" + suggestions[m.slashSelected].Name)
	m.input.SetCursor(len([]rune(m.input.Value())))
	return true
}

func (m *AgentModel) trySubmit() tea.Cmd {
	pasted := m.hasPasteBlocks()
	draftSummary := m.submittedDraftSummary()
	savedParts := append([]inputDraftPart(nil), m.draftParts...)
	savedImages := append([]analyzer.ImageAttachment(nil), m.draftImages...)
	savedInput := m.input.Value()
	savedCursor := m.input.Position()
	text := strings.TrimSpace(m.currentInputValue())
	if text == "" && len(savedImages) == 0 {
		return nil
	}
	if !pasted && strings.HasPrefix(text, "/") {
		cmd, _ := splitSlashCommand(text)
		if len(savedImages) == 0 || cmd == "image" {
			return m.handleSlashCommand(text)
		}
	}
	if text == "" {
		text = "请分析所附图片，并结合当前任务继续。"
	}
	if runewidth.StringWidth(text) <= 4000 {
		m.inputHistory = append(m.inputHistory, text)
	}
	m.historyIdx = -1

	followUp := !m.awaitGoal && (m.sessionLive || m.done)
	firstTurn := m.awaitGoal || (!m.sessionLive && !m.done && !followUp)

	m.clearInputDraft()
	m.input.Blur()
	cancelInputCursorAnchor()
	m.awaitGoal = false
	m.done = false
	m.sessionLive = true
	m.returnToLiveTail()
	m.recalcLayout()
	m.appendSubmittedUserLine(text, pasted, draftSummary, savedImages)
	m.refreshViewport()

	var ok bool
	if followUp {
		ok = m.ctrl.PrepareFollowUpWithAttachments(text, savedImages)
	} else if firstTurn {
		m.ctrl.SetInitialGoalWithAttachments(text, savedImages)
		ok = m.ctrl.beginRun()
	} else {
		ok = m.ctrl.PrepareFollowUpWithAttachments(text, savedImages)
		followUp = true
	}

	if !ok {
		if followUp {
			m.done = true
		} else {
			m.awaitGoal = true
			m.sessionLive = false
		}
		m.input.Focus()
		m.draftParts = savedParts
		m.draftImages = savedImages
		m.input.SetValue(savedInput)
		m.input.SetCursor(savedCursor)
		m.appendLine("error", "Agent 仍在运行，请稍候", "busy")
		m.recalcLayout()
		m.refreshViewport()
		m.scheduleInputCursorAnchor()
		return nil
	}

	return agentStartCmd(followUp)
}

func streamRefreshCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return streamRefreshMsg{} })
}

func commandOutputRefreshCmd() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg { return commandOutputRefreshMsg{} })
}

func streamCollapseCmd(id int) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return streamCollapseMsg{id: id} })
}

func cmdOutputCollapseCmd(group int) tea.Cmd {
	return tea.Tick(12*time.Second, func(time.Time) tea.Msg { return cmdOutputCollapseMsg{group: group} })
}

func isCommandCompletionEvent(e harness.UIEvent) bool {
	if e.Kind != harness.EventResult {
		return false
	}
	msg := strings.TrimSpace(e.Message)
	return msg == "命令执行完成" || msg == "命令执行完成（无输出）"
}

func (m *AgentModel) recallInputHistory(delta int) {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.historyIdx < 0 {
		if delta < 0 {
			m.historyIdx = len(m.inputHistory) - 1
		} else {
			return
		}
	} else {
		m.historyIdx += delta
		if m.historyIdx < 0 {
			m.historyIdx = 0
		}
		if m.historyIdx >= len(m.inputHistory) {
			m.historyIdx = -1
			m.input.SetValue("")
			return
		}
	}
	m.input.SetValue(encodeInputValue(m.inputHistory[m.historyIdx]))
	m.draftParts = nil
}

func (m *AgentModel) handleSlashCommand(text string) tea.Cmd {
	cmd, arg := splitSlashCommand(text)
	if cmd == "image" {
		m.draftParts = nil
		m.input.SetValue("")
		m.input.SetCursor(0)
		m.slashSelected = 0
		m.returnToLiveTail()
		m.recalcLayout()
		m.appendLine("info", "正在附加图片…", text)
		m.refreshViewport()
		sessionID := ""
		if m.ctrl != nil {
			sessionID = m.ctrl.Stats().SessionID
		}
		return attachImageCmd(arg, sessionID)
	}
	m.clearInputDraft()
	// Slash commands are submissions too. When the user invokes one while
	// reading older output, keep the result visible instead of silently
	// appending it below the current scroll position.
	m.returnToLiveTail()
	m.recalcLayout()
	switch {
	case cmd == "clear":
		m.clearView()
		return nil
	case cmd == "" || cmd == "help":
		m.appendLine("info", "浏览: ↑↓/jk 逐行 · PgUp/PgDn 翻页 · g/Home 顶部 · G 底部 · e 全部展开/折叠\n可用命令: "+slashCommandNames(), "help")
	case cmd == "new" || cmd == "restart":
		return m.startNewSession(arg)
	case cmd == "status":
		target := m.statusContextText()
		m.appendLine("info", fmt.Sprintf("状态: running=%v step=%d/%d target=%s connection=%s · %s", m.running, m.currentStep, m.maxSteps, target, m.statusLine, m.sessionStatsText(false)), "status")
	case cmd == "cost":
		m.appendLine("info", m.sessionStatsText(true), "cost")
	case cmd == "model":
		m.appendLine("info", "模型: "+m.title, "model")
	case cmd == "compact":
		m.appendLine("info", "已折叠最近的长输出/思考块；完整上下文仍保留在会话历史中。", "compact")
		for i := range m.lines {
			if m.lines[i].kind == "subagent_result" || m.lines[i].kind == "stream" || m.lines[i].kind == "cmdout" {
				m.lines[i].collapsed = true
			}
		}
	case cmd == "memory":
		m.handleMemorySlash(arg)
	case cmd == "agents" || cmd == "agents.md" || cmd == "agent":
		m.handleAgentsSlash(arg)
	case cmd == "sessions":
		summaries, err := harness.ListSessionSummaries()
		if err != nil {
			m.appendLine("error", "读取会话失败: "+err.Error(), err.Error())
			break
		}
		if len(summaries) == 0 {
			m.appendLine("info", "暂无可恢复会话", "no sessions")
			break
		}
		var b strings.Builder
		// The viewport sticks to the bottom, so render oldest -> newest here.
		// CLI/picker APIs remain newest-first.
		for i := len(summaries) - 1; i >= 0; i-- {
			s := summaries[i]
			line := fmt.Sprintf("%s · step %d · %s", s.ID, s.StepNum, s.SavedAt.Format("01-02 15:04"))
			if goal := strings.TrimSpace(s.Goal); goal != "" {
				line += " · " + truncateStr(goal, 60)
			}
			b.WriteString(line + "\n")
		}
		m.appendLine("result", strings.TrimSpace(b.String()), b.String())
	case cmd == "resume":
		return m.resumeSessionSlash(arg)
	case cmd == "tsecbench":
		return m.startTSecBenchMode(arg)
	case cmd == "config":
		m.appendLine("info", fmt.Sprintf("连接: %s · 模型: %s · 最大步数: %d", m.statusLine, m.title, m.maxSteps), text)
	case cmd == "sudo":
		m.appendLine("info", "即将由系统 sudo 验证本机管理员权限；输入内容不会进入 DeepSentry。", "manual sudo validation")
		m.refreshViewport()
		m.cursorAnchor.release()
		return sudoValidationCmd(nil)
	case cmd == "connect":
		if m.running {
			m.appendLine("error", "请等待当前任务结束后再打开通讯工具窗口。", text)
			break
		}
		if err := m.ensureOwnedChat(); err != nil {
			m.appendLine("error", "启动聊天服务失败: "+err.Error(), text)
			break
		}
		exe, err := os.Executable()
		if err != nil {
			m.appendLine("error", err.Error(), text)
			break
		}
		cfgPath, err := filepath.Abs(viper.ConfigFileUsed())
		if err != nil {
			m.appendLine("error", err.Error(), text)
			break
		}
		args := []string{"--chat-setup", "-c", cfgPath}
		if proxy := config.GlobalConfig.ControllerProxy; proxy != "" {
			args = append(args, "--proxy", proxy)
		}
		m.appendLine("info", "即将打开通讯工具连接窗口（飞书/QQ/微信/企微）。退出 DeepSentry 时，聊天进程会一起停止。", text)
		m.refreshViewport()
		m.cursorAnchor.release()
		return tea.ExecProcess(exec.Command(exe, args...), func(err error) tea.Msg { return chatSetupResultMsg{err} })
	case cmd == "mcp":
		result := m.handleMCPSlash(arg)
		m.refreshViewport()
		return result
	case cmd == "skill":
		result := m.handleSkillSlash(arg)
		m.refreshViewport()
		return result
	case cmd == "exit" || cmd == "quit":
		m.quitting = true
		cancelInputCursorAnchor()
		return tea.Quit
	default:
		m.appendLine("error", "未知命令: /"+cmd+"（可用 "+slashCommandNames()+"）", text)
	}
	m.refreshViewport()
	return nil
}

func sudoValidationCmd(respCh chan bool) tea.Cmd {
	return tea.ExecProcess(exec.Command("sudo", "-v"), func(err error) tea.Msg {
		return sudoAuthResultMsg{respCh: respCh, err: err}
	})
}

func restoreTerminalAfterSudoCmd(respCh chan bool, ok bool) tea.Cmd {
	return tea.Sequence(
		tea.EnterAltScreen,
		tea.EnableMouseAllMotion,
		tea.EnableBracketedPaste,
		tea.ClearScreen,
		func() tea.Msg { return sudoTerminalRestoredMsg{respCh: respCh, ok: ok} },
	)
}

func (m *AgentModel) startTSecBenchMode(arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if strings.TrimSpace(config.GlobalConfig.BenchmarkBaseURL) == "" || strings.TrimSpace(config.GlobalConfig.BenchmarkToken) == "" {
		m.appendLine("info", "未检测到完整 TSecBench 配置，Agent 会先引导填写 benchmark_base_url 和 benchmark_token，并通过 config_manage 保存。", "tsecbench missing config")
	}
	return m.startNewSession(tsecbenchModePrompt(arg))
}

func tsecbenchModePrompt(arg string) string {
	arg = strings.TrimSpace(arg)
	var b strings.Builder
	b.WriteString("进入 TSecBench 跑分模式。优先使用内置工具 tsecbench，不要手写 curl。")
	b.WriteString("先检查 benchmark_base_url/benchmark_token 配置是否存在；如果缺失，询问用户并使用 config_manage 安全写入。")
	b.WriteString("配置存在后，先调用 tsecbench action=list/status 拉取题目和进度，再根据用户目标选择题目启动容器。")
	b.WriteString("默认不要获取 hint，不要提交 flag，除非用户明确同意；提交后及时 close 容器释放资源。全程不要明文输出 benchmark_token。")
	if arg != "" {
		b.WriteString("用户补充目标：")
		b.WriteString(arg)
	}
	return b.String()
}

func (m *AgentModel) handleMemorySlash(arg string) {
	fields := strings.Fields(arg)
	action := "list"
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	if action == "clues" || action == "clue" {
		state := m.currentAgentState()
		if state == nil {
			m.appendLine("error", "当前 Agent 没有可用会话状态", arg)
			return
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "clear") {
			state.ReplaceCoreClues(nil)
			message := "已清空当前会话核心线索板（不会删除跨会话 Memory）"
			m.appendResultLine(message, message)
			return
		}
		prompt := strings.TrimSpace(state.CoreCluesPrompt(12000))
		if prompt == "" {
			m.appendLine("info", "当前会话尚未提取到核心线索", arg)
			return
		}
		m.appendLine("result", prompt, prompt)
		return
	}
	store := m.currentMemoryStore()
	if store == nil {
		m.appendLine("error", "当前 Agent 没有可用 MemoryStore", arg)
		return
	}
	switch action {
	case "list", "status", "ls":
		entries := store.ActiveEntries()
		if len(entries) == 0 {
			m.appendLine("info", "结构化 Memory 为空", arg)
			return
		}
		var b strings.Builder
		for _, e := range entries {
			b.WriteString(fmt.Sprintf("[%s] %s = %s\n", e.Scope, e.Key, truncateStr(e.Value, 160)))
		}
		m.appendLine("result", strings.TrimSpace(b.String()), b.String())
	case "clear", "reset", "init":
		scope := "all"
		if len(fields) > 1 {
			scope = fields[1]
		}
		n, err := store.Clear(scope)
		if err != nil {
			m.appendLine("error", "清空 Memory 失败: "+err.Error(), err.Error())
			return
		}
		message := fmt.Sprintf("已清空结构化 Memory（范围: %s，删除 %d 条）", scope, n)
		m.appendResultLine(message, message)
	default:
		m.appendLine("info", "用法: /memory list | /memory clues [clear] | /memory clear [all|target|global]", arg)
	}
}

func (m *AgentModel) handleAgentsSlash(arg string) {
	store := m.currentMemoryStore()
	if store == nil {
		m.appendLine("error", "当前 Agent 没有可用 MemoryStore", arg)
		return
	}
	fields := strings.Fields(arg)
	action := "status"
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	switch action {
	case "status", "list", "ls":
		m.appendLine("info", fmt.Sprintf("AGENTS.md 已加载 %d 个来源（包含内置默认）。可手动编辑 ~/.deepsentry/AGENTS.md；Agent 也会在用户明确要求永久记住或多轮形成稳定偏好时智能归纳维护。用 /agents clear 清空外部 AGENTS.md。", store.AgentsMDCount()), arg)
	case "clear", "reset", "init":
		n, err := store.ClearExternalAgentsMD()
		if err != nil {
			m.appendLine("error", "清空 AGENTS.md 失败: "+err.Error(), err.Error())
			return
		}
		message := fmt.Sprintf("已清空外部 AGENTS.md（删除 %d 个文件，内置默认保留）", n)
		m.appendResultLine(message, message)
	default:
		m.appendLine("info", "用法: /agents status | /agents clear", arg)
	}
}

func (m *AgentModel) currentMemoryStore() *memory.Store {
	if m.ctrl == nil || m.ctrl.cfg.Agent == nil {
		return nil
	}
	return m.ctrl.cfg.Agent.MemoryStore
}

func (m *AgentModel) currentAgentState() *harness.AgentState {
	if m.ctrl == nil || m.ctrl.cfg.Agent == nil {
		return nil
	}
	return m.ctrl.cfg.Agent.State
}

func (m *AgentModel) resumeSessionSlash(arg string) tea.Cmd {
	if m.ctrl == nil {
		m.appendLine("error", "当前界面没有可用控制器", arg)
		m.refreshViewport()
		return nil
	}
	if m.running {
		m.appendLine("error", "当前任务仍在运行；请先 Esc 停止后再 /resume。", arg)
		m.refreshViewport()
		return nil
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.appendLine("info", "用法: /resume <session_id> [补充说明]；可先用 /sessions 查看可恢复会话。", arg)
		m.refreshViewport()
		return nil
	}
	sessionID := fields[0]
	supplement := strings.TrimSpace(strings.TrimPrefix(arg, sessionID))
	step, err := m.ctrl.ResumeSession(sessionID, supplement)
	if err != nil {
		m.appendLine("error", "恢复会话失败: "+err.Error(), err.Error())
		m.refreshViewport()
		return nil
	}

	m.lines = []logLine{}
	m.lineID = 0
	m.streamIdx = -1
	m.streamTick = false
	m.cmdOutputGroup = 0
	m.activeCmdGroup = 0
	m.running = false
	m.thinking = false
	m.currentStep = step
	m.done = false
	m.awaitGoal = false
	m.sessionLive = true
	m.autoStart = false
	m.autoScroll = true
	m.stopping = false
	m.pendingConfirm = nil
	m.pendingAsk = nil
	m.historyIdx = -1
	m.currentTarget = ""
	m.copyToast = ""
	m.tokenUsage = tokenStats{}
	m.trimmedLines = 0
	m.startupInfo.SessionID = sessionID
	m.startupInfo.AwaitGoal = false
	m.startupInfo.StartedAt = time.Now().Format("2006-01-02 15:04:05")
	m.bannerCache = ""
	m.bannerCacheW = 0
	m.clearInputDraft()
	cancelInputCursorAnchor()

	m.input.Blur()
	if m.ctrl.cfg.History != nil {
		m.restoreConversationHistory(*m.ctrl.cfg.History)
	}
	m.appendLine("info", fmt.Sprintf("已恢复会话 %s (step %d)，开始继续执行。", shortSessionID(sessionID), step), sessionID)
	m.refreshViewport()
	if m.ctrl.beginRun() {
		return agentStartCmd(true)
	}
	m.appendLine("error", "Agent 仍在运行，请稍候", "busy")
	m.refreshViewport()
	return nil
}

func (m *AgentModel) handleMCPSlash(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	action := "status"
	args := map[string]string{"action": "status"}
	if len(fields) > 0 {
		switch fields[0] {
		case "list", "status":
			args["action"] = "status"
			out, err := config.ManageConfig(args)
			if err != nil {
				m.appendLine("error", "MCP status 失败: "+err.Error(), err.Error())
				return nil
			}
			message := out + "\n\n实时连接:\n" + mcp.FormatServerStatus()
			m.appendResultLine(message, message)
			return nil
		case "import":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /mcp import /path/to/claude_desktop_config.json", arg)
				return nil
			}
			args = map[string]string{"action": "import_claude_mcp", "import_path": strings.TrimSpace(strings.TrimPrefix(arg, "import"))}
			action = "import"
		case "add", "add-http":
			if len(fields) < 3 {
				m.appendLine("error", "用法: /mcp add <name> <command|https://url> [args] [token_env=ENV] [enabled_tools=a,b]", arg)
				return nil
			}
			args = map[string]string{"action": "add_mcp_server", "name": fields[1], "structured": "true"}
			isHTTP := fields[0] == "add-http" || strings.HasPrefix(fields[2], "http://") || strings.HasPrefix(fields[2], "https://")
			if isHTTP {
				args["type"] = "streamable_http"
				args["url"] = fields[2]
			} else {
				args["type"] = "stdio"
				args["command"] = fields[2]
			}
			optionStart := 3
			if !isHTTP && len(fields) >= 4 && !strings.Contains(fields[3], "=") {
				args["args"] = fields[3]
				optionStart = 4
			}
			for _, field := range fields[optionStart:] {
				if k, v, ok := strings.Cut(field, "="); ok {
					if k == "token_env" {
						k = "bearer_token_env_var"
					}
					args[k] = v
				}
			}
			action = "add"
		case "resources":
			server := ""
			if len(fields) > 1 {
				server = fields[1]
			}
			return mcpQueryCmd("resources", func() (string, error) { return formatMCPResources(server), nil })
		case "read":
			if len(fields) < 3 {
				m.appendLine("error", "用法: /mcp read <server> <uri>", arg)
				return nil
			}
			return mcpQueryCmd("read", func() (string, error) { return mcp.ReadResource(fields[1], strings.Join(fields[2:], " ")) })
		case "prompts":
			server := ""
			if len(fields) > 1 {
				server = fields[1]
			}
			return mcpQueryCmd("prompts", func() (string, error) { return formatMCPPrompts(server), nil })
		case "prompt":
			if len(fields) < 3 {
				m.appendLine("error", "用法: /mcp prompt <server> <name> [key=value ...]", arg)
				return nil
			}
			promptArgs := map[string]string{}
			for _, field := range fields[3:] {
				if key, value, ok := strings.Cut(field, "="); ok {
					promptArgs[key] = value
				}
			}
			return mcpQueryCmd("prompt", func() (string, error) { return mcp.GetPrompt(fields[1], fields[2], promptArgs) })
		case "login":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /mcp login <server>", arg)
				return nil
			}
			cfg, ok := configuredMCPServer(fields[1])
			if !ok {
				m.appendLine("error", "未找到已启用 MCP Server: "+fields[1], arg)
				return nil
			}
			if strings.EqualFold(cfg.Type, "stdio") || cfg.URL == "" {
				m.appendLine("error", "只有远程 Streamable HTTP MCP Server 支持 OAuth 登录", arg)
				return nil
			}
			m.appendLine("info", "正在启动 MCP OAuth 登录；浏览器授权完成后会自动连接…", fields[1])
			return mcpQueryCmd("login", func() (string, error) {
				if err := mcp.ConnectOAuth(mcpServerConfig(cfg)); err != nil {
					return "", err
				}
				return "MCP OAuth 登录成功并已实时连接: " + cfg.Name, nil
			})
		case "reconnect", "retry":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /mcp reconnect <server>", arg)
				return nil
			}
			name := fields[1]
			m.appendLine("info", "正在重新连接 MCP Server: "+name, name)
			return mcpQueryCmd("reconnect", func() (string, error) {
				if err := mcp.Reconnect(name); err != nil {
					return "", err
				}
				return "MCP 已重新连接（不会自动重放上一次工具调用）: " + name, nil
			})
		case "off", "disable":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /mcp off <name>", arg)
				return nil
			}
			args = map[string]string{"action": "disable_mcp_server", "name": fields[1]}
			action = "off"
		case "on", "enable":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /mcp on <name>", arg)
				return nil
			}
			args = map[string]string{"action": "enable_mcp_server", "name": fields[1]}
			action = "on"
		case "remove", "rm":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /mcp remove <name>", arg)
				return nil
			}
			args = map[string]string{"action": "remove_mcp_server", "name": fields[1]}
			action = "remove"
		default:
			m.appendLine("info", "用法: /mcp status | reconnect <name> | import <claude.json> | add <name> <command|url> | login <name> | resources/read | prompts/prompt | off/on/remove", arg)
			return nil
		}
	}
	if action == "status" {
		out, err := config.ManageConfig(args)
		if err != nil {
			m.appendLine("error", "MCP status 失败: "+err.Error(), err.Error())
			return nil
		}
		message := out + "\n\n实时连接:\n" + mcp.FormatServerStatus()
		m.appendResultLine(message, message)
		return nil
	}
	return mcpAdminCmd(action, args)
}

func mcpQueryCmd(action string, fn func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		out, err := fn()
		return mcpResultMsg{action: action, out: out, err: err}
	}
}

func mcpAdminCmd(action string, args map[string]string) tea.Cmd {
	return func() tea.Msg {
		out, err := config.ManageConfig(args)
		if err != nil {
			return mcpResultMsg{action: action, err: err}
		}
		name := strings.TrimSpace(args["name"])
		switch action {
		case "off", "remove":
			mcp.Disconnect(name)
		case "add", "on":
			if cfg, ok := configuredMCPServer(name); ok {
				if err := mcp.Connect(mcpServerConfig(cfg)); err != nil {
					out += "\n[WARN] 配置已保存，但实时连接失败: " + err.Error()
				} else {
					out += "\n[OK] 已实时连接，无需重启。"
				}
			}
		case "import":
			for _, cfg := range config.GlobalConfig.MCPServerConfigs {
				if cfg.Disabled {
					continue
				}
				if err := mcp.Connect(mcpServerConfig(cfg)); err != nil {
					out += "\n[WARN] " + cfg.Name + " 实时连接失败: " + err.Error()
				}
			}
		}
		return mcpResultMsg{action: action, out: out}
	}
}

func configuredMCPServer(name string) (config.MCPServerConfig, bool) {
	for _, cfg := range config.GlobalConfig.MCPServerConfigs {
		if cfg.Name == name && !cfg.Disabled {
			return cfg, true
		}
	}
	return config.MCPServerConfig{}, false
}

func mcpServerConfig(cfg config.MCPServerConfig) mcp.ServerConfig {
	return mcp.ServerConfig{
		Name: cfg.Name, Type: cfg.Type, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, CWD: cfg.CWD,
		URL: cfg.URL, Headers: cfg.Headers, BearerTokenEnvVar: cfg.BearerTokenEnvVar,
		EnabledTools: cfg.EnabledTools, DisabledTools: cfg.DisabledTools,
		StartupTimeoutSec: cfg.StartupTimeoutSec, ToolTimeoutSec: cfg.ToolTimeoutSec,
		Required: cfg.Required, Disabled: cfg.Disabled,
	}
}

func formatMCPResources(server string) string {
	resources := mcp.ListResources(server)
	templates := mcp.ListResourceTemplates(server)
	if len(resources) == 0 && len(templates) == 0 {
		return "没有发现匹配的 MCP Resource。"
	}
	var b strings.Builder
	for _, resource := range resources {
		fmt.Fprintf(&b, "- %s · %s · %s", resource.Server, resource.URI, firstNonEmpty(resource.Title, resource.Name))
		if resource.MIMEType != "" {
			fmt.Fprintf(&b, " · %s", resource.MIMEType)
		}
		b.WriteByte('\n')
	}
	for _, template := range templates {
		fmt.Fprintf(&b, "- %s · template=%s · %s", template.Server, template.URITemplate, firstNonEmpty(template.Title, template.Name))
		if template.MIMEType != "" {
			fmt.Fprintf(&b, " · %s", template.MIMEType)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func formatMCPPrompts(server string) string {
	prompts := mcp.ListPrompts(server)
	if len(prompts) == 0 {
		return "没有发现匹配的 MCP Prompt。"
	}
	var b strings.Builder
	for _, prompt := range prompts {
		fmt.Fprintf(&b, "- %s · %s · %s", prompt.Server, prompt.Name, firstNonEmpty(prompt.Title, prompt.Name))
		if prompt.Description != "" {
			fmt.Fprintf(&b, "\n  %s", prompt.Description)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func (m *AgentModel) formatLocalSkillList() string {
	var catalog *skills.SkillCatalog
	if m.ctrl != nil && m.ctrl.cfg.Agent != nil {
		catalog = m.ctrl.cfg.Agent.Catalog
	}
	disabled := config.GlobalConfig.EffectiveDisabledSkills()
	if catalog != nil {
		disabled = append([]string(nil), catalog.DisabledSkills...)
	}
	globalDisabled := false
	visibleDisabled := disabled[:0]
	for _, name := range disabled {
		if strings.TrimSpace(name) == "*" {
			globalDisabled = true
			continue
		}
		visibleDisabled = append(visibleDisabled, name)
	}
	disabled = visibleDisabled
	if (catalog == nil || len(catalog.Skills) == 0) && len(disabled) == 0 && !globalDisabled {
		return "当前没有发现 Skill。可使用 /skill find <关键词> 搜索 ClawHub 与 skills.sh。"
	}
	loaded := map[string]string{}
	if m.ctrl != nil && m.ctrl.cfg.Agent != nil && m.ctrl.cfg.Agent.State != nil && m.ctrl.cfg.Agent.State.LoadedSkills != nil {
		loaded = m.ctrl.cfg.Agent.State.LoadedSkills
	}
	var b strings.Builder
	activeCount := 0
	if catalog != nil {
		activeCount = len(catalog.Skills)
	}
	b.WriteString(fmt.Sprintf("可用 Skills：%d 个\n", activeCount))
	if globalDisabled {
		b.WriteString("Skill 功能：已全局禁用（使用 /skill on 恢复）\n")
	}
	if catalog != nil {
		for _, meta := range catalog.Skills {
			if !meta.UserInvocable {
				continue
			}
			flags := make([]string, 0, 2)
			if _, ok := loaded[meta.Name]; ok {
				flags = append(flags, "已加载")
			}
			if !meta.AllowImplicit {
				flags = append(flags, "仅显式调用")
			}
			flagText := ""
			if len(flags) > 0 {
				flagText = " [" + strings.Join(flags, " / ") + "]"
			}
			b.WriteString(fmt.Sprintf("- %s%s: %s\n", meta.Name, flagText, truncateStr(meta.Description, 140)))
		}
	}
	if len(disabled) > 0 {
		b.WriteString(fmt.Sprintf("\n已禁用 Skills：%d 个\n", len(disabled)))
		for _, name := range disabled {
			b.WriteString("- " + name + " [已禁用]\n")
		}
	}
	b.WriteString("\n查看: /skill list · 全局开关: /skill on|off · 单项启停: /skill on|off <name> · 仅启用一个: /skill only <name> · 查找市场: /skill find <关键词>")
	return strings.TrimSpace(b.String())
}

func (m *AgentModel) handleSkillSlash(arg string) tea.Cmd {
	fields, parseErr := splitSkillCommandFields(arg)
	if parseErr != nil {
		m.appendLine("error", "Skill 命令参数无效: "+parseErr.Error(), arg)
		return nil
	}
	args := map[string]string{"action": "status"}
	action := "status"
	if len(fields) > 0 {
		switch fields[0] {
		case "list":
			m.reloadSkillCatalog()
			message := m.formatLocalSkillList()
			m.appendResultLine(message, message)
			return nil
		case "rescan", "reload", "refresh":
			m.reloadSkillCatalog()
			return nil
		case "status":
			args["action"] = "status"
		case "load":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill load <skill-name>", arg)
				return nil
			}
			m.loadCurrentSkill(fields[1])
			return nil
		case "unload", "close":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill unload <skill-name>", arg)
				return nil
			}
			m.unloadCurrentSkill(fields[1])
			return nil
		case "off", "disable":
			if len(fields) == 1 {
				m.setSkillSystemDisabled(true)
				return nil
			}
			m.setSkillDisabled(fields[1], true)
			return nil
		case "on", "enable":
			if len(fields) == 1 {
				m.setSkillSystemDisabled(false)
				return nil
			}
			m.setSkillDisabled(fields[1], false)
			return nil
		case "only":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill only <skill-name>", arg)
				return nil
			}
			m.enableOnlySkill(fields[1])
			return nil
		case "find", "search":
			marketArgs, query := parseSkillMarketArgs(fields[1:])
			if query == "" {
				m.appendLine("error", "用法: /skill find <关键词> [market=all|clawhub|skills.sh] [limit=8]", arg)
				return nil
			}
			marketArgs["action"] = "search"
			marketArgs["query"] = query
			m.appendLine("info", "正在搜索 ClawHub / skills.sh（只读，不会安装）…", query)
			m.refreshViewport()
			return skillMarketCmd("search", marketArgs)
		case "inspect", "info":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill inspect <clawhub:slug|skills:owner/repo@skill>", arg)
				return nil
			}
			marketArgs, _ := parseSkillMarketArgs(fields[2:])
			marketArgs["action"] = "inspect"
			marketArgs["source"] = fields[1]
			m.appendLine("info", "正在检查 Skill 来源与安全状态…", fields[1])
			m.refreshViewport()
			return skillMarketCmd("inspect", marketArgs)
		case "install", "import":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill import </path/name.skill> [acknowledge-risk] [force]\n或: /skill install <source> [acknowledge-risk] [force]", arg)
				return nil
			}
			marketArgs, _ := parseSkillMarketArgs(fields[2:])
			marketArgs["action"] = "install"
			marketArgs["source"] = fields[1]
			marketArgs["confirm_install"] = "true"
			for _, flag := range fields[2:] {
				switch strings.ToLower(flag) {
				case "acknowledge-risk", "ack-risk":
					marketArgs["acknowledge_risk"] = "true"
				case "force", "--force":
					marketArgs["force"] = "true"
				}
			}
			if fields[0] == "import" || strings.HasSuffix(strings.ToLower(fields[1]), ".skill") || strings.HasPrefix(fields[1], "/") || strings.HasPrefix(fields[1], "~/") {
				m.appendLine("info", "正在读取本地 .skill，校验路径并静态审查；不会执行包内脚本…", fields[1])
			} else {
				m.appendLine("info", "正在下载并静态审查 Skill；不会执行其中脚本…", fields[1])
			}
			m.refreshViewport()
			return skillMarketCmd("install", marketArgs)
		case "managed", "installed":
			return skillMarketCmd("managed", map[string]string{"action": "managed"})
		case "updates", "check-updates", "outdated":
			marketArgs := map[string]string{"action": "check_updates"}
			if len(fields) > 1 {
				marketArgs["name"] = fields[1]
			}
			return skillMarketCmd("check_updates", marketArgs)
		case "update", "upgrade":
			marketArgs := map[string]string{"action": "update", "confirm_update": "true"}
			if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") && fields[1] != "acknowledge-risk" && fields[1] != "ack-risk" {
				marketArgs["name"] = fields[1]
			}
			for _, flag := range fields[1:] {
				if flag == "acknowledge-risk" || flag == "ack-risk" {
					marketArgs["acknowledge_risk"] = "true"
				}
			}
			m.appendLine("info", "正在检查并更新未冻结 Skill；旧版本会保留为可回滚备份…", arg)
			return skillMarketCmd("update", marketArgs)
		case "pin", "unpin":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill "+fields[0]+" <name>", arg)
				return nil
			}
			return skillMarketCmd(fields[0], map[string]string{"action": fields[0], "name": fields[1]})
		case "uninstall":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill uninstall <name>", arg)
				return nil
			}
			return skillMarketCmd("uninstall", map[string]string{"action": "uninstall", "name": fields[1], "confirm_remove": "true"})
		case "rollback", "restore":
			if len(fields) < 2 {
				m.appendLine("error", "用法: /skill rollback <name> [version|digest-prefix]", arg)
				return nil
			}
			marketArgs := map[string]string{"action": "rollback", "name": fields[1], "confirm_rollback": "true"}
			if len(fields) > 2 {
				marketArgs["version"] = fields[2]
			}
			return skillMarketCmd("rollback", marketArgs)
		case "audit", "check":
			return skillMarketCmd("audit", map[string]string{"action": "audit"})
		case "add":
			source := strings.TrimSpace(strings.TrimPrefix(arg, "add"))
			if source == "" {
				m.appendLine("error", "用法: /skill add /path/to/skills", arg)
				return nil
			}
			args = map[string]string{"action": "add_skill_source", "source": source}
			action = "add"
		case "source-off", "off-source":
			source := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
			if source == "" {
				m.appendLine("error", "用法: /skill source-off /path/to/skills", arg)
				return nil
			}
			args = map[string]string{"action": "disable_skill_source", "source": source}
			action = "source-off"
		case "source-on", "on-source":
			source := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
			if source == "" {
				m.appendLine("error", "用法: /skill source-on /path/to/skills", arg)
				return nil
			}
			args = map[string]string{"action": "enable_skill_source", "source": source}
			action = "source-on"
		case "remove", "rm":
			source := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
			if source == "" {
				m.appendLine("error", "用法: /skill remove /path/to/skills", arg)
				return nil
			}
			args = map[string]string{"action": "remove_skill_source", "source": source}
			action = "remove"
		default:
			m.appendLine("info", "用法: /skill list | on|off（全局） | on|off <name>（单项） | only <name>（仅启用一个） | load <name> | find|inspect|install|import <file.skill> | managed|updates|update|pin|unpin|uninstall|rollback|audit | add|source-off|source-on|remove <dir>", arg)
			return nil
		}
	}
	out, err := config.ManageConfig(args)
	if err != nil {
		m.appendLine("error", "Skill "+action+" 失败: "+err.Error(), err.Error())
		return nil
	}
	if m.ctrl != nil && m.ctrl.cfg.Agent != nil && m.ctrl.cfg.Agent.Catalog != nil {
		switch action {
		case "add", "source-off", "source-on", "remove":
			m.ctrl.cfg.Agent.Catalog.Sources = skills.ResolveSources(config.GlobalConfig.SkillSources, config.GlobalConfig.DisabledSkillSources)
		}
	}
	m.reloadSkillCatalog()
	message := out + "\nSkill 配置已立即刷新；当前会话已加载的有效 Skill 也已重读。"
	m.appendResultLine(message, message)
	return nil
}

// splitSkillCommandFields is a deliberately small, non-executing command-line
// lexer. It lets users drag a quoted .skill path containing spaces into the
// TUI without invoking a shell or expanding variables/globs.
func splitSkillCommandFields(input string) ([]string, error) {
	var fields []string
	var current strings.Builder
	var quote rune
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) {
			next := runes[i+1]
			if next == '\\' || next == '\'' || next == '"' || unicode.IsSpace(next) {
				current.WriteRune(next)
				i++
				continue
			}
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("引号未闭合")
	}
	flush()
	return fields, nil
}

func parseSkillMarketArgs(fields []string) (map[string]string, string) {
	args := map[string]string{}
	var text []string
	for _, field := range fields {
		if key, value, ok := strings.Cut(field, "="); ok {
			switch strings.ToLower(strings.TrimLeft(key, "-")) {
			case "market", "limit", "dest":
				args[strings.ToLower(strings.TrimLeft(key, "-"))] = value
				continue
			}
		}
		text = append(text, field)
	}
	return args, strings.TrimSpace(strings.Join(text, " "))
}

func skillMarketCmd(action string, args map[string]string) tea.Cmd {
	return func() tea.Msg {
		out, err := skills.ManageMarketplace(args)
		return skillMarketResultMsg{action: action, out: out, err: err}
	}
}

func (m *AgentModel) reloadSkillCatalog() {
	if m.ctrl == nil || m.ctrl.cfg.Agent == nil || m.ctrl.cfg.Agent.Catalog == nil {
		return
	}
	catalog := m.ctrl.cfg.Agent.Catalog
	if len(catalog.Sources) == 0 {
		catalog.Sources = skills.ResolveSources(config.GlobalConfig.SkillSources, config.GlobalConfig.DisabledSkillSources)
	}
	if err := catalog.ReloadWithDisabled(config.GlobalConfig.EffectiveDisabledSkills()); err != nil {
		m.appendLine("error", "Skill 目录刷新失败: "+err.Error(), err.Error())
		return
	}
	refreshed, unloaded := reconcileTUILoadedSkills(m.ctrl.cfg.Agent.State, catalog)
	detail := fmt.Sprintf("Skill 目录已刷新：%d 个可用。", len(catalog.Skills))
	if refreshed > 0 || unloaded > 0 {
		detail += fmt.Sprintf("已重载 %d，已移除失效/禁用 %d。", refreshed, unloaded)
	}
	m.appendLine("info", detail, "skill catalog refreshed")
}

func reconcileTUILoadedSkills(state *harness.AgentState, catalog *skills.SkillCatalog) (refreshed, unloaded int) {
	if state == nil || catalog == nil || len(state.LoadedSkills) == 0 {
		return 0, 0
	}
	for oldName := range state.LoadedSkills {
		meta, ok := catalog.FindSkill(oldName)
		if !ok {
			delete(state.LoadedSkills, oldName)
			unloaded++
			continue
		}
		content, err := skills.LoadSkillContent(*meta)
		if err != nil {
			delete(state.LoadedSkills, oldName)
			unloaded++
			continue
		}
		if oldName != meta.Name {
			delete(state.LoadedSkills, oldName)
		}
		state.LoadedSkills[meta.Name] = content
		refreshed++
	}
	return refreshed, unloaded
}

func (m *AgentModel) loadCurrentSkill(name string) {
	if m.ctrl == nil || m.ctrl.cfg.Agent == nil || m.ctrl.cfg.Agent.Catalog == nil {
		m.appendLine("error", "当前 Agent 没有可用 Skill 目录", name)
		return
	}
	if m.ctrl.cfg.Agent.Catalog.IsDisabled(name) {
		hint := "/skill on " + name
		if m.ctrl.cfg.Agent.Catalog.IsDisabled("*") {
			hint = "/skill on"
		}
		m.appendLine("error", "Skill 已被禁用: "+name+"；使用 "+hint+" 恢复", name)
		return
	}
	meta, ok := m.ctrl.cfg.Agent.Catalog.FindSkill(name)
	if !ok {
		m.appendLine("error", "未找到 Skill: "+name, name)
		return
	}
	if !meta.UserInvocable {
		m.appendLine("error", "Skill 不允许用户直接调用: "+name, name)
		return
	}
	content, err := skills.LoadSkillContent(*meta)
	if err != nil {
		m.appendLine("error", "加载 Skill 失败: "+err.Error(), err.Error())
		return
	}
	if m.ctrl.cfg.Agent.State.LoadedSkills == nil {
		m.ctrl.cfg.Agent.State.LoadedSkills = map[string]string{}
	}
	m.ctrl.cfg.Agent.State.LoadedSkills[meta.Name] = content
	message := fmt.Sprintf("已加载 Skill [%s] 到当前会话 (%d 字符)", meta.Name, len(content))
	m.appendResultLine(message, message)
}

func (m *AgentModel) unloadCurrentSkill(name string) {
	m.setSkillDisabled(name, true)
}

func (m *AgentModel) setSkillDisabled(name string, disabled bool) {
	name = strings.TrimSpace(name)
	action := "enable_skill"
	label := "启用"
	if disabled {
		action = "disable_skill"
		label = "禁用"
	}
	out, err := config.ManageConfig(map[string]string{"action": action, "name": name})
	if err != nil {
		m.appendLine("error", "Skill "+label+"失败: "+err.Error(), err.Error())
		return
	}
	// Reloading applies the denylist to discovery and reconciles LoadedSkills,
	// so unload/off works even when the Skill was not loaded in this session.
	m.reloadSkillCatalog()
	message := out + "\n已立即应用到当前会话，并持久化到 disabled_skills。"
	if !disabled && config.GlobalConfig.SkillsDisabled {
		message += "\n注意：Skill 功能仍处于全局关闭状态；还需执行 /skill on。"
	}
	m.appendResultLine(message, message)
}

func (m *AgentModel) setSkillSystemDisabled(disabled bool) {
	action := "enable_skills"
	label := "启用"
	if disabled {
		action = "disable_skills"
		label = "禁用"
	}
	out, err := config.ManageConfig(map[string]string{"action": action})
	if err != nil {
		m.appendLine("error", "Skill 全局"+label+"失败: "+err.Error(), err.Error())
		return
	}
	m.reloadSkillCatalog()
	message := out + "\n已立即应用到当前会话；单个名称的 disabled_skills 设置保持不变。"
	m.appendResultLine(message, message)
}

func (m *AgentModel) enableOnlySkill(name string) {
	name = strings.TrimSpace(name)
	if m.ctrl == nil || m.ctrl.cfg.Agent == nil || m.ctrl.cfg.Agent.Catalog == nil {
		m.appendLine("error", "当前 Agent 没有可用 Skill 目录", name)
		return
	}
	catalog := m.ctrl.cfg.Agent.Catalog
	fresh, err := skills.LoadCatalog(catalog.Sources)
	if err != nil {
		m.appendLine("error", "Skill 目录刷新失败: "+err.Error(), err.Error())
		return
	}
	selected, ok := fresh.FindSkill(name)
	if !ok {
		m.appendLine("error", "未找到 Skill: "+name+"；请先用 /skill list 查看准确名称", name)
		return
	}
	names := make([]string, 0, len(fresh.Skills))
	for _, meta := range fresh.Skills {
		names = append(names, meta.Name)
	}
	out, err := config.ManageConfig(map[string]string{
		"action":           "enable_only_skill",
		"name":             selected.Name,
		"available_skills": strings.Join(names, "\n"),
	})
	if err != nil {
		m.appendLine("error", "Skill only 失败: "+err.Error(), err.Error())
		return
	}
	m.reloadSkillCatalog()
	message := out + fmt.Sprintf("\n已立即应用到当前会话：保留 %s，禁用其他 %d 个已发现 Skill。", selected.Name, max(0, len(names)-1))
	m.appendResultLine(message, message)
}

func (m *AgentModel) startNewSession(goal string) tea.Cmd {
	if m.ctrl == nil {
		m.appendLine("error", "当前界面没有可重置的 Agent 控制器", "no controller")
		m.refreshViewport()
		return nil
	}
	if m.running {
		m.appendLine("error", "当前任务仍在运行；请先 Esc 停止，或直接输入新指令中途打断。", "running")
		m.refreshViewport()
		return nil
	}
	sessionID, hasGoal, err := m.ctrl.StartNewSession(goal)
	if err != nil {
		m.appendLine("error", "新建会话失败: "+err.Error(), err.Error())
		m.refreshViewport()
		return nil
	}

	m.lines = []logLine{}
	m.lineID = 0
	m.streamIdx = -1
	m.streamTick = false
	m.cmdOutputGroup = 0
	m.activeCmdGroup = 0
	m.running = false
	m.thinking = false
	m.currentStep = 0
	m.done = false
	m.awaitGoal = !hasGoal
	m.sessionLive = hasGoal
	m.autoStart = false
	m.autoScroll = true
	m.stopping = false
	m.pendingConfirm = nil
	m.pendingAsk = nil
	m.historyIdx = -1
	m.currentTarget = ""
	m.copyToast = ""
	m.tokenUsage = tokenStats{}
	m.trimmedLines = 0
	m.startupInfo.SessionID = sessionID
	m.startupInfo.AwaitGoal = !hasGoal
	m.startupInfo.StartedAt = time.Now().Format("2006-01-02 15:04:05")
	m.bannerCache = ""
	m.bannerCacheW = 0
	m.clearInputDraft()
	cancelInputCursorAnchor()

	if hasGoal {
		m.input.Blur()
		m.appendLine("info", fmt.Sprintf("已开启新会话 %s，并开始执行新任务。", shortSessionID(sessionID)), sessionID)
		m.appendLine("user", "You: "+summarizeIfNeeded(goal), goal)
		m.refreshViewport()
		if m.ctrl.beginRun() {
			return agentStartCmd(false)
		}
		m.appendLine("error", "Agent 仍在运行，请稍候", "busy")
		m.refreshViewport()
		return nil
	}

	m.input.Focus()
	m.appendLine("info", fmt.Sprintf("已开启新会话 %s。输入任务后 Enter 开始。", shortSessionID(sessionID)), sessionID)
	m.refreshViewport()
	m.scheduleInputCursorAnchor()
	return nil
}

func (m *AgentModel) clearView() {
	m.lines = []logLine{}
	m.lineID = 0
	m.streamIdx = -1
	m.cmdOutputGroup = 0
	m.activeCmdGroup = 0
	m.trimmedLines = 0
	m.appendLine("info", "已清空当前视图", "clear")
	m.refreshViewport()
}

func (m *AgentModel) inputFocused() bool {
	return m.input.Focused()
}

func (m AgentModel) currentInputValue() string {
	segments := make([]string, 0, len(m.draftParts)+1)
	for _, part := range m.draftParts {
		segments = append(segments, part.text)
	}
	segments = append(segments, decodeInputValue(m.input.Value()))
	return joinDraftSegments(segments)
}

// normalizeInputNewlines keeps pasted/user-authored text inert when it is
// rendered by the terminal. Some browser and editor clipboards still emit
// CR-only line endings. A bare CR is meaningful in command output (progress
// bars use it to redraw a line), but must be treated as a newline in user
// input or every HTML source line visually overwrites the previous one.
func normalizeInputNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func joinDraftSegments(segments []string) string {
	var b strings.Builder
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		if b.Len() > 0 && !endsWithWhitespace(b.String()) && !startsWithWhitespace(segment) {
			b.WriteByte('\n')
		}
		b.WriteString(segment)
	}
	return b.String()
}

func startsWithWhitespace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

func endsWithWhitespace(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsSpace(r)
}

func (m AgentModel) hasPasteBlocks() bool {
	for _, part := range m.draftParts {
		if part.pasted {
			return true
		}
	}
	return false
}

func draftImageBytes(images []analyzer.ImageAttachment) int64 {
	var total int64
	for _, image := range images {
		if image.Size > 0 {
			total += image.Size
		}
	}
	return total
}

func (m AgentModel) draftDisplayPrefix() string {
	if len(m.draftParts) == 0 && len(m.draftImages) == 0 {
		return ""
	}
	var rows []string
	for i, image := range m.draftImages {
		rows = append(rows, fmt.Sprintf("[图片 %d] %s · %.1f KiB", i+1, image.Name, float64(image.Size)/1024))
	}
	pasteIndex := 0
	for _, part := range m.draftParts {
		if part.pasted {
			pasteIndex++
			rows = append(rows, pasteSummary(part.text, pasteIndex))
			continue
		}
		if part.text != "" {
			rows = append(rows, part.text)
		}
	}
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\n") + "\n"
}

func (m AgentModel) submittedDraftSummary() string {
	if !m.hasPasteBlocks() && len(m.draftImages) == 0 {
		return ""
	}
	lines := make([]string, 0, len(m.draftParts)+1)
	for i, image := range m.draftImages {
		lines = append(lines, fmt.Sprintf("图片 %d：%s (%s, %.1f KiB)", i+1, image.Name, image.MediaType, float64(image.Size)/1024))
	}
	pasteIndex := 0
	for _, part := range m.draftParts {
		if part.pasted {
			pasteIndex++
			lines = append(lines, pasteSummary(part.text, pasteIndex)+" "+pastePreview(part.text))
			continue
		}
		if text := strings.TrimSpace(part.text); text != "" {
			lines = append(lines, "补充文字："+truncateStr(text, 240))
		}
	}
	if suffix := strings.TrimSpace(decodeInputValue(m.input.Value())); suffix != "" {
		lines = append(lines, "补充文字："+truncateStr(suffix, 240))
	}
	return strings.Join(lines, "\n")
}

func encodeInputValue(s string) string {
	s = normalizeInputNewlines(s)
	return strings.ReplaceAll(s, "\n", string(inputLineBreak))
}

func decodeInputValue(s string) string {
	return strings.ReplaceAll(s, string(inputLineBreak), "\n")
}

func (m *AgentModel) moveInputCursorLine(delta int) bool {
	if delta == 0 {
		return false
	}
	runes := []rune(m.input.Value())
	pos := m.input.Position()
	if pos < 0 {
		pos = 0
	}
	if pos > len(runes) {
		pos = len(runes)
	}
	width := ChromeContentWidth(m.width) - 2
	if width <= 0 {
		width = 78
	}
	type cursorPoint struct{ row, col int }
	points := make([]cursorPoint, len(runes)+1)
	row, col := 0, 0
	if prefix := m.draftDisplayPrefix(); prefix != "" {
		for _, r := range prefix {
			if r == '\n' || r == '\r' {
				row++
				col = 0
				continue
			}
			w := max(1, runewidth.RuneWidth(r))
			if col > 0 && col+w > width {
				row++
				col = 0
			}
			col += w
		}
	}
	for i := 0; i <= len(runes); i++ {
		cursorWidth := 1
		if i < len(runes) && runes[i] != inputLineBreak {
			cursorWidth = max(1, runewidth.RuneWidth(runes[i]))
		}
		cursorRow, cursorCol := row, col
		if cursorCol > 0 && cursorCol+cursorWidth > width {
			cursorRow++
			cursorCol = 0
		}
		points[i] = cursorPoint{row: cursorRow, col: cursorCol}
		if i == len(runes) {
			break
		}
		if runes[i] == inputLineBreak {
			row++
			col = 0
			continue
		}
		w := max(1, runewidth.RuneWidth(runes[i]))
		if col > 0 && col+w > width {
			row++
			col = 0
		}
		col += w
	}
	current := points[pos]
	targetRow := current.row + delta
	if targetRow < 0 {
		return false
	}
	best, bestDistance := -1, int(^uint(0)>>1)
	for i, point := range points {
		if point.row != targetRow {
			continue
		}
		distance := point.col - current.col
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance {
			best, bestDistance = i, distance
		}
	}
	if best < 0 || best == pos {
		return false
	}
	m.input.SetCursor(best)
	return true
}

func (m *AgentModel) clearInputDraft() {
	m.inputAllSelected = false
	m.draftParts = nil
	m.draftImages = nil
	m.input.SetValue("")
	m.input.SetCursor(0)
	m.slashSelected = 0
}

func (m *AgentModel) acceptPaste(text string) {
	text = normalizeInputNewlines(text)
	if text == "" {
		return
	}
	if m.inputAllSelected {
		m.clearInputDraft()
	}
	base := decodeInputValue(m.input.Value())
	cursor := m.input.Position()
	baseRunes := []rune(base)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(baseRunes) {
		cursor = len(baseRunes)
	}
	prefix := string(baseRunes[:cursor])
	tail := string(baseRunes[cursor:])
	if isLargePaste(text) {
		if prefix != "" {
			m.draftParts = append(m.draftParts, inputDraftPart{text: prefix})
		}
		m.draftParts = append(m.draftParts, inputDraftPart{text: text, pasted: true})
		m.input.SetValue(encodeInputValue(tail))
		m.input.SetCursor(0)
		m.slashSelected = 0
		return
	}
	textRunes := []rune(text)
	full := prefix + text + tail
	nextCursor := cursor + len(textRunes)
	m.input.SetValue(encodeInputValue(full))
	m.input.SetCursor(nextCursor)
	m.clampSlashSelection()
}

func (m *AgentModel) removeLastDraftPart() {
	if len(m.draftParts) == 0 {
		if len(m.draftImages) > 0 {
			m.draftImages = m.draftImages[:len(m.draftImages)-1]
		}
		return
	}
	last := m.draftParts[len(m.draftParts)-1]
	m.draftParts = m.draftParts[:len(m.draftParts)-1]
	if last.pasted || last.text == "" {
		return
	}
	runes := []rune(last.text)
	if len(runes) > 0 {
		runes = runes[:len(runes)-1]
	}
	m.input.SetValue(encodeInputValue(string(runes)))
	m.input.SetCursor(len([]rune(m.input.Value())))
}

func (m *AgentModel) appendInputNewline() {
	value := []rune(m.input.Value())
	pos := m.input.Position()
	if pos < 0 {
		pos = 0
	}
	if pos > len(value) {
		pos = len(value)
	}
	value = append(value[:pos], append([]rune{inputLineBreak}, value[pos:]...)...)
	m.input.SetValue(string(value))
	m.input.SetCursor(pos + 1)
	m.slashSelected = 0
}

func isLargePaste(s string) bool {
	s = normalizeInputNewlines(s)
	if s == "" {
		return false
	}
	// Match Claude Code: keep short clips (including a 2-line mask with a
	// trailing newline) inline; only collapse dumps that would blow the prompt.
	// See https://github.com/anthropics/claude-code/issues/85453
	lines := strings.Count(s, "\n") + 1
	return lines > largePasteMaxLines || len([]rune(s)) > largePasteMaxRunes
}

// toggleAllCollapsible treats e as a global two-state switch. If anything is
// collapsed it expands everything; only when everything is already expanded
// does it collapse everything. Command-output lines in one group count as one
// logical item, and an active reasoning stream is intentionally left visible.
func (m *AgentModel) toggleAllCollapsible() (count int, collapsed bool) {
	seenGroups := make(map[int]struct{})
	hasCollapsed := false

	for i := range m.lines {
		line := &m.lines[i]
		switch {
		case isCollapsibleUserLine(*line):
			count++
			hasCollapsed = hasCollapsed || line.collapsed
		case line.kind == "result" && line.raw != "" && line.raw != line.content:
			count++
			hasCollapsed = hasCollapsed || line.collapsed
		case line.kind == "cmdout" && line.group > 0:
			if _, seen := seenGroups[line.group]; !seen {
				seenGroups[line.group] = struct{}{}
				count++
			}
			hasCollapsed = hasCollapsed || line.collapsed
		case line.kind == "subagent_result":
			count++
			hasCollapsed = hasCollapsed || line.collapsed
		case line.kind == "stream" && line.settled:
			count++
			hasCollapsed = hasCollapsed || line.collapsed
		}
	}

	if count == 0 {
		return 0, false
	}

	collapsed = !hasCollapsed
	for i := range m.lines {
		line := &m.lines[i]
		switch {
		case isCollapsibleUserLine(*line):
			line.collapsed = collapsed
		case line.kind == "result" && line.raw != "" && line.raw != line.content:
			line.collapsed = collapsed
		case line.kind == "cmdout" && line.group > 0:
			line.collapsed = collapsed
		case line.kind == "subagent_result":
			line.collapsed = collapsed
		case line.kind == "stream" && line.settled:
			line.collapsed = collapsed
		}
	}
	return count, collapsed
}

func isCollapsibleUserLine(line logLine) bool {
	return line.kind == "user" && line.raw != "" && "You: "+line.raw != line.content
}

// toggleLastCollapsible is retained for package-level compatibility. The UI
// now deliberately applies the global toggle behavior to every collapsible.
func (m *AgentModel) toggleLastCollapsible() {
	m.toggleAllCollapsible()
}

func (m *AgentModel) lastStreamLineID() int {
	for i := len(m.lines) - 1; i >= 0; i-- {
		if m.lines[i].kind == "stream" {
			return m.lines[i].id
		}
	}
	return 0
}

func settlesCompletedStream(kind harness.EventKind) bool {
	switch kind {
	case harness.EventStepStart, harness.EventThought, harness.EventAction, harness.EventResult,
		harness.EventError, harness.EventFinish, harness.EventCheckpoint, harness.EventAwaitUser,
		harness.EventSubAgentStart, harness.EventSubAgentStep, harness.EventSubAgentAction,
		harness.EventSubAgentResult, harness.EventTargetStatus:
		return true
	default:
		return false
	}
}

func (m *AgentModel) settleCompletedStreams() {
	for i := len(m.lines) - 1; i >= 0; i-- {
		if m.lines[i].kind != "stream" {
			continue
		}
		if m.lines[i].complete {
			m.lines[i].settled = true
		}
		return
	}
}

func (m *AgentModel) collapseCommandOutputGroup(group int) {
	if group <= 0 {
		return
	}
	headSet := false
	for i := range m.lines {
		if m.lines[i].kind == "cmdout" && m.lines[i].group == group {
			m.lines[i].collapsed = true
			if !headSet {
				m.lines[i].groupHead = true
				headSet = true
			} else {
				m.lines[i].groupHead = false
			}
		}
	}
}

func (m *AgentModel) commandOutputGroupStats(group int) (lines, chars int, first string) {
	for _, line := range m.lines {
		if line.kind != "cmdout" || line.group != group {
			continue
		}
		lines++
		chars += len(line.raw)
		if first == "" {
			first = strings.TrimSpace(line.content)
		}
	}
	return lines, chars, first
}

func (m *AgentModel) applyEvent(e harness.UIEvent) {
	switch e.Kind {
	case harness.EventStepStart:
		m.currentStep = e.Step
		m.maxSteps = e.MaxSteps
		m.appendLine("step", fmt.Sprintf("Step %d / %d", e.Step, e.MaxSteps), e.Message)
	case harness.EventThinking:
		m.thinking = true
		m.streamIdx = -1
	case harness.EventStreamDelta:
		m.thinking = false
		m.appendStreamDelta(e.Message)
	case harness.EventStreamEnd:
		m.finalizeStream(e.Detail)
	case harness.EventThought:
		m.appendThoughtLine(e.Message)
	case harness.EventTokenUsage:
		m.recordTokenUsage(e)
	case harness.EventAction:
		if e.Action != nil && e.Action.Type == harness.ActionTodo {
			line := harness.FormatTodoList(e.Action.Todos)
			m.appendLine("todo", line, line)
			break
		}
		if e.Action != nil && e.Action.Type == harness.ActionExecute {
			m.startCommandOutputGroup()
		}
		kind := "tool"
		line := FormatActionLine(e.Action)
		if e.Action != nil && e.Action.Type == harness.ActionTask {
			kind = "subagent_start"
			if len(e.Action.ParallelTasks) > 0 {
				line = FormatActionLine(e.Action)
			} else if strings.TrimSpace(e.Action.TaskName) == "" || strings.TrimSpace(e.Action.TaskPrompt) == "" {
				line = FormatActionLine(e.Action)
			} else {
				line = fmt.Sprintf("Sub-agent · %s → %s", e.Action.TaskName, truncateStr(e.Action.TaskPrompt, 60))
			}
		}
		if line != "" {
			m.appendLine(kind, line, line)
		}
	case harness.EventSubAgentStart:
		m.noteTarget(e)
		m.appendLine("subagent_start", fmt.Sprintf("Sub-agent · %s%s", e.Message, tuiTargetSuffix(e)), e.Detail)
	case harness.EventSubAgentStep:
		m.noteTarget(e)
		m.appendLine("subagent_start", "Sub-agent · "+e.Message+tuiTargetSuffix(e), e.Detail)
	case harness.EventSubAgentAction:
		m.noteTarget(e)
		action := e.Action
		if action != nil && (e.TargetName != "" || e.TargetProtocol != "" || e.TargetHost != "") {
			copyAction := *action
			if strings.TrimSpace(copyAction.TargetProtocol) != "local" {
				if copyAction.TargetName == "" {
					copyAction.TargetName = e.TargetName
				}
				if copyAction.TargetProtocol == "" {
					copyAction.TargetProtocol = e.TargetProtocol
				}
				if copyAction.TargetHost == "" {
					copyAction.TargetHost = e.TargetHost
				}
			}
			action = &copyAction
		}
		line := FormatActionLine(action)
		if line == "" {
			line = e.Message
		}
		m.appendLine("tool", "Sub-agent · "+line+tuiTargetSuffix(e), line)
	case harness.EventSubAgentResult:
		m.noteTarget(e)
		m.appendLine("subagent_result", truncateLines(e.Detail, 3), e.Detail)
	case harness.EventTargetStatus:
		m.noteTarget(e)
		status := e.Status
		if status == "" {
			status = "info"
		}
		m.appendLine("target", fmt.Sprintf("[%s] %s%s %s", status, e.Message, tuiTargetSuffix(e), e.Detail), e.Detail)
	case harness.EventResult:
		if strings.Contains(e.Message, "任务清单") || strings.Contains(e.Detail, "任务清单") {
			// 完整清单已在 EventAction 展示，避免重复
			break
		}
		if strings.Contains(e.Detail, "子 Agent") || strings.Contains(e.Message, "子 Agent") {
			m.appendLine("subagent_result", truncateLines(e.Message, 3), e.Detail)
		} else {
			m.appendResultLine(e.Message, e.Detail)
		}
	case harness.EventCommandOutput:
		m.appendCommandOutput(e.Message)
	case harness.EventError, harness.EventCheckpoint:
		m.appendLine("error", e.Message, e.Message)
	case harness.EventAwaitUser:
		prompt := e.Message
		options := []string(nil)
		if e.Action != nil {
			if strings.TrimSpace(e.Action.Question) != "" {
				prompt = e.Action.Question
			}
			options = e.Action.Options
		}
		m.appendAskLine(prompt, options)
	case harness.EventInfo, harness.EventRiskAuto, harness.EventBatchAuto, harness.EventDenied:
		m.appendLine("info", e.Message, e.Message)
	case harness.EventFinish:
		m.appendLine("success", e.Message, e.Message)
		if e.Detail != "" {
			m.appendLine("info", "审计: "+e.Detail, e.Detail)
		}
	}
}

func (m *AgentModel) recordTokenUsage(e harness.UIEvent) {
	total := e.TotalTokens
	if total <= 0 {
		total = e.PromptTokens + e.CompletionTokens
	}
	if total <= 0 {
		return
	}
	m.tokenUsage.PromptTokens += e.PromptTokens
	m.tokenUsage.CompletionTokens += e.CompletionTokens
	m.tokenUsage.TotalTokens += total
	m.tokenUsage.Calls++
}

func (m *AgentModel) noteTarget(e harness.UIEvent) {
	label := targetLabel(e.TargetName, e.TargetProtocol, e.TargetHost)
	if label != "" {
		m.currentTarget = label
	}
}

func tuiTargetSuffix(e harness.UIEvent) string {
	label := targetLabel(e.TargetName, e.TargetProtocol, e.TargetHost)
	if label == "" {
		return ""
	}
	return " @ " + label
}

func targetLabel(name, proto, host string) string {
	if name == "" && host == "" && proto == "" {
		return ""
	}
	label := name
	if label == "" {
		label = host
	}
	if proto != "" && host != "" {
		return fmt.Sprintf("%s (%s %s)", label, proto, host)
	}
	if proto != "" {
		return fmt.Sprintf("%s (%s)", label, proto)
	}
	return label
}

func (m *AgentModel) appendStreamDelta(delta string) {
	if delta == "" {
		return
	}
	if m.streamIdx < 0 || m.streamIdx >= len(m.lines) {
		m.lineID++
		m.lines = append(m.lines, logLine{kind: "stream", content: streamDisplay(delta, false), raw: delta, step: m.currentStep, id: m.lineID, at: time.Now()})
		m.streamIdx = len(m.lines) - 1
	} else {
		ln := &m.lines[m.streamIdx]
		ln.raw += delta
		ln.content = streamDisplay(ln.raw, false)
	}
}

func (m *AgentModel) finalizeStream(full string) {
	if m.streamIdx >= 0 && m.streamIdx < len(m.lines) {
		ln := &m.lines[m.streamIdx]
		if strings.TrimSpace(full) != "" {
			ln.raw = full
		}
		ln.content = streamDisplay(ln.raw, true)
		ln.complete = true
	}
	m.streamIdx = -1
}

func (m *AgentModel) collapseStreamLine(id int) {
	for i := range m.lines {
		if m.lines[i].id == id && m.lines[i].kind == "stream" && m.lines[i].settled {
			m.lines[i].collapsed = true
			return
		}
	}
}

func streamDisplay(raw string, done bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if done {
			return "AI 思考完成"
		}
		return "AI 正在思考..."
	}

	if action := parseStreamAction(raw); action != nil {
		if strings.TrimSpace(action.Thought) != "" {
			return "思考: " + action.Thought
		}
		switch action.Type {
		case harness.ActionFinish:
			if strings.TrimSpace(action.FinalReport) != "" {
				return "正在整理最终报告..."
			}
			return "准备完成任务..."
		case harness.ActionExecute:
			if action.Command != "" {
				return "准备执行 Shell: " + action.Command
			}
		case harness.ActionTool:
			if action.ToolName != "" {
				return "准备调用工具: " + action.ToolName
			}
		case harness.ActionTask:
			if action.TaskName != "" {
				return "准备委派子 Agent: " + action.TaskName
			}
			if len(action.ParallelTasks) > 0 {
				return fmt.Sprintf("准备并行委派 %d 个子 Agent", len(action.ParallelTasks))
			}
		case harness.ActionTodo:
			return "正在更新任务清单..."
		case harness.ActionAskUser:
			return "需要用户补充信息..."
		case harness.ActionReadFile:
			return "准备读取文件: " + action.Path
		case harness.ActionGrep:
			return "准备搜索: " + action.Pattern
		case harness.ActionLS:
			return "准备列目录: " + action.Path
		}
	}

	if thought := extractJSONStringField(raw, "thought"); thought != "" {
		return "思考: " + thought
	}
	if finalReport := extractJSONStringField(raw, "final_report"); finalReport != "" {
		return "正在整理最终报告..."
	}
	if action := extractJSONStringField(raw, "action"); action != "" {
		return "正在生成动作: " + action
	}
	if done {
		return "AI 思考完成"
	}
	return "AI 正在思考..."
}

func parseStreamAction(raw string) *harness.AgentAction {
	var action harness.AgentAction
	if err := json.Unmarshal([]byte(raw), &action); err == nil {
		return &action
	}
	return nil
}

func extractJSONStringField(raw, field string) string {
	marker := `"` + field + `":`
	idx := strings.Index(raw, marker)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(raw[idx+len(marker):])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	var b strings.Builder
	escaped := false
	for _, r := range rest {
		if escaped {
			switch r {
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			case 'r':
				b.WriteRune('\r')
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func (m *AgentModel) appendLine(kind, display, raw string) {
	m.lineID++
	m.lines = append(m.lines, logLine{kind: kind, content: display, raw: raw, step: m.currentStep, id: m.lineID, collapsed: kind == "subagent_result", at: time.Now()})
	m.trimLogLines()
}

// appendThoughtLine suppresses provider/runtime duplicate thought events. Some
// streaming-compatible APIs repeat the final thought once as a completed
// message; rendering both copies looks like a broken terminal even though the
// underlying event stream is the source of the duplication.
func (m *AgentModel) appendThoughtLine(message string) {
	message = strings.TrimSpace(sanitizeTUIText(message))
	if message == "" {
		return
	}
	if len(m.lines) > 0 {
		last := m.lines[len(m.lines)-1]
		if last.kind == "thought" && last.step == m.currentStep && strings.TrimSpace(last.raw) == message {
			return
		}
	}
	m.appendLine("thought", message, message)
}

func (m *AgentModel) restoreConversationHistory(history []analyzer.Message) {
	if len(history) == 0 {
		return
	}
	restored := 0
	for _, message := range history {
		content := strings.TrimSpace(message.Content)
		switch message.Role {
		case "user":
			if !isRestorableUserMessage(content) {
				continue
			}
			content = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(content, "需求："), "需求:"))
			m.appendRestoredLine("user", "You: "+summarizeIfNeeded(content), content)
			restored++
		case "assistant":
			var action harness.AgentAction
			if json.Unmarshal([]byte(content), &action) != nil {
				continue
			}
			switch {
			case action.Type == harness.ActionFinish || action.IsFinished:
				report := strings.TrimSpace(action.FinalReport)
				if report == "" {
					report = strings.TrimSpace(action.Thought)
				}
				if report != "" {
					m.appendRestoredLine("success", report, report)
					restored++
				}
			case action.Type == harness.ActionAskUser && strings.TrimSpace(action.Question) != "":
				prompt := renderAskPrompt(action.Question, action.Options)
				m.appendRestoredLine("ask", prompt, action.Question)
				restored++
			}
		}
	}
	if restored > 0 {
		m.lines = append([]logLine{{
			kind:    "info",
			content: fmt.Sprintf("↻ 已恢复 %d 条历史对话记录（工具回灌与系统控制消息已隐藏）", restored),
			raw:     "restored conversation",
			id:      0,
		}}, m.lines...)
	}
}

func isRestorableUserMessage(content string) bool {
	if content == "" {
		return false
	}
	for _, prefix := range []string{"Output:", "系统警告:", "【系统】", "上一步执行失败:", "用户拒绝执行"} {
		if strings.HasPrefix(content, prefix) {
			return false
		}
	}
	return true
}

func (m *AgentModel) appendRestoredLine(kind, display, raw string) {
	m.lineID++
	m.lines = append(m.lines, logLine{kind: kind, content: display, raw: raw, id: m.lineID})
	m.trimLogLines()
}

func (m *AgentModel) trimLogLines() {
	const maxLogLines = 1000
	if len(m.lines) <= maxLogLines {
		return
	}
	for len(m.lines) > maxLogLines {
		removeAt := -1
		for i := range m.lines {
			if !preserveConversationLine(m.lines[i].kind) {
				removeAt = i
				break
			}
		}
		// User dialogue is more valuable than a strict memory cap. If a very
		// long session only contains conversational records, keep it intact.
		if removeAt < 0 {
			break
		}
		m.removeLogLine(removeAt)
		m.trimmedLines++
	}

	// Trimming can remove the original head of a command-output group. Mark
	// the first retained row as its new head so collapse/expand never makes a
	// long tool result disappear completely.
	seenGroups := make(map[int]bool)
	for i := range m.lines {
		if m.lines[i].kind != "cmdout" || m.lines[i].group <= 0 {
			continue
		}
		m.lines[i].groupHead = !seenGroups[m.lines[i].group]
		seenGroups[m.lines[i].group] = true
	}
}

func preserveConversationLine(kind string) bool {
	switch kind {
	case "user", "ask", "success", "error", "confirm_result":
		return true
	default:
		return false
	}
}

func (m *AgentModel) removeLogLine(index int) {
	if index < 0 || index >= len(m.lines) {
		return
	}
	copy(m.lines[index:], m.lines[index+1:])
	m.lines = m.lines[:len(m.lines)-1]
	switch {
	case m.streamIdx == index:
		m.streamIdx = -1
	case m.streamIdx > index:
		m.streamIdx--
	}
}

func (m *AgentModel) startCommandOutputGroup() {
	m.cmdOutputGroup++
	m.activeCmdGroup = m.cmdOutputGroup
}

func (m *AgentModel) appendCommandOutputLine(display, raw string) {
	if m.activeCmdGroup <= 0 {
		m.startCommandOutputGroup()
	}
	group := m.activeCmdGroup
	head := true
	for i := len(m.lines) - 1; i >= 0; i-- {
		if m.lines[i].kind == "cmdout" && m.lines[i].group == group {
			head = false
			break
		}
		if m.lines[i].kind == "tool" || m.lines[i].kind == "step" {
			break
		}
	}
	m.lineID++
	m.lines = append(m.lines, logLine{kind: "cmdout", content: display, raw: raw, step: m.currentStep, id: m.lineID, group: group, groupHead: head, at: time.Now()})
	m.trimLogLines()
}

func (m *AgentModel) appendCommandOutput(raw string) {
	clean := strings.TrimRight(sanitizeTUIText(raw), "\n")
	if strings.TrimSpace(clean) == "" {
		return
	}
	for _, line := range strings.Split(clean, "\n") {
		// Preserve meaningful indentation, but do not fill the viewport with
		// empty progress frames emitted by interactive CLI tools.
		if strings.TrimSpace(line) == "" {
			continue
		}
		m.appendCommandOutputLine(line, line)
	}
}

func (m *AgentModel) appendResultLine(display, detail string) {
	display = strings.TrimSpace(sanitizeTUIText(display))
	detail = strings.TrimSpace(sanitizeTUIText(detail))
	if detail == "" {
		detail = display
	}
	m.appendLine("result", display, detail)
	if detail != display && (strings.Count(detail, "\n") >= 8 || runewidth.StringWidth(detail) > 500) {
		m.lines[len(m.lines)-1].collapsed = true
	}
}

func (m *AgentModel) appendAskLine(prompt string, options []string) {
	prompt = strings.TrimSpace(sanitizeTUIText(prompt))
	if prompt == "" {
		return
	}
	// Keep raw intact for the Agent/checkpoint, but avoid showing an identical
	// adjacent paragraph twice when a model repeats the question while forming
	// an ask action.
	rendered := renderAskPrompt(dedupeAdjacentDisplayLines(prompt), options)
	now := time.Now()
	for i := len(m.lines) - 1; i >= 0; i-- {
		line := m.lines[i]
		if line.kind != "ask" || strings.TrimSpace(line.raw) != prompt {
			continue
		}
		sameStep := line.step == m.currentStep
		recentDuplicate := !line.at.IsZero() && now.Sub(line.at) <= 5*time.Second
		if !sameStep && !recentDuplicate {
			continue
		}
		// EventAwaitUser and askMsg travel through different queues. Coalesce
		// them even when a thought event lands between them, then keep the single
		// question at the end immediately above its answer input.
		line.content = rendered
		line.step = m.currentStep
		m.removeLogLine(i)
		m.lines = append(m.lines, line)
		return
	}
	m.appendLine("ask", rendered, prompt)
}

func dedupeAdjacentDisplayLines(text string) string {
	lines := strings.Split(normalizeInputNewlines(text), "\n")
	out := make([]string, 0, len(lines))
	lastNonEmpty := ""
	for _, line := range lines {
		normalized := strings.Join(strings.Fields(line), " ")
		if normalized != "" && normalized == lastNonEmpty {
			continue
		}
		out = append(out, line)
		if normalized == "" {
			lastNonEmpty = ""
		} else {
			lastNonEmpty = normalized
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (m *AgentModel) appendConfirmLine(prompt string) {
	prompt = strings.TrimSpace(sanitizeTUIText(prompt))
	if prompt == "" {
		prompt = "请确认是否执行此操作。"
	}
	if len(m.lines) > 0 {
		last := m.lines[len(m.lines)-1]
		if last.kind == "confirm" && strings.TrimSpace(last.raw) == prompt {
			return
		}
	}
	m.appendLine("confirm", prompt, prompt)
}

func (m *AgentModel) resolveLastConfirm(decision approvalDecision) {
	for i := len(m.lines) - 1; i >= 0; i-- {
		if m.lines[i].kind != "confirm" {
			continue
		}
		m.lines[i].kind = "confirm_result"
		m.lines[i].content = "✗ 已拒绝高风险操作"
		switch decision {
		case approvalAllowOnce:
			m.lines[i].content = "✓ 已批准本次操作"
		case approvalAllowAllSession:
			m.lines[i].content = "✓ 已允许本次会话所有高危操作"
		case approvalAllowSession:
			m.lines[i].content = "✓ 已允许本会话同类操作"
		}
		return
	}
}

func renderAskPrompt(prompt string, options []string) string {
	prompt = strings.TrimSpace(sanitizeTUIText(prompt))
	if len(options) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	wroteHeading := false
	for i, opt := range options {
		opt = strings.TrimSpace(sanitizeTUIText(opt))
		if opt == "" {
			continue
		}
		if !wroteHeading {
			b.WriteString("\n\n### 可选项")
			wroteHeading = true
		}
		b.WriteString(fmt.Sprintf("\n%d. %s", i+1, opt))
	}
	return strings.TrimSpace(b.String())
}

func (m *AgentModel) refreshViewport() {
	// Bubble Tea normally repaints only rows whose strings changed. Complex
	// wide glyphs, styled borders and fast viewport/footer movement can leave a
	// terminal's physical rows out of sync with that cache. A private marker on
	// every row makes the next content refresh an atomic full-frame repaint;
	// inputCursorOutput strips the marker before stdout, so it cannot affect
	// width calculations or the macOS IME cursor anchor.
	m.frameVersion++
	shouldStick := m.autoScroll || m.viewport.AtBottom()
	var b strings.Builder
	contentW := max(4, m.viewport.Width)
	if !m.startupInfo.isEmpty() {
		b.WriteString(m.cachedWelcomeBanner(contentW))
	}
	if m.trimmedLines > 0 {
		notice := fmt.Sprintf("… 已精简 %d 条旧工具/思考日志，用户对话、询问和最终结论已保留", m.trimmedLines)
		b.WriteString(renderWrapped(styleInfo, notice, contentW) + "\n\n")
	}
	for _, ln := range m.lines {
		ts := lineTimestampPlain(ln)
		switch ln.kind {
		case "step":
			b.WriteString(renderWrapped(styleStep, ts+"▸ "+ln.content, contentW) + "\n")
		case "user":
			// Normalize again while rendering so checkpoints created by older
			// versions cannot reintroduce terminal-active carriage returns.
			body := normalizeInputNewlines(ln.content)
			if isCollapsibleUserLine(ln) {
				if ln.collapsed {
					body += "  [e 全部展开原文]"
				} else {
					body = "You: " + normalizeInputNewlines(ln.raw) + "\n[e 全部折叠]"
				}
			}
			b.WriteString(renderWrapped(styleAccent, ts+"▸ "+body, contentW) + "\n\n")
		case "thought":
			b.WriteString(renderWrapped(styleThought, ts+"💭 "+ln.content, contentW) + "\n\n")
		case "stream":
			body := ln.content
			if ln.collapsed {
				body = "AI 思考已完成  [e 全部展开]"
			} else if ln.settled && strings.TrimSpace(body) != "" {
				body += "\n[e 全部折叠]"
			}
			b.WriteString(renderWrapped(styleStream, ts+"▸ "+body, contentW) + "\n\n")
		case "todo":
			b.WriteString(renderWrapped(styleTodoBox, ts+ln.content, contentW) + "\n\n")
		case "tool":
			b.WriteString(renderWrapped(styleToolBox, ts+"⚡ "+ln.content, contentW) + "\n\n")
		case "target":
			b.WriteString(renderWrapped(styleTargetBox, ts+"📡 "+ln.content, contentW) + "\n\n")
		case "subagent_start":
			b.WriteString(renderWrapped(styleSubAgentBox, ts+"🔀 "+ln.content, contentW) + "\n")
		case "subagent_result":
			body := ln.raw
			if ln.collapsed {
				body = ln.content + "  [e 全部展开]"
			} else {
				body += "\n[e 全部折叠]"
			}
			b.WriteString(renderWrapped(styleSubAgentResult, ts+"📦 "+body, contentW) + "\n\n")
		case "result":
			body := ln.raw
			if body == "" {
				body = ln.content
			}
			if ln.raw != "" && ln.raw != ln.content {
				if ln.collapsed {
					body = ln.content + "  [e 全部展开完整结果]"
				} else {
					body += "\n[e 全部折叠]"
				}
			}
			b.WriteString(renderWrapped(styleResult, ts+"└ "+body, contentW) + "\n\n")
		case "cmdout":
			if ln.collapsed {
				if !ln.groupHead {
					continue
				}
				lines, chars, first := m.commandOutputGroupStats(ln.group)
				summary := fmt.Sprintf("命令输出已折叠：%d 行 / %d 字符", lines, chars)
				if first != "" {
					summary += " · " + truncateStr(first, min(80, contentW/2))
				}
				b.WriteString(renderWrapped(styleResult, ts+"└ "+summary+"  [e 全部展开]", contentW) + "\n\n")
			} else {
				b.WriteString(renderWrapped(styleResult, ts+"└ "+ln.content, contentW) + "\n")
			}
		case "ask":
			b.WriteString(renderMarkdownAsk(ln.content, ts, contentW) + "\n\n")
		case "confirm":
			b.WriteString(renderMarkdownConfirm(ln.content, ts, contentW) + "\n\n")
		case "confirm_result":
			style := styleError
			if strings.HasPrefix(ln.content, "✓") {
				style = styleSuccess
			}
			b.WriteString(renderWrapped(style, ts+ln.content, contentW) + "\n\n")
		case "error":
			b.WriteString(renderWrapped(styleError, ts+"✗ "+ln.content, contentW) + "\n")
		case "success":
			b.WriteString(renderWrapped(styleSuccess, ts+"✓ 报告", contentW) + "\n")
			b.WriteString(renderMarkdownReport(ln.content, contentW) + "\n\n")
		default:
			b.WriteString(renderWrapped(styleInfo, ts+ln.content, contentW) + "\n")
		}
	}
	if m.thinking {
		b.WriteString("\n" + m.spinner.View() + styleInfo.Render(" Thinking..."))
	}
	m.viewportPlain = stripANSI(b.String())
	m.viewport.SetContent(b.String())
	if shouldStick {
		m.viewport.GotoBottom()
	}
}

func (m *AgentModel) returnToLiveTail() {
	m.autoScroll = true
	m.viewport.GotoBottom()
}

func lineTimestampPlain(ln logLine) string {
	if ln.at.IsZero() {
		return ""
	}
	return "[" + ln.at.Format("15:04:05") + "] "
}

func (m AgentModel) renderHeader(w int) string {
	contentW := max(1, w-styleHeader.GetHorizontalFrameSize())
	left := sanitizeTUIText(fmt.Sprintf("DeepSentry Agent  │  %s", m.title))
	if contentW < 48 {
		return styleHeader.Width(w).Render(runewidth.Truncate(left, contentW, "…"))
	}
	rightMax := max(18, contentW/2)
	if contentW >= 100 {
		rightMax = min(contentW-24, (contentW*2)/3)
	}
	right := sanitizeTUIText(m.headerStatsText(rightMax))
	if right == "" {
		return styleHeader.Width(w).Render(runewidth.Truncate(left, contentW, "…"))
	}

	right = runewidth.Truncate(right, rightMax, "…")
	leftW := contentW - lipgloss.Width(right) - 1
	leftW = max(1, leftW)
	left = runewidth.Truncate(left, leftW, "…")
	gap := contentW - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return styleHeader.Width(w).Render(left + strings.Repeat(" ", gap) + right)
}

func (m AgentModel) headerStatsText(maxWidth int) string {
	if m.ctrl == nil {
		return ""
	}
	stats := m.ctrl.Stats()
	return formatHeaderStats(stats, m.running || stats.Running, m.tokenUsageLabel(stats), maxWidth)
}

func formatHeaderStats(stats SessionStats, running bool, tokenLabel string, maxWidth int) string {
	state := "idle"
	if running {
		state = "run"
	}
	fullSID := headerSessionID(stats.SessionID, 0)
	midSID := headerSessionID(stats.SessionID, 12)
	shortSID := shortSessionID(stats.SessionID)
	if shortSID == "" {
		shortSID = "sid -"
	}
	tokenCompact := strings.TrimSuffix(tokenLabel, " tok")
	candidates := []string{
		fmt.Sprintf("会话 %s · 状态 %s · 轮次 %d · 消息 %d · token %s", fullSID, state, stats.Turns, stats.Messages, tokenCompact),
		fmt.Sprintf("会话 %s · 状态 %s · 轮次 %d · 消息 %d · token %s", midSID, state, stats.Turns, stats.Messages, tokenCompact),
		fmt.Sprintf("%s · %s · 轮次 %d · 消息 %d · %s", midSID, state, stats.Turns, stats.Messages, tokenLabel),
		fmt.Sprintf("%s · %s · 轮%d · 消息%d · %s", shortSID, state, stats.Turns, stats.Messages, tokenLabel),
	}
	for _, text := range candidates {
		if maxWidth <= 0 || lipgloss.Width(text) <= maxWidth {
			return text
		}
	}
	return runewidth.Truncate(candidates[len(candidates)-1], maxWidth, "…")
}

func (m AgentModel) sessionStatsText(verbose bool) string {
	if m.ctrl == nil {
		return "会话统计不可用"
	}
	stats := m.ctrl.Stats()
	if !verbose {
		return fmt.Sprintf("会话=%s · 轮次=%d · 消息=%d · %s", shortSessionID(stats.SessionID), stats.Turns, stats.Messages, m.tokenUsageLabel(stats))
	}
	if m.tokenUsage.TotalTokens > 0 {
		return fmt.Sprintf("会话: %s · 用户轮次: %d · 历史消息: %d · 真实 token: %s total（prompt %s / completion %s / calls %d）",
			firstNonEmpty(stats.SessionID, "-"),
			stats.Turns,
			stats.Messages,
			formatApproxTokens(m.tokenUsage.TotalTokens),
			formatApproxTokens(m.tokenUsage.PromptTokens),
			formatApproxTokens(m.tokenUsage.CompletionTokens),
			m.tokenUsage.Calls,
		)
	}
	return fmt.Sprintf("会话: %s · 用户轮次: %d · 历史消息: %d · 上下文估算: ~%s tok（当前模型响应未返回 usage，按本地历史粗略估算）",
		firstNonEmpty(stats.SessionID, "-"),
		stats.Turns,
		stats.Messages,
		formatApproxTokens(stats.ApproxTokens),
	)
}

func (m AgentModel) statusContextText() string {
	if strings.TrimSpace(m.currentTarget) != "" {
		return m.currentTarget
	}
	switch {
	case m.pendingConfirm != nil:
		return "等待确认"
	case m.pendingAsk != nil:
		return "等待补充"
	case m.stopping:
		return "正在停止"
	case m.running:
		if m.thinking {
			return "模型思考中"
		}
		return "执行中"
	case m.done || m.sessionLive:
		return "就绪/可继续"
	default:
		return "就绪/等待任务"
	}
}

func (m AgentModel) tokenUsageLabel(stats SessionStats) string {
	if m.tokenUsage.TotalTokens > 0 {
		return fmt.Sprintf("%s tok", formatApproxTokens(m.tokenUsage.TotalTokens))
	}
	return fmt.Sprintf("~%s tok", formatApproxTokens(stats.ApproxTokens))
}

func shortSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	parts := strings.Split(id, "_")
	tail := parts[len(parts)-1]
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	return "sid " + tail
}

func headerSessionID(id string, maxTail int) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "sid -"
	}
	id = strings.TrimPrefix(id, "session_")
	if maxTail > 0 && len(id) > maxTail {
		id = id[len(id)-maxTail:]
	}
	return "sid " + id
}

func formatApproxTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}

func (m AgentModel) View() string {
	if m.quitting {
		m.hideInputCursorAnchor()
		return ""
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	// View receives a model value, so it is safe to normalize and recompute the
	// layout here. Event handlers still recalculate eagerly, but the final
	// render must not trust a stale viewport height: a missed/delayed resize or
	// a footer whose height changed between events would otherwise make
	// MaxHeight silently cut the input box off the bottom of the terminal.
	m.width, m.height = w, h
	renderW := TerminalRenderWidth(w)
	if w < 8 || h < 6 {
		m.hideInputCursorAnchor()
		text := runewidth.Truncate("DeepSentry · 窗口过小", renderW, "")
		return m.withCursorFrameMarker(styleApp.Width(renderW).Height(h).MaxHeight(h).Render(text))
	}
	m.recalcLayout()
	layout := m.frameLayout()
	bodyH := layout.bodyHeight

	header := m.renderHeader(renderW)
	stepInfo := ""
	if m.currentStep > 0 {
		stepInfo = fmt.Sprintf("Step %d/%d", m.currentStep, m.maxSteps)
	}
	var help string
	if m.copyToast != "" {
		help = m.copyToast
	} else if m.pendingConfirm != nil {
		help = "Y 仅本次 · A 本会话同类允许 · S 本次会话允许所有高危操作 · N/Esc 拒绝 · Enter 不批准"
	} else if m.pendingAsk != nil {
		help = "输入补充内容或选项编号 · Enter 继续 · Shift+Enter 换行"
	} else {
		help = m.footerHelpText()
	}
	statusW := max(1, renderW-styleStatusBar.GetHorizontalFrameSize())
	statusParts := []string{m.statusLine, m.statusContextText()}
	if stepInfo != "" {
		statusParts = append(statusParts, stepInfo)
	}
	if scroll := m.scrollPositionText(); scroll != "" {
		statusParts = append(statusParts, scroll)
	}
	statusParts = append(statusParts, time.Now().Format("15:04:05"))
	statusText := strings.Join(statusParts, "  │  ")
	footerMarker := encodeFooterFrameMarker(m.footerVersion)
	status := tagRenderedLines(styleStatusBar.Width(renderW).Render(runewidth.Truncate(sanitizeTUIText(statusText), statusW, "…")), footerMarker)
	body := ""
	if bodyH > 0 {
		// recalcLayout already applies this height. Assigning it on this local
		// copy as well makes the relationship explicit and keeps viewport.View
		// from returning more rows than the body wrapper was budgeted for.
		m.viewport.Height = bodyH
		body = lipgloss.NewStyle().
			Width(renderW).
			Height(bodyH).
			Padding(0, 1).
			Render(m.viewportView())
	}
	inputLine := tagRenderedLines(lipgloss.NewStyle().PaddingLeft(inputLinePaddingLeft).Render(m.renderInputLine()), footerMarker)
	helpW := max(1, renderW-2)
	helpText := runewidth.Truncate(help, helpW, "…")
	var helpLine string
	if m.copyToast != "" {
		helpLine = lipgloss.NewStyle().PaddingLeft(1).Foreground(colorAccent).Render(" " + helpText)
	} else {
		helpLine = styleHelpHint.PaddingLeft(1).Render(" " + helpText)
	}
	helpLine = tagRenderedLines(helpLine, footerMarker)
	footerParts := []string{}
	suggestions := m.renderSlashSuggestions()
	if suggestions != "" {
		footerParts = append(footerParts, suggestions)
	}
	footerParts = append(footerParts, status, inputLine, helpLine)
	footer := lipgloss.JoinVertical(lipgloss.Left, footerParts...)
	mainParts := []string{header}
	if body != "" {
		mainParts = append(mainParts, body)
	}
	mainParts = append(mainParts, footer)
	main := lipgloss.JoinVertical(lipgloss.Left, mainParts...)
	// The component budget above is exact. Do not apply MaxHeight here:
	// lipgloss clips from the bottom, which turns a layout mismatch into a
	// disappearing input border/help line. Tests assert the exact frame height
	// instead, so an overflow is caught rather than hidden from the user.
	view := styleApp.Width(renderW).Height(h).Render(main)
	// Publish the IME anchor from the exact components used for this frame.
	// This avoids drift when suggestions, wrapped input, theme frames, or
	// terminal resizing change the footer height.
	m.syncInputCursorAnchorForLayout(
		lipgloss.Height(header),
		lipgloss.Height(body),
		renderedBlockHeight(suggestions),
		lipgloss.Height(status),
	)
	return m.withCursorFrameMarker(view)
}

func (m *AgentModel) invalidateFooter() {
	m.footerVersion++
	// Scrolling moves viewport rows while leaving much of the footer text
	// unchanged. Invalidate the whole physical frame so wide glyphs and panel
	// borders cannot leave stale cells behind.
	m.frameVersion++
}

func tagRenderedLines(block, marker string) string {
	if block == "" || marker == "" {
		return block
	}
	lines := strings.Split(block, "\n")
	for i := range lines {
		lines[i] += marker
	}
	return strings.Join(lines, "\n")
}

func (m AgentModel) withCursorFrameMarker(view string) string {
	view = tagRenderedLines(view, encodeFooterFrameMarker(m.frameVersion))
	row, col, mode := 0, 0, cursorAnchorPassthrough
	if m.cursorAnchor != nil {
		row, col, mode = m.cursorAnchor.snapshot()
	}
	return view + encodeCursorFrameMarker(row, col, mode)
}

func (m AgentModel) scrollPositionText() string {
	if m.viewport.TotalLineCount() <= m.viewport.VisibleLineCount() {
		return ""
	}
	if m.viewport.YOffset <= 0 {
		return "滚动 顶部"
	}
	if m.viewport.AtBottom() {
		return "滚动 底部"
	}
	return fmt.Sprintf("滚动 %.0f%%", m.viewport.ScrollPercent()*100)
}

type agentFrameLayout struct {
	renderWidth       int
	headerHeight      int
	suggestionsHeight int
	statusHeight      int
	inputHeight       int
	helpHeight        int
	bodyHeight        int
}

// frameLayout is the single source of truth for both rendering and the
// physical IME cursor anchor. Heights are measured from the actual rendered
// components instead of duplicating a collection of one-line assumptions.
func (m AgentModel) frameLayout() agentFrameLayout {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	renderW := TerminalRenderWidth(w)
	headerH := lipgloss.Height(m.renderHeader(renderW))
	suggestionsH := renderedBlockHeight(m.renderSlashSuggestions())
	statusH := lipgloss.Height(styleStatusBar.Width(renderW).Render(""))
	inputH := lipgloss.Height(lipgloss.NewStyle().PaddingLeft(inputLinePaddingLeft).Render(m.renderInputLine()))
	helpH := lipgloss.Height(styleHelpHint.PaddingLeft(1).Render(" "))
	fixedH := styleApp.GetVerticalFrameSize() + headerH + suggestionsH + statusH + inputH + helpH
	bodyH := h - fixedH
	if bodyH < 0 {
		bodyH = 0
	}
	return agentFrameLayout{
		renderWidth:       renderW,
		headerHeight:      headerH,
		suggestionsHeight: suggestionsH,
		statusHeight:      statusH,
		inputHeight:       inputH,
		helpHeight:        helpH,
		bodyHeight:        bodyH,
	}
}

func (m *AgentModel) recalcLayout() {
	// Input wrapping, suggestion menus and terminal resize all move fixed
	// regions vertically. Force the next renderer diff to repaint those rows.
	m.frameVersion++
	w := m.width
	if w <= 0 {
		w = 80
	}
	layout := m.frameLayout()
	m.viewport.Width = max(1, layout.renderWidth-2)
	// bubbles/viewport expects a positive height even when an extremely short
	// terminal leaves no body row. View omits the body in that edge case.
	m.viewport.Height = max(1, layout.bodyHeight)
	m.input.Width = clampInputWidth(ChromeContentWidth(w) - 2)
}

func (m AgentModel) slashSuggestionLineCount() int {
	n := len(m.visibleSlashSuggestions())
	if n == 0 {
		return 0
	}
	return n + 2
}

func (m AgentModel) footerHelpText() string {
	if m.pendingAsk != nil {
		return "输入补充内容或选项编号 · Enter 继续 · PgUp 翻阅 · Ctrl+Home 顶部 · Ctrl+End 底部"
	}
	if m.inputFocused() {
		if m.running {
			return "Esc 中断 · Enter 发送 · Shift+Enter 换行 · Ctrl+A 全选 · " + pasteShortcutHelp() + " · ↑↓ 历史 · Ctrl+U 清空 · Tab 浏览"
		}
		return "Enter 发送 · Shift+Enter 换行 · Ctrl+A 全选 · " + pasteShortcutHelp() + " · ↑↓ 历史 · PgUp 翻阅 · Ctrl+Home/End 顶/底 · /help"
	}
	if m.running {
		return "Tab 输入新指令并 Enter 可中途打断 · Esc 停止 · ↑↓/jk 滚动 · g/Home 顶部 · G 底部 · e 全展/全折 · Y/N 确认"
	}
	return "Tab 输入 · 鼠标拖选 · Ctrl+C 复制 · ↑↓/jk 滚动 · g/Home 顶部 · G 底部 · e 全展/全折 · q 退出 · /help"
}

func (m AgentModel) inputHintText() string {
	if m.awaitGoal {
		return "> 描述安全任务，Enter 开始..."
	}
	if m.pendingAsk != nil {
		return "> 输入补充信息或选项编号，Enter 继续..."
	}
	if m.sessionLive || m.done {
		return "> 追问上一题或继续排查，Enter 发送..."
	}
	return "> Enter 发送..."
}

func (m AgentModel) inputPlaceholderText() string {
	if m.awaitGoal {
		return "> task, Enter to start..."
	}
	if m.pendingAsk != nil {
		return "> answer, Enter to continue..."
	}
	if m.sessionLive || m.done {
		return "> follow up, Enter to send..."
	}
	return "> Enter to send..."
}

func (m AgentModel) renderInputLine() string {
	w := ChromeContentWidth(m.width)
	border := styleInputBorder
	if m.inputFocused() {
		border = styleInputBorderFocused
	}
	innerW := w - 2

	m.input.Width = innerW
	m.input.Placeholder = m.inputPlaceholderText()

	var rows []string
	switch {
	case m.inputFocused():
		rows, _, _ = m.focusedInputRows(innerW)
	case m.running && !m.awaitGoal:
		hint := "Agent 执行中..."
		if m.pendingConfirm != nil {
			hint = "等待确认 · Y 本次 / A 同类 / S 全部 / N 拒绝"
		} else if m.pendingAsk != nil {
			hint = "等待补充信息 · Tab 输入"
		}
		rows = []string{styleInfo.Render(runewidth.Truncate(hint, innerW, "..."))}
	default:
		if m.input.Value() == "" {
			rows = []string{styleInfo.Render(runewidth.Truncate(m.inputHintText(), innerW, "..."))}
		} else {
			rows = []string{m.input.View()}
		}
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	for i := range rows {
		rows[i] = fitStyledLine(rows[i], innerW)
	}
	return renderChromeBox(rows, w, border)
}

func (m AgentModel) visibleSlashSuggestions() []slashCommand {
	suggestions := m.slashSuggestions()
	limit := 5
	if m.height > 0 {
		// Keep at least one viewport row plus header/status/input/help chrome.
		limit = min(limit, max(0, m.height-11))
	}
	if limit == 0 {
		return nil
	}
	if len(suggestions) > limit {
		return suggestions[:limit]
	}
	return suggestions
}

func (m AgentModel) renderSlashSuggestions() string {
	suggestions := m.visibleSlashSuggestions()
	if len(suggestions) == 0 {
		return ""
	}
	w := ChromeContentWidth(m.width)
	innerW := w - 2
	if innerW < 12 {
		return ""
	}
	selected := m.slashSelected
	if selected < 0 {
		selected = 0
	}
	if selected >= len(suggestions) {
		selected = len(suggestions) - 1
	}
	nameW := 16
	if innerW < 44 {
		nameW = 12
	}
	descW := innerW - nameW - 4
	if descW < 8 {
		descW = 8
	}
	rows := make([]string, 0, len(suggestions))
	for i, cmd := range suggestions {
		prefix := "  "
		nameStyle := styleInputLine
		descStyle := styleInfo
		if i == selected {
			prefix = "> "
			nameStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
			descStyle = lipgloss.NewStyle().Foreground(colorText)
		}
		name := runewidth.Truncate("/"+cmd.Name, nameW, "…")
		desc := runewidth.Truncate(cmd.Description, descW, "…")
		row := prefix + nameStyle.Render(name+strings.Repeat(" ", max(0, nameW-runewidth.StringWidth(name)))) + "  " + descStyle.Render(desc)
		rows = append(rows, fitStyledLine(row, innerW))
	}
	return lipgloss.NewStyle().PaddingLeft(1).Render(renderChromeBox(rows, w, styleInputBorderFocused))
}

func (m AgentModel) renderFocusedInputContent(width int) string {
	rows, _, _ := m.focusedInputRows(width)
	return strings.Join(rows, "\n")
}

func (m AgentModel) maxInputContentRows() int {
	if m.height > 0 {
		return max(2, min(5, max(1, m.height/4)))
	}
	return 5
}

func (m AgentModel) focusedInputRows(width int) ([]string, int, int) {
	if width <= 0 {
		return []string{""}, 0, 0
	}
	value := decodeInputValue(m.input.Value())
	inputPos := m.input.Position()
	if prefix := m.draftDisplayPrefix(); prefix != "" {
		value = prefix + value
		inputPos += len([]rune(prefix))
	}
	placeholder := false
	if value == "" {
		value = m.inputPlaceholderText()
		placeholder = true
	}
	pos := inputPos
	runes := []rune(value)
	if pos < 0 {
		pos = 0
	}
	if pos > len(runes) {
		pos = len(runes)
	}

	textStyle := styleInputLine
	if m.inputAllSelected && !placeholder {
		textStyle = styleSelection
	}
	if placeholder {
		textStyle = styleInfo.Background(colorSurface)
	}

	type unit struct {
		text   string
		width  int
		cursor bool
	}
	units := make([]unit, 0, len(runes)+1)
	for i := 0; i <= len(runes); i++ {
		if i == pos {
			cursorText := " "
			if i < len(runes) {
				cursorText = string(runes[i])
			}
			units = append(units, unit{text: cursorText, width: max(1, runewidth.StringWidth(cursorText)), cursor: true})
			if i < len(runes) {
				continue
			}
		}
		if i >= len(runes) {
			continue
		}
		r := runes[i]
		if r == '\n' || r == '\r' {
			units = append(units, unit{text: "\n", width: 0})
			continue
		}
		units = append(units, unit{text: string(r), width: max(1, runewidth.RuneWidth(r))})
	}

	rows := []string{""}
	rowWidths := []int{0}
	cursorRow, cursorCol := 0, 0
	for _, u := range units {
		if u.text == "\n" {
			rows = append(rows, "")
			rowWidths = append(rowWidths, 0)
			continue
		}
		last := len(rows) - 1
		if rowWidths[last] > 0 && rowWidths[last]+u.width > width {
			rows = append(rows, "")
			rowWidths = append(rowWidths, 0)
			last++
		}
		if u.cursor {
			cursorRow = last
			cursorCol = rowWidths[last]
			rows[last] += styleInputCursor.Render(u.text)
		} else {
			rows[last] += textStyle.Render(u.text)
		}
		rowWidths[last] += u.width
	}

	maxRows := m.maxInputContentRows()
	if len(rows) > maxRows {
		start := cursorRow - maxRows + 1
		if start < 0 {
			start = 0
		}
		if start > len(rows)-maxRows {
			start = len(rows) - maxRows
		}
		rows = rows[start : start+maxRows]
		cursorRow -= start
	}
	return rows, cursorRow, cursorCol
}

const inputLinePaddingLeft = 1

func (m AgentModel) inputCursorAnchor() (row, col int, ok bool) {
	if !m.inputFocused() {
		return 0, 0, false
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if w < 8 || h < 6 {
		return 0, 0, false
	}
	// Use the same measured frame budget as View. In particular, never derive
	// the IME anchor from viewport.Height: that field may still describe the
	// previous frame while the input has wrapped, suggestions appeared, or a
	// resize event is being processed.
	m.width, m.height = w, h
	layout := m.frameLayout()
	return m.inputCursorAnchorForLayout(
		layout.headerHeight,
		layout.bodyHeight,
		layout.suggestionsHeight,
		layout.statusHeight,
	)
}

func (m AgentModel) inputCursorAnchorForLayout(headerH, bodyH, suggestionsH, statusH int) (row, col int, ok bool) {
	if !m.inputFocused() {
		return 0, 0, false
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if w < 8 || h < 6 {
		return 0, 0, false
	}
	innerW := ChromeContentWidth(w) - 2
	_, cursorRow, cursorCol := m.focusedInputRows(innerW)
	if cursorCol >= innerW {
		cursorCol = innerW - 1
	}
	if cursorCol < 0 {
		cursorCol = 0
	}
	// Coordinates are 1-based. Use the actual rendered component heights
	// instead of assuming a one-line header/status or deriving suggestions
	// from their item count. The input itself has a one-cell outer inset and
	// a one-cell box border.
	outerTop := styleApp.GetMarginTop() + styleApp.GetBorderTopSize() + styleApp.GetPaddingTop()
	outerLeft := styleApp.GetMarginLeft() + styleApp.GetBorderLeftSize() + styleApp.GetPaddingLeft()
	row = outerTop + headerH + bodyH + suggestionsH + statusH + 1 + cursorRow + 1
	col = outerLeft + inputLinePaddingLeft + 1 + cursorCol + 1
	if row < 1 || row > h || col < 1 || col > TerminalRenderWidth(w) {
		return 0, 0, false
	}
	return row, col, true
}

func renderedBlockHeight(block string) int {
	if block == "" {
		return 0
	}
	return lipgloss.Height(block)
}

func clampInputWidth(w int) int {
	if w < 1 {
		return 1
	}
	return w
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// 兼容旧 Model 别名
type Model = AgentModel

func NewModel(title, status string, maxSteps int) AgentModel {
	return NewAgentModel(nil, title, status, maxSteps, false, false, StartupInfo{})
}

func WaitConfirm(program *tea.Program, action *harness.AgentAction, prompt string) approvalDecision {
	ch := make(chan approvalDecision, 1)
	program.Send(confirmMsg{action: action, prompt: prompt, respCh: ch})
	return <-ch
}

func WaitUserInput(program *tea.Program, action *harness.AgentAction) (string, bool) {
	if action == nil {
		return "", false
	}
	ch := make(chan string, 1)
	prompt := strings.TrimSpace(action.Question)
	if prompt == "" {
		prompt = "请补充继续任务所需的信息。"
	}
	program.Send(askMsg{action: action, prompt: prompt, options: action.Options, respCh: ch})
	answer := <-ch
	return answer, strings.TrimSpace(answer) != ""
}

func EventCmd(e harness.UIEvent) tea.Cmd {
	return func() tea.Msg { return uiEventMsg(e) }
}

func DoneCmd() tea.Cmd { return func() tea.Msg { return agentDoneMsg{} } }

var styleAccent = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)

func truncateLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n..."
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return ""
	}
	if displayWidth(s) <= n {
		return s
	}
	return truncateDisplay(s, n, "…")
}

func pasteSummary(s string, index int) string {
	s = normalizeInputNewlines(s)
	lines := strings.Count(s, "\n") + 1
	chars := len([]rune(s))
	return fmt.Sprintf("[粘贴文本 #%d · %d 行 · %d 字符]", index, lines, chars)
}

func pastePreview(s string) string {
	s = normalizeInputNewlines(s)
	s = strings.ReplaceAll(s, "\n", " ↵ ")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "（空白内容）"
	}
	return truncateStr(s, 120)
}

func summarizeUserText(s string) string {
	s = normalizeInputNewlines(s)
	lines := strings.Count(s, "\n") + 1
	chars := len([]rune(s))
	first := strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if first == "" {
		first = strings.TrimSpace(s)
	}
	first = truncateStr(first, 80)
	return fmt.Sprintf("[长文本已折叠：%d 行 / %d 字符] %s", lines, chars, first)
}

func renderWrapped(style lipgloss.Style, text string, width int) string {
	if width <= 0 {
		width = 80
	}
	text = sanitizeTUIText(ui.TerminalText(text))
	innerW := width - style.GetHorizontalFrameSize()
	if innerW < 1 {
		innerW = 1
	}
	wrapped := wrapDisplay(text, innerW)
	return style.Width(styleRenderWidth(style, width)).Render(wrapped)
}

func wrapDisplay(s string, width int) string {
	if width <= 0 {
		return s
	}
	parts := strings.Split(s, "\n")
	for i, part := range parts {
		parts[i] = wrapDisplayClusters(part, width)
	}
	return strings.Join(parts, "\n")
}
