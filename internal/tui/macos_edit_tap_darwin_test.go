//go:build darwin

package tui

import "testing"

func TestMacEditTapSymbolsLoad(t *testing.T) {
	loadMacEditTap()
	if cgEventTapCreate == nil || cgEventGetFlags == nil || cfRunLoopRun == nil || kCFRunLoopCommonModes == 0 {
		t.Fatal("macOS edit-key tap symbols did not load")
	}
}
