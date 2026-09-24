package builtin

import (
	"ai-edr/internal/config"
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetInventoryExplainsEvidenceNextStep(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig.Targets = []config.TargetConfig{{Name: "sw-01", Protocol: "ssh", Host: "192.0.2.10", User: "ops", Tags: []string{"prod"}}}
	output, err := FleetInventory(Runtime{}, "all")
	if err != nil || !strings.Contains(output, "sw-01") || !strings.Contains(output, "matched=1") || !strings.Contains(output, "尚未采集") || !strings.Contains(output, "fleet_exec") {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestFleetFileDownloadKeepsTargetsSeparate(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	first, closeFirst := fleetDownloadFTPServer(t, "FIRST")
	defer closeFirst()
	second, closeSecond := fleetDownloadFTPServer(t, "SECOND")
	defer closeSecond()
	config.GlobalConfig.Targets = []config.TargetConfig{
		{Name: "same", Protocol: "ftp", Host: first, User: "user", Password: "pass"},
		{Name: "same", Protocol: "ftp", Host: second, User: "user", Password: "pass"},
	}
	base := filepath.Join(t.TempDir(), "evidence.txt")
	output, err := FleetFile(Runtime{}, "all", "download", "hello.txt", base, 2)
	if err != nil || !strings.Contains(output, "2/2 成功") {
		t.Fatalf("download: output=%s err=%v", output, err)
	}
	for path, expected := range map[string]string{base + ".001-same": "FIRST", base + ".002-same": "SECOND"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != expected {
			t.Fatalf("path=%s data=%q err=%v", path, data, err)
		}
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("shared destination was used: %v", err)
	}
}

func fleetDownloadFTPServer(t *testing.T, content string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(conn, "220 ready\r\n")
		reader := bufio.NewReader(conn)
		var dataListener net.Listener
		defer func() {
			if dataListener != nil {
				_ = dataListener.Close()
			}
		}()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.ToUpper(strings.Fields(line)[0]) {
			case "USER":
				_, _ = fmt.Fprint(conn, "331 password\r\n")
			case "PASS":
				_, _ = fmt.Fprint(conn, "230 logged in\r\n")
			case "TYPE":
				_, _ = fmt.Fprint(conn, "200 binary\r\n")
			case "PASV":
				dataListener, err = net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					return
				}
				port := dataListener.Addr().(*net.TCPAddr).Port
				_, _ = fmt.Fprintf(conn, "227 Entering Passive Mode (127,0,0,1,%d,%d)\r\n", port/256, port%256)
			case "RETR":
				_, _ = fmt.Fprint(conn, "150 transfer\r\n")
				dataConn, err := dataListener.Accept()
				if err != nil {
					return
				}
				_, _ = fmt.Fprint(dataConn, content)
				_ = dataConn.Close()
				_ = dataListener.Close()
				dataListener = nil
				_, _ = fmt.Fprint(conn, "226 done\r\n")
			case "QUIT":
				_, _ = fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				_, _ = fmt.Fprint(conn, "500 unknown\r\n")
			}
		}
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
		<-done
	}
}
