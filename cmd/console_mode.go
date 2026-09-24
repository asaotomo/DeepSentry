package main

// Windows console input flags. Values match golang.org/x/sys/windows so the
// bitmask can be tested on non-Windows hosts.
const (
	consoleEnableProcessedInput = 0x1
	consoleEnableLineInput      = 0x2
	consoleEnableEchoInput      = 0x4
	consoleEnableWindowInput    = 0x8
	consoleEnableMouseInput     = 0x10
	consoleEnableQuickEditMode  = 0x40
	consoleEnableExtendedFlags  = 0x80
	consoleEnableVTInput        = 0x200
)

// windowsConsoleInputMode keeps virtual-terminal keyboard input and turns off
// the console modes that inject non-keyboard events into stdin.
//
// QuickEdit pauses the whole process on a click. Mouse and window input, once
// virtual-terminal input is on, arrive as SGR reports such as ESC[<35;x;yM.
// The setup wizard treats those bytes as keys and redraws continuously.
func windowsConsoleInputMode(mode uint32) uint32 {
	mode |= consoleEnableVTInput | consoleEnableExtendedFlags
	mode &^= consoleEnableQuickEditMode | consoleEnableMouseInput | consoleEnableWindowInput
	return mode
}

// windowsSurveyInputMode keeps arrow keys and Tab as real console key events.
// Virtual-terminal input turns Down into the bytes ESC [ B; survey then inserts
// "[B" into the filter and the option list disappears. Mouse input stays off
// so pointer movement does not redraw the wizard.
func windowsSurveyInputMode(mode uint32) uint32 {
	mode &^= consoleEnableVTInput | consoleEnableMouseInput | consoleEnableQuickEditMode | consoleEnableWindowInput
	mode |= consoleEnableExtendedFlags | consoleEnableProcessedInput
	return mode
}
