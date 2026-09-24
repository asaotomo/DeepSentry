//go:build !windows

package chat

import (
	"os/exec"
	"testing"
)

func TestConfigureOwnedDoesNotDetach(t *testing.T) {
	cmd := exec.Command("true")
	configureOwned(cmd)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setsid {
		t.Fatal("owned chat must stay in the parent session")
	}
}
