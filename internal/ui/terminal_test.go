package ui

import "testing"

func TestSttySaneArgsByOS(t *testing.T) {
	gotDarwin := sttySaneArgs("darwin")
	if len(gotDarwin) != 3 || gotDarwin[0] != "-f" || gotDarwin[1] != "/dev/tty" || gotDarwin[2] != "sane" {
		t.Fatalf("unexpected darwin stty args: %#v", gotDarwin)
	}

	gotLinux := sttySaneArgs("linux")
	if len(gotLinux) != 3 || gotLinux[0] != "-F" || gotLinux[1] != "/dev/tty" || gotLinux[2] != "sane" {
		t.Fatalf("unexpected linux stty args: %#v", gotLinux)
	}
}
