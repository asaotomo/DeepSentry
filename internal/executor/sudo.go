package executor

import (
	"os/exec"
	"runtime"
	"strings"
	"unicode"
)

// CommandUsesSudo recognizes sudo as a shell token, including the common
// absolute path. It intentionally also sees tokens inside sh -c strings so an
// embedded sudo cannot open /dev/tty behind the TUI.
func CommandUsesSudo(command string) bool {
	return len(sudoTokenOffsets(command)) > 0
}

// ForceNonInteractiveSudo adds -n to every sudo token. sudo otherwise opens
// /dev/tty directly and races Bubble Tea for keyboard input.
func ForceNonInteractiveSudo(command string) string {
	offsets := sudoTokenOffsets(command)
	if len(offsets) == 0 {
		return command
	}
	var b strings.Builder
	last := 0
	for _, token := range offsets {
		start, end := token[0], token[1]
		b.WriteString(command[last:end])
		next := end
		for next < len(command) && unicode.IsSpace(rune(command[next])) {
			next++
		}
		if !hasSudoNonInteractiveOption(command[next:]) {
			b.WriteString(" -n")
		}
		last = end
		_ = start
	}
	b.WriteString(command[last:])
	return b.String()
}

func LocalSudoCredentialReady() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return exec.Command("sudo", "-n", "true").Run() == nil
}

func sudoTokenOffsets(command string) [][2]int {
	var out [][2]int
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(command); {
		if escaped {
			escaped = false
			i++
			continue
		}
		if command[i] == '\\' && !inSingle {
			escaped = true
			i++
			continue
		}
		if command[i] == '\'' && !inDouble {
			inSingle = !inSingle
			i++
			continue
		}
		if command[i] == '"' && !inSingle {
			inDouble = !inDouble
			i++
			continue
		}
		start, end := -1, -1
		switch {
		case strings.HasPrefix(command[i:], "/usr/bin/sudo"):
			start, end = i, i+len("/usr/bin/sudo")
		case strings.HasPrefix(command[i:], "sudo"):
			start, end = i, i+len("sudo")
		}
		quotedShell := false
		if start >= 0 && (inSingle || inDouble) {
			quotedShell = shellCommandStringPrefix(command[:start])
		}
		if start >= 0 && (!inSingle && !inDouble || quotedShell) && shellTokenBoundary(command, start-1) && shellTokenBoundary(command, end) {
			out = append(out, [2]int{start, end})
			i = end
			continue
		}
		i++
	}
	return out
}

func shellCommandStringPrefix(prefix string) bool {
	lower := strings.ToLower(prefix)
	for _, marker := range []string{"sh -c ", "bash -c ", "zsh -c ", "dash -c ", "shell -c "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func shellTokenBoundary(s string, index int) bool {
	if index < 0 || index >= len(s) {
		return true
	}
	r := rune(s[index])
	return unicode.IsSpace(r) || strings.ContainsRune(";&|()<>`$\\\"'", r)
}

func hasSudoNonInteractiveOption(rest string) bool {
	if rest == "" {
		return false
	}
	end := strings.IndexFunc(rest, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(";&|()<>`$\\\"'", r)
	})
	if end < 0 {
		end = len(rest)
	}
	option := rest[:end]
	if option == "-n" || option == "--non-interactive" {
		return true
	}
	return strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.Contains(strings.TrimPrefix(option, "-"), "n")
}
