package builtin

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// NetConnections 读取 /proc/net 解析连接（无需 ss/netstat）
func NetConnections(rt Runtime, filter string) (string, error) {
	if rt.IsWindows {
		return netConnectionsWindows(rt, filter)
	}

	entries, err := readAllSockets()
	if err != nil {
		return "", err
	}

	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		filter = "all"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 网络连接 (解析 /proc/net)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("%-5s %-21s %-25s %-15s %s\n", "PROTO", "LOCAL", "REMOTE", "STATE", "INODE"))

	count := 0
	for _, e := range entries {
		switch filter {
		case "listen":
			if e.State != "LISTEN" {
				continue
			}
		case "established":
			if e.State != "ESTABLISHED" {
				continue
			}
		}
		b.WriteString(formatSocket(e) + "\n")
		count++
	}
	b.WriteString(fmt.Sprintf("\n共 %d 条\n", count))
	return truncate(b.String(), 15000), nil
}

// PortListen 监听端口（/proc/net/tcp LISTEN）
func PortListen(rt Runtime) (string, error) {
	return NetConnections(rt, "listen")
}

// RouteTable 路由表（/proc/net/route）
func RouteTable(rt Runtime) (string, error) {
	if rt.IsWindows {
		return windowsSystemCommand(rt, "route print", "Windows 路由表", 12000)
	}
	data, err := readTarget("/proc/net/route")
	if err != nil {
		return "", err
	}
	routes, err := parseProcRouteFixed(string(data))
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 路由表 (/proc/net/route)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("%-8s %-16s %-16s %-6s %s\n", "IFACE", "DEST", "GATEWAY", "FLAGS", "MASK"))
	for _, r := range routes {
		b.WriteString(fmt.Sprintf("%-8s %-16s %-16s %-6s %s\n", r.Iface, r.Dest, r.Gateway, r.Flags, r.Mask))
	}
	return b.String(), nil
}

// ARPTable ARP 缓存（/proc/net/arp）
func ARPTable(rt Runtime) (string, error) {
	if rt.IsWindows {
		return windowsSystemCommand(rt, "arp -a", "Windows ARP 表", 12000)
	}
	data, err := readTarget("/proc/net/arp")
	if err != nil {
		return "", err
	}
	entries, err := parseProcARP(string(data))
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s ARP 表 (/proc/net/arp)\n", rt.tag()))
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("%-16s %-18s %-6s %s\n", e.IP, e.HWAddress, e.Flags, e.Device))
	}
	return b.String(), nil
}

// MemInfo 内存信息（/proc/meminfo）
func MemInfo(rt Runtime) (string, error) {
	if rt.IsWindows {
		return windowsSystemCommand(rt, windowsPowerShellCommand(`Get-CimInstance Win32_OperatingSystem | Select-Object TotalVisibleMemorySize,FreePhysicalMemory`), "Windows 内存信息", 4000)
	}
	data, err := readTarget("/proc/meminfo")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s 内存信息 (/proc/meminfo)\n%s", rt.tag(), truncate(string(data), 4000)), nil
}

// ProcessList 进程列表（遍历 /proc/[pid]/comm，无需 ps 命令）
func ProcessList(rt Runtime, limit int) (string, error) {
	if rt.IsWindows {
		if rt.Exec == nil {
			return "", fmt.Errorf("执行器未初始化")
		}
		out, err := rt.Exec.Run("tasklist")
		return fmt.Sprintf("%s Windows 进程列表\n%s", rt.tag(), truncate(out, 12000)), err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	pids, err := listTarget("/proc")
	if err != nil {
		return "", err
	}

	var numericPids []int
	for _, name := range pids {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		numericPids = append(numericPids, pid)
	}
	sort.Ints(numericPids)

	type procInfo struct {
		pid  int
		name string
	}
	// 仅读取前 limit 个 PID 的 comm，避免远程 SFTP 逐 PID 全量拉取
	var procs []procInfo
	for _, pid := range numericPids {
		if len(procs) >= limit {
			break
		}
		comm, err := readTarget(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			continue
		}
		procs = append(procs, procInfo{pid: pid, name: strings.TrimSpace(string(comm))})
	}
	totalProcs := len(numericPids)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 进程列表 (/proc, 无需 ps)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("%-8s %s\n", "PID", "COMM"))
	for _, p := range procs {
		b.WriteString(fmt.Sprintf("%-8d %s\n", p.pid, p.name))
	}
	if totalProcs > limit {
		b.WriteString(fmt.Sprintf("\n...(共 %d 进程，仅显示前 %d)...", totalProcs, limit))
	}
	return b.String(), nil
}

// FileHash 文件 SHA256（通过 ReadTargetFile，无需 sha256sum 命令）
func FileHash(rt Runtime, path string) (string, error) {
	data, err := readTargetLimited(path, 10<<20)
	if err != nil {
		return "", err
	}
	sum := sha256Sum(data)
	return fmt.Sprintf("%s 文件哈希\n路径: %s\nSHA256: %s\n大小: %d 字节", rt.tag(), strings.TrimSpace(path), sum, len(data)), nil
}

func windowsSystemCommand(rt Runtime, command, label string, limit int) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	out, err := rt.Exec.Run(command)
	return fmt.Sprintf("%s %s\n%s", rt.tag(), label, truncate(out, limit)), err
}
