package tui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
)

func copyToClipboard(text string) error {
	if text == "" {
		return fmt.Errorf("empty clipboard")
	}
	return clipboard.WriteAll(text)
}

func readClipboardText() (string, error) {
	return clipboard.ReadAll()
}

func stripANSI(s string) string {
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
