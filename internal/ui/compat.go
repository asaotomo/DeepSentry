package ui

import (
	"os"
	"runtime"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// PlainTextMode reports whether terminal-facing text should avoid emoji,
// box drawing and other ambiguous-width symbols.
func PlainTextMode() bool {
	if envTruthy("DEEPSENTRY_FANCY") || envTruthy("DEEPSENTRY_EMOJI") {
		return false
	}
	if envTruthy("DEEPSENTRY_PLAIN") || envTruthy("DEEPSENTRY_ASCII") || envTruthy("DEEPSENTRY_NO_EMOJI") {
		return true
	}
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	if runtime.GOOS == "windows" {
		// Legacy cmd.exe often renders emoji/box drawing as boxes, while modern
		// terminals such as Windows Terminal advertise ANSI/UTF-8 capable envs.
		if windowsModernTerminal(term) {
			return false
		}
		return true
	}
	if term == "" || term == "dumb" || term == "linux" || term == "vt100" || term == "ansi" {
		return true
	}
	lang := strings.ToLower(os.Getenv("LC_ALL") + " " + os.Getenv("LC_CTYPE") + " " + os.Getenv("LANG"))
	if strings.TrimSpace(lang) != "" && !strings.Contains(lang, "utf-8") && !strings.Contains(lang, "utf8") {
		return true
	}
	return false
}

func ColorEnabled() bool {
	if envTruthy("DEEPSENTRY_COLOR") || envTruthy("CLICOLOR_FORCE") {
		return true
	}
	if envTruthy("DEEPSENTRY_NO_COLOR") || envPresent("NO_COLOR") || envTruthy("CLICOLOR_DISABLED") || strings.TrimSpace(os.Getenv("CLICOLOR")) == "0" {
		return false
	}
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	return term != "dumb"
}

func windowsModernTerminal(term string) bool {
	if os.Getenv("WT_SESSION") != "" || os.Getenv("WindowsTerminal") != "" {
		return true
	}
	if strings.EqualFold(os.Getenv("ConEmuANSI"), "ON") || os.Getenv("ANSICON") != "" {
		return true
	}
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	if strings.Contains(termProgram, "vscode") || strings.Contains(termProgram, "wezterm") || strings.Contains(termProgram, "mintty") {
		return true
	}
	return strings.Contains(term, "xterm") || strings.Contains(term, "screen") || strings.Contains(term, "tmux")
}

func envPresent(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}

func envTruthy(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// Sym returns a terminal-safe symbol. In plain mode the fallback must be ASCII.
func Sym(fancy, fallback string) string {
	if PlainTextMode() {
		return fallback
	}
	return fancy
}

// prefixGutter is the visible gap after every log/status icon. Two ASCII
// spaces stay readable even when a wide emoji paints into the next cell.
const prefixGutter = "  "

// Prefix reserves a two-cell icon column and a consistent gutter before text.
// VS-16 emoji (🛠️, ⚠️, ℹ️) often overflow one cell on macOS terminals, so they
// get an extra pad; the following text still starts after a visible gap.
func Prefix(fancy, fallback string) string {
	s := Sym(fancy, fallback)
	if s == "" {
		return ""
	}
	if PlainTextMode() {
		return s + " "
	}
	if strings.ContainsRune(s, '\uFE0F') || strings.ContainsRune(s, '\u20E3') {
		s += " "
	}
	if w := ansi.StringWidth(s); w < 2 {
		s += strings.Repeat(" ", 2-w)
	}
	return s + prefixGutter
}

func StripANSIIfPlain(s string) string {
	if ColorEnabled() {
		return s
	}
	return StripANSI(s)
}

// StripANSI removes CSI color/style sequences unconditionally. It is used by
// non-TTY sinks whose output is consumed as logs rather than terminal frames.
func StripANSI(s string) string {
	var b strings.Builder
	inSeq := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inSeq {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				inSeq = false
			}
			continue
		}
		if ch == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			inSeq = true
			i++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func TerminalText(s string) string {
	if !ColorEnabled() {
		s = StripANSI(s)
	}
	if !PlainTextMode() {
		return s
	}
	replacer := strings.NewReplacer(
		"✅", "[OK]",
		"❌", "[ERR]",
		"⚠️", "[WARN]",
		"⚠", "[WARN]",
		"💡", "[TIP]",
		"📖", "[DOC]",
		"📂", "[LOG]",
		"📁", "[FILE]",
		"📋", "[TODO]",
		"📝", "[REPORT]",
		"📊", "[STAT]",
		"🔍", "[SCAN]",
		"🔎", "[SEARCH]",
		"🧠", "[AI]",
		"⚡", "[RUN]",
		"🚀", "[RUN]",
		"🔴", "[HIGH]",
		"🟢", "[LOW]",
		"🟡", "[MED]",
		"🚫", "[DENY]",
		"🔀", "[SUB]",
		"📦", "[SUB]",
		"📡", "[TARGET]",
		"📚", "[SKILL]",
		"🔧", "[TOOL]",
		"🔌", "[API]",
		"🛠️", "[CFG]",
		"🛠", "[CFG]",
		"💾", "[SESSION]",
		"ℹ️", "[INFO]",
		"⏳", "[WAIT]",
		"⏱️", "[TIME]",
		"⏹️", "[STOP]",
		"♻️", "[RESUME]",
		"🎯", "[TASK]",
		"💻", "[CMD]",
		"🌐", "[URL]",
		"🤖", "[AI]",
		"🔑", "[KEY]",
		"🔄", "[STEP]",
		"🖥️", "[MODE]",
		"🔐", "[SSH]",
		"✏️", "[EDIT]",
		"❓", "[ASK]",
		"⏭", "[SKIP]",
		"▶", ">",
		"▸", ">",
	)
	return replacer.Replace(s)
}
