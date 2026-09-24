package main

import "fmt"

func printUsage() {
	fmt.Print(`
DeepSentry — AI 安全应急 Agent

用法:
  deepsentry [选项] [任务描述...]

示例:
  deepsentry                                # 默认进入 TUI 面板
  deepsentry "排查服务器内存与监听端口"
  deepsentry --task "审计 SSH 登录日志"
  deepsentry --plan "配置每 10 分钟 CPU 监控并通知"
  deepsentry --competition --task "排查本题网络故障并给出证据化答案"
  deepsentry -proxy http://127.0.0.1:8080 --task "扫描 192.168.1.0/24 的 80 端口"
  deepsentry -socks5 socks5://127.0.0.1:1080 --task "扫描 192.168.1.0/24"
  deepsentry --no-tui -c config.yaml "审计 SSH 登录日志"
  deepsentry --theme light -c config.yaml  # 当前进程固定日间主题
  deepsentry --no-tui --json --task "自动巡检目标机 /proc"
  deepsentry --webshell --task "读取最近登录日志"
  deepsentry --batch -y "自动巡检目标机 /proc"
  deepsentry --scheduler -c config.yaml       # 仅运行定时任务调度器
  # TUI 内 /schedule 查看、修改、取消、重置、删除已建立的定时任务
  deepsentry --resume session_abc123
  deepsentry --session session_chat_demo --task "继续排查"
  deepsentry --list-sessions
  deepsentry --list-sessions --json
  deepsentry --init                         # 重新运行配置向导
  deepsentry --chat-setup-cli -c config.yaml # SSH/CMD 终端连接向导
  deepsentry --inspect -c config.yaml        # 多设备巡检，输出 Word/Markdown
  deepsentry --chat-setup -c config.yaml    # 扫码连接飞书/QQ/微信/企微
  deepsentry --chat -c config.yaml          # 仅运行聊天控制服务
  deepsentry --chat-stop                    # 停止仍在运行的聊天服务

  deepsentry --tui --pick-session              # TUI 选择恢复会话
  # TUI 内输入 /tsecbench 可进入 TSecBench 跑分模式
  go run ./cmd/benchmark/ -c config.yaml --tui # Benchmark 可视化

选项:
  -c string         配置文件路径 (默认搜索 ./config.yaml, ~/.deepsentry/)
  -tui              启用全屏 TUI（默认）
  -no-tui           使用经典 stdout CLI
  -task string      任务描述（agent/脚本推荐）
  -q string         任务描述（--task 简写）
  -plan             计划模式：必要时先追问，再生成 todo 计划并执行
  -competition      比赛模式：按 10 分钟限时、证据闭环和评分规范执行
  -subagent-max-steps int
                    子 Agent 最大步数上限（覆盖 config.yaml 的 subagent_max_steps）
  -proxy string      HTTP/HTTPS 出站代理（LLM、HTTP、扫描、SSH/Telnet/FTP）
  -socks5 string     SOCKS5 出站代理（与 -proxy 互斥）
  -json             经典模式输出 JSONL 事件
  -quiet            经典模式仅输出关键结果和错误
  -reply-final      仅输出最终任务结论（供聊天机器人回复）
  -webshell         WebShell/非 TTY 友好模式（提交后台执行，立即返回报告/进度路径）
  --no-color        禁用彩色输出（默认启用颜色）
  --computer-check  检查本机 Computer Use 驱动、权限与桌面状态，不发送输入
  --theme string    TUI 主题：auto（默认）| dark | light
  -chat             运行聊天机器人控制服务
  -chat-setup-cli   终端选平台、扫码/输入凭据、状态/连接检查和解绑
  -inspect          执行 inspection.devices 配置的巡检
  -inspect-selector 设备名称、标签或 all
  -chat-setup       打开通讯工具连接窗口（飞书/QQ/微信/企微扫码；钉钉手动配置）
  -chat-stop        停止仍在运行的聊天服务
  -scheduler        仅运行本地定时任务调度器
  -version          显示版本
  -pick-session     配合 --tui，图形化选择 checkpoint 会话
  -batch            无人值守模式（自动批准操作）
  -y                配合 -batch，跳过 batch 模式二次确认
  -init             强制重新配置 LLM / SSH
  -resume string    从 checkpoint 恢复会话（必须已存在）
  -session string   绑定会话 ID（存在则恢复并追加任务，不存在则新建）
  -list-sessions    列出可恢复的会话 ID

退出码:
  0                 正常结束
  1                 配置、会话、初始化或 Agent 创建失败
  2                 命令行参数无效或包含未知选项

环境变量:
  DEEPSENTRY_*      覆盖 config.yaml 中的配置项
  DEEPSENTRY_AGENT_RUNTIME
                    v3（默认）| legacy（兼容回滚）
  DEEPSENTRY_TERMINAL_THEME
                    auto（自动探测背景）| dark | light
  target_protocol   local | ssh | telnet | ftp
  telnet_host       Telnet 目标 (IP:Port，默认 23)
  ftp_host          FTP/FTPS 目标 (FTP/Explicit FTPS 默认 21，Implicit FTPS 默认 990)

文档:
  docs/操作手册.md   完整操作手册
  README.md          架构与工具说明

`)
}
