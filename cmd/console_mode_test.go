package main

import "testing"

func TestWindowsConsoleInputModeDropsMouseReports(t *testing.T) {
	in := uint32(consoleEnableProcessedInput |
		consoleEnableLineInput |
		consoleEnableEchoInput |
		consoleEnableMouseInput |
		consoleEnableWindowInput |
		consoleEnableQuickEditMode)
	got := windowsConsoleInputMode(in)
	if got&consoleEnableVTInput == 0 || got&consoleEnableExtendedFlags == 0 {
		t.Fatalf("virtual terminal input not enabled: %#x", got)
	}
	for _, flag := range []uint32{
		consoleEnableMouseInput,
		consoleEnableWindowInput,
		consoleEnableQuickEditMode,
	} {
		if got&flag != 0 {
			t.Fatalf("flag %#x still set in %#x", flag, got)
		}
	}
	if got&consoleEnableLineInput == 0 || got&consoleEnableEchoInput == 0 {
		t.Fatalf("cooked keyboard input was cleared: %#x", got)
	}
}

func TestWindowsSurveyInputModeKeepsArrowKeys(t *testing.T) {
	in := windowsConsoleInputMode(consoleEnableLineInput | consoleEnableEchoInput | consoleEnableProcessedInput)
	got := windowsSurveyInputMode(in)
	if got&consoleEnableVTInput != 0 || got&consoleEnableMouseInput != 0 {
		t.Fatalf("survey mode still injects mouse or VT keys: %#x", got)
	}
	if got&consoleEnableProcessedInput == 0 || got&consoleEnableExtendedFlags == 0 {
		t.Fatalf("keyboard processing was cleared: %#x", got)
	}
}
