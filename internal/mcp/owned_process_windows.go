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
	out, err := exec.Command("wmic", "process", "where", "ProcessId="+strconv.Itoa(pid), "get", "CommandLine", "/value").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "CommandLine="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func processPPID(pid int) int {
	if pid <= 0 {
		return 0
	}
	out, err := exec.Command("wmic", "process", "where", "ProcessId="+strconv.Itoa(pid), "get", "ParentProcessId", "/value").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "ParentProcessId="); ok {
			ppid, convErr := strconv.Atoi(strings.TrimSpace(value))
			if convErr == nil {
				return ppid
			}
		}
	}
	return 0
}
