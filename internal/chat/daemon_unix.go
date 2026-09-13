//go:build !windows

package chat

import (
	"os"
	"os/exec"
	"syscall"
)

func configureOwned(cmd *exec.Cmd) {
	// Stay in the parent session so closing the binary can stop chat.
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func killDaemon(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
