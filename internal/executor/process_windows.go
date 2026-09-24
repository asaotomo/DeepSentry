//go:build windows

package executor

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func configureCommandProcessGroup(_ *exec.Cmd) {}

func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		// Stop the shell and its interpreter together; killing only PowerShell
		// leaves the Python script running after the timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		_ = cmd.Process.Kill()
	}
}
