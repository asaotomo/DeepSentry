package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/builtin"
	"ai-edr/internal/chat"
	"ai-edr/internal/collector"
	"ai-edr/internal/config"
	"ai-edr/internal/desktop"
	"ai-edr/internal/executor"
	"ai-edr/internal/harness"
	"ai-edr/internal/harness/subagent"
	"ai-edr/internal/inspection"
	"ai-edr/internal/logger"
	"ai-edr/internal/mcp"
	"ai-edr/internal/scheduler"
	"ai-edr/internal/tools"
	"ai-edr/internal/tui"
	"ai-edr/internal/ui"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

func main() {
	os.Exit(runCLI())
}

func runCLI() (exitCode int) {
	// 1. 跨平台控制台初始化
	enableWindowsANSI()
	defer boundedCleanup("MCP", 3*time.Second, mcp.CloseAll)
	defer boundedCleanup("浏览器会话", 2*time.Second, builtin.CloseBrowserSessions)

	// 🟢 [核心增强] 强制设置 Windows 控制台代码页为 UTF-8
	// 这解决了即便开启了 ANSI 渲染，底层系统命令输出依然可能坚持使用 GBK 的问题
	if runtime.GOOS == "windows" {
		_ = exec.Command("cmd", "/c", "chcp 65001").Run()
	}

	// 2. Flag 解析
	configFile := flag.String("c", "", "指定配置文件路径")
	batchMode := flag.Bool("batch", false, "开启无人值守模式")
	autoYes := flag.Bool("y", false, "跳过 batch 模式确认")
	reconf := flag.Bool("init", false, "强制重新配置")
	resumeSession := flag.String("resume", "", "恢复 checkpoint 会话 ID")
	sessionFlag := flag.String("session", "", "绑定会话 ID（存在则恢复并追加任务，不存在则新建）")
	listSessions := flag.Bool("list-sessions", false, "列出可恢复的会话")
	pickSession := flag.Bool("pick-session", false, "TUI 选择 checkpoint 会话")
	tuiMode := flag.Bool("tui", true, "启用全屏 TUI 界面（默认）")
	noTUI := flag.Bool("no-tui", false, "使用经典 stdout CLI")
	taskFlag := flag.String("task", "", "任务描述（agent/脚本推荐）")
	taskShort := flag.String("q", "", "任务描述（--task 简写）")
	planMode := flag.Bool("plan", false, "计划模式：必要时先追问，再生成 todo 计划并执行")
	competitionMode := flag.Bool("competition", false, "比赛模式：按 10 分钟限时、证据闭环和评分规范优化执行/输出")
	subAgentMaxStepsFlag := flag.Int("subagent-max-steps", 0, "子 Agent 初始步数窗口；关闭自适应时为上限（默认读取 subagent_max_steps，未配置为 15）")
	httpProxyFlag := flag.String("proxy", "", "控制端 HTTP/HTTPS 代理，例如 http://127.0.0.1:8080")
	socks5ProxyFlag := flag.String("socks5", "", "控制端 SOCKS5 代理，例如 socks5://127.0.0.1:1080")
	jsonOutput := flag.Bool("json", false, "经典模式输出 JSONL 事件")
	quiet := flag.Bool("quiet", false, "经典模式仅输出关键结果和错误")
	replyFinal := flag.Bool("reply-final", false, "仅输出最终任务结论（聊天机器人回复）")
	webshellMode := flag.Bool("webshell", false, "WebShell/非 TTY 友好模式（提交后台执行，立即返回报告/进度路径）")
	noColor := flag.Bool("no-color", false, "禁用彩色输出（也可设置 NO_COLOR=1 或 DEEPSENTRY_NO_COLOR=1）")
	themeFlag := flag.String("theme", "", "TUI 主题 auto|dark|light（默认自动适应终端背景）")
	chatMode := flag.Bool("chat", false, "运行聊天机器人控制服务")
	inspectRun := flag.Bool("inspect", false, "执行配置的多设备巡检并生成 Word/Markdown")
	inspectSelector := flag.String("inspect-selector", "all", "巡检设备名称或标签")
	chatSetupCLI := flag.Bool("chat-setup-cli", false, "纯终端机器人连接向导（SSH/CMD）")
	chatSetup := flag.Bool("chat-setup", false, "打开通讯工具连接窗口（飞书/QQ/微信/企微扫码，钉钉手动配置）")
	chatStop := flag.Bool("chat-stop", false, "停止仍在运行的聊天服务")
	schedulerMode := flag.Bool("scheduler", false, "仅运行本地定时任务调度器")
	computerCheck := flag.Bool("computer-check", false, "检查本机桌面操作依赖和权限，不发送输入")
	showVersion := flag.Bool("version", false, "显示版本")
	showHelp := flag.Bool("h", false, "显示帮助")
	showHelpLong := flag.Bool("help", false, "显示帮助")
	flag.Usage = printUsage
	flag.Parse()
	if *computerCheck {
		return runComputerCheck()
	}
	if os.Getenv("DEEPSENTRY_WEBSHELL_SUPERVISOR") == "1" {
		return runWebShellSupervisor()
	}
	if _, ok := tui.NormalizeTerminalTheme(*themeFlag); !ok {
		fmt.Printf("%s--theme 只支持 auto|dark|light，当前为 %q\n", ui.Prefix("❌", "[ERR]"), *themeFlag)
		ui.Exit(1)
		return
	}
	if *noColor {
		_ = os.Setenv("DEEPSENTRY_NO_COLOR", "1")
		_ = os.Setenv("NO_COLOR", "1")
	}
	configureSurveyCompatibility()
	tui.ConfigureTerminalPreferences(*themeFlag)
	if *webshellMode {
		*noTUI = true
		*quiet = true
		*batchMode = true
		*autoYes = true
	}
	if *replyFinal {
		*noTUI = true
		*quiet = true
	}
	if *inspectRun || *schedulerMode || *chatMode || *chatSetup || *chatSetupCLI || *chatStop {
		*noTUI = true
		*quiet = true
	}
	if os.Getenv("DEEPSENTRY_NO_VT") == "1" {
		*noTUI = true
	}
	if *noTUI {
		*tuiMode = false
	}
	interactiveTerminal := isInteractiveTerminal()
	explicitOneShot := strings.TrimSpace(*taskFlag) != "" || strings.TrimSpace(*taskShort) != "" || len(flag.Args()) > 0 || strings.TrimSpace(*resumeSession) != "" || strings.TrimSpace(*sessionFlag) != ""
	// --no-tui with an explicit task is primarily a one-shot automation mode.
	// WebShell/SFTP clients often allocate a pseudo-TTY, but that must not make
	// ask_user or high-risk confirmations wait forever on stdin.
	nonInteractive := resolvedNonInteractive(*jsonOutput, *quiet, interactiveTerminal, *noTUI, explicitOneShot)
	if nonInteractive && !flagWasPassed("tui") {
		*tuiMode = false
	}
	*tuiMode = resolvedTUIMode(*tuiMode, *noTUI, nonInteractive, flagWasPassed("tui"), interactiveTerminal)
	// Only modes that actually render a TUI or run an interactive survey need
	// terminal restoration.  Emitting alternate-screen reset sequences from a
	// one-shot/WebShell process can corrupt the final shell prompt.
	if *tuiMode || (interactiveTerminal && !nonInteractive) {
		defer ui.ResetTerminalState()
	}

	if *showHelp || *showHelpLong {
		printUsage()
		return
	}
	if *showVersion {
		fmt.Printf("DeepSentry v%s Ultimate (build %s)\n", ui.Version, ui.BuildTime)
		return
	}
	startupProxy, proxyErr := config.ResolveStartupProxy(*httpProxyFlag, *socks5ProxyFlag)
	if proxyErr != nil {
		fmt.Printf("%s代理参数无效: %v\n", ui.Prefix("❌", "[ERR]"), proxyErr)
		ui.Exit(1)
		return
	}

	if !*tuiMode && !nonInteractive {
		ui.PrintBanner()
		fmt.Println(ui.Prefix("💡", "[TIP]") + "提示: 默认会进入全屏 Agent 面板；使用 --no-tui 切换经典输出")
		fmt.Println(ui.Prefix("📖", "[DOC]") + "完整手册: docs/操作手册.md  ·  --help 查看命令")
		fmt.Println()
	}

	if *listSessions {
		ids, err := harness.ListSessions()
		if err != nil {
			if *jsonOutput {
				printJSON(map[string]any{"error": err.Error()})
			} else {
				fmt.Printf("%s列出会话失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
			}
			ui.Exit(1)
			return
		}
		if *jsonOutput {
			printJSON(map[string]any{"sessions": ids})
			return
		}
		if len(ids) == 0 {
			fmt.Println("无可恢复会话")
			return
		}
		fmt.Println("可恢复会话:")
		for _, id := range ids {
			fmt.Printf("  - %s\n", id)
		}
		return
	}

	// 3. 配置加载
	err := config.InitConfig(*configFile)
	needWizard := *reconf
	if err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			needWizard = true
		} else {
			fmt.Printf("%s配置文件加载失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
			ui.Exit(1)
		}
	}
	startup := tui.StartupInfo{
		Version:   ui.Version,
		BuildTime: ui.BuildTime,
	}
	if wd, err := os.Getwd(); err == nil {
		startup.WorkDir = wd
	}
	if *tuiMode {
		startup.ToolCount = tools.CountEnabled()
	}

	if needWizard {
		if nonInteractive || !isInteractiveTerminal() {
			if *jsonOutput {
				printJSON(map[string]any{
					"error":   "missing_config",
					"message": "未检测到配置文件；请先运行 deepsentry --init，或通过 -c 指定配置文件。",
				})
			} else {
				fmt.Println(ui.Prefix("❌", "[ERR]") + "未检测到配置文件。请先运行: deepsentry --init")
			}
			ui.Exit(1)
		}
		fmt.Println(ui.Prefix("⚠️", "[WARN]") + "未检测到配置文件或请求重新初始化，进入向导模式...")
		ui.DisableMouseTracking()
		if err := runElegantWizard(); err != nil {
			fmt.Printf("%s向导中断: %v\n", ui.Prefix("❌", "[ERR]"), err)
			ui.Exit(1)
		}
	} else {
		msg := fmt.Sprintf("已加载配置: %s", viper.ConfigFileUsed())
		if *tuiMode {
			startup.ConfigPath = viper.ConfigFileUsed()
		} else if !*jsonOutput && !*quiet {
			fmt.Printf("%s%s\n", ui.Prefix("📂", "[CFG]"), ui.StripANSIIfPlain("\033[1;32m"+msg+"\033[0m"))
		}
	}
	effectiveTheme := config.GlobalConfig.TerminalTheme
	if strings.TrimSpace(*themeFlag) != "" {
		// CLI 覆盖仅对当前进程生效，不改写 config.yaml。
		effectiveTheme = *themeFlag
	}
	tui.ConfigureTerminalPreferences(effectiveTheme)
	if startupProxy != "" {
		// Command-line proxy is process-scoped and deliberately does not rewrite
		// config.yaml. Detached WebShell children receive the original args.
		config.GlobalConfig.ControllerProxy = startupProxy
	}
	if config.IsLocalProvider(config.GlobalConfig.Provider) &&
		(config.GlobalConfig.ContextWindowTokens <= 0 || !config.GlobalConfig.UseNativeTools) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_, _ = config.ApplyLocalRuntimeContext(ctx, &config.GlobalConfig)
		cancel()
	}

	if *inspectRun {
		if *chatMode || *chatSetup || *chatSetupCLI || *chatStop || *schedulerMode || explicitOneShot {
			fmt.Println("--inspect 不能与其他运行模式同用")
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		report, err := inspection.Run(ctx, config.GlobalConfig, *inspectSelector)
		if report.Markdown != "" {
			fmt.Printf("Markdown: %s\nWord: %s\n证据清单: %s\n", report.Markdown, report.Word, report.Manifest)
		}
		if err != nil {
			fmt.Println(err)
			return 1
		}
		return 0
	}
	if *chatMode || *chatSetup || *chatSetupCLI || *chatStop {
		if *schedulerMode || explicitOneShot || *webshellMode || *reconf {
			fmt.Println("--chat/--chat-setup/--chat-stop 不能与任务执行、--scheduler、--webshell 或 --init 同用")
			return 1
		}
		if *chatStop {
			if err := chat.StopDetachedChat(config.GlobalConfig.Chat); err != nil {
				fmt.Printf("停止后台聊天服务失败: %v\n", err)
				return 1
			}
			fmt.Println("后台聊天服务已停止。")
			return 0
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		exe, err := os.Executable()
		if err != nil {
			fmt.Println(err)
			return 1
		}
		cfgPath, err := filepath.Abs(viper.ConfigFileUsed())
		if err != nil {
			fmt.Println(err)
			return 1
		}
		runner := chat.ProcessRunner(exe, cfgPath, startupProxy)
		if *chatSetupCLI {
			err = chat.SetupCLI(ctx, config.GlobalConfig.Chat, runner, os.Stdin, os.Stdout)
		} else if *chatSetup {
			err = chat.Setup(ctx, config.GlobalConfig.Chat, runner)
		} else {
			var service *chat.Service
			service, err = chat.New(ctx, config.GlobalConfig.Chat, runner)
			if err == nil {
				err = service.Serve()
			}
		}
		if err != nil {
			fmt.Printf("聊天服务启动失败: %v\n", err)
			return 1
		}
		return 0
	}

	// 4. 获取用户需求 / 恢复会话
	var history []analyzer.Message
	sessionID := *resumeSession
	boundSession := strings.TrimSpace(*sessionFlag)
	if boundSession != "" {
		if strings.TrimSpace(sessionID) != "" && sessionID != boundSession {
			fmt.Println("--resume 与 --session 不能指向不同会话")
			return 1
		}
		sessionID = boundSession
	}
	awaitGoal := false
	resumeSupplement := resumeUserSupplement(*taskFlag, *taskShort, flag.Args())

	if *pickSession && *tuiMode {
		id, cancelled, err := tui.PickSession()
		if err != nil {
			fmt.Printf("%s会话选择失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
			ui.Exit(1)
		}
		if cancelled {
			return
		}
		sessionID = id
	}

	if sessionID != "" {
		cp, err := harness.LoadCheckpoint(sessionID)
		if err != nil {
			if boundSession == "" || strings.TrimSpace(*resumeSession) != "" {
				fmt.Printf("%s恢复会话失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
				ui.Exit(1)
			}
			// --session with no checkpoint yet: create a fresh session under this ID.
			userGoal := strings.TrimSpace(resumeSupplement)
			if userGoal == "" {
				if *tuiMode {
					awaitGoal = true
				} else if nonInteractive {
					if *jsonOutput {
						printJSON(map[string]any{
							"error":   "missing_task",
							"message": "新建 --session 需要 --task/-q 或任务描述参数。",
						})
					} else {
						fmt.Println(ui.Prefix("❌", "[ERR]") + "新建 --session 需要任务描述。")
					}
					ui.Exit(1)
				}
			} else {
				history = []analyzer.Message{{Role: "user", Content: userGoal}}
			}
		} else {
			history = cp.History
			if len(history) == 0 {
				history = []analyzer.Message{{Role: "user", Content: "继续之前的任务"}}
			}
			if strings.TrimSpace(resumeSupplement) != "" {
				content := strings.TrimSpace(resumeSupplement)
				if !*replyFinal {
					content = "用户补充：" + content
				}
				history = append(history, analyzer.Message{
					Role:    "user",
					Content: content,
				})
			}
			if !*tuiMode && !*quiet {
				fmt.Printf("%s已恢复会话 %s (step %d)\n", ui.Prefix("♻️", "[RESUME]"), sessionID, cp.StepNum)
				if strings.TrimSpace(resumeSupplement) != "" {
					fmt.Println(ui.Prefix("💡", "[TIP]") + "已追加本次 --task/参数作为用户补充并继续执行")
				}
			}
		}
	} else {
		userGoal := ""
		args := flag.Args()
		if strings.TrimSpace(*taskFlag) != "" {
			userGoal = *taskFlag
		} else if strings.TrimSpace(*taskShort) != "" {
			userGoal = *taskShort
		} else if len(args) < 1 {
			if *tuiMode {
				awaitGoal = true
			} else if *schedulerMode {
				userGoal = ""
			} else if nonInteractive {
				if *jsonOutput {
					printJSON(map[string]any{
						"error":   "missing_task",
						"message": "非交互模式需要 --task/-q 或任务描述参数。",
						"example": "deepsentry --json --task \"审计 SSH 登录日志\"",
					})
				} else {
					fmt.Println(ui.Prefix("❌", "[ERR]") + "非交互模式需要任务描述。示例: deepsentry --quiet --task \"审计 SSH 登录日志\"")
				}
				ui.Exit(1)
			} else {
				prompt := &survey.Input{
					Message: ui.Prefix("🎯", "[TASK]") + "请输入您的安全应急需求:",
					Help:    "示例: 排查内存异常 | 审计监听端口 | 分析 auth.log 暴力破解 | 查找 webshell",
					Suggest: func(toComplete string) []string {
						hints := []string{
							"排查目标机内存与监听端口",
							"分析 SSH 登录失败记录",
							"审计网络暴露面与异常连接",
							"在 Web 目录狩猎 webshell",
							"读取 /var/log/syslog 最近错误",
						}
						if toComplete == "" {
							return hints
						}
						var out []string
						for _, h := range hints {
							if strings.Contains(h, toComplete) {
								out = append(out, h)
							}
						}
						return out
					},
				}
				if err := askOne(prompt, &userGoal); err != nil {
					fmt.Println("\n" + ui.Prefix("❌", "[ERR]") + "操作已取消")
					return
				}
				ui.ResetTerminalState()
				if strings.TrimSpace(userGoal) == "" {
					fmt.Println(ui.Prefix("❌", "[ERR]") + "未提供需求，程序退出。")
					return
				}
			}
		} else {
			userGoal = strings.Join(args, " ")
		}
		if userGoal != "" {
			history = []analyzer.Message{
				{Role: "user", Content: fmt.Sprintf("需求：%s", userGoal)},
			}
		}
	}

	if *webshellMode && os.Getenv("DEEPSENTRY_WEBSHELL_CHILD") != "1" {
		reportPath, progressPath, statusPath, latestPath, err := launchDetachedWebShell(logger.TitleFromHistory(history))
		if err != nil {
			fmt.Printf("[WEB] 后台任务启动失败: %v\n", err)
			ui.Exit(1)
			return
		}
		printWebShellDetachedNotice(reportPath, progressPath, statusPath, latestPath)
		return
	}

	// 5. 初始化执行环境
	if *jsonOutput {
		executor.SetModeOutputEnabled(false)
	}
	for {
		isTelnetTarget := strings.EqualFold(strings.TrimSpace(config.GlobalConfig.TargetProtocol), "telnet") || (config.GlobalConfig.TargetProtocol == "" && config.GlobalConfig.TelnetHost != "")
		if isTelnetTarget && !*jsonOutput && !*quiet {
			loginTimeout := config.GlobalConfig.TelnetLoginTimeoutSec
			if loginTimeout <= 0 {
				loginTimeout = 20
			}
			fmt.Printf("%s正在连接 Telnet %s（登录超时 %ds）...\n", ui.Prefix("🔌", "[TELNET]"), config.GlobalConfig.TelnetHost, loginTimeout)
		}
		if *tuiMode {
			out := captureStdout(func() {
				err = executor.Init(config.GlobalConfig)
			})
			absorbStartupOutput(&startup, out)
		} else {
			err = executor.Init(config.GlobalConfig)
		}
		if err == nil {
			break
		}
		if isTelnetTarget {
			if nonInteractive || !isInteractiveTerminal() {
				emitInitFailure(*jsonOutput, "telnet_connect_failed", fmt.Sprintf("Telnet 连接/认证失败: %v", err))
				ui.Exit(1)
			}
			fmt.Printf("\n%sTelnet 连接/认证失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
			choice := ""
			if askErr := askOne(&survey.Select{Message: "请选择处理方式:", Options: []string{
				ui.Prefix("🔧", "[CFG]") + "修改 Telnet/网络设备配置",
				ui.Prefix("💻", "[LOCAL]") + "切换为本地模式",
				ui.Prefix("❌", "[EXIT]") + "退出程序",
			}}, &choice); askErr != nil {
				emitInitFailure(false, "telnet_connect_failed", err.Error())
				ui.Exit(1)
			}
			ui.ResetTerminalState()
			switch {
			case strings.Contains(choice, "修改 Telnet"):
				if wizardErr := runTelnetWizard(); wizardErr != nil {
					fmt.Printf("%sTelnet 配置向导已中止: %v\n", ui.Prefix("❌", "[ERR]"), wizardErr)
					ui.Exit(1)
					return
				}
				continue
			case strings.Contains(choice, "本地模式"):
				switchToLocalMode()
				continue
			default:
				ui.Exit(1)
			}
		}

		if config.GlobalConfig.SSHHost != "" {
			if nonInteractive || !isInteractiveTerminal() {
				emitInitFailure(*jsonOutput, "ssh_connect_failed", fmt.Sprintf("SSH 连接失败: %v", err))
				ui.Exit(1)
			}
			fmt.Printf("\n%s%s\n", ui.Prefix("❌", "[ERR]"), ui.StripANSIIfPlain(fmt.Sprintf("\033[1;31mSSH 连接失败: %v\033[0m", err)))
			choice := ""
			options := make([]string, 0, 4)
			if _, keyMismatch := executor.InspectSSHHostKeyMismatch(err); keyMismatch {
				options = append(options, ui.Prefix("🔐", "[TRUST]")+"核对并更新 SSH 主机密钥")
			}
			options = append(options,
				ui.Prefix("🔧", "[CFG]")+"修改 SSH 配置 (重新输入密码)",
				ui.Prefix("💻", "[LOCAL]")+"切换为 本地模式 (清除 SSH 配置)",
				ui.Prefix("❌", "[EXIT]")+"退出程序",
			)
			prompt := &survey.Select{
				Message: "检测到 SSH 连接失败，请选择操作:",
				Options: options,
			}
			if err := askOne(prompt, &choice); err != nil {
				fmt.Printf("%sSSH 连接失败且未选择处理方式: %v\n", ui.Prefix("❌", "[ERR]"), err)
				ui.Exit(1)
			}
			ui.ResetTerminalState()
			if strings.Contains(choice, "核对并更新 SSH 主机密钥") {
				updated, updateErr := confirmAndReplaceSSHHostKey(err)
				if updateErr != nil {
					fmt.Printf("%s更新 SSH 主机密钥失败: %v\n", ui.Prefix("❌", "[ERR]"), updateErr)
				}
				if updated {
					fmt.Println(ui.Prefix("🔄", "[RETRY]") + "主机密钥已更新，正在使用原认证配置重新连接...")
				}
				continue
			} else if strings.Contains(choice, "修改 SSH 配置") {
				if wizardErr := runSSHWizard(false); wizardErr != nil {
					fmt.Printf("%sSSH 配置向导已中止: %v\n", ui.Prefix("❌", "[ERR]"), wizardErr)
					ui.Exit(1)
					return
				}
				continue
			} else if strings.Contains(choice, "切换为 本地模式") {
				switchToLocalMode()
				continue
			} else {
				ui.Exit(1)
			}
		}
		emitInitFailure(*jsonOutput, "executor_init_failed", fmt.Sprintf("初始化执行环境失败: %v", err))
		ui.Exit(1)
	}
	defer func() {
		if executor.Current != nil {
			boundedCleanup("执行器连接", 2*time.Second, executor.Current.Close)
		}
	}()

	// Batch mode can approve destructive actions. Require an explicit -y in
	// non-interactive environments, and ask once before starting any background
	// scheduler or Agent work in an interactive terminal (including TUI mode).
	needsBatchPrompt, batchApprovalErr := batchApprovalRequirement(*batchMode, *autoYes, interactiveTerminal)
	if batchApprovalErr != nil {
		emitInitFailure(*jsonOutput, "batch_confirmation_required", batchApprovalErr.Error())
		ui.Exit(1)
	}
	if needsBatchPrompt {
		fmt.Println("\n" + ui.TerminalText("\033[41;37m ⚠️  警告：无人值守模式 (BATCH MODE) 已开启 ⚠️ \033[0m"))
		confirm := false
		prompt := &survey.Confirm{Message: "确认要在无人值守模式下运行吗?", Default: false}
		_ = askOne(prompt, &confirm)
		ui.ResetTerminalState()
		if !confirm {
			return
		}
	}

	schedRunner := scheduler.NewRunner(config.GlobalConfig)
	schedRunner.ConfigPath = viper.ConfigFileUsed()
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	if *schedulerMode {
		fmt.Printf("%sDeepSentry 定时任务调度器已启动，store=%s interval=%s\n", ui.Prefix("⏱️", "[TIME]"), schedRunner.Store.Path, schedulerInterval())
		schedRunner.Start(schedCtx, schedulerInterval())
		waitForSignal()
		return
	}
	// A one-shot/no-TUI/WebShell invocation must not race a background scheduler
	// against process shutdown.  Use explicit --scheduler for headless service
	// mode; interactive/TUI sessions may still host the convenience scheduler.
	if config.GlobalConfig.SchedulerEnabled && !nonInteractive {
		schedRunner.Start(schedCtx, schedulerInterval())
		if *tuiMode {
			startup.Notices = append(startup.Notices, fmt.Sprintf("定时任务调度已启用：%s", schedRunner.Store.Path))
		}
	}

	// 6. Batch Mode 状态提示（确认已在任何后台任务启动前完成）
	if *batchMode && *tuiMode {
		startup.Notices = append(startup.Notices, "Batch 模式已开启：高风险操作会自动批准，请确认目标环境可接受。")
	}
	if *planMode && *tuiMode {
		startup.Notices = append(startup.Notices, "计划模式已开启：Agent 会先澄清/规划，再继续执行。")
	}

	// 7. 初始化报告
	reporter, reportPath, reportErr := logger.NewReporterWithTitle(logger.TitleFromHistory(history))
	if reportErr != nil {
		emitInitFailure(*jsonOutput, "report_init_failed", reportErr.Error())
		ui.Exit(1)
	}
	defer reporter.Close()
	if *tuiMode {
		startup.ReportPath = reportPath
	} else if !*jsonOutput && !*quiet {
		fmt.Printf("[*] 审计日志: %s\n", reportPath)
	}

	// 8. 环境感知
	if !*tuiMode && !*jsonOutput && !*quiet {
		fmt.Println(ui.Prefix("🔍", "[SCAN]") + "正在采集系统指纹...")
	}
	sysCtx := collector.GetSystemContext()

	connInfo := "本地模式"
	switch executor.CurrentMode() {
	case "ssh":
		connInfo = fmt.Sprintf("SSH -> %s", config.GlobalConfig.SSHHost)
		if deviceReporter, ok := executor.Current.(executor.NetworkDeviceReporter); ok {
			info := deviceReporter.NetworkDeviceInfo()
			connInfo = fmt.Sprintf("SSH -> %s · %s · prompt %s", config.GlobalConfig.SSHHost, info.Vendor, info.Prompt)
		}
	case "telnet":
		connInfo = fmt.Sprintf("Telnet -> %s", config.GlobalConfig.TelnetHost)
		if reporter, ok := executor.Current.(executor.NetworkDeviceReporter); ok {
			info := reporter.NetworkDeviceInfo()
			connInfo = fmt.Sprintf("Telnet -> %s · %s · prompt %s", config.GlobalConfig.TelnetHost, info.Vendor, info.Prompt)
		}
	case "ftp":
		connInfo = fmt.Sprintf("FTP -> %s", config.GlobalConfig.FTPHost)
	}
	if len(config.GlobalConfig.Targets) > 0 && executor.CurrentMode() == "local" {
		connInfo = fmt.Sprintf("Fleet 多目标: %d 台", len(config.GlobalConfig.Targets))
	}
	if rawProxy := strings.TrimSpace(config.GlobalConfig.ControllerProxy); rawProxy != "" {
		connInfo += " · proxy " + config.ControllerProxySummary(rawProxy)
	}

	if *tuiMode {
		startup.ConnInfo = connInfo
		startup.OS = sysCtx.OS
		startup.Arch = sysCtx.Arch
		startup.Username = sysCtx.Username
		startup.Hostname = sysCtx.Hostname
		startup.ModelInfo = config.GlobalConfig.ModelDisplayInfo()
		startup.TargetCount = len(config.GlobalConfig.Targets)
		startup.NativeTools = config.GlobalConfig.UseNativeTools
		executor.SetModeOutputEnabled(false)
	} else {
		executor.SetModeOutputEnabled(!*jsonOutput && !*quiet)
		if !*jsonOutput && !*quiet {
			fmt.Println("--------------------------------------------------")
			fmt.Printf("[+] 连接状态: %s\n", ui.StripANSIIfPlain("\033[1;33m"+connInfo+"\033[0m"))
			fmt.Printf("[+] 目标系统: %s / %s\n", sysCtx.OS, sysCtx.Arch)
			fmt.Printf("[+] 用户信息: %s\n", sysCtx.Username)
			fmt.Printf("[+] LLM: %s / %s\n", config.GlobalConfig.Provider, config.GlobalConfig.ModelName)
			fmt.Println("--------------------------------------------------")
		}
	}

	// 9. 启动 Deep Agent Harness 分析循环
	var agent *harness.DeepAgent
	if *tuiMode {
		out := captureStdout(func() {
			agent, err = harness.NewDeepAgent(harness.Config{
				BatchMode: *batchMode,
				SessionID: sessionID,
			})
		})
		absorbStartupOutput(&startup, out)
	} else {
		agent, err = harness.NewDeepAgent(harness.Config{
			BatchMode: *batchMode,
			SessionID: sessionID,
		})
	}
	if err != nil {
		if *jsonOutput {
			printJSON(map[string]any{"error": "agent_init_failed", "message": err.Error()})
		} else {
			fmt.Printf("%sDeep Agent 初始化失败: %v\n", ui.Prefix("❌", "[ERR]"), err)
		}
		ui.Exit(1)
	}
	if *tuiMode {
		inventory := mcp.ConnectedInventory()
		startup.MCPCount = len(inventory)
		startup.MCPSummary = mcp.FormatConnectedInventory()
		startup.ChatSummary = chat.StartupSummary(config.GlobalConfig.Chat)
	}

	if sessionID != "" {
		if cp, err := harness.LoadCheckpoint(sessionID); err == nil {
			agent.RestoreFromCheckpoint(cp)
		}
	}

	maxSteps := viper.GetInt("max_steps")
	if maxSteps <= 0 {
		maxSteps = 30
	}
	subAgentMaxSteps := viper.GetInt("subagent_max_steps")
	if *subAgentMaxStepsFlag > 0 {
		subAgentMaxSteps = *subAgentMaxStepsFlag
	}
	if subAgentMaxSteps <= 0 {
		subAgentMaxSteps = 15
	}

	var confirmationMu sync.Mutex
	var confirmStop <-chan struct{}
	sessionApprovals := make(map[string]string)
	allowAllChatSession := false
	confirmFn := func(action *harness.AgentAction) bool {
		confirmationMu.Lock()
		defer confirmationMu.Unlock()
		if dir := strings.TrimSpace(os.Getenv("DEEPSENTRY_CHAT_CONFIRM_DIR")); dir != "" {
			scopeKey, scopeLabel := action.ApprovalScopeKey, action.ApprovalScopeLabel
			if scopeKey == "" {
				scopeKey, scopeLabel = harness.SessionApprovalScope(action)
			}
			if allowAllChatSession {
				return true
			}
			if _, ok := sessionApprovals[scopeKey]; ok && scopeKey != "" {
				return true
			}
			summary := fmt.Sprintf("%s %s", action.Type, action.ToolName)
			if strings.TrimSpace(action.ToolName) == "" {
				summary = string(action.Type)
			}
			if err := chat.WriteConfirmRequest(dir, chat.ConfirmRequest{
				Summary:    strings.TrimSpace(summary),
				Command:    harness.ConfirmPreview(action),
				Reason:     strings.TrimSpace(action.Reason),
				ScopeKey:   scopeKey,
				ScopeLabel: scopeLabel,
			}); err != nil {
				fmt.Fprintln(os.Stderr, "无法把确认请求发到聊天，已拒绝本次高危操作；请检查 chat.store 目录是否可写。")
				return false
			}
			reply, ok := chat.WaitConfirmReply(dir, confirmStop, 10*time.Minute)
			if !ok || reply.Decision == "deny" {
				return false
			}
			if reply.Decision == "allow_all_session" {
				allowAllChatSession = true
			}
			if reply.Decision == "allow_session" && scopeKey != "" {
				sessionApprovals[scopeKey] = scopeLabel
			}
			return true
		}
		if nonInteractive {
			// A one-shot process has no reliable confirmation channel.  Batch -y
			// bypasses this callback; all other high-risk actions fail closed.
			if action.ToolName == "computer_use" {
				fmt.Fprintln(os.Stderr, ui.Prefix("🚫", "[DENY]")+"无人值守任务不会操作本机鼠标键盘。请在前台会话里执行；确需无人值守操作桌面时，另设 DEEPSENTRY_COMPUTER_USE_UNATTENDED=1。")
				return false
			}
			fmt.Fprintln(os.Stderr, ui.Prefix("🚫", "[DENY]")+"非交互任务已拒绝需要人工确认的操作；如已授权无人值守执行，请显式使用 --batch -y。")
			return false
		}
		if allowAllChatSession {
			return true
		}
		scopeKey, scopeLabel := action.ApprovalScopeKey, action.ApprovalScopeLabel
		if scopeKey == "" {
			scopeKey, scopeLabel = harness.SessionApprovalScope(action)
		}
		if label, ok := sessionApprovals[scopeKey]; ok && scopeKey != "" {
			fmt.Printf("✅ 已命中本会话授权：%s\n", label)
			return true
		}
		reason := action.Reason
		message := fmt.Sprintf("%s确认执行 %s（%s）", ui.Prefix("🔴", "[HIGH]"), action.Type, reason)
		if action.Type == harness.ActionExecute {
			message = fmt.Sprintf("%s风险: 高 (%s) -> 如何处理?", ui.Prefix("🔴", "[HIGH]"), reason)
		} else if action.Type == harness.ActionTool {
			message = fmt.Sprintf("%s工具 %s（风险: %s，原因: %s）", ui.Prefix("🔴", "[HIGH]"), action.ToolName, action.RiskLevel, reason)
		}
		choice := "拒绝"
		_ = askOne(&survey.Select{
			Message: message,
			Options: []string{"仅允许本次", "本会话允许同类操作", "本次会话允许所有高危操作", "拒绝"},
			Default: "拒绝",
			Help:    "会话授权使用保守指纹；文件修改仅匹配同一动作和同一文件。新会话自动失效。",
		}, &choice)
		ui.ResetTerminalState()
		if choice == "本次会话允许所有高危操作" {
			allowAllChatSession = true
		}
		if choice == "本会话允许同类操作" && scopeKey != "" {
			sessionApprovals[scopeKey] = scopeLabel
		}
		return choice != "拒绝"
	}

	loopCfg := harness.RunLoopConfig{
		SysCtx:           sysCtx,
		History:          &history,
		Reporter:         reporter,
		ReportPath:       reportPath,
		BatchMode:        *batchMode,
		NonInteractive:   nonInteractive,
		PauseOnAskUser:   false,
		MaxSteps:         maxSteps,
		SubAgentMaxSteps: subAgentMaxSteps,
		MultiTurn:        *replyFinal,
		ChatReply:        *replyFinal,
		PlanMode:         *planMode,
		CompetitionMode:  *competitionMode,
		ConfirmFn:        confirmFn,
	}
	if !*tuiMode && !nonInteractive {
		loopCfg.AwaitUserFn = func(action *harness.AgentAction) (string, bool) {
			question := action.Question
			if strings.TrimSpace(question) == "" {
				question = "请补充继续任务所需的信息。"
			}
			if len(action.Options) > 0 {
				var b strings.Builder
				b.WriteString(question)
				for i, opt := range action.Options {
					b.WriteString(fmt.Sprintf("\n%d. %s", i+1, opt))
				}
				question = b.String()
			}
			answer := ""
			_ = askOne(&survey.Input{Message: question}, &answer)
			ui.ResetTerminalState()
			return strings.TrimSpace(answer), strings.TrimSpace(answer) != ""
		}
	}

	if *tuiMode {
		startup.MaxSteps = maxSteps
		startup.BatchMode = *batchMode
		startup.SessionID = sessionID
		startup.AwaitGoal = awaitGoal
		startup.ToolCount = tools.CountEnabled()
		startup.SubAgentCount = subagent.Count()
		if agent.Catalog != nil {
			startup.SkillCount = len(agent.Catalog.Skills)
		}
		if err := tui.Run(tui.SessionConfig{
			Agent:            agent,
			SysCtx:           sysCtx,
			History:          &history,
			Reporter:         reporter,
			ReportPath:       reportPath,
			BatchMode:        *batchMode,
			MaxSteps:         maxSteps,
			SubAgentMaxSteps: subAgentMaxSteps,
			ConnInfo:         connInfo,
			ModelInfo:        config.GlobalConfig.ModelDisplayInfo(),
			Startup:          startup,
			AwaitGoal:        awaitGoal,
			MultiTurn:        true,
			PlanMode:         *planMode,
			CompetitionMode:  *competitionMode,
		}); err != nil {
			fmt.Printf("%sTUI 退出: %v\n", ui.Prefix("❌", "[ERR]"), err)
			ui.Exit(1)
		}
		return
	}

	if *jsonOutput && *quiet {
		fmt.Fprintln(os.Stderr, ui.Prefix("⚠️", "[WARN]")+"--json 与 --quiet 同时使用时，--quiet 会抑制部分 JSONL 事件。")
	}
	var outSink harness.UISink = harness.NewStdoutSink()
	if *jsonOutput {
		outSink = harness.NewJSONSink()
	}
	if *webshellMode {
		outSink = harness.NewWebShellSink(outSink)
	} else if *replyFinal {
		outSink = harness.NewFinalReplySink()
	} else if *quiet {
		outSink = harness.NewQuietSink(outSink)
	}
	if dir := strings.TrimSpace(os.Getenv("DEEPSENTRY_CHAT_CONFIRM_DIR")); *replyFinal && dir != "" {
		images, imageErr := chat.LoadControlImages(dir)
		if imageErr != nil {
			fmt.Fprintln(os.Stderr, "聊天图片清单读取失败")
			return 2
		}
		chat.ApplyControlImages(&history, images, *taskFlag)
		if len(history) > 0 && history[len(history)-1].Role == "user" {
			history[len(history)-1].Content += chat.FileInstructions()
		}
		outSink = &chat.ControlSink{Base: outSink, Dir: dir}
		loopCfg.DrainInput = func() []analyzer.Message { return chat.DrainControlInput(dir) }
		loopCfg.AwaitUserFn = func(action *harness.AgentAction) (string, bool) {
			confirmationMu.Lock()
			defer confirmationMu.Unlock()
			question := action.Question
			for i, option := range action.Options {
				question += fmt.Sprintf("\n%d. %s", i+1, option)
			}
			if err := chat.WriteConfirmRequest(dir, chat.ConfirmRequest{Kind: "input", Summary: question}); err != nil {
				return "", false
			}
			reply, ok := chat.WaitConfirmReply(dir, loopCfg.Stop, 10*time.Minute)
			return reply.Text, ok && reply.Decision == "user_input"
		}
	}
	loopCfg.UI = outSink
	runCtx, stopRunSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopRunSignals()
	loopCfg.Stop = runCtx.Done()
	confirmStop = loopCfg.Stop
	runResult := agent.RunLoop(loopCfg)
	if !*jsonOutput && !*quiet {
		fmt.Printf("%s任务结束: status=%s step=%d reason=%s\n", ui.Prefix("⏹️", "[DONE]"), runResult.Status, runResult.Step, runResult.Reason)
		printExitHint()
	}
	if !runResult.Successful() {
		return 2
	}
	return 0
}

func flagWasPassed(name string) bool {
	seen := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func resolvedTUIMode(requested, noTUI, nonInteractive, explicitTUI, interactiveTerminal bool) bool {
	if !requested || noTUI {
		return false
	}
	if nonInteractive && !explicitTUI {
		return false
	}
	// Pipes, redirects, cron and CI cannot render an alternate-screen app
	// reliably. Use readable classic output unless --tui was explicit.
	if !interactiveTerminal && !explicitTUI {
		return false
	}
	return true
}

func resolvedNonInteractive(jsonOutput, quiet, interactiveTerminal, noTUI, explicitOneShot bool) bool {
	return jsonOutput || quiet || !interactiveTerminal || (noTUI && explicitOneShot)
}

func batchApprovalRequirement(batchMode, autoYes, interactiveTerminal bool) (bool, error) {
	if !batchMode || autoYes {
		return false, nil
	}
	if !interactiveTerminal {
		return false, fmt.Errorf("非交互环境启用 --batch 时必须同时传入 -y，避免未经确认自动执行高风险操作")
	}
	return true, nil
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(v)
}

func emitInitFailure(jsonOutput bool, code, message string) {
	if jsonOutput {
		printJSON(map[string]any{
			"error":   code,
			"message": message,
		})
		return
	}
	fmt.Printf("%s%s\n", ui.Prefix("❌", "[ERR]"), message)
}

type webShellTaskStatus struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	SupervisorPID int    `json:"supervisor_pid,omitempty"`
	WorkerPID     int    `json:"worker_pid,omitempty"`
	ReportPath    string `json:"report_path"`
	ProgressPath  string `json:"progress_path"`
	StatusPath    string `json:"status_path"`
	QueuedAt      string `json:"queued_at"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	Error         string `json:"error,omitempty"`
}

func launchDetachedWebShell(title string) (string, string, string, string, error) {
	if err := os.MkdirAll("reports", 0o700); err != nil {
		return "", "", "", "", err
	}
	if err := os.Chmod("reports", 0o700); err != nil {
		return "", "", "", "", err
	}
	now := time.Now()
	stamp := now.Format("20060102_150405") + fmt.Sprintf("_%09d", now.Nanosecond())
	progressFile, progressPath, reportPath, err := createWebShellArtifacts(stamp)
	if err != nil {
		return "", "", "", "", err
	}
	statusPath := webShellStatusPath(progressPath)
	latestPath, err := filepath.Abs(filepath.Join("reports", "latest_webshell.txt"))
	if err != nil {
		_ = progressFile.Close()
		return "", "", "", "", err
	}

	notice := webShellNoticeText(reportPath, progressPath, statusPath, latestPath)
	_, _ = progressFile.WriteString(notice + "\n\n")
	latest := notice + "\n"
	if strings.TrimSpace(title) != "" {
		latest += "任务标题: " + logger.NormalizeReportTitle(title) + "\n"
	}
	if err := writePrivateFile(latestPath, []byte(latest)); err != nil {
		_ = progressFile.Close()
		return "", "", "", "", err
	}
	status := webShellTaskStatus{
		SchemaVersion: 1,
		Status:        "queued",
		ReportPath:    reportPath,
		ProgressPath:  progressPath,
		StatusPath:    statusPath,
		QueuedAt:      now.Format(time.RFC3339Nano),
	}
	if err := writeWebShellStatus(statusPath, status); err != nil {
		_ = progressFile.Close()
		return "", "", "", "", err
	}

	exe, err := os.Executable()
	if err != nil {
		_ = progressFile.Close()
		return "", "", "", "", err
	}
	// #nosec G204 G702 -- exe 由 os.Executable 返回，参数是操作员用来启动当前进程的原始 argv；未经 shell 解析，用于受控后台重启自身。
	cmd := exec.Command(exe, os.Args[1:]...)
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	cmd.Env = append(envWithout(os.Environ(), "DEEPSENTRY_COLOR", "CLICOLOR_FORCE", "DEEPSENTRY_WEBSHELL_SUPERVISOR", "DEEPSENTRY_WEBSHELL_CHILD"),
		"DEEPSENTRY_WEBSHELL_SUPERVISOR=1",
		"DEEPSENTRY_REPORT_PATH="+reportPath,
		"DEEPSENTRY_WEBSHELL_PROGRESS_PATH="+progressPath,
		"DEEPSENTRY_WEBSHELL_STATUS_PATH="+statusPath,
		"DEEPSENTRY_NO_COLOR=1",
		"NO_COLOR=1",
	)
	cmd.Stdin = nil
	cmd.Stdout = progressFile
	cmd.Stderr = progressFile
	configureDetachedProcess(cmd)

	if err := cmd.Start(); err != nil {
		_ = progressFile.Close()
		return "", "", "", "", err
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	_ = progressFile.Close()
	return reportPath, progressPath, statusPath, latestPath, nil
}

func runComputerCheck() int {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	response := desktop.Default.SelfCheck(ctx)
	ready := response.OK && response.State != nil && response.State.Ready
	if !ready && runtime.GOOS == "darwin" && response.State != nil && response.Category != "capture_failed" {
		if err := desktop.RequestPermissions(ctx); err == nil {
			fmt.Fprintln(os.Stderr, "已请求 macOS 授权。请在「系统设置 → 隐私与安全性」里给 computer-use-helper 打开「辅助功能」和「屏幕录制」，然后重新运行 --computer-check。更换新版 helper 后需要重新授权。")
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	switch {
	case ready:
		fmt.Fprintln(os.Stderr, "桌面操作可用：状态正常，已实际截图一次并删除测试图。")
		return 0
	case response.Category == "capture_failed":
		fmt.Fprintln(os.Stderr, "权限显示已开启，但实际截图失败："+response.Error)
	case response.State != nil && response.State.Reason != "":
		fmt.Fprintln(os.Stderr, "桌面操作暂不可用："+response.State.Reason)
	case response.Error != "":
		fmt.Fprintln(os.Stderr, "桌面操作暂不可用："+response.Error)
	}
	return 1
}

func runWebShellSupervisor() int {
	reportPath := strings.TrimSpace(os.Getenv("DEEPSENTRY_REPORT_PATH"))
	progressPath := strings.TrimSpace(os.Getenv("DEEPSENTRY_WEBSHELL_PROGRESS_PATH"))
	statusPath := strings.TrimSpace(os.Getenv("DEEPSENTRY_WEBSHELL_STATUS_PATH"))
	var err error
	if reportPath, err = validateWebShellArtifactPath(reportPath, "report"); err != nil {
		fmt.Printf("[WEB] 状态: failed exit_code=2 error=%s\n", err)
		return 2
	}
	if progressPath, err = validateWebShellArtifactPath(progressPath, "progress"); err != nil {
		fmt.Printf("[WEB] 状态: failed exit_code=2 error=%s\n", err)
		return 2
	}
	if statusPath, err = validateWebShellArtifactPath(statusPath, "status"); err != nil {
		fmt.Printf("[WEB] 状态: failed exit_code=2 error=%s\n", err)
		return 2
	}
	status := readWebShellStatus(statusPath)
	status.SchemaVersion = 1
	status.Status = "starting"
	status.SupervisorPID = os.Getpid()
	status.ReportPath = reportPath
	status.ProgressPath = progressPath
	status.StatusPath = statusPath
	if status.QueuedAt == "" {
		status.QueuedAt = time.Now().Format(time.RFC3339Nano)
	}
	_ = writeWebShellStatus(statusPath, status)

	exe, err := os.Executable()
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		status.FinishedAt = time.Now().Format(time.RFC3339Nano)
		code := 1
		status.ExitCode = &code
		_ = writeWebShellStatus(statusPath, status)
		fmt.Printf("[WEB] 状态: failed exit_code=%d error=%s\n", code, err)
		return code
	}

	// #nosec G204 G702 -- executable and argv are the current trusted process;
	// the worker is launched without a shell and with an explicit environment.
	worker := exec.Command(exe, os.Args[1:]...)
	if wd, wdErr := os.Getwd(); wdErr == nil {
		worker.Dir = wd
	}
	worker.Env = append(envWithout(os.Environ(), "DEEPSENTRY_WEBSHELL_SUPERVISOR", "DEEPSENTRY_WEBSHELL_CHILD"),
		"DEEPSENTRY_WEBSHELL_CHILD=1",
	)
	worker.Stdin = nil
	worker.Stdout = os.Stdout
	worker.Stderr = os.Stderr
	if err = worker.Start(); err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		status.FinishedAt = time.Now().Format(time.RFC3339Nano)
		code := 1
		status.ExitCode = &code
		_ = writeWebShellStatus(statusPath, status)
		fmt.Printf("[WEB] 状态: failed exit_code=%d error=%s\n", code, err)
		return code
	}
	status.Status = "running"
	status.WorkerPID = worker.Process.Pid
	status.StartedAt = time.Now().Format(time.RFC3339Nano)
	_ = writeWebShellStatus(statusPath, status)
	fmt.Printf("[WEB] 状态: running pid=%d\n", status.WorkerPID)

	waitErr := worker.Wait()
	code := 0
	if waitErr != nil {
		code = 1
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		status.Error = waitErr.Error()
	}
	if code == 0 {
		if _, statErr := os.Stat(reportPath); statErr != nil { // #nosec G703 -- reportPath is validated as reports/report_*.md before use.
			code = 3
			status.Error = "worker exited without a readable report"
		}
	}
	status.ExitCode = &code
	status.FinishedAt = time.Now().Format(time.RFC3339Nano)
	if code == 0 {
		status.Status = "completed"
	} else {
		status.Status = "failed"
	}
	_ = writeWebShellStatus(statusPath, status)
	fmt.Printf("\n[WEB] 状态: %s exit_code=%d finished_at=%s\n", status.Status, code, status.FinishedAt)
	return code
}

func envWithout(env []string, keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[strings.ToUpper(strings.TrimSpace(key))] = true
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if !blocked[strings.ToUpper(key)] {
			out = append(out, entry)
		}
	}
	return out
}

func createWebShellArtifacts(stamp string) (*os.File, string, string, error) {
	for i := 0; i < 1000; i++ {
		suffix := stamp
		if i > 0 {
			suffix = fmt.Sprintf("%s_%d", stamp, i+1)
		}
		progressPath, err := filepath.Abs(filepath.Join("reports", "webshell_progress_"+suffix+".log"))
		if err != nil {
			return nil, "", "", err
		}
		file, err := os.OpenFile(progressPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, "", "", err
		}
		reportPath, err := filepath.Abs(filepath.Join("reports", "report_"+suffix+".md"))
		if err != nil {
			_ = file.Close()
			_ = os.Remove(progressPath)
			return nil, "", "", err
		}
		return file, progressPath, reportPath, nil
	}
	return nil, "", "", fmt.Errorf("无法生成唯一的 WebShell 任务文件名")
}

func validateWebShellArtifactPath(path, kind string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("WebShell %s path is empty", kind)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absReports, err := filepath.Abs("reports")
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absReports, absPath)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("WebShell %s path must stay under reports/: %s", kind, path)
	}
	if rel != filepath.Base(absPath) {
		return "", fmt.Errorf("WebShell %s path must be a direct reports/ file: %s", kind, path)
	}
	base := filepath.Base(absPath)
	if !webShellArtifactNameAllowed(base, kind) {
		return "", fmt.Errorf("WebShell %s path has unsupported filename: %s", kind, base)
	}
	return absPath, nil
}

func webShellArtifactNameAllowed(base, kind string) bool {
	switch kind {
	case "report":
		return strings.HasPrefix(base, "report_") && strings.HasSuffix(base, ".md")
	case "progress":
		return strings.HasPrefix(base, "webshell_progress_") && strings.HasSuffix(base, ".log")
	case "status":
		return strings.HasPrefix(base, "webshell_status_") && strings.HasSuffix(base, ".json")
	case "latest":
		return base == "latest_webshell.txt"
	case "any":
		return webShellArtifactNameAllowed(base, "report") ||
			webShellArtifactNameAllowed(base, "progress") ||
			webShellArtifactNameAllowed(base, "status") ||
			webShellArtifactNameAllowed(base, "latest")
	default:
		return false
	}
}

func writePrivateFile(path string, data []byte) error {
	safePath, err := validateWebShellArtifactPath(path, "any")
	if err != nil {
		return err
	}
	if err := os.WriteFile(safePath, data, 0o600); err != nil { // #nosec G703 -- safePath is restricted to known WebShell artifact files under reports/.
		return err
	}
	return os.Chmod(safePath, 0o600) // #nosec G703 -- safePath is restricted to known WebShell artifact files under reports/.
}

func webShellStatusPath(progressPath string) string {
	dir, name := filepath.Split(progressPath)
	name = strings.TrimPrefix(name, "webshell_progress_")
	name = strings.TrimSuffix(name, filepath.Ext(name))
	return filepath.Join(dir, "webshell_status_"+name+".json")
}

func writeWebShellStatus(path string, status webShellTaskStatus) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("WebShell status path is empty")
	}
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writePrivateFile(path, raw)
}

func readWebShellStatus(path string) webShellTaskStatus {
	var status webShellTaskStatus
	safePath, err := validateWebShellArtifactPath(path, "status")
	if err != nil {
		return status
	}
	raw, err := os.ReadFile(safePath) // #nosec G703 -- safePath is restricted to reports/webshell_status_*.json.
	if err == nil {
		_ = json.Unmarshal(raw, &status)
	}
	return status
}

func printWebShellDetachedNotice(reportPath, progressPath, statusPath, latestPath string) {
	fmt.Print(webShellNoticeText(reportPath, progressPath, statusPath, latestPath) + "\n")
	_ = os.Stdout.Sync()
}

func webShellNoticeText(reportPath, progressPath, statusPath, latestPath string) string {
	return fmt.Sprintf("[WEB] DeepSentry 任务已提交后台执行\n"+
		"[WEB] 执行结果报告: %s\n"+
		"[WEB] 实时进度日志: %s\n"+
		"[WEB] 任务状态文件: %s\n"+
		"[WEB] 固定索引文件: %s\n"+
		"[WEB] 查看进度: cat %s\n"+
		"[WEB] 查看状态: cat %s\n"+
		"[WEB] 查看报告: cat %s",
		reportPath, progressPath, statusPath, latestPath, progressPath, statusPath, reportPath)
}

func boundedCleanup(label string, timeout time.Duration, cleanup func()) {
	if cleanup == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		cleanup()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Fprintf(os.Stderr, "[WARN] %s关闭超过 %s，已停止等待并继续退出\n", label, timeout)
	}
}

func captureStdout(fn func()) (out string) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		done <- buf.String()
	}()
	os.Stdout = w
	defer func() {
		os.Stdout = old
		_ = w.Close()
		out = <-done
	}()
	fn()
	return ""
}

func appendLogLines(dst []string, raw string) []string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(stripANSI(line))
		if line != "" {
			dst = append(dst, line)
		}
	}
	return dst
}

func absorbStartupOutput(info *tui.StartupInfo, raw string) {
	for _, line := range appendLogLines(nil, raw) {
		if strings.Contains(line, "[模式切换]") {
			info.ModeLine = line
			continue
		}
		info.Notices = append(info.Notices, line)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inSeq := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inSeq {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				inSeq = false
			}
			continue
		}
		if ch == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			inSeq = true
			i++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func printExitHint() {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	exe, _ = filepath.Abs(exe)
	if _, err := os.Stat(exe); err != nil {
		fmt.Println("\n" + ui.Prefix("⚠️", "[WARN]") + "可执行文件已被删除或移动（常见于运行期间执行了 build.sh 重建 build/ 目录）。")
		fmt.Println("   请重新编译后运行: bash build.sh && cd build && ./deepsentry -c config.yaml")
		return
	}
	dir := filepath.Dir(exe)
	fmt.Printf("\n%s继续任务: %s --tui -c config.yaml\n", ui.Prefix("💡", "[TIP]"), exe)
	if dir != "" {
		fmt.Printf("   (当前目录: %s)\n", dir)
	}
}

func schedulerInterval() time.Duration {
	sec := config.GlobalConfig.SchedulerIntervalSec
	if sec <= 0 {
		sec = 30
	}
	if sec < 5 {
		sec = 5
	}
	return time.Duration(sec) * time.Second
}

func waitForSignal() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	fmt.Println("\n" + ui.Prefix("⏹️", "[STOP]") + "定时任务调度器已退出")
}

// ---------------------------------------------------------------------
// 辅助函数：向导与循环
// ---------------------------------------------------------------------

func confirmAndReplaceSSHHostKey(connectErr error) (bool, error) {
	info, ok := executor.InspectSSHHostKeyMismatch(connectErr)
	if !ok {
		return false, fmt.Errorf("当前错误不是可确认的 SSH 主机密钥变更")
	}

	fmt.Printf("\n%sSSH 主机密钥发生变化。这不是旧算法兼容或密码错误。\n", ui.Prefix("⚠️", "[HOST KEY]"))
	fmt.Printf("   目标: %s\n", info.Hostname)
	fmt.Printf("   known_hosts: %s\n", info.KnownHostsPath)
	for _, fingerprint := range info.KnownFingerprints {
		fmt.Printf("   已记录指纹: %s\n", fingerprint)
	}
	fmt.Printf("   当前指纹（尚未验证）: %s\n", info.ReceivedFingerprint)
	fmt.Println("   请通过云厂商控制台、服务器本地终端或管理员提供的可信渠道独立核对当前指纹。")

	confirmed := false
	if err := askOne(&survey.Confirm{
		Message: "我已独立核对当前 SHA256 指纹，确认仅替换该目标的旧记录:",
		Default: false,
	}, &confirmed); err != nil {
		return false, err
	}
	ui.ResetTerminalState()
	if !confirmed {
		fmt.Println(ui.Prefix("🛑", "[SAFE]") + "已取消更新；known_hosts 保持不变。")
		return false, nil
	}
	if err := executor.ReplaceSSHHostKeyFromMismatch(connectErr); err != nil {
		return false, err
	}
	fmt.Printf("%s已安全替换 %s 中该目标的旧主机密钥记录。\n", ui.Prefix("✅", "[OK]"), info.KnownHostsPath)
	return true, nil
}

// runSSHWizard 统一的 SSH 配置向导
func runSSHWizard(skipHostName bool) error {
	// 🟢 动态标题：根据场景显示不同标题，体验更流畅
	if skipHostName {
		fmt.Println("\n" + ui.Prefix("🔐", "[SSH]") + ui.StripANSIIfPlain("\033[1;34mSSH 身份认证\033[0m")) // 初次设置显示这个
	} else {
		fmt.Println("\n" + ui.Prefix("🛠️", "[CFG]") + ui.StripANSIIfPlain("\033[1;34mSSH 配置修正\033[0m")) // 只有出错重连时才显示这个
	}

	// 🟢 只有在"非跳过"模式下，才询问主机名
	if !skipHostName {
		var host string
		if err := askOne(&survey.Input{
			Message: "SSH 主机 (IP:Port):",
			Default: config.GlobalConfig.SSHHost,
		}, &host); err != nil {
			return err
		}
		viper.Set("target_protocol", "ssh")
		viper.Set("ssh_host", host)
		config.GlobalConfig.TargetProtocol = "ssh"
		config.GlobalConfig.SSHHost = host // 立即更新内存变量
	}

	var user string
	if err := askOne(&survey.Input{
		Message: "SSH 用户名:",
		Default: "root", // 给个默认值 root，方便一点
	}, &user); err != nil {
		return err
	}
	viper.Set("ssh_user", user)

	authMethod := ""
	if err := askOne(&survey.Select{
		Message: "认证方式:",
		Options: []string{"Password", "Private Key"},
		Default: "Password",
	}, &authMethod); err != nil {
		return err
	}

	if authMethod == "Password" {
		var pwd string
		if err := askOne(&survey.Password{Message: "密码:"}, &pwd); err != nil {
			return err
		}
		viper.Set("ssh_password", pwd)
		viper.Set("ssh_key_path", "")
	} else {
		var keyPath string
		defKey := config.GlobalConfig.SSHKeyPath
		if defKey == "" {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				defKey = filepath.Join(home, ".ssh", "id_rsa")
			} else {
				defKey = filepath.Join(".ssh", "id_rsa")
			}
		}
		if err := askOne(&survey.Input{Message: "私钥路径:", Default: defKey}, &keyPath); err != nil {
			return err
		}
		viper.Set("ssh_key_path", keyPath)
		viper.Set("ssh_password", "")
	}

	deviceType := firstNonEmptyString(config.GlobalConfig.SSHDeviceType, "auto")
	if err := askOne(&survey.Select{
		Message: "SSH 目标类型:",
		Options: []string{"auto", "linux", "huawei", "h3c", "ruijie", "cisco", "generic"},
		Default: deviceType,
		Help:    "auto 会先识别 Linux/SFTP，不支持时切换到交互式网络设备 CLI。",
	}, &deviceType); err != nil {
		return err
	}
	prompt := config.GlobalConfig.SSHPrompt
	if err := askOne(&survey.Input{Message: "网络设备命令 Prompt（可空自动探测）:", Default: prompt}, &prompt); err != nil {
		return err
	}
	enablePassword := ""
	if err := askOne(&survey.Password{Message: "特权密码（华为/H3C super，Cisco/锐捷 enable；留空保持原值）:"}, &enablePassword); err != nil {
		return err
	}
	viper.Set("ssh_device_type", deviceType)
	viper.Set("ssh_prompt", prompt)
	if enablePassword != "" {
		viper.Set("ssh_enable_password", enablePassword)
	}

	// 保存并刷新配置
	if err := saveCurrentConfig(); err != nil {
		fmt.Printf("%s配置保存失败: %v\n", ui.Prefix("⚠️", "[WARN]"), err)
	}
	// 刷新全局变量
	config.GlobalConfig.TargetProtocol = viper.GetString("target_protocol")
	config.GlobalConfig.SSHUser = viper.GetString("ssh_user")
	config.GlobalConfig.SSHPassword = viper.GetString("ssh_password")
	config.GlobalConfig.SSHKeyPath = viper.GetString("ssh_key_path")
	config.GlobalConfig.SSHDeviceType = viper.GetString("ssh_device_type")
	config.GlobalConfig.SSHPrompt = viper.GetString("ssh_prompt")
	config.GlobalConfig.SSHEnablePassword = viper.GetString("ssh_enable_password")
	ui.ResetTerminalState()
	return nil
}

func runTelnetWizard() error {
	fmt.Println("\n" + ui.Prefix("🛠️", "[TELNET]") + "Telnet / 网络设备配置修正")
	var target config.TargetConfig
	target.Protocol = "telnet"
	target.Host = config.GlobalConfig.TelnetHost
	target.User = firstNonEmptyString(config.GlobalConfig.TelnetUser, "root")
	target.Prompt = config.GlobalConfig.TelnetPrompt
	target.AuthPromptRegex = config.GlobalConfig.TelnetAuthPromptRegex
	target.DeviceType = firstNonEmptyString(config.GlobalConfig.TelnetDeviceType, "auto")
	target.EnablePassword = config.GlobalConfig.TelnetEnablePassword
	if err := ask([]*survey.Question{
		{Name: "host", Prompt: &survey.Input{Message: "主机 (IP[:Port]):", Default: target.Host}, Validate: survey.Required},
		{Name: "user", Prompt: &survey.Input{Message: "用户名:", Default: target.User}},
		{Name: "device_type", Prompt: &survey.Select{Message: "设备类型:", Options: []string{"auto", "huawei", "h3c", "ruijie", "cisco", "linux", "generic"}, Default: target.DeviceType}},
	}, &target); err != nil {
		return err
	}
	if err := askOne(&survey.Password{Message: "Telnet 密码（留空会清除旧密码）:"}, &target.Password); err != nil {
		return err
	}
	if err := askOne(&survey.Input{Message: "命令 Prompt（可空自动探测）:", Default: target.Prompt}, &target.Prompt); err != nil {
		return err
	}
	if err := askOne(&survey.Input{Message: "认证 Prompt 正则（可空）:", Default: target.AuthPromptRegex}, &target.AuthPromptRegex); err != nil {
		return err
	}
	var privilegePassword string
	if err := askOne(&survey.Password{Message: "特权密码（华为/H3C super，Cisco/锐捷 enable；留空保持原值）:"}, &privilegePassword); err != nil {
		return err
	}
	if privilegePassword != "" {
		target.EnablePassword = privilegePassword
	}
	target.Host = normalizeTargetHost("telnet", target.Host)
	applySingleTargetConfig(target)
	if err := saveCurrentConfig(); err != nil {
		fmt.Printf("%sTelnet 配置保存失败: %v\n", ui.Prefix("⚠️", "[WARN]"), err)
	}
	config.GlobalConfig.TargetProtocol = "telnet"
	config.GlobalConfig.TelnetHost = target.Host
	config.GlobalConfig.TelnetUser = target.User
	config.GlobalConfig.TelnetPassword = target.Password
	config.GlobalConfig.TelnetPrompt = target.Prompt
	config.GlobalConfig.TelnetAuthPromptRegex = target.AuthPromptRegex
	config.GlobalConfig.TelnetDeviceType = target.DeviceType
	config.GlobalConfig.TelnetEnablePassword = viper.GetString("telnet_enable_password")
	ui.ResetTerminalState()
	return nil
}

func switchToLocalMode() {
	viper.Set("target_protocol", "local")
	viper.Set("ssh_host", "")
	viper.Set("ssh_user", "")
	viper.Set("ssh_password", "")
	viper.Set("ssh_key_path", "")
	viper.Set("ssh_device_type", "auto")
	viper.Set("ssh_prompt", "")
	viper.Set("ssh_enable_password", "")
	viper.Set("telnet_host", "")
	viper.Set("telnet_user", "")
	viper.Set("telnet_password", "")
	viper.Set("telnet_prompt", "")
	viper.Set("telnet_auth_prompt_regex", "")
	viper.Set("telnet_device_type", "auto")
	viper.Set("telnet_enable_password", "")
	viper.Set("ftp_host", "")
	viper.Set("ftp_user", "")
	viper.Set("ftp_password", "")
	viper.Set("ftp_tls_mode", "plain")
	viper.Set("ftp_tls_server_name", "")
	viper.Set("ftp_tls_ca_file", "")
	viper.Set("ftp_tls_insecure_skip_verify", false)
	viper.Set("ftp_data_mode", "passive")
	viper.Set("ftp_active_address", "")

	config.GlobalConfig.TargetProtocol = "local"
	config.GlobalConfig.SSHHost = ""
	config.GlobalConfig.SSHUser = ""
	config.GlobalConfig.SSHPassword = ""
	config.GlobalConfig.SSHKeyPath = ""
	config.GlobalConfig.SSHDeviceType = "auto"
	config.GlobalConfig.SSHPrompt = ""
	config.GlobalConfig.SSHEnablePassword = ""
	config.GlobalConfig.TelnetHost = ""
	config.GlobalConfig.TelnetUser = ""
	config.GlobalConfig.TelnetPassword = ""
	config.GlobalConfig.TelnetPrompt = ""
	config.GlobalConfig.TelnetAuthPromptRegex = ""
	config.GlobalConfig.TelnetDeviceType = "auto"
	config.GlobalConfig.TelnetEnablePassword = ""
	config.GlobalConfig.FTPHost = ""
	config.GlobalConfig.FTPUser = ""
	config.GlobalConfig.FTPPassword = ""
	config.GlobalConfig.FTPTLSMode = "plain"
	config.GlobalConfig.FTPTLSServerName = ""
	config.GlobalConfig.FTPTLSCAFile = ""
	config.GlobalConfig.FTPTLSInsecureSkipVerify = false
	config.GlobalConfig.FTPDataMode = "passive"
	config.GlobalConfig.FTPActiveAddress = ""

	if err := saveCurrentConfig(); err != nil {
		fmt.Printf("%s已切换为本地模式，但配置保存失败: %v\n", ui.Prefix("⚠️", "[WARN]"), err)
		return
	}
	fmt.Println(ui.Prefix("✅", "[OK]") + "已切换为本地模式并清除单目标远程配置")
}

func saveCurrentConfig() error {
	if path := strings.TrimSpace(viper.ConfigFileUsed()); path != "" {
		return config.WriteConfigAsPrivate(path)
	}
	return config.SaveConfig()
}

func runSingleTargetWizard() (config.TargetConfig, error) {
	targets, err := runTargetEntriesWizard(1)
	if err != nil {
		return config.TargetConfig{}, err
	}
	if len(targets) == 0 {
		return config.TargetConfig{Protocol: "ssh"}, nil
	}
	return targets[0], nil
}

func runTargetsWizard() ([]config.TargetConfig, error) {
	countText := "3"
	if err := askOne(&survey.Input{
		Message: "要录入几台服务器:",
		Default: "3",
	}, &countText); err != nil {
		return nil, err
	}
	count := 3
	if parsed, err := strconv.Atoi(strings.TrimSpace(countText)); err == nil {
		count = parsed
	}
	if count <= 0 {
		count = 1
	}
	if count > 100 {
		count = 100
	}
	return runTargetEntriesWizard(count)
}

func runTargetEntriesWizard(count int) ([]config.TargetConfig, error) {
	targets := make([]config.TargetConfig, 0, count)
	used := map[string]bool{}
	for i := 1; i <= count; i++ {
		var t config.TargetConfig
		defaultName := fmt.Sprintf("target-%02d", i)
		if err := ask([]*survey.Question{
			{Name: "name", Prompt: &survey.Input{Message: fmt.Sprintf("第 %d 台名称:", i), Default: defaultName}},
			{Name: "protocol", Prompt: &survey.Select{Message: "连接协议:", Options: []string{"ssh", "telnet", "ftp"}, Default: "ssh"}},
			{Name: "host", Prompt: &survey.Input{Message: "主机 (IP[:Port]):"}, Validate: survey.Required},
			{Name: "user", Prompt: &survey.Input{Message: "用户名:", Default: "root"}},
		}, &t); err != nil {
			return nil, err
		}
		t.Name = uniqueTargetName(firstNonEmptyString(t.Name, defaultName), used)
		// FTP's default port depends on the TLS mode selected below (:21 for
		// plain/explicit, :990 for implicit), so leave a portless FTP host for
		// the executor to normalize after that choice is known.
		if t.Protocol != "ftp" {
			t.Host = normalizeTargetHost(t.Protocol, t.Host)
		}
		switch t.Protocol {
		case "ssh":
			auth := ""
			if err := askOne(&survey.Select{Message: "SSH 认证方式:", Options: []string{"Password", "Private Key"}, Default: "Password"}, &auth); err != nil {
				return nil, err
			}
			if auth == "Private Key" {
				defKey := ""
				if home, err := os.UserHomeDir(); err == nil {
					defKey = filepath.Join(home, ".ssh", "id_rsa")
				}
				if err := askOne(&survey.Input{Message: "私钥路径:", Default: defKey}, &t.KeyPath); err != nil {
					return nil, err
				}
			} else {
				if err := askOne(&survey.Password{Message: "密码:"}, &t.Password); err != nil {
					return nil, err
				}
			}
			if err := askOne(&survey.Select{Message: "SSH 目标类型:", Options: []string{"auto", "linux", "huawei", "h3c", "ruijie", "cisco", "generic"}, Default: "auto", Help: "网络设备将使用 PTY 交互 CLI，不会拼接 Linux shell marker。"}, &t.DeviceType); err != nil {
				return nil, err
			}
			if err := askOne(&survey.Input{Message: "网络设备命令 Prompt（可空自动探测）:", Help: "例如 <Core-Switch> 或 RG-S5750#；正则请用 regex: 前缀。"}, &t.Prompt); err != nil {
				return nil, err
			}
			if err := askOne(&survey.Password{Message: "特权密码（华为/H3C super，Cisco/锐捷 enable；可选）:"}, &t.EnablePassword); err != nil {
				return nil, err
			}
		case "telnet":
			if err := askOne(&survey.Password{Message: "Telnet 密码:"}, &t.Password); err != nil {
				return nil, err
			}
			if err := askOne(&survey.Select{Message: "网络设备类型:", Options: []string{"auto", "huawei", "h3c", "ruijie", "cisco", "linux", "generic"}, Default: "auto", Help: "推荐 auto；华为 VRP/H3C Comware/锐捷 RGOS 可显式指定以自动关闭分页。"}, &t.DeviceType); err != nil {
				return nil, err
			}
			if err := askOne(&survey.Input{Message: "命令 Prompt (可空，自动探测):", Help: "只用于登录完成后的命令边界，例如 <Hexin_S12708>；正则请用 regex: 前缀。"}, &t.Prompt); err != nil {
				return nil, err
			}
			if err := askOne(&survey.Input{Message: "认证 Prompt 正则 (可空):", Help: "仅在设备密码提示非常规时填写，例如 (?i)passcode[:：]；不会用于命令结束判断。"}, &t.AuthPromptRegex); err != nil {
				return nil, err
			}
			if err := askOne(&survey.Password{Message: "特权密码（华为/H3C super，Cisco/锐捷 enable；可选）:"}, &t.EnablePassword); err != nil {
				return nil, err
			}
		case "ftp":
			if t.User == "" {
				t.User = "anonymous"
			}
			if err := askOne(&survey.Password{Message: "FTP 密码 (匿名可空):"}, &t.Password); err != nil {
				return nil, err
			}
			if err := askOne(&survey.Select{Message: "FTP TLS 模式:", Options: []string{"plain", "explicit", "implicit"}, Default: "plain", Help: "生产环境建议 explicit；implicit 通常使用 990 端口。"}, &t.FTPTLSMode); err != nil {
				return nil, err
			}
			if t.FTPTLSMode != "plain" {
				if err := askOne(&survey.Input{Message: "TLS 证书主机名 (可空):", Help: "留空使用 FTP host；连接 IP 但证书签发给域名时填证书域名。"}, &t.FTPTLSServerName); err != nil {
					return nil, err
				}
				if err := askOne(&survey.Input{Message: "私有 CA PEM 路径 (可空):"}, &t.FTPTLSCAFile); err != nil {
					return nil, err
				}
			}
			if err := askOne(&survey.Select{Message: "FTP 数据通道:", Options: []string{"passive", "active", "auto"}, Default: "passive", Help: "被动模式对 NAT/防火墙更友好；主动模式需允许服务端回连。"}, &t.FTPDataMode); err != nil {
				return nil, err
			}
			if t.FTPDataMode == "active" {
				if err := askOne(&survey.Input{Message: "主动模式对外 IP (可空自动):"}, &t.FTPActiveAddress); err != nil {
					return nil, err
				}
			}
		}
		tags := ""
		if err := askOne(&survey.Input{Message: "标签 (逗号分隔，可空):"}, &tags); err != nil {
			return nil, err
		}
		t.Tags = splitTags(tags)
		targets = append(targets, t)
	}
	return targets, nil
}

func applySingleTargetConfig(t config.TargetConfig) {
	clearSingleTargetConfig()
	viper.Set("target_protocol", t.Protocol)
	switch t.Protocol {
	case "telnet":
		viper.Set("telnet_host", t.Host)
		viper.Set("telnet_user", firstNonEmptyString(t.User, "root"))
		viper.Set("telnet_password", t.Password)
		viper.Set("telnet_prompt", t.Prompt)
		viper.Set("telnet_auth_prompt_regex", t.AuthPromptRegex)
		viper.Set("telnet_device_type", firstNonEmptyString(t.DeviceType, "auto"))
		viper.Set("telnet_enable_password", t.EnablePassword)
	case "ftp":
		viper.Set("ftp_host", t.Host)
		viper.Set("ftp_user", firstNonEmptyString(t.User, "anonymous"))
		viper.Set("ftp_password", t.Password)
		viper.Set("ftp_tls_mode", firstNonEmptyString(t.FTPTLSMode, "plain"))
		viper.Set("ftp_tls_server_name", t.FTPTLSServerName)
		viper.Set("ftp_tls_ca_file", t.FTPTLSCAFile)
		viper.Set("ftp_tls_insecure_skip_verify", t.FTPTLSInsecureSkipVerify)
		viper.Set("ftp_data_mode", firstNonEmptyString(t.FTPDataMode, "passive"))
		viper.Set("ftp_active_address", t.FTPActiveAddress)
	default:
		viper.Set("target_protocol", "ssh")
		viper.Set("ssh_host", t.Host)
		viper.Set("ssh_user", firstNonEmptyString(t.User, "root"))
		viper.Set("ssh_password", t.Password)
		viper.Set("ssh_key_path", t.KeyPath)
		viper.Set("ssh_device_type", firstNonEmptyString(t.DeviceType, "auto"))
		viper.Set("ssh_prompt", t.Prompt)
		viper.Set("ssh_enable_password", t.EnablePassword)
	}
}

func clearSingleTargetConfig() {
	for _, key := range []string{
		"ssh_host", "ssh_user", "ssh_password", "ssh_key_path", "ssh_device_type", "ssh_prompt", "ssh_enable_password",
		"telnet_host", "telnet_user", "telnet_password", "telnet_prompt", "telnet_auth_prompt_regex", "telnet_device_type", "telnet_enable_password",
		"ftp_host", "ftp_user", "ftp_password", "ftp_tls_mode", "ftp_tls_server_name", "ftp_tls_ca_file", "ftp_data_mode", "ftp_active_address",
	} {
		viper.Set(key, "")
	}
	viper.Set("ftp_tls_insecure_skip_verify", false)
}

func normalizeTargetHost(protocol, host string) string {
	host = strings.TrimSpace(host)
	if host == "" || strings.Contains(host, ":") {
		return host
	}
	switch protocol {
	case "telnet":
		return host + ":23"
	case "ftp":
		return host + ":21"
	default:
		return host + ":22"
	}
}

func uniqueTargetName(name string, used map[string]bool) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "target"
	}
	out := base
	for i := 2; used[out]; i++ {
		out = fmt.Sprintf("%s-%d", base, i)
	}
	used[out] = true
	return out
}

func splitTags(raw string) []string {
	var tags []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return tags
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func resumeUserSupplement(taskFlag, taskShort string, args []string) string {
	if strings.TrimSpace(taskFlag) != "" {
		return strings.TrimSpace(taskFlag)
	}
	if strings.TrimSpace(taskShort) != "" {
		return strings.TrimSpace(taskShort)
	}
	if len(args) > 0 {
		return strings.TrimSpace(strings.Join(args, " "))
	}
	return ""
}

const contextWindowAutoChoice = "自动（推荐，优先读取本地运行时）"
const contextWindowCustomChoice = "自定义"

var contextWindowChoices = []string{
	contextWindowAutoChoice,
	"64K (65,536 tokens)",
	"128K (131,072 tokens)",
	"256K (262,144 tokens)",
	"512K (524,288 tokens)",
	"1M (1,048,576 tokens)",
	"2M (2,097,152 tokens)",
	contextWindowCustomChoice,
}

func contextWindowTokensFromChoice(choice string) (int, bool) {
	switch strings.TrimSpace(choice) {
	case contextWindowAutoChoice:
		return 0, true
	case "64K (65,536 tokens)":
		return 65_536, true
	case "128K (131,072 tokens)":
		return 131_072, true
	case "256K (262,144 tokens)":
		return 262_144, true
	case "512K (524,288 tokens)":
		return 524_288, true
	case "1M (1,048,576 tokens)":
		return 1_048_576, true
	case "2M (2,097,152 tokens)":
		return 2_097_152, true
	default:
		return 0, false
	}
}

func contextWindowDefaultChoice(tokens int) string {
	for _, choice := range contextWindowChoices {
		if value, ok := contextWindowTokensFromChoice(choice); ok && value == tokens {
			return choice
		}
	}
	if tokens > 0 {
		return contextWindowCustomChoice
	}
	return contextWindowAutoChoice
}

func validateCustomContextWindow(value interface{}) error {
	n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(value)))
	if err != nil || n < 4_096 || n > 4_194_304 {
		return fmt.Errorf("请输入 4096-4194304 的整数 token")
	}
	return nil
}

var wizardProviderOrder = []string{"deepseek", "qwen", "qianfan", "volcengine", "teleai", "hunyuan", "openai", "anthropic", "google", "minimax", "glm", "mimo", "xai", "ollama", "lmstudio", "vllm", "llamacpp", "sglang", "localai"}

const (
	localVisionChoiceAuto     = "自动识别（无法确认时关闭图片）"
	localVisionChoiceEnabled  = "支持图片理解"
	localVisionChoiceDisabled = "仅支持文本"
)

func localVisionMode(choice string) string {
	switch choice {
	case localVisionChoiceEnabled:
		return "enabled"
	case localVisionChoiceDisabled:
		return "disabled"
	default:
		return "auto"
	}
}

func wizardProviderLabel(id string) string {
	p, ok := config.FindProvider(id)
	if !ok {
		return "其他 (自定义/中转)"
	}
	detail := p.Model
	if model, ok := config.FindModelPreset(id, p.Model); ok && model.SupportsVision {
		detail += " · 图片理解"
	}
	if id == "deepseek" {
		detail += " · 推荐"
	}
	if config.IsLocalProvider(id) {
		detail = "选择服务可用模型 · 检测图片能力"
	}
	return p.DisplayName + " (" + detail + ")"
}

var wizardProviderOptions = func() []string {
	var labels []string
	for _, id := range wizardProviderOrder {
		labels = append(labels, wizardProviderLabel(id))
	}
	return append(labels, "其他 (自定义/中转)")
}()
var wizardRecommendedProvider = wizardProviderLabel("deepseek")

func wizardProviderID(label string) string {
	for _, id := range wizardProviderOrder {
		if wizardProviderLabel(id) == label {
			return id
		}
	}
	return "custom"
}

func wizardModelSuggestions(provider string, discovered ...string) func(string) []string {
	return func(query string) []string {
		var ids []string
		candidates := discovered
		if len(candidates) == 0 {
			candidates = config.ProviderModelSuggestions(provider)
		}
		for _, id := range candidates {
			if strings.Contains(strings.ToLower(id), strings.ToLower(strings.TrimSpace(query))) {
				ids = append(ids, id)
			}
		}
		return ids
	}
}

var wizardThemeOptions = []string{
	"自动适应终端背景（推荐）",
	"深色主题（固定）",
	"日间主题（固定）",
}

func wizardTerminalTheme(choice string) string {
	switch {
	case strings.Contains(choice, "日间"):
		return "light"
	case strings.Contains(choice, "深色"):
		return "dark"
	default:
		return "auto"
	}
}

func wizardThemeDefault(theme string) string {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "light":
		return wizardThemeOptions[2]
	case "dark":
		return wizardThemeOptions[1]
	default:
		return wizardThemeOptions[0]
	}
}

// runElegantWizard 完整初始化向导
func runElegantWizard() error {
	fmt.Println("\n" + ui.Prefix("🛠️", "[INIT]") + ui.StripANSIIfPlain("\033[1;34mDeepSentry 初始化向导\033[0m"))
	fmt.Println("-------------------------------------------")

	var themeChoice string
	if err := askOne(&survey.Select{
		Message: ui.Prefix("🎨", "[THEME]") + "终端主题:",
		Options: wizardThemeOptions,
		Default: wizardThemeDefault(viper.GetString("terminal_theme")),
		Help:    "auto 会探测终端背景明暗；远程终端探测不准时可固定 dark/light。",
	}, &themeChoice); err != nil {
		return err
	}
	terminalTheme := wizardTerminalTheme(themeChoice)
	viper.Set("terminal_theme", terminalTheme)
	tui.ConfigureTerminalPreferences(terminalTheme)

	// 1. 第一步：选择 AI 提供商 (用于生成智能默认值)
	var providerLabel string
	providerPrompt := &survey.Select{
		Message: ui.Prefix("🤖", "[AI]") + "请选择您的 AI 模型服务商:",
		Options: wizardProviderOptions,
		Default: wizardRecommendedProvider,
	}
	if err := askOne(providerPrompt, &providerLabel); err != nil {
		return err
	}

	providerID := wizardProviderID(providerLabel)
	defaultURL := ""
	defaultModel := ""
	urlHelp := "请输入 API Base URL（可只填 /v1，自动补全 chat/completions）"

	switch providerID {
	case "deepseek":
		urlHelp = "DeepSeek 官方 OpenAI 兼容 Base URL；默认 deepseek-flash（产品名 V4.1 Flash）原生多模态，可接收图片与 MCP 截图回灌"
	case "openai":
		urlHelp = "OpenAI 官方 Base URL；默认 GPT-6 Astra (gpt-6-astra)，可选 GPT-6 Sol/Luna，走 Responses API"
	case "anthropic":
		urlHelp = "Anthropic 官方 Messages API Base URL；默认 claude-opus-5-5；高难度任务可选 claude-fable-5-1"
	case "google":
		urlHelp = "Gemini 官方 OpenAI 兼容 Base URL；默认 gemini-3.8-flash"
	case "qwen":
		urlHelp = "阿里云百炼 OpenAI 兼容 Base URL；默认 qwen3.8-max，更快可选 qwen3.8-flash"
	case "hunyuan":
		urlHelp = "腾讯云 TokenHub OpenAI 兼容 Base URL；hunyuan-turbos-latest 已下线，默认 hy4-preview"
	case "minimax":
		urlHelp = "MiniMax 中国站 OpenAI 兼容 Base URL；MiniMax-M3 支持图片/视频输入、1M 上下文和工具调用"
	case "glm":
		urlHelp = "智谱标准 API Base URL；glm-5.3-flash 支持图片/视频/文件输入、1M 上下文和工具调用"
	case "qianfan":
		urlHelp = "千帆 Coding Plan 官方 OpenAI 兼容 Base URL；使用 Coding Plan 订阅 API Key"
	case "volcengine":
		urlHelp = "火山方舟 Coding Plan 官方 OpenAI 兼容 Base URL；请使用 Coding Plan 专用地址与 API Key"
	case "teleai":
		urlHelp = "电信星辰/TokenHub OpenAI 兼容 Base URL；如控制台分配了专属 endpoint，请覆盖此值"
	case "mimo":
		urlHelp = "MiMo Token Plan 中国站 OpenAI 兼容 Base URL；默认 mimo-v2.6-pro，更快可选 mimo-v2.6-flash 或 mimo-v2.6-pro-ultraspeed"
	case "xai":
		urlHelp = "xAI 官方 OpenAI 兼容 Base URL；默认 Grok 4.7，模型 ID 为 grok-4.7"
	case "lmstudio":
		urlHelp = "LM Studio 本地服务默认端口 1234；请先在 LM Studio 中启动服务并加载模型"
	case "vllm":
		urlHelp = "vLLM OpenAI 兼容服务默认端口 8000；如配置了 --api-key，请填写密钥"
	case "llamacpp":
		urlHelp = "llama.cpp 的 llama-server 默认端口 8080；模型 ID 以 /v1/models 返回值为准"
	case "sglang":
		urlHelp = "SGLang OpenAI 兼容服务默认端口 30000"
	case "localai":
		urlHelp = "LocalAI OpenAI 兼容服务默认端口 8080"
	}

	if preset, ok := config.FindProvider(providerID); ok && defaultURL == "" {
		defaultURL = preset.APIURL
		defaultModel = preset.Model
		if providerID == "ollama" {
			urlHelp = "Ollama OpenAI 兼容端点，默认端口 11434；请先 pull 模型并启动服务"
		}
	}
	localProvider := config.IsLocalProvider(providerID)
	var localURL, localKey string
	var localModels []string
	if localProvider {
		// Historical Ollama/LM Studio presets are kept for existing config files,
		// but a new setup must select a model that this server actually exposes.
		defaultModel = ""
		if err := askOne(&survey.Input{
			Message: ui.Prefix("🌐", "[URL]") + "本地模型 API 地址:",
			Default: defaultURL,
			Help:    urlHelp,
		}, &localURL, survey.WithValidator(func(value interface{}) error {
			_, err := config.LocalModelsURL(fmt.Sprint(value))
			return err
		})); err != nil {
			return err
		}
		if err := askOne(&survey.Password{
			Message: ui.Prefix("🔑", "[KEY]") + "API Key (未启用鉴权可回车跳过):",
			Help:    "vLLM 等服务如启用了 API Key，请填写；否则留空",
		}, &localKey); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		discovered, discoverErr := config.DiscoverLocalModels(ctx, localURL, localKey)
		cancel()
		if discoverErr != nil {
			fmt.Printf("%s无法读取模型列表（%v）；请手动填写服务实际加载的 Model ID。\n", ui.Prefix("⚠️", "[WARN]"), discoverErr)
			defaultModel = ""
		} else {
			localModels = discovered
			if len(localModels) == 1 {
				defaultModel = localModels[0]
			}
			fmt.Printf("%s发现 %d 个可用模型；下一步按 Tab 查看候选并选择 Model ID。\n", ui.Prefix("✅", "[OK]"), len(localModels))
		}
	}
	contextDefault := contextWindowDefaultChoice(viper.GetInt("context_window_tokens"))
	if localProvider && !config.IsLocalProvider(viper.GetString("provider")) {
		contextDefault = contextWindowAutoChoice
	}

	// 3. 构建核心配置问题 (带动态默认值)
	var qs = []*survey.Question{
		{
			Name: "api_protocol",
			Prompt: &survey.Select{
				Message: ui.Prefix("🔌", "[API]") + "API 协议:",
				Options: []string{"auto", "openai_chat", "anthropic_messages", "openai_responses"},
				Default: "auto",
				Help:    "大多数国产/本地模型选 auto/openai_chat；Claude 选 anthropic_messages；OpenAI Responses 可手动选择 openai_responses",
			},
		},
		{
			Name: "api_url",
			Prompt: &survey.Input{
				Message: ui.Prefix("🌐", "[URL]") + "API 地址 (Endpoint):",
				Default: defaultURL,
				Help:    urlHelp,
			},
			Validate: survey.Required,
		},
		{
			Name: "model_name",
			Prompt: &survey.Input{
				Message: ui.Prefix("🧠", "[MODEL]") + "模型名称 (Model ID):",
				Default: defaultModel,
				Suggest: wizardModelSuggestions(providerID, localModels...),
				Help:    "按 Tab 查看本提供商模型；也可直接输入控制台允许的 Model ID。本地服务请填写实际可用的模型 ID",
			},
			Validate: survey.Required,
		},
		{
			Name: "context_window_choice",
			Prompt: &survey.Select{
				Message: ui.Prefix("📏", "[CTX]") + "模型/服务端实际上下文长度:",
				Options: contextWindowChoices,
				Default: contextDefault,
				Help:    "应填 API 或本地运行时的实际限制，不确定时选自动；本地服务需与实际 num_ctx/max_model_len 等设置一致",
			},
		},
		{
			Name: "api_key",
			Prompt: &survey.Password{
				Message: ui.Prefix("🔑", "[KEY]") + "API Key (本地模型可回车跳过):",
				Help:    "云端 API 一般必填；本地服务如果启用了鉴权，也应填写对应密钥",
			},
		},
		// 🟢 1. 在向导中增加最大轮数配置
		{
			Name: "max_steps",
			Prompt: &survey.Input{
				Message: ui.Prefix("🔄", "[STEP]") + "最大对话轮数 (Max Steps):",
				Default: "30",
				Help:    "防止 AI 陷入死循环的最大交互次数",
			},
		},
		{
			Name: "subagent_max_steps",
			Prompt: &survey.Input{
				Message: ui.Prefix("🔀", "[SUB]") + "子 Agent 最大步数上限:",
				Default: "15",
				Help:    "AI 可按任务难度估算子 Agent 步数，但不会超过这个用户上限",
			},
		},
	}
	if localProvider {
		// The local endpoint/key were collected before querying /v1/models.
		// Do not ask them again alongside the remaining questions.
		filtered := qs[:0]
		for _, q := range qs {
			if q.Name != "api_url" && q.Name != "api_key" {
				filtered = append(filtered, q)
			}
		}
		qs = filtered
		qs = append(qs,
			&survey.Question{
				Name: "model_parameter_b",
				Prompt: &survey.Input{
					Message: ui.Prefix("🧠", "[MODEL]") + "模型参数量（B，用于指令密度适配）:",
					Default: "0",
					Help:    "模型名已含 14b/20b/30b/70b 时可留 0 自动识别",
				},
				Validate: func(value interface{}) error {
					n, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(value)), 64)
					if err != nil || n < 0 || n > 1000 {
						return fmt.Errorf("请输入 0-1000 的数字")
					}
					return nil
				},
			},
		)
	}

	answers := struct {
		ApiUrl           string `survey:"api_url"`
		Protocol         string `survey:"api_protocol"`
		ModelName        string `survey:"model_name"`
		ApiKey           string `survey:"api_key"`
		MaxSteps         string `survey:"max_steps"`
		SubAgentMaxSteps string `survey:"subagent_max_steps"`
		ModelParameterB  string `survey:"model_parameter_b"`
		ContextChoice    string `survey:"context_window_choice"`
	}{}

	// 执行问答
	err := ask(qs, &answers)
	if err != nil {
		return err
	}
	if localProvider {
		answers.ApiUrl = localURL
		answers.ApiKey = localKey
	}
	localRuntime := config.LocalModelRuntimeInfo{}
	if localProvider {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		var runtimeErr error
		localRuntime, runtimeErr = config.DiscoverLocalModelRuntime(ctx, providerID, answers.ApiUrl, answers.ApiKey, answers.ModelName)
		cancel()
		if runtimeErr != nil {
			fmt.Printf("%s未能读取运行时配置（%v）；将使用手动选择或保守默认值。\n", ui.Prefix("⚠️", "[WARN]"), runtimeErr)
		}
	}
	contextWindowTokens, knownChoice := contextWindowTokensFromChoice(answers.ContextChoice)
	if !knownChoice {
		customDefault := viper.GetInt("context_window_tokens")
		if customDefault <= 0 {
			customDefault = 32_768
		}
		customValue := strconv.Itoa(customDefault)
		if err := askOne(&survey.Input{
			Message: ui.Prefix("📏", "[CTX]") + "自定义上下文长度（tokens）:",
			Default: customValue,
			Help:    "范围 4096-4194304；请使用精确 token 数",
		}, &customValue, survey.WithValidator(validateCustomContextWindow)); err != nil {
			return err
		}
		contextWindowTokens, _ = strconv.Atoi(strings.TrimSpace(customValue))
	}
	if localProvider && contextWindowTokens == 0 && localRuntime.ContextWindowTokens > 0 {
		contextWindowTokens = localRuntime.ContextWindowTokens
		fmt.Printf("%s使用当前已加载实例的上下文窗口：%d tokens。\n", ui.Prefix("✅", "[OK]"), contextWindowTokens)
	}

	if answers.ApiKey == "" {
		answers.ApiKey = "none"
	}
	visionMode := "auto"
	localNativeTools := false
	if localProvider {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		vision, visionErr := config.DiscoverLocalModelVision(ctx, providerID, answers.ApiUrl, answers.ApiKey, answers.ModelName)
		cancel()
		visionDefault := localVisionChoiceAuto
		switch {
		case visionErr != nil:
			fmt.Printf("%s无法读取该模型的图片能力（%v）；可根据已加载模型和服务配置手动选择。\n", ui.Prefix("⚠️", "[WARN]"), visionErr)
		case vision.Known && vision.Supported:
			visionDefault = localVisionChoiceEnabled
			fmt.Printf("%s本地服务报告模型 %q 支持图片理解。\n", ui.Prefix("✅", "[OK]"), answers.ModelName)
		case vision.Known:
			visionDefault = localVisionChoiceDisabled
			fmt.Printf("%s本地服务报告模型 %q 不支持图片理解。\n", ui.Prefix("ℹ️", "[INFO]"), answers.ModelName)
		default:
			fmt.Printf("%s本地服务没有提供模型 %q 的明确图片能力信息；请按实际模型与服务配置选择。\n", ui.Prefix("ℹ️", "[INFO]"), answers.ModelName)
		}
		visionChoice := visionDefault
		if err := askOne(&survey.Select{
			Message: ui.Prefix("🖼️", "[VISION]") + "当前本地模型能否理解图片:",
			Options: []string{localVisionChoiceAuto, localVisionChoiceEnabled, localVisionChoiceDisabled},
			Default: visionDefault,
			Help:    "以当前加载的模型及服务端多模态配置为准；自动模式无法确认时会关闭图片输入。",
		}, &visionChoice); err != nil {
			return err
		}
		visionMode = localVisionMode(visionChoice)
		nativeDefault := localRuntime.NativeToolsKnown && localRuntime.NativeToolsSupported &&
			(answers.Protocol == "auto" || answers.Protocol == "openai_chat")
		if localRuntime.NativeToolsKnown {
			fmt.Printf("%s模型工具调用能力：%s。\n", ui.Prefix("🛠️", "[TOOLS]"), map[bool]string{true: "支持", false: "未报告支持"}[localRuntime.NativeToolsSupported])
		}
		localNativeTools = nativeDefault
		if err := askOne(&survey.Confirm{
			Message: ui.Prefix("🛠️", "[TOOLS]") + "启用原生工具调用（提高本地 Agent 执行动作的可靠性）?",
			Default: nativeDefault,
			Help:    "仅当模型和本地服务都支持 OpenAI 工具调用时启用；不支持时可关闭，使用 JSON 兼容路径。",
		}, &localNativeTools); err != nil {
			return err
		}
	}

	// 4. 保存配置
	viper.Set("provider", providerID)
	viper.Set("api_protocol", answers.Protocol)
	viper.Set("api_url", answers.ApiUrl)
	viper.Set("model_name", answers.ModelName)
	viper.Set("api_key", answers.ApiKey)
	viper.Set("model_profile", "auto")
	viper.Set("context_window_tokens", contextWindowTokens)
	viper.Set("vision_mode", visionMode)
	if preset, ok := config.FindProvider(providerID); ok {
		viper.Set("use_native_tools", preset.NativeTools)
	}
	if localProvider {
		viper.Set("use_native_tools", localNativeTools)
		viper.Set("model_parameter_b", answers.ModelParameterB)
	}
	// 🟢 2. 保存最大轮数 (Viper 会自动处理类型，这里存为字符串或数字均可被 GetInt 读取)
	viper.Set("max_steps", answers.MaxSteps)
	viper.Set("subagent_max_steps", answers.SubAgentMaxSteps)
	if err := runTSecBenchWizard(); err != nil {
		return err
	}

	mode := ""
	fmt.Println(ui.Prefix("💡", "[TIP]") + "管理模式默认本地任务；Windows cmd 若上下键无效，可用 Tab / Shift+Tab 切换，Enter 确认。")
	if err := askOne(&survey.Select{
		Message: ui.Prefix("🖥️", "[MODE]") + "管理模式:",
		Options: []string{"本地任务", "远程管理 1 台服务器", "远程管理多台服务器 (Fleet)"},
		Default: "本地任务",
		Help:    "Windows cmd 里方向键可能被终端拦截，可用 Tab / Shift+Tab 切换选项。",
	}, &mode); err != nil {
		return err
	}

	var saveErr error
	switch {
	case strings.Contains(mode, "多台"):
		targets, wizardErr := runTargetsWizard()
		if wizardErr != nil {
			return wizardErr
		}
		viper.Set("targets", targets)
		viper.Set("target_protocol", "local")
		clearSingleTargetConfig()
		if saveErr = config.SaveConfig(); saveErr != nil {
			fmt.Printf("%s配置保存失败: %v\n", ui.Prefix("❌", "[ERR]"), saveErr)
		} else {
			fmt.Printf("%s已保存 %d 台 Fleet 目标至 config.yaml\n", ui.Prefix("✅", "[OK]"), len(targets))
		}
	case strings.Contains(mode, "1 台"):
		target, wizardErr := runSingleTargetWizard()
		if wizardErr != nil {
			return wizardErr
		}
		viper.Set("targets", []config.TargetConfig(nil))
		applySingleTargetConfig(target)
		if saveErr = config.SaveConfig(); saveErr != nil {
			fmt.Printf("%s配置保存失败: %v\n", ui.Prefix("❌", "[ERR]"), saveErr)
		} else {
			fmt.Println(ui.Prefix("✅", "[OK]") + "单台远程配置已保存至 config.yaml")
		}
	default:
		viper.Set("target_protocol", "local")
		viper.Set("targets", []config.TargetConfig(nil))
		clearSingleTargetConfig()
		if saveErr = config.SaveConfig(); saveErr != nil {
			fmt.Printf("%s配置保存失败: %v\n", ui.Prefix("❌", "[ERR]"), saveErr)
		} else {
			fmt.Println(ui.Prefix("✅", "[OK]") + "配置已保存至 config.yaml")
		}
	}
	if saveErr != nil {
		return saveErr
	}

	// 刷新全局配置
	var loaded config.Config
	if err := viper.Unmarshal(&loaded); err != nil {
		return fmt.Errorf("刷新配置失败: %w", err)
	}
	config.ApplyProviderDefaults(&loaded)
	if err := config.ValidateRuntimeConfig(loaded); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	config.GlobalConfig = loaded
	fmt.Println("-------------------------------------------")
	ui.ResetTerminalState()
	return nil
}

func runTSecBenchWizard() error {
	enable := false
	if err := askOne(&survey.Confirm{
		Message: ui.Prefix("🏁", "[TSEC]") + "是否配置 /tsecbench 跑分模式?",
		Default: false,
		Help:    "配置后可在 TUI 中输入 /tsecbench，Agent 会使用 TSecBench API 拉题、启动容器、提交 flag 并关闭容器。",
	}, &enable); err != nil {
		return err
	}
	if !enable {
		return nil
	}

	defaultBase := firstNonEmptyString(
		viper.GetString("benchmark_base_url"),
		viper.GetString("BENCHMARK_BASE_URL"),
		os.Getenv("BENCHMARK_BASE_URL"),
		"https://tsecbench.zc.tencent.com",
	)
	var answers struct {
		BaseURL string `survey:"benchmark_base_url"`
		Token   string `survey:"benchmark_token"`
	}
	qs := []*survey.Question{
		{
			Name: "benchmark_base_url",
			Prompt: &survey.Input{
				Message: ui.Prefix("🌐", "[TSEC]") + "benchmark_base_url:",
				Default: defaultBase,
				Help:    "例如 https://tsecbench.zc.tencent.com",
			},
			Validate: survey.Required,
		},
		{
			Name: "benchmark_token",
			Prompt: &survey.Password{
				Message: ui.Prefix("🔑", "[TSEC]") + "benchmark_token:",
				Help:    "平台创建跑分任务后下发的 BENCHMARK_TOKEN；保存到 config.yaml，不会在报告中明文输出。",
			},
			Validate: survey.Required,
		},
	}
	if err := ask(qs, &answers); err != nil {
		return err
	}
	viper.Set("benchmark_base_url", strings.TrimSpace(answers.BaseURL))
	viper.Set("benchmark_token", strings.TrimSpace(answers.Token))
	fmt.Println(ui.Prefix("✅", "[OK]") + "已启用 /tsecbench 跑分模式配置")
	return nil
}
