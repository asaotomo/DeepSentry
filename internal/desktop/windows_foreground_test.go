package desktop

import "testing"

func TestWindowsForegroundTitleDoesNotInvalidateObservation(t *testing.T) {
	before := State{Driver: "windows-sendinput", Ready: true, Width: 1920, Height: 1080, Foreground: "123456:Report - unsaved"}
	after := before
	after.Foreground = "123456:Report - saved"
	if !sameDesktop(before, after) {
		t.Fatal("title change rejected")
	}
	after.Foreground = "789012:Report - saved"
	if sameDesktop(before, after) {
		t.Fatal("foreign foreground accepted")
	}
	after = before
	after.Foreground = "-2147483648:Editor - saved"
	before.Foreground = "-2147483648:Editor - unsaved"
	if !sameDesktop(before, after) {
		t.Fatal("signed HWND title change rejected")
	}
	after = before
	after.Width = 1280
	if sameDesktop(before, after) {
		t.Fatal("display change accepted")
	}
	after = before
	after.Foreground = "invalid:title"
	if sameDesktop(before, after) {
		t.Fatal("invalid HWND accepted")
	}
}
