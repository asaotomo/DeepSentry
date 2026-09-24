package ui

import (
	"strings"
	"testing"
)

func TestPrefixIconColumn(t *testing.T) {
	t.Setenv("DEEPSENTRY_FANCY", "1")
	t.Setenv("DEEPSENTRY_PLAIN", "")
	for _, icon := range []string{"🛠️", "📚", "🔀", "🔧", "🔌", "🧠", "💾", "▶", "⚠️", "ℹ️", "💡"} {
		got := Prefix(icon, "[CFG]")
		if !strings.HasPrefix(got, icon) {
			t.Fatalf("prefix lost icon: %q", got)
		}
		if !strings.HasSuffix(got, "  ") {
			t.Fatalf("icon %q missing gutter: %q", icon, got)
		}
		if strings.HasSuffix(got+"Native", icon+"Native") {
			t.Fatalf("icon %q glued to following text: %q", icon, got)
		}
	}
	t.Setenv("DEEPSENTRY_FANCY", "")
	t.Setenv("DEEPSENTRY_PLAIN", "1")
	if Prefix("🛠️", "[CFG]") != "[CFG] " {
		t.Fatal("plain terminal prefix changed")
	}
}
