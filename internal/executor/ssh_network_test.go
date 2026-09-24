package executor

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"ai-edr/internal/config"

	"golang.org/x/crypto/ssh"
)

func TestSSHNetworkDeviceAutoFallbackAndHuaweiCLI(t *testing.T) {
	addr, cleanup := startMockHuaweiSSHServer(t)
	defer cleanup()

	ex, err := newSSHExecutor(config.Config{
		SSHHost: addr, SSHUser: "hexin", SSHPassword: "secret",
		SSHHostKeyPolicy: "insecure", SSHDeviceType: "auto", SSHPrompt: "<SSH_S12708>",
		SSHEnablePassword: "super-secret", SSHCommandTimeoutSec: 3,
	})
	if err != nil {
		t.Fatalf("newSSHExecutor: %v", err)
	}
	defer ex.Close()
	reporter, ok := ex.(NetworkDeviceReporter)
	if !ok {
		t.Fatalf("auto fallback returned %T, want network CLI executor", ex)
	}
	info := reporter.NetworkDeviceInfo()
	if info.Vendor != "huawei" || info.Prompt != "<SSH_S12708>" {
		t.Fatalf("device info=%#v", info)
	}
	out, err := ex.Run("display ip interface brief")
	if err != nil {
		t.Fatalf("run: %v output=%q", err, out)
	}
	for _, want := range []string{"Vlanif10", "10.0.10.1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
	for _, unwanted := range []string{"display ip interface brief", "---- More ----", "<SSH_S12708>"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("output retained %q: %q", unwanted, out)
		}
	}
	if _, err := ex.Run("system-view"); err != nil {
		t.Fatalf("enter Huawei system-view: %v", err)
	}
	if info := reporter.NetworkDeviceInfo(); info.Prompt != "[SSH_S12708]" {
		t.Fatalf("system-view prompt=%q, want [SSH_S12708]", info.Prompt)
	}
	if _, err := ex.Run("quit"); err != nil {
		t.Fatalf("leave Huawei system-view: %v", err)
	}
	if info := reporter.NetworkDeviceInfo(); info.Prompt != "<SSH_S12708>" {
		t.Fatalf("user-view prompt=%q, want <SSH_S12708>", info.Prompt)
	}
	if _, err := ex.ReadTargetFile("/etc/passwd"); err == nil {
		t.Fatal("network CLI must reject generic file reads")
	}
}

func startMockHuaweiSSHServer(t *testing.T) (string, func()) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() == "hexin" && string(password) == "secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	serverConfig.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tcpConn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer tcpConn.Close()
		_, channels, requests, handshakeErr := ssh.NewServerConn(tcpConn, serverConfig)
		if handshakeErr != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			channel, channelRequests, acceptErr := newChannel.Accept()
			if acceptErr != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer channel.Close()
				shell := false
				for request := range channelRequests {
					switch request.Type {
					case "pty-req":
						_ = request.Reply(true, nil)
					case "shell":
						_ = request.Reply(true, nil)
						shell = true
					case "subsystem", "exec":
						_ = request.Reply(false, nil)
					default:
						_ = request.Reply(false, nil)
					}
					if shell {
						serveHuaweiSSHCLI(channel)
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		wg.Wait()
	}
}

func serveHuaweiSSHCLI(channel ssh.Channel) {
	reader := bufio.NewReader(channel)
	_, _ = fmt.Fprint(channel, "Huawei Versatile Routing Platform Software\r\n<SSH_S12708>")
	privileged := false
	systemView := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		switch command {
		case "":
			if systemView {
				_, _ = fmt.Fprint(channel, "[SSH_S12708]")
			} else {
				_, _ = fmt.Fprint(channel, "<SSH_S12708>")
			}
		case "super":
			_, _ = fmt.Fprint(channel, "super\r\nPassword: ")
			password, _ := reader.ReadString('\n')
			if strings.TrimSpace(password) != "super-secret" {
				_, _ = fmt.Fprint(channel, "Error: Wrong password.\r\n<SSH_S12708>")
				continue
			}
			privileged = true
			_, _ = fmt.Fprint(channel, "\r\nNow user privilege is level 3.\r\n<SSH_S12708>")
		case "screen-length 0 temporary":
			_, _ = fmt.Fprint(channel, "screen-length 0 temporary\r\nInfo: The configuration takes effect.\r\n<SSH_S12708>")
		case "display ip interface brief":
			if !privileged {
				_, _ = fmt.Fprint(channel, "Error: Permission denied.\r\n<SSH_S12708>")
				continue
			}
			_, _ = fmt.Fprint(channel, "display ip interface brief\r\nInterface IP Address/Mask\r\nVlanif10 10.0.10.1/24\r\n---- More ----")
			space, _ := reader.ReadByte()
			if space != ' ' {
				return
			}
			_, _ = fmt.Fprint(channel, "\r\nGE1/0/1 unassigned\r\n<SSH_S12708>")
		case "system-view":
			if !privileged {
				_, _ = fmt.Fprint(channel, "Error: Permission denied.\r\n<SSH_S12708>")
				continue
			}
			systemView = true
			_, _ = fmt.Fprint(channel, "system-view\r\nEnter system view, return user view with Ctrl+Z.\r\n[SSH_S12708]")
		case "quit":
			systemView = false
			_, _ = fmt.Fprint(channel, "quit\r\n<SSH_S12708>")
		default:
			prompt := "<SSH_S12708>"
			if systemView {
				prompt = "[SSH_S12708]"
			}
			_, _ = fmt.Fprintf(channel, "Error: Unrecognized command: %s\r\n%s", command, prompt)
		}
	}
}

func TestNetworkPrivilegeCommands(t *testing.T) {
	tests := []struct {
		device  string
		command string
	}{
		{"huawei", "super"},
		{"h3c", "super"},
		{"ruijie", "enable"},
		{"cisco", "enable"},
		{"generic", ""},
		{"linux", ""},
	}
	for _, test := range tests {
		if got := networkPrivilegeCommand(test.device); got != test.command {
			t.Errorf("networkPrivilegeCommand(%q)=%q want %q", test.device, got, test.command)
		}
	}
	if !networkPrivilegePromptAccepted("huawei", "<Core-SW>") || !networkPrivilegePromptAccepted("h3c", "<Core-H3C>") {
		t.Fatal("Huawei/H3C super must accept an unchanged user-view prompt")
	}
	if networkPrivilegePromptAccepted("cisco", "edge>") || !networkPrivilegePromptAccepted("cisco", "edge#") {
		t.Fatal("Cisco enable must require a privileged # prompt")
	}
}
