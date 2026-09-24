package builtin

import (
	_ "embed"
	"strings"
)

//go:embed zipdicts/password_list.txt
var zipCrackerPasswordList string

func isBuiltinZIPDictionary(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "builtin", "default", "6000", "6000.txt", "password_list.txt", "zipcracker":
		return true
	default:
		return false
	}
}

func emitBuiltinZIPDictionary(emit func(string) bool) bool {
	for _, line := range strings.Split(zipCrackerPasswordList, "\n") {
		candidate := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if candidate == "" {
			continue
		}
		if !emit(candidate) {
			return false
		}
	}
	return true
}
