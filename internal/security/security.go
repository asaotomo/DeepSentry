package security

import (
	"ai-edr/internal/executor"
	"fmt"
	"strings"
)

// CheckRisk 评估命令的风险等级。
// 策略尽量贴近 Claude Code 的交互体验：只读观测命令默认放行，明确有副作用的操作才确认。
// 返回值: (riskLevel: "high"|"low", reason: string)
func CheckRisk(cmd string) (string, string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "low", "空命令"
	}

	analyzeCmd := normalizeCommand(cmd)

	if reason := dangerousShellPattern(analyzeCmd); reason != "" {
		return "high", reason
	}
	if isReadOnlyNetworkCLI(analyzeCmd) {
		return "low", "网络设备只读 display/show 命令"
	}

	subCmds := splitShellCommands(analyzeCmd)
	for _, sub := range subCmds {
		risk, reason := checkSingleCommand(sub)
		if risk == "high" {
			return "high", reason
		}
	}

	return "low", "只读/低副作用操作"
}

// LooksLikeFlagCommand reports whether the would-be shell command is actually a
// CTF/AWD flag or extracted plaintext. Models sometimes try to execute
// flag{...} after recovering an archive; that is not a command and must not be
// confirmed as a high-risk shell action.
func LooksLikeFlagCommand(cmd string) bool {
	cmd = normalizeCommand(strings.TrimSpace(cmd))
	if cmd == "" {
		return false
	}
	if isCTFFlagToken(strings.Trim(cmd, `"'`)) {
		return true
	}
	for _, sub := range splitShellCommands(cmd) {
		fields := strings.Fields(strings.TrimSpace(sub))
		if len(fields) == 0 {
			continue
		}
		if isCTFFlagToken(strings.Trim(fields[0], `"'`)) {
			return true
		}
	}
	return false
}

func isCTFFlagToken(token string) bool {
	token = strings.TrimSpace(token)
	if len(token) < 6 || !strings.HasSuffix(token, "}") {
		return false
	}
	lower := strings.ToLower(token)
	for _, prefix := range []string{"flag{", "ctf{", "key{", "ans{", "answer{"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		inner := token[len(prefix) : len(token)-1]
		return inner != "" && !strings.ContainsAny(inner, " \t\n")
	}
	return false
}

func isReadOnlyNetworkCLI(command string) bool {
	parts := strings.Split(command, "|")
	if len(parts) == 0 {
		return false
	}
	root := strings.Fields(strings.TrimSpace(parts[0]))
	if len(root) == 0 || (strings.ToLower(root[0]) != "display" && strings.ToLower(root[0]) != "show") {
		return false
	}
	seenFilter := false
	for _, filter := range parts[1:] {
		fields := strings.Fields(strings.TrimSpace(filter))
		if len(fields) == 0 {
			return false
		}
		switch strings.ToLower(fields[0]) {
		case "include", "exclude", "begin", "section", "count", "no-more":
			seenFilter = true
		default:
			// VRP/Comware regular expressions use an unescaped vertical bar
			// for alternation: `| include rate|packets|bytes`. Once a filter
			// begins, these segments are regex alternatives, not shell pipes.
			if !seenFilter || looksLikeShellPipeline(fields[0]) {
				return false
			}
		}
	}
	return true
}

func looksLikeShellPipeline(first string) bool {
	first = strings.ToLower(strings.TrimSpace(first))
	first = strings.TrimPrefix(first, "sudo")
	switch first {
	case "sh", "bash", "zsh", "cmd", "powershell", "rm", "mv", "cp", "dd", "tee", "cat", "sed", "awk", "perl", "python", "python3", "curl", "wget", "nc", "ncat", "socat":
		return true
	default:
		return false
	}
}

// CanReviewHighRiskWithAI 表示规则引擎判高后是否进入 AI 二次复核。
// 人工确认采用“双高危”策略：规则高危 + AI 高危才弹窗；
// AI 明确判低则自动放行，AI 超时/失败/低置信度则失败关闭到人工确认。
func CanReviewHighRiskWithAI(cmd, reason string) bool {
	cmd = strings.TrimSpace(cmd)
	reason = strings.TrimSpace(reason)
	if cmd == "" || reason == "" {
		return false
	}
	if LooksLikeFlagCommand(cmd) {
		return false
	}
	return true
}

func normalizeCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, "local_run ") {
		cmd = strings.TrimSpace(strings.TrimPrefix(cmd, "local_run "))
	}
	return cleanShellWrapper(cmd)
}

// cleanShellWrapper 清洗 Shell 包装器和引号
func cleanShellWrapper(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	prefixes := []string{"/bin/sh -c", "sh -c", "/bin/bash -c", "bash -c", "cmd /c", "powershell -Command", "powershell -c"}
	for _, p := range prefixes {
		if len(cmd) > len(p) && strings.EqualFold(cmd[:len(p)], p) {
			cmd = cmd[len(p):]
			cmd = strings.TrimSpace(cmd)
			break
		}
	}

	// 移除首尾的引号 (单引号或双引号)
	if len(cmd) >= 2 {
		first := cmd[0]
		last := cmd[len(cmd)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			cmd = cmd[1 : len(cmd)-1]
		}
	}

	return strings.TrimSpace(cmd)
}

func splitShellCommands(cmd string) []string {
	out := make([]string, 0, 4)
	var current strings.Builder
	var quote byte
	escaped := false
	flush := func() {
		if part := strings.TrimSpace(current.String()); part != "" {
			out = append(out, part)
		}
		current.Reset()
	}
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			current.WriteByte(ch)
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteByte(ch)
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			current.WriteByte(ch)
			continue
		}
		ampersandOperator := ch == '&' &&
			!(i > 0 && cmd[i-1] == '>') &&
			!(i+1 < len(cmd) && cmd[i+1] == '>')
		if ch == ';' || ch == '|' || ampersandOperator {
			flush()
			if (ch == '|' || ch == '&') && i+1 < len(cmd) && cmd[i+1] == ch {
				i++
			}
			continue
		}
		current.WriteByte(ch)
	}
	flush()
	return out
}

func dangerousShellPattern(cmd string) string {
	lower := strings.ToLower(cmd)
	if hasFileWriteRedirection(cmd) {
		return "检测到文件重定向，可能覆盖/写入文件"
	}
	if strings.Contains(cmd, "|") {
		if strings.Contains(lower, "| sh") || strings.Contains(lower, "| bash") ||
			strings.Contains(lower, "| sudo") || strings.Contains(lower, "| powershell") ||
			strings.Contains(lower, "| pwsh") {
			return "检测到管道执行脚本"
		}
	}
	if strings.Contains(lower, "$(") || strings.Contains(lower, "`") {
		return "检测到命令替换，需确认真实执行内容"
	}
	return ""
}

// hasFileWriteRedirection 只识别 shell 语法中真正向文件/设备写入的 > / >> / &>。
// 2>&1、1>&2、>&2 只是复制文件描述符，不会创建或覆盖文件，不应误报。
// 引号内的 > 是普通文本，同样忽略。
func hasFileWriteRedirection(cmd string) bool {
	var quote byte
	escaped := false
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch != '>' {
			continue
		}
		// >&N / >&- 与 2>&1 都是 fd duplication/close，没有文件写入。
		if i+1 < len(cmd) && cmd[i+1] == '&' {
			j := i + 2
			if j < len(cmd) && cmd[j] == '-' {
				continue
			}
			if j < len(cmd) && cmd[j] >= '0' && cmd[j] <= '9' {
				continue
			}
		}
		return true
	}
	return false
}

// checkSingleCommand 单个命令判定逻辑
func checkSingleCommand(subCmd string) (string, string) {
	subCmd = strings.TrimSpace(subCmd)
	if subCmd == "" {
		return "low", ""
	}

	parts := strings.Fields(subCmd)
	if len(parts) == 0 {
		return "low", ""
	}

	// 获取动词并转小写
	verb := strings.ToLower(parts[0])
	// 二次清洗：防止动词本身带引号 (如 "cd")
	verb = strings.Trim(verb, "\"'")

	if isAssignmentOrEnvPrefix(verb) && len(parts) > 1 {
		return checkSingleCommand(strings.Join(parts[1:], " "))
	}

	lowRiskVerbs := map[string]bool{
		// 浏览与查看
		"ls": true, "dir": true, "pwd": true, "cd": true,
		"cat": true, "echo": true, "printf": true, "head": true, "tail": true,
		"more": true, "less": true, "tree": true,
		"find": true, "grep": true, "findstr": true,
		"stat": true, "file": true, "where": true, "which": true,
		"awk": true, "sed": true, "sort": true, "uniq": true, "wc": true,
		"cut": true, "tr": true, "xargs": true,

		// 系统/网络信息
		"whoami": true, "id": true, "hostname": true, "uname": true,
		"uptime": true, "date": true, "w": true, "who": true, "last": true,
		"lastlog": true, "groups": true, "env": true, "printenv": true,
		"history": true, "locale": true, "ulimit": true,
		"ps": true, "top": true, "tasklist": true, "free": true, "df": true, "du": true,
		"vmstat": true, "iostat": true, "mpstat": true, "sar": true,
		"lsof": true, "fuser": true, "journalctl": true, "dmesg": true,
		"loginctl": true, "systemd-analyze": true,
		"ipconfig": true, "ifconfig": true, "ip": true, "netstat": true, "ss": true,
		"ping": true, "arp": true, "route": true, "nslookup": true, "dig": true,
		"host": true, "traceroute": true, "tracepath": true, "mtr": true,
		// 交换机/路由器只读 CLI（Huawei/H3C display，Ruijie/Cisco show）
		"display": true, "show": true,
		// 仅退出当前网络设备视图，不修改配置。
		"quit": true, "return": true, "exit": true,
		"wmic": true, "ver": true, "scutil": true, "sw_vers": true,
		"curl": true,

		// 文件内容查看
		"type": true,

		"get-childitem": true, "gci": true,
		"get-content": true, "gc": true,
		"get-location": true, "gl": true,
		"get-process": true, "gps": true,
		"get-service": true, "gsv": true,
		"get-date": true, "get-host": true,
		"write-host": true, "write-output": true,
		"select-object": true, "where-object": true, "foreach-object": true,
	}

	if lowRiskVerbs[verb] {
		if reason := lowRiskCommandWithDangerousArgs(verb, parts[1:]); reason != "" {
			return "high", reason
		}
		return "low", "只读/低副作用操作"
	}
	if isKnownReadOnlySubcommand(verb, parts[1:]) {
		return "low", "已知只读子命令"
	}

	highRiskVerbs := map[string]bool{
		// 破坏性操作
		"rm": true, "del": true, "erase": true, "rmdir": true,
		"mv": true, "move": true, "cp": true, "copy": true,
		"mkdir": true, "touch": true, "wget": true,
		"mkfs": true, "format": true, "fdisk": true, "dd": true,
		"shred": true, "wipe": true, "truncate": true,

		// 系统控制与权限
		"reboot": true, "shutdown": true, "halt": true, "poweroff": true, "init": true,
		"systemctl": true, "service": true, "sc": true, "reg": true,
		"chmod": true, "chown": true, "chgrp": true, "attrib": true,
		"useradd": true, "usermod": true, "userdel": true, "passwd": true,
		"groupadd": true, "groupmod": true, "groupdel": true,
		"sudo": true, "su": true, "doas": true,
		// 网络设备权限/配置视图。即使命令本身不写配置，也会扩大后续操作权限。
		"super": true, "enable": true, "system-view": true, "configure": true,
		"interface": true, "save": true, "reset": true,
		"mount": true, "umount": true, "crontab": true,

		// 进程与网络传输
		"kill": true, "pkill": true, "killall": true, "taskkill": true,
		"nc": true, "ncat": true, "socat": true,
		"ssh": true, "scp": true, "rsync": true, "ftp": true, "sftp": true,
		"nmap": true, "masscan": true, "tcpdump": true,

		// PowerShell 敏感操作
		"invoke-expression": true, "iex": true,
		"start-process": true,
	}

	if highRiskVerbs[verb] {
		return "high", fmt.Sprintf("敏感指令: %s", verb)
	}

	return "high", fmt.Sprintf("未识别指令(%s)，无法静态确认副作用", verb)
}

func isKnownReadOnlySubcommand(verb string, args []string) bool {
	if len(args) == 0 {
		return false
	}
	sub := strings.ToLower(strings.Trim(args[0], "\"'"))
	if verb == "git" && sub == "remote" && len(args) > 1 {
		next := strings.ToLower(strings.Trim(args[1], "\"'"))
		return next == "-v" || next == "--verbose"
	}
	allowed := map[string]map[string]bool{
		"kubectl": {
			"get": true, "describe": true, "logs": true, "top": true, "version": true,
			"cluster-info": true, "api-resources": true, "api-versions": true,
		},
		"docker":    {"ps": true, "inspect": true, "logs": true, "stats": true, "version": true, "info": true, "images": true},
		"podman":    {"ps": true, "inspect": true, "logs": true, "stats": true, "version": true, "info": true, "images": true},
		"git":       {"status": true, "log": true, "diff": true, "show": true, "rev-parse": true, "remote": true},
		"systemctl": {"status": true, "show": true, "is-active": true, "is-enabled": true, "list-units": true, "list-unit-files": true},
		"service":   {"status": true},
		"sc":        {"query": true, "queryex": true},
		"reg":       {"query": true},
	}
	return allowed[verb][sub]
}

func isAssignmentOrEnvPrefix(verb string) bool {
	if verb == "env" {
		return true
	}
	return strings.Contains(verb, "=") && !strings.HasPrefix(verb, "-")
}

func lowRiskCommandWithDangerousArgs(verb string, args []string) string {
	for i, arg := range args {
		trimmed := strings.TrimSpace(arg)
		lower := strings.ToLower(trimmed)
		if lower == "" {
			continue
		}
		if lower == "-exec" || strings.HasPrefix(lower, "-exec=") || lower == "-delete" {
			return fmt.Sprintf("%s 参数包含可执行/删除动作: %s", verb, arg)
		}
		if verb == "xargs" && isDangerousToken(lower) {
			return fmt.Sprintf("xargs 将执行敏感指令: %s", arg)
		}
		if (verb == "sed" || verb == "perl") && (lower == "-i" || strings.HasPrefix(lower, "-i")) {
			return fmt.Sprintf("%s 原地修改文件", verb)
		}
		if verb == "curl" {
			if lower == "--insecure" || strings.HasPrefix(lower, "--insecure=") || curlShortOptionsContainInsecure(lower) {
				return "curl 跳过 TLS 证书验证，可能被中间人篡改"
			}
			if lower == "-o" || lower == "--output" || lower == "-O" || strings.HasPrefix(lower, "--output=") {
				return "curl 下载写入文件"
			}
			if lower == "-d" || lower == "--data" || lower == "--data-raw" || lower == "--data-binary" || strings.HasPrefix(lower, "-d") {
				return "curl 发送请求体，可能改变远端状态"
			}
			if trimmed == "-T" || lower == "--upload-file" || strings.HasPrefix(lower, "--upload-file=") ||
				trimmed == "-F" || lower == "--form" || strings.HasPrefix(lower, "--form=") ||
				lower == "--json" || strings.HasPrefix(lower, "--json=") ||
				lower == "--data-urlencode" || strings.HasPrefix(lower, "--data-urlencode=") {
				return "curl 将上传数据或发送请求体，可能改变远端状态"
			}
			if (lower == "-x" || lower == "--request") && i+1 < len(args) {
				method := strings.ToUpper(strings.Trim(args[i+1], "\"'"))
				if method != "GET" && method != "HEAD" && method != "OPTIONS" {
					return "curl 使用非只读 HTTP 方法: " + method
				}
			}
		}
		if verb == "wget" && (lower == "-o" || lower == "-O" || lower == "--output-document" || strings.HasPrefix(lower, "--output-document=")) {
			return "wget 下载写入文件"
		}
		if i == 0 && isDangerousToken(lower) {
			return fmt.Sprintf("%s 将调用敏感指令: %s", verb, arg)
		}
	}
	return ""
}

func curlShortOptionsContainInsecure(arg string) bool {
	if len(arg) < 2 || arg[0] != '-' || strings.HasPrefix(arg, "--") {
		return false
	}
	// curl's -k disables verification while uppercase -K means --config.
	return strings.Contains(arg[1:], "k")
}

func isDangerousToken(token string) bool {
	token = strings.Trim(token, "\"'")
	switch token {
	case "rm", "sh", "bash", "sudo", "su", "chmod", "chown", "systemctl", "service", "kill", "curl", "wget", "nc", "ncat":
		return true
	default:
		return false
	}
}

// SafeExecV3 执行命令的安全封装
func SafeExecV3(cmd string) (string, error) {
	if executor.Current == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	return executor.Current.Run(cmd)
}
