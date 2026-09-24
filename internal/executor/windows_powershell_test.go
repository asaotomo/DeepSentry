package executor

import (
	"strings"
	"testing"
)

func TestPowerShellEncodedCommandParsing(t *testing.T) {
	shell, script := parsePowerShellCommand("powershell -NoProfile -NonInteractive -EncodedCommand UABSAEkATgBUAA==")
	if shell != "powershell" || !strings.HasPrefix(strings.ToLower(script), "-encodedcommand ") {
		t.Fatalf("encoded command altered: %q %q", shell, script)
	}
}
