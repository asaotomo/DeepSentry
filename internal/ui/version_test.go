package ui

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultVersionMatchesReleaseVersionFile(t *testing.T) {
	raw, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(raw)); Version != want {
		t.Fatalf("ui.Version=%q differs from VERSION=%q", Version, want)
	}
}
