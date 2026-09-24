package collector

import (
	"strings"
	"testing"

	"ai-edr/internal/executor"
)

type collectorNetworkExecutor struct{ runs int }

func (e *collectorNetworkExecutor) Run(string) (string, error)             { e.runs++; return "", nil }
func (e *collectorNetworkExecutor) ReadTargetFile(string) ([]byte, error)  { return nil, nil }
func (e *collectorNetworkExecutor) ListTargetDir(string) ([]string, error) { return nil, nil }
func (e *collectorNetworkExecutor) IsRemote() bool                         { return true }
func (e *collectorNetworkExecutor) Close()                                 {}
func (e *collectorNetworkExecutor) Mode() string                           { return "telnet" }
func (e *collectorNetworkExecutor) NetworkDeviceInfo() executor.NetworkDeviceInfo {
	return executor.NetworkDeviceInfo{Vendor: "h3c", Prompt: "<H3C-Core>"}
}

func TestGetSystemContextDoesNotSendLinuxProbesToNetworkDevice(t *testing.T) {
	original := executor.Current
	t.Cleanup(func() { executor.Current = original })
	ex := &collectorNetworkExecutor{}
	executor.Current = ex
	ctx := GetSystemContext()
	if ex.runs != 0 {
		t.Fatalf("sent %d Linux probes to network device", ex.runs)
	}
	if ctx.DeviceType != "h3c" || ctx.Hostname != "H3C-Core" || ctx.OS == "Unknown System" {
		t.Fatalf("context=%#v", ctx)
	}
	prompt := ctx.GenerateSystemPrompt()
	if !strings.Contains(prompt, "禁止 uname") || !strings.Contains(prompt, "network_device_baseline") {
		t.Fatalf("network guidance missing: %s", prompt)
	}
}
