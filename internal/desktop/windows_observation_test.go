package desktop

import (
	"context"
	"testing"
)

func TestWindowsTitleChangeStillAllowsObservedClick(t *testing.T) {
	s, b := fixture(t)
	b.state.Driver = "windows-sendinput"
	b.state.Foreground = "123456:Editor - unsaved"
	obs := observe(t, s)
	b.state.Foreground = "123456:Editor - saved"
	result := s.Call(context.Background(), "test-session", clickArgs(obs))
	if !result.OK || b.calls != 1 {
		t.Fatalf("same-window click rejected: %+v calls=%d", result, b.calls)
	}
}

func TestWindowsOtherForegroundRejectsObservedClick(t *testing.T) {
	s, b := fixture(t)
	b.state.Driver = "windows-sendinput"
	b.state.Foreground = "123456:Editor"
	obs := observe(t, s)
	b.state.Foreground = "222222:Other window"
	result := s.Call(context.Background(), "test-session", clickArgs(obs))
	if result.Category != "desktop_changed" || b.calls != 0 {
		t.Fatalf("other-window click accepted: %+v calls=%d", result, b.calls)
	}
}
