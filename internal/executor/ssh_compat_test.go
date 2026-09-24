package executor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/config"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestSSHLegacyAlgorithmListsIncludeOldServerSuites(t *testing.T) {
	kex, ciphers, macs, hostKeys := sshLegacyAlgorithmLists()
	for _, want := range []string{
		ssh.InsecureKeyExchangeDH1SHA1,
		ssh.InsecureKeyExchangeDH14SHA1,
		ssh.InsecureKeyExchangeDHGEXSHA1,
	} {
		if !containsSSHAlgo(kex, want) {
			t.Fatalf("legacy kex missing %s: %v", want, kex)
		}
	}
	for _, want := range []string{ssh.InsecureCipherAES128CBC, ssh.InsecureCipherTripleDESCBC} {
		if !containsSSHAlgo(ciphers, want) {
			t.Fatalf("legacy cipher missing %s: %v", want, ciphers)
		}
	}
	if !containsSSHAlgo(macs, ssh.HMACSHA1) || !containsSSHAlgo(macs, ssh.InsecureHMACSHA196) {
		t.Fatalf("legacy mac missing hmac-sha1: %v", macs)
	}
	if !containsSSHAlgo(hostKeys, ssh.KeyAlgoRSA) || !containsSSHAlgo(hostKeys, "ssh-dss") {
		t.Fatalf("legacy host key missing ssh-rsa/ssh-dss: %v", hostKeys)
	}
}

func TestBuildSSHClientConfigDefaultsToLegacyAndKeyboardInteractive(t *testing.T) {
	cfg := config.Config{SSHUser: "admin", SSHPassword: "secret"}
	client, err := buildSSHClientConfig(cfg, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatal(err)
	}
	if !containsSSHAlgo(client.KeyExchanges, ssh.InsecureKeyExchangeDH1SHA1) {
		t.Fatalf("default client should offer DH1: %v", client.KeyExchanges)
	}
	if !containsSSHAlgo(client.Ciphers, ssh.InsecureCipherAES128CBC) {
		t.Fatalf("default client should offer aes128-cbc: %v", client.Ciphers)
	}
	if !containsSSHAlgo(client.HostKeyAlgorithms, ssh.KeyAlgoRSA) {
		t.Fatalf("default client should accept ssh-rsa host keys: %v", client.HostKeyAlgorithms)
	}
	if len(client.Auth) < 2 {
		t.Fatalf("password login should also offer keyboard-interactive, got %d methods", len(client.Auth))
	}
	if client.Timeout.Seconds() < 20 {
		t.Fatalf("connect timeout too short for old appliances: %s", client.Timeout)
	}
}

func TestBuildSSHClientConfigCanDisableLegacy(t *testing.T) {
	cfg := config.Config{SSHUser: "root", SSHPassword: "x", SSHLegacyCompat: "false"}
	client, err := buildSSHClientConfig(cfg, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatal(err)
	}
	if len(client.KeyExchanges) != 0 || len(client.Ciphers) != 0 {
		t.Fatalf("modern mode should leave algorithm lists empty so Go defaults apply: kex=%v ciphers=%v", client.KeyExchanges, client.Ciphers)
	}
}

func TestBuildSSHClientConfigPrefersPinnedHostKeyType(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pinnedKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	host := "[127.0.0.1]:2222"
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(host)}, pinnedKey) + "\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := buildSSHClientConfig(config.Config{
		SSHHost:           host,
		SSHUser:           "root",
		SSHPassword:       "test",
		SSHHostKeyPolicy:  "accept-new",
		SSHKnownHostsPath: knownHosts,
	}, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatal(err)
	}
	if len(client.HostKeyAlgorithms) == 0 || client.HostKeyAlgorithms[0] != ssh.KeyAlgoECDSA256 {
		t.Fatalf("pinned ECDSA key should be preferred, got %v", client.HostKeyAlgorithms)
	}
}

func TestFormatSSHHandshakeErrorPreservesCause(t *testing.T) {
	cause := errors.New("host key mismatch")
	wrapped := formatSSHHandshakeError("127.0.0.1:22", cause)
	if !errors.Is(wrapped, cause) {
		t.Fatalf("handshake wrapper lost cause: %v", wrapped)
	}
}

func TestParseSSHPrivateKeySupportsEncryptedRSAAndSSHRsa(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "id_rsa")
	encrypted := filepath.Join(dir, "id_rsa.enc")
	writeTestRSAKey(t, plain, "")
	writeTestRSAKey(t, encrypted, "oldpass")

	signer, err := parseSSHPrivateKey(plain, "")
	if err != nil {
		t.Fatalf("plain rsa: %v", err)
	}
	if _, ok := signer.(ssh.MultiAlgorithmSigner); !ok {
		t.Fatalf("RSA signer should advertise ssh-rsa + rsa-sha2, type=%T", signer)
	}
	if _, err := parseSSHPrivateKey(encrypted, ""); err == nil || !strings.Contains(err.Error(), "ssh_key_passphrase") {
		t.Fatalf("encrypted key without passphrase should fail clearly, got %v", err)
	}
	if _, err := parseSSHPrivateKey(encrypted, "oldpass"); err != nil {
		t.Fatalf("encrypted rsa with passphrase: %v", err)
	}
}

func TestNormalizeAndDetectLegacyNetworkDevices(t *testing.T) {
	if got := normalizeDeviceType("junos"); got != "juniper" {
		t.Fatalf("junos alias=%q", got)
	}
	if got := normalizeDeviceType("fortigate"); got != "fortinet" {
		t.Fatalf("fortigate alias=%q", got)
	}
	if got := normalizeDeviceType("cisco-asa"); got != "asa" {
		t.Fatalf("asa alias=%q", got)
	}
	cases := []struct {
		banner, prompt, want string
	}{
		{"Juniper Junos", "admin@srx>", "juniper"},
		{"FortiGate-60E", "FGT #", "fortinet"},
		{"Palo Alto Networks PAN-OS", "admin@PA-220>", "paloalto"},
		{"Hillstone StoneOS", "Hillstone#", "hillstone"},
		{"Sangfor NGAF", "sangfor>", "sangfor"},
		{"Check Point Gaia", "gw>", "checkpoint"},
		{"Cisco Adaptive Security Appliance", "ciscoasa>", "asa"},
		{"H3C SecPath", "<USG>", "h3c"},
	}
	for _, test := range cases {
		if got := detectTelnetDeviceType("auto", test.banner, test.prompt); got != test.want {
			t.Errorf("detect(%q)=%q want %q", test.banner, got, test.want)
		}
	}
	if cmds := networkPagingCommands("juniper", ""); len(cmds) == 0 || cmds[0] != "set cli screen-length 0" {
		t.Fatalf("juniper paging=%v", cmds)
	}
	if cmds := networkPagingCommands("asa", ""); len(cmds) == 0 || cmds[0] != "terminal pager 0" {
		t.Fatalf("asa paging=%v", cmds)
	}
}

func writeTestRSAKey(t *testing.T, path, passphrase string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	if passphrase != "" {
		//lint:ignore SA1019 This fixture intentionally verifies compatibility with legacy RFC 1423 encrypted PEM keys.
		block, err = x509.EncryptPEMBlock(rand.Reader, block.Type, block.Bytes, []byte(passphrase), x509.PEMCipherAES256)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsSSHAlgo(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
