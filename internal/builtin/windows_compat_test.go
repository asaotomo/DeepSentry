package builtin

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeWindowsPowerShellCommand(t *testing.T, command string) string {
	t.Helper()
	const marker = "-EncodedCommand "
	index := strings.Index(command, marker)
	if index < 0 {
		t.Fatalf("missing encoded PowerShell command: %q", command)
	}
	raw, err := base64.StdEncoding.DecodeString(command[index+len(marker):])
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("invalid encoded script: %v", err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	return string(utf16.Decode(units))
}

func TestWindowsToolsAvoidRemovedWMICAndPreservePaths(t *testing.T) {
	rec := &recordingExecutor{}
	rt := Runtime{IsWindows: true, Exec: rec}
	path := `C:\Users\示例用户\a"b's.log`
	if _, err := FileTail(rt, path, 25); err != nil {
		t.Fatal(err)
	}
	script := decodeWindowsPowerShellCommand(t, rec.commands[len(rec.commands)-1])
	if !strings.Contains(script, `-LiteralPath 'C:\Users\示例用户\a"b''s.log'`) {
		t.Fatalf("path corrupted: %s", script)
	}
	for _, call := range []func() (string, error){
		func() (string, error) { return TargetHealthSummary(rt) },
		func() (string, error) { return DiskUsage(rt, `C:\`) },
		func() (string, error) { return RouteTable(rt) },
		func() (string, error) { return ARPTable(rt) },
		func() (string, error) { return MemInfo(rt) },
		func() (string, error) { return ServiceUnits(rt, "Windows Update", 7) },
	} {
		if _, err := call(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range rec.commands {
		if strings.Contains(strings.ToLower(command), "wmic") {
			t.Fatalf("WMIC still required: %s", command)
		}
	}
	serviceScript := decodeWindowsPowerShellCommand(t, rec.commands[len(rec.commands)-1])
	if !strings.Contains(serviceScript, "Windows Update") || !strings.Contains(serviceScript, "-First 7") {
		t.Fatalf("query and limit lost: %s", serviceScript)
	}
}
