package builtin

import (
	"strings"
	"testing"

	"ai-edr/internal/executor"
)

type networkDeviceExecutor struct{ commands []string }

func (e *networkDeviceExecutor) Run(command string) (string, error) {
	e.commands = append(e.commands, command)
	return "evidence for " + command, nil
}
func (e *networkDeviceExecutor) ReadTargetFile(string) ([]byte, error)  { return nil, nil }
func (e *networkDeviceExecutor) ListTargetDir(string) ([]string, error) { return nil, nil }
func (e *networkDeviceExecutor) IsRemote() bool                         { return true }
func (e *networkDeviceExecutor) Close()                                 {}
func (e *networkDeviceExecutor) NetworkDeviceInfo() executor.NetworkDeviceInfo {
	return executor.NetworkDeviceInfo{Vendor: "huawei", Prompt: "<Core-S12708>"}
}

func TestNetworkDeviceBaselineUsesVendorReadOnlyCommands(t *testing.T) {
	ex := &networkDeviceExecutor{}
	out, err := NetworkDeviceBaseline(Runtime{Exec: ex, IsRemote: true}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.commands) < 6 || ex.commands[0] != "display version" {
		t.Fatalf("commands=%v", ex.commands)
	}
	for _, want := range []string{"profile=huawei", "display interface brief", "display ip routing-table"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestNetworkBaselineCommandProfiles(t *testing.T) {
	if got := networkBaselineCommands("ruijie"); len(got) == 0 || got[0] != "show version" {
		t.Fatalf("ruijie commands=%v", got)
	}
	if got := networkBaselineCommands("h3c"); len(got) == 0 || got[0] != "display version" {
		t.Fatalf("h3c commands=%v", got)
	}
	if got := networkBaselineCommands("juniper"); len(got) == 0 || got[0] != "show version" {
		t.Fatalf("juniper commands=%v", got)
	}
	if got := networkBaselineCommands("fortinet"); len(got) == 0 || !strings.Contains(got[0], "system status") {
		t.Fatalf("fortinet commands=%v", got)
	}
}

func TestNetworkDeviceDiagnoseUsesFocusedEvidenceSet(t *testing.T) {
	ex := &networkDeviceExecutor{}
	out, err := NetworkDeviceDiagnose(Runtime{Exec: ex, IsRemote: true}, "auto", "routing")
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.commands) != 3 || ex.commands[0] != "display ip routing-table" {
		t.Fatalf("commands=%v", ex.commands)
	}
	if !strings.Contains(out, "focus=routing") || strings.Contains(out, "display logbuffer") {
		t.Fatalf("unexpected focused output: %s", out)
	}
}
