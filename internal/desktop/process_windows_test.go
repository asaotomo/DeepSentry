//go:build windows

package desktop

import (
	"context"
	"os/exec"
	"testing"
)

func TestHelperDoesNotOpenConsole(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "powershell.exe")
	configureDesktopCommand(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("helper may steal foreground with a console window")
	}
}
