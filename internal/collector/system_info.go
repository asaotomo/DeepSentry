package collector

import (
	"ai-edr/internal/executor"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// SystemContext 存储全维度的系统指纹 (目标系统)
type SystemContext struct {
	OS             string
	Arch           string
	Hostname       string
	KernelVersion  string
	Uptime         string
	Username       string
	IsRoot         bool
	MemoryStatus   string
	DiskStatus     string
	CPUInfo        string
	LocalIPs       []string
	Virtualization string
	Shell          string
	PackageManager string
	DeviceType     string
	DevicePrompt   string
	DeviceProtocol string
}

// GetSystemContext 采集系统信息 (针对当前 Executor 指向的目标)
func GetSystemContext() SystemContext {
	ctx := SystemContext{}
	if reporter, ok := executor.Current.(executor.NetworkDeviceReporter); ok {
		info := reporter.NetworkDeviceInfo()
		if info.Vendor != "" && info.Vendor != "linux" {
			ctx.DeviceType = info.Vendor
			ctx.DevicePrompt = info.Prompt
			ctx.DeviceProtocol = executor.CurrentMode()
			ctx.OS = networkDeviceOS(info.Vendor)
			ctx.Arch = "Network Appliance"
			ctx.Hostname = hostnameFromNetworkPrompt(info.Prompt)
			ctx.Username = "CLI user"
			ctx.Shell = "Network device CLI"
			ctx.KernelVersion = "通过 display/show version 获取"
			ctx.MemoryStatus = "通过 network_device_baseline 或厂商 display/show 命令获取"
			ctx.PackageManager = "not-applicable"
			return ctx
		}
	}

	// 辅助函数：通过当前 Executor 执行命令
	run := func(cmd string) string {
		if executor.Current == nil {
			return ""
		}
		// 使用 Run 方法获取输出
		out, _ := executor.Current.Run(cmd)
		return strings.TrimSpace(out)
	}

	// --------------------------------------------------------
	// 1. 核心修复：智能识别目标系统类型
	// --------------------------------------------------------
	isWindows := false
	isRemote := executor.Current != nil && executor.Current.IsRemote()

	if !isRemote && runtime.GOOS == "windows" {
		if local, ok := localWindowsContext(); ok {
			return local
		}
	}

	if !isRemote {
		// [本地模式] 优先使用 Go 运行时信息，避免执行 uname 报错
		if runtime.GOOS == "windows" {
			isWindows = true
			// 🟢 修复点：移除 cmd /c 包装，直接调用 ver 减少引号解析风险
			verOut := run("ver")
			if verOut != "" && !strings.Contains(strings.ToLower(verOut), "not recognized") {
				ctx.OS = verOut
			} else {
				ctx.OS = "Microsoft Windows (Local)"
			}
		} else {
			// Linux / Darwin (Mac)
			ctx.OS = run("uname -s")
		}
	} else {
		// [远程模式] 优先探测是否为 Windows，防止 uname 报错干扰
		winVer := run("ver")
		if winVer != "" && strings.Contains(strings.ToLower(winVer), "windows") {
			isWindows = true
			ctx.OS = winVer
		} else {
			// 尝试 Linux 的 uname
			osCheck := run("uname -s")
			if osCheck != "" && !strings.Contains(strings.ToLower(osCheck), "not recognized") {
				ctx.OS = osCheck
				// 尝试获取 Linux 发行版名称
				distro := run("grep ^PRETTY_NAME /etc/os-release | cut -d= -f2")
				if distro != "" {
					ctx.OS = strings.Trim(distro, "\"")
				}
			} else {
				ctx.OS = "Unknown System"
			}
		}
	}

	// --------------------------------------------------------
	// 2. 根据系统类型分叉采集
	// --------------------------------------------------------

	if isWindows {
		// === Windows 采集逻辑 ===
		ctx.Arch = run("echo %PROCESSOR_ARCHITECTURE%")
		ctx.Hostname = run("hostname")
		ctx.Username = run("whoami")
		ctx.KernelVersion = ctx.OS

		// Windows 资源信息 (CIM，兼容未安装 WMIC 的 Windows 11)
		ctx.MemoryStatus = run(`powershell -NoProfile -NonInteractive -Command "Get-CimInstance Win32_OperatingSystem | Select-Object TotalVisibleMemorySize,FreePhysicalMemory"`)
		ctx.MemoryStatus = strings.ReplaceAll(ctx.MemoryStatus, "\r\n", " ")

		ctx.DiskStatus = run(`powershell -NoProfile -NonInteractive -Command "Get-CimInstance Win32_LogicalDisk | Select-Object DeviceID,FreeSpace,Size"`)
		ctx.CPUInfo = run(`powershell -NoProfile -NonInteractive -Command "Get-CimInstance Win32_Processor | Select-Object Name"`)

		// 获取 IP 地址
		ctx.LocalIPs = []string{run("ipconfig | findstr IPv4")}

		ctx.Shell = "cmd.exe / powershell.exe"

		// 简单的管理员检测
		adminCheck := run("net session")
		if !strings.Contains(adminCheck, "拒绝访问") && !strings.Contains(adminCheck, "Access is denied") && adminCheck != "" {
			ctx.IsRoot = true
		}

		ctx.PackageManager = "winget/choco"

	} else {
		// === Linux/MacOS 采集逻辑 ===
		ctx.Arch = run("uname -m")
		ctx.Hostname = run("hostname")
		ctx.Username = run("whoami")
		ctx.IsRoot = (run("id -u") == "0")
		ctx.KernelVersion = run("uname -r")

		ctx.Uptime = run("uptime -p")
		if ctx.Uptime == "" {
			ctx.Uptime = run("uptime")
		}

		if strings.Contains(ctx.OS, "Darwin") {
			ctx.MemoryStatus = "MacOS Memory"
			ctx.CPUInfo = run("sysctl -n machdep.cpu.brand_string")
		} else {
			ctx.MemoryStatus = run("free -h | head -n 2")
			ctx.CPUInfo = run("grep 'Model name' /proc/cpuinfo | head -1 | cut -d: -f2")
		}

		ctx.DiskStatus = run("df -h | grep -E '^/dev/|Filesystem|/$'")
		ctx.LocalIPs = []string{run("hostname -I")}

		ctx.Shell = run("echo $SHELL")
		if run("test -f /.dockerenv && echo yes") == "yes" {
			ctx.Virtualization = "docker"
		} else {
			ctx.Virtualization = "physical/vm"
		}

		if run("which apt-get") != "" {
			ctx.PackageManager = "apt-get"
		} else if run("which yum") != "" {
			ctx.PackageManager = "yum"
		} else if run("which apk") != "" {
			ctx.PackageManager = "apk"
		} else if run("which brew") != "" {
			ctx.PackageManager = "homebrew"
		} else {
			ctx.PackageManager = "unknown"
		}
	}

	return ctx
}

// GenerateSystemPrompt 生成 Prompt (核心大脑配置)
func (ctx SystemContext) GenerateSystemPrompt() string {
	userRole := "普通用户"
	if ctx.IsRoot {
		userRole = "Root管理员/系统管理员"
	}

	connectionType := "本地直连 (Local Mode)"
	targetDesc := "当前目标即本机 (Target == Controller)"
	networkGuidance := ""
	if executor.Current != nil && executor.Current.IsRemote() {
		switch executor.CurrentMode() {
		case "telnet":
			connectionType = "Telnet 远程连接 (Telnet Mode)"
			targetDesc = "你正在通过 Telnet 操作远程主机；可执行命令，但文件桥能力弱于 SSH/SFTP"
			if ctx.DeviceType != "" {
				protocol := strings.ToUpper(strings.TrimSpace(ctx.DeviceProtocol))
				if protocol == "" {
					protocol = "交互式"
				}
				targetDesc = fmt.Sprintf("你正在通过 %s 操作 %s 网络设备，当前 prompt=%s", protocol, networkDeviceOS(ctx.DeviceType), ctx.DevicePrompt)
				networkGuidance = networkDevicePromptGuidance(ctx.DeviceType)
			}
		case "ftp":
			connectionType = "FTP 远程连接 (FTP Mode)"
			targetDesc = "你正在通过 FTP 操作远程主机；仅适合目录/文件读取、上传、下载，不支持 shell 命令执行"
		default:
			connectionType = "SSH 远程连接 (SSH Mode)"
			targetDesc = "你正在通过 SSH 操作远程主机 (Target)"
		}
	}

	// 🟢 [新增] 获取本机(控制端) 的系统信息
	localOS := runtime.GOOS
	localArch := runtime.GOARCH
	localShellHint := ""

	// 根据本机系统给出命令建议
	if localOS == "windows" {
		localShellHint = "(本机是 Windows，local_run 请优先使用 CMD 语法，如 dir, type, copy)"
	} else if localOS == "darwin" {
		localShellHint = "(本机是 macOS，local_run 请使用 Bash/Zsh 语法，如 ls, cat, cp)"
	} else {
		localShellHint = "(本机是 Linux，local_run 请使用 Bash 语法)"
	}

	return fmt.Sprintf(`
【系统架构感知】
- 连接模式: %s (%s)
- 你的身份: 智能运维 Agent (运行在 控制端/Controller)
- **控制端环境(本机)**: %s / %s %s
- **控制端当前时间(本机)**: %s
- **目标环境(Target)**:
  - 系统: %s (%s)
  - 用户: %s (%s)
  - 主机名: %s
  - 内核: %s
  - 资源摘要: %s

【核心能力与命令路由】
1. **目标执行 (Target Exec)** - [默认模式]
   - **命令格式**: 直接输入命令 (如 'ls', 'dir')
   - 作用域: 在 **目标环境** 执行。
2. **本机执行 (Controller Exec)**
   - **命令格式**: 前缀 **'local_run '** (例如: 'local_run ls -la')
   - 作用域: 在 **控制端环境** 执行。注意区分本机操作系统！
   - 禁止用裸 ssh/scp/sftp 访问已配置 targets；它们不会读取 DeepSentry 配置里的密码/私钥，可能卡在交互式密码提示。多目标/远程主机请用 fleet_exec、fleet_file 或 target_selector。
3. **数据协同 (Data Bridge)**
   - **上传**: 'upload <本机路径> <远程路径>'
   - **下载**: 'download <远程路径> <本机路径>'
   - FTP 模式下优先使用 file_download/file_upload/read_file/ls，不要执行 shell 命令。
%s

【AI 行为准则】
1. **JSON 格式**: 必须严格返回 JSON，**严禁**使用 Markdown 代码块。
2. **行动法则**: 使用 Deep Agent Harness 的 action 字段 (tool/read_file/execute/finish 等)，禁止仅返回 thought。
3. **拒绝幻觉**: 严禁脑补结果。
4. **环境意识**: 严格区分 '本机' 和 '目标'。如果用户说 "把本机的X上传"，请先确认本机是 Windows 还是 Mac/Linux，再选择正确的 local_run 命令 (dir vs ls)。
5. **自我保护**: **严禁** 移动、删除或修改 'config.yaml', 'deepsentry.exe' 以及 'reports/' 目录。
6. **稳定性约束 (重要)**: 严禁将超过 3 个复杂命令通过 '&&' 拼接。对于复杂的扫描任务（如 grep 多个关键字），必须**拆分成多次交互步骤**执行，防止 SSH 会话因命令过长而崩溃。
7. **JSON 严格语法**: 
   - 字符串内的 **双引号 (")** 必须转义为 **(\")**。
   - 字符串内的 **反斜杠 (\)** 必须转义为 **(\\)**。
     (错误: {"cmd": "grep '\' file"})
     (正确: {"cmd": "grep '\\' file"})
8. **最终报告**: 设置 "is_finished": true 时，必须在 "final_report" 中详细总结。
9. **时间基准**: 「控制端当前时间」为判断「近期」「今天」「最近 N 小时/天」等时效性问题的基准；分析日志与时间线时请以此为准。
`,
		connectionType, targetDesc,
		localOS, localArch, localShellHint,
		formatLocalTime(),
		ctx.OS, ctx.Arch,
		ctx.Username, userRole,
		ctx.Hostname,
		ctx.KernelVersion,
		ctx.MemoryStatus,
		networkGuidance)
}

func networkDeviceOS(vendor string) string {
	switch strings.ToLower(vendor) {
	case "huawei":
		return "Huawei VRP switch/router"
	case "h3c":
		return "H3C Comware switch/router"
	case "ruijie":
		return "Ruijie RGOS switch/router"
	case "cisco":
		return "Cisco IOS switch/router"
	default:
		return "Generic network switch/router"
	}
}

func hostnameFromNetworkPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	prompt = strings.Trim(prompt, "<>[]#$%")
	if index := strings.Index(prompt, "("); index > 0 {
		prompt = prompt[:index]
	}
	if prompt == "" {
		return "network-device"
	}
	return prompt
}

func networkDevicePromptGuidance(vendor string) string {
	commands := "优先使用 network_device_baseline；其他排查使用设备支持的 display/show 命令。"
	switch strings.ToLower(vendor) {
	case "huawei", "h3c":
		commands = "优先使用 network_device_baseline；常用只读命令为 display version、display device、display interface brief、display ip routing-table、display logbuffer。"
	case "ruijie", "cisco":
		commands = "优先使用 network_device_baseline；常用只读命令为 show version、show interfaces status、show ip interface brief、show ip route、show logging。"
	}
	return `
【网络设备 CLI 约束】
- 目标不是 Linux/Windows Shell，禁止 uname、ls、cat、grep、sudo、分号、&&、$?、heredoc 和 Shell marker。
- 单接口详情（如 display interface GigabitEthernet1/0/1）默认直接读取完整输出，不要先拼接长串 ` + "`| include a|b|c`" + `；include 是区分大小写的行投影，会丢失关联上下文。
- 只有结果明确出现 output_truncated=true、命令超时或未找回 prompt 才称为“截断”；projection=filtered 表示设备主动过滤，TUI 的“展开完整结果”只是界面折叠。
- ` + commands + `
- 默认保持只读；进入 system-view/configure terminal、保存配置、重启端口/设备、修改 ACL/路由/VLAN 必须按中高风险操作审批。
- Telnet 运行时会自动处理命令 prompt 和 More 分页；不要自行追加分页关闭命令或结束标记。
`
}

// formatLocalTime 控制端本机当前时间（每次生成 Prompt 时刷新）
func formatLocalTime() string {
	now := time.Now()
	zone, _ := now.Zone()
	return fmt.Sprintf("%s %s", now.Format("2006-01-02 15:04:05"), zone)
}
