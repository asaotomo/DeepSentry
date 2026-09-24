package builtin

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type procOwner struct {
	PID     string
	Comm    string
	Cmdline string
}

// ProcSocketMap 将 /proc/net socket inode 关联到进程，替代 lsof/ss -p。
func ProcSocketMap(rt Runtime, filter string, limit int) (string, error) {
	if rt.IsWindows {
		return "", fmt.Errorf("当前 Windows 暂不支持 /proc socket 映射，请使用 execute netstat -ano")
	}
	if limit <= 0 {
		limit = 80
	}
	if limit > 300 {
		limit = 300
	}
	sockets, err := readAllSockets()
	if err != nil {
		return "", err
	}
	filter = strings.ToLower(strings.TrimSpace(filter))

	selected := make([]socketEntry, 0, limit)
	neededInodes := make(map[string]struct{})
	for _, s := range sockets {
		if filter != "" && filter != "all" && filter != "owned" {
			hay := strings.ToLower(fmt.Sprintf("%s %s %d %s %d %s", s.Proto, s.LocalIP, s.LocalPort, s.RemoteIP, s.RemotePort, s.State))
			if !strings.Contains(hay, filter) {
				continue
			}
		}
		selected = append(selected, s)
		neededInodes[s.Inode] = struct{}{}
		if len(selected) >= limit {
			break
		}
	}
	owners := map[string]procOwner{}
	if filter == "owned" {
		owners = buildSocketOwners(neededInodes)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Socket/PID 映射\n", rt.tag()))
	b.WriteString("Proto Local -> Remote State Inode PID Process Cmdline\n\n")
	shown := 0
	for _, s := range selected {
		owner := owners[s.Inode]
		if owner.PID == "" && filter == "owned" {
			continue
		}
		b.WriteString(fmt.Sprintf("%-4s %-21s -> %-21s %-12s inode=%s pid=%s comm=%s cmd=%s\n",
			s.Proto,
			fmt.Sprintf("%s:%d", s.LocalIP, s.LocalPort),
			fmt.Sprintf("%s:%d", s.RemoteIP, s.RemotePort),
			s.State,
			s.Inode,
			emptyDash(owner.PID),
			emptyDash(owner.Comm),
			truncateOneLine(owner.Cmdline, 120),
		))
		shown++
		if shown >= limit {
			b.WriteString(fmt.Sprintf("\n...(仅显示前 %d 条)...\n", limit))
			break
		}
	}
	if filter != "owned" {
		b.WriteString("\n提示: 为保持远程扫描速度，默认不展开 PID owner；需要进程归属可使用 filter=owned。\n")
	}
	if shown == 0 {
		b.WriteString("(无匹配 socket)\n")
	}
	return b.String(), nil
}

func buildSocketOwners(wanted map[string]struct{}) map[string]procOwner {
	owners := map[string]procOwner{}
	if len(wanted) == 0 {
		return owners
	}
	pids, err := listTarget("/proc")
	if err != nil {
		return owners
	}
	sort.Strings(pids)
	for _, pid := range pids {
		if pid == "" || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		fdDir := filepath.Join("/proc", pid, "fd")
		fds, err := listTarget(fdDir)
		if err != nil {
			continue
		}
		owner := procOwner{PID: pid, Comm: readProcText(filepath.Join("/proc", pid, "comm")), Cmdline: readProcCmdline(pid)}
		for _, fd := range fds {
			link, err := readTargetLink(filepath.Join(fdDir, fd))
			if err != nil {
				continue
			}
			inode := socketInode(link)
			if inode == "" {
				continue
			}
			if _, ok := wanted[inode]; !ok {
				continue
			}
			if _, exists := owners[inode]; !exists {
				owners[inode] = owner
			}
			if len(owners) >= len(wanted) {
				return owners
			}
		}
	}
	return owners
}

func readProcText(path string) string {
	data, err := readTarget(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readProcCmdline(pid string) string {
	data, err := readTarget(filepath.Join("/proc", pid, "cmdline"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
}

func socketInode(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "socket:[") || !strings.HasSuffix(s, "]") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(s, "socket:["), "]")
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func truncateOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
