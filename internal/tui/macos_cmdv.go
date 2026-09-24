package tui

// macosCmdVMsg is sent when macOS HID polling sees Command+V while this
// DeepSentry session's host window is frontmost. The terminal itself usually
// swallows that chord for images, so the TUI never gets a key event; this
// message is the replacement trigger. Other apps (Cursor, browsers, another
// terminal tab) must not fire it.
type macosCmdVMsg struct{}

type macosCmdVIgnoredMsg struct{}

// macosCmdAMsg and macosCmdZMsg are the macOS Command+A / Command+Z chords.
// Terminals swallow Command, so select-all and undo never arrive as Ctrl+A / Ctrl+Z.
type macosCmdAMsg struct{}

type macosCmdZMsg struct{}

const (
	cgEventFlagCommand = 0x00100000
	cgEventFlagShift   = 0x00020000
	cgEventFlagControl = 0x00040000
	cgEventFlagAlt     = 0x00080000
)

// macEditChordToSwallow reports the input-box action for a Command chord.
// Command+A and Command+Z are swallowed so the terminal does not select or
// undo the whole screen. Command+V is left for the terminal to paste text.
func macEditChordToSwallow(flags uint64, keycode int64) (any, bool) {
	if flags&cgEventFlagCommand == 0 || flags&cgEventFlagControl != 0 || flags&cgEventFlagAlt != 0 || flags&cgEventFlagShift != 0 {
		return nil, false
	}
	switch keycode {
	case 0x00:
		return macosCmdAMsg{}, true
	case 0x06:
		return macosCmdZMsg{}, true
	default:
		return nil, false
	}
}
