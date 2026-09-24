//go:build windows

package desktop

import (
	"os/exec"
	"syscall"
)

func configureDesktopCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
