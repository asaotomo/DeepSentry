package executor

import "testing"

func TestCommandUsesSudoRecognizesShellTokensOnly(t *testing.T) {
	for _, command := range []string{
		"sudo du -sh /Users/*",
		"echo ok && /usr/bin/sudo -u root id",
		"sh -c 'sudo cat /private/etc/master.passwd'",
	} {
		if !CommandUsesSudo(command) {
			t.Fatalf("sudo token not detected: %q", command)
		}
	}
	for _, command := range []string{"echo sudoers", "printf pseudo", "cat /etc/sudoers"} {
		if CommandUsesSudo(command) {
			t.Fatalf("false sudo detection: %q", command)
		}
	}
}

func TestForceNonInteractiveSudo(t *testing.T) {
	tests := map[string]string{
		"sudo du -sh /Users/*":                        "sudo -n du -sh /Users/*",
		"sudo -n true":                                "sudo -n true",
		"sudo -E -u root id":                          "sudo -n -E -u root id",
		"/usr/bin/sudo id":                            "/usr/bin/sudo -n id",
		"sudo --preserve-env id && sudo whoami":       "sudo -n --preserve-env id && sudo -n whoami",
		"sh -c 'sudo cat /etc/hosts'":                 "sh -c 'sudo -n cat /etc/hosts'",
		"printf 'sudo text' && /usr/bin/sudo -n true": "printf 'sudo text' && /usr/bin/sudo -n true",
	}
	for input, want := range tests {
		if got := ForceNonInteractiveSudo(input); got != want {
			t.Fatalf("ForceNonInteractiveSudo(%q)=%q want %q", input, got, want)
		}
	}
}
