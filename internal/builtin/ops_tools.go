package builtin

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

func TargetHealthSummary(rt Runtime) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	cmd := "hostname; uname -a 2>/dev/null; uptime 2>/dev/null; df -h 2>/dev/null | head -20; free -h 2>/dev/null | head -5; ps -eo pid,comm,%cpu,%mem --sort=-%cpu 2>/dev/null | head -12"
	if rt.IsWindows {
		cmd = windowsPowerShellCommand(`$os=Get-CimInstance Win32_OperatingSystem; $os | Select-Object Caption,Version,TotalVisibleMemorySize,FreePhysicalMemory; Get-CimInstance Win32_LogicalDisk | Select-Object DeviceID,FreeSpace,Size; Get-Process | Sort-Object CPU -Descending | Select-Object -First 12 Id,ProcessName,CPU`)
	}
	out, err := rt.Exec.Run(cmd)
	return fmt.Sprintf("%s 健康摘要\n%s", rt.tag(), truncate(out, 12000)), err
}

func DiskUsage(rt Runtime, path string) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if strings.TrimSpace(path) == "" {
		path = "/"
		if rt.IsWindows {
			path = ""
		}
	}
	cmd := "df -h " + shellQuote(path)
	if rt.IsWindows {
		cmd = windowsPowerShellCommand(`Get-CimInstance Win32_LogicalDisk | Select-Object DeviceID,FreeSpace,Size`)
	}
	out, err := rt.Exec.Run(cmd)
	return fmt.Sprintf("%s 磁盘使用\n%s", rt.tag(), truncate(out, 8000)), err
}

func FileTail(rt Runtime, path string, lines int) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path 必填")
	}
	if lines <= 0 {
		lines = 100
	}
	if lines > 1000 {
		lines = 1000
	}
	cmd := fmt.Sprintf("tail -n %d %s", lines, shellQuote(path))
	if rt.IsWindows {
		cmd = windowsPowerShellCommand(fmt.Sprintf("Get-Content -Tail %d -LiteralPath %s", lines, powerShellLiteral(path)))
	}
	out, err := rt.Exec.Run(cmd)
	return fmt.Sprintf("%s Tail %s\n%s", rt.tag(), path, truncate(out, 12000)), err
}

func LoginAudit(rt Runtime, lines int) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	cmd := fmt.Sprintf("(tail -n %d /var/log/auth.log 2>/dev/null || tail -n %d /var/log/secure 2>/dev/null || last -n %d 2>/dev/null)", lines, lines, lines)
	if rt.IsWindows {
		cmd = windowsPowerShellCommand(fmt.Sprintf("Get-WinEvent -LogName Security -MaxEvents %d | Select-Object TimeCreated,Id,ProviderName,Message", lines))
	}
	out, err := rt.Exec.Run(cmd)
	return fmt.Sprintf("%s 登录审计\n%s", rt.tag(), truncate(out, 16000)), err
}

func ServiceUnits(rt Runtime, query string, limit int) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if limit <= 0 {
		limit = 80
	}
	if limit > 300 {
		limit = 300
	}
	cmd := fmt.Sprintf("systemctl list-units --type=service --all --no-pager 2>/dev/null | head -n %d", limit)
	if strings.TrimSpace(query) != "" {
		cmd = fmt.Sprintf("systemctl list-units --type=service --all --no-pager 2>/dev/null | grep -i %s | head -n %d", shellQuote(query), limit)
	}
	if rt.IsWindows {
		cmd = windowsPowerShellCommand(fmt.Sprintf("Get-Service | Where-Object { $_.Name -like %s -or $_.DisplayName -like %s } | Select-Object -First %d Name,Status,DisplayName", powerShellLiteral("*"+query+"*"), powerShellLiteral("*"+query+"*"), limit))
	}
	out, err := rt.Exec.Run(cmd)
	return fmt.Sprintf("%s 服务列表\n%s", rt.tag(), truncate(out, 12000)), err
}

// Encode the script so cmd.exe and Windows paths cannot alter PowerShell syntax.
func windowsPowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], u)
	}
	return "powershell -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
}

func powerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
