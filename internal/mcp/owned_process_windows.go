//go:build windows

package mcp

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

func killOwnedPID(pid int) {
	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
}

func listenerPID(port int) int {
	if port < 1024 || port > 65535 {
		return 0
	}
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return 0
	}
	needle := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.Contains(strings.ToLower(line), "listen") {
			continue
		}
		if !strings.Contains(fields[1], needle) {
			continue
		}
		pid, convErr := strconv.Atoi(fields[len(fields)-1])
		if convErr == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

func processCommandLine(pid int) string {
	if pid <= 0 {
		return ""
	}
	script := "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $p=Get-CimInstance Win32_Process -Filter 'ProcessId=" + strconv.Itoa(pid) + "'; if($p){[Console]::Write($p.CommandLine)}"
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func processPPID(pid int) int {
	if pid <= 0 {
		return 0
	}
	script := "$p=Get-CimInstance Win32_Process -Filter 'ProcessId=" + strconv.Itoa(pid) + "'; if($p){[Console]::Write($p.ParentProcessId)}"
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return 0
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid
}
