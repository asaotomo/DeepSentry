package executor

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestFTPLoginUSER230AndEPSVTransfer(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{userImmediate: true, epsv: true, greetingPreliminary: true})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{FTPHost: server.addr, FTPUser: "allowlisted"})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	data, err := ex.ReadTargetFile("hello.txt")
	if err != nil || string(data) != "HELLO_COMPAT\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	commands := server.Commands()
	if containsFTPCommand(commands, "PASS") {
		t.Fatalf("PASS must not be sent after USER 230: %#v", commands)
	}
	if !containsFTPCommand(commands, "EPSV") || containsFTPCommand(commands, "PASV") {
		t.Fatalf("EPSV path not used: %#v", commands)
	}
}

func TestFTPPASVFallbackIgnoresAdvertisedForeignHost(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{pasvAdvertisedHost: "203,0,113,77"})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{FTPHost: server.addr, FTPUser: "user", FTPPassword: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	names, err := ex.ListTargetDir("/")
	if err != nil || len(names) != 1 || names[0] != "hello.txt" {
		t.Fatalf("names=%#v err=%v", names, err)
	}
	commands := server.Commands()
	if !containsFTPCommand(commands, "EPSV") || !containsFTPCommand(commands, "PASV") {
		t.Fatalf("expected EPSV -> PASV fallback: %#v", commands)
	}
}

func TestFTPPASVFallbackWhenEPSVDataPortIsUnreachable(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{epsv: true, epsvUnreachable: true})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{FTPHost: server.addr, FTPUser: "user", FTPPassword: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	data, err := ex.ReadTargetFile("hello.txt")
	if err != nil || string(data) != "HELLO_COMPAT\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	commands := server.Commands()
	if !containsFTPCommand(commands, "EPSV") || !containsFTPCommand(commands, "PASV") {
		t.Fatalf("expected unreachable EPSV data port to fall back to PASV: %#v", commands)
	}
}

func TestFTPActiveEPRTTransfer(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{
		FTPHost: server.addr, FTPUser: "user", FTPPassword: "pass", FTPDataMode: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	data, err := ex.ReadTargetFile("hello.txt")
	if err != nil || string(data) != "HELLO_COMPAT\n" {
		t.Fatalf("active EPRT data=%q err=%v", data, err)
	}
	commands := server.Commands()
	if !containsFTPCommand(commands, "EPRT") || containsFTPCommand(commands, "EPSV") || containsFTPCommand(commands, "PASV") {
		t.Fatalf("active EPRT path not used: %#v", commands)
	}
}

func TestFTPActiveFallsBackToPORT(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{rejectEPRT: true})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{
		FTPHost: server.addr, FTPUser: "user", FTPPassword: "pass", FTPDataMode: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	names, err := ex.ListTargetDir("/")
	if err != nil || len(names) != 1 || names[0] != "hello.txt" {
		t.Fatalf("active PORT names=%#v err=%v", names, err)
	}
	commands := server.Commands()
	if !containsFTPCommand(commands, "EPRT") || !containsFTPCommand(commands, "PORT") {
		t.Fatalf("expected EPRT -> PORT fallback: %#v", commands)
	}
}

func TestFTPDataModeAutoFallsBackToActive(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{rejectPassive: true})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{
		FTPHost: server.addr, FTPUser: "user", FTPPassword: "pass", FTPDataMode: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	data, err := ex.ReadTargetFile("hello.txt")
	if err != nil || string(data) != "HELLO_COMPAT\n" {
		t.Fatalf("auto active fallback data=%q err=%v", data, err)
	}
	commands := server.Commands()
	for _, command := range []string{"EPSV", "PASV", "EPRT"} {
		if !containsFTPCommand(commands, command) {
			t.Fatalf("auto fallback missing %s: %#v", command, commands)
		}
	}
}

func TestFTPActiveRejectsControllerProxy(t *testing.T) {
	_, err := newFTPExecutor(config.Config{
		FTPHost: "127.0.0.1:21", FTPDataMode: "active", ControllerProxy: "socks5://127.0.0.1:1080",
	})
	if err == nil || !strings.Contains(err.Error(), "controller_proxy") {
		t.Fatalf("active FTP with proxy err=%v", err)
	}
}

func TestFTPImmediateTransferRejectionDoesNotWaitForDataTimeout(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{epsv: true, retrMode: "reject"})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{
		FTPHost: server.addr, FTPUser: "user", FTPPassword: "pass",
		FTPTransferTimeoutSec: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	started := time.Now()
	_, err = ex.ReadTargetFile("missing.txt")
	if err == nil || !strings.Contains(err.Error(), "550") {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("immediate 550 waited on data socket for %s", elapsed)
	}
}

func TestFTPInterruptedDownloadKeepsPreviousDestination(t *testing.T) {
	server := startFTPCompatServer(t, ftpCompatOptions{epsv: true, retrMode: "partial"})
	defer server.Close()
	ex, err := newFTPExecutor(config.Config{FTPHost: server.addr, FTPUser: "user", FTPPassword: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	destination := filepath.Join(t.TempDir(), "evidence.bin")
	if err := os.WriteFile(destination, []byte("VERIFIED_OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.downloadFile("partial.bin", destination); err == nil || !strings.Contains(err.Error(), "426") {
		t.Fatalf("download err=%v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "VERIFIED_OLD" {
		t.Fatalf("destination=%q err=%v", data, err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".deepsentry-ftp-download-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary downloads left behind: %#v err=%v", matches, err)
	}
}

func TestFTPRejectsCommandInjectionAndExplicitlyReportsReadLimit(t *testing.T) {
	if err := validateFTPCommand("RETR ok\r\nDELE evidence"); err == nil {
		t.Fatal("CRLF command injection accepted")
	}
	if port, err := parseEPSVPort("229 Entering Extended Passive Mode (|||6446|)"); err != nil || port != 6446 {
		t.Fatalf("EPSV port=%d err=%v", port, err)
	}
	if port, err := parsePASVPort("227 Entering Passive Mode (10,0,0,1,25,46)"); err != nil || port != 6446 {
		t.Fatalf("PASV port=%d err=%v", port, err)
	}
	args, err := splitFTPCommandLine(`download "/archive/evidence one.bin" "C:\evidence files\one.bin"`)
	if err != nil || len(args) != 3 || args[1] != "/archive/evidence one.bin" || args[2] != `C:\evidence files\one.bin` {
		t.Fatalf("quoted args=%#v err=%v", args, err)
	}
	args, err = splitFTPCommandLine(`download "\\server\share\one.bin" "C:\Temp\"`)
	if err != nil || len(args) != 3 || args[1] != `\\server\share\one.bin` || args[2] != `C:\Temp\` {
		t.Fatalf("Windows UNC or trailing separator changed: args=%#v err=%v", args, err)
	}
	args, err = splitFTPCommandLine(`download '/remote/O'\''Brien report.txt' 'C:\Users\O'\''Brien\report.txt'`)
	if err != nil || len(args) != 3 || args[1] != `/remote/O'Brien report.txt` || args[2] != `C:\Users\O'Brien\report.txt` {
		t.Fatalf("apostrophe in transfer path changed: args=%#v err=%v", args, err)
	}
	if _, err := splitFTPCommandLine(`download "unterminated`); err == nil {
		t.Fatal("unterminated quoted path accepted")
	}
	if _, err := (&FTPExecutor{}).Run(`download "" "/tmp/report.txt"`); err == nil {
		t.Fatal("empty FTP transfer path accepted")
	}
	left, right := net.Pipe()
	go func() {
		_, _ = io.WriteString(right, "12345")
		_ = right.Close()
	}()
	defer left.Close()
	if _, err := readFTPDataLimited(left, 4); err == nil || !strings.Contains(err.Error(), "安全读取上限") {
		t.Fatalf("oversized read err=%v", err)
	}
}

func TestFTPControlPeerUsesConfiguredTargetBehindProxy(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	f := &FTPExecutor{conn: left, host: "ftp.internal.example:21"}
	host, err := f.controlPeerHost()
	if err != nil || host != "ftp.internal.example" {
		t.Fatalf("controlPeerHost=%q err=%v want configured FTP target", host, err)
	}
}

type ftpCompatOptions struct {
	userImmediate       bool
	epsv                bool
	epsvUnreachable     bool
	greetingPreliminary bool
	pasvAdvertisedHost  string
	retrMode            string
	rejectEPRT          bool
	rejectPassive       bool
}

type ftpCompatServer struct {
	addr     string
	listener net.Listener
	done     chan struct{}
	mu       sync.Mutex
	commands []string
}

func startFTPCompatServer(t *testing.T, options ftpCompatOptions) *ftpCompatServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &ftpCompatServer{addr: listener.Addr().String(), listener: listener, done: make(chan struct{})}
	go server.serve(options)
	return server
}

func (s *ftpCompatServer) serve(options ftpCompatOptions) {
	defer close(s.done)
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if options.greetingPreliminary {
		_, _ = fmt.Fprint(conn, "120 service ready soon\r\n")
	}
	_, _ = fmt.Fprint(conn, "220-compat ftp\r\n220 ready\r\n")
	var dataListener net.Listener
	var activeDataAddr string
	closeData := func() {
		if dataListener != nil {
			_ = dataListener.Close()
			dataListener = nil
		}
		activeDataAddr = ""
	}
	defer closeData()
	openData := func() (int, error) {
		closeData()
		dataListener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		return dataListener.Addr().(*net.TCPAddr).Port, nil
	}
	openDataConn := func() (net.Conn, error) {
		if dataListener != nil {
			return dataListener.Accept()
		}
		if activeDataAddr != "" {
			return net.DialTimeout("tcp", activeDataAddr, time.Second)
		}
		return nil, fmt.Errorf("data channel not prepared")
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		s.mu.Lock()
		s.commands = append(s.commands, line)
		s.mu.Unlock()
		parts := strings.SplitN(line, " ", 2)
		command := strings.ToUpper(parts[0])
		argument := ""
		if len(parts) == 2 {
			argument = parts[1]
		}
		switch command {
		case "USER":
			if options.userImmediate {
				_, _ = fmt.Fprint(conn, "230 logged in by USER\r\n")
			} else {
				_, _ = fmt.Fprint(conn, "331 password required\r\n")
			}
		case "PASS":
			_, _ = fmt.Fprint(conn, "230 logged in\r\n")
		case "TYPE", "OPTS":
			_, _ = fmt.Fprint(conn, "200 ok\r\n")
		case "EPSV":
			if !options.epsv || options.rejectPassive {
				_, _ = fmt.Fprint(conn, "500 EPSV unsupported\r\n")
				continue
			}
			port, openErr := openData()
			if openErr != nil {
				_, _ = fmt.Fprint(conn, "425 cannot open data\r\n")
				continue
			}
			if options.epsvUnreachable {
				closeData()
			}
			_, _ = fmt.Fprintf(conn, "229 Entering Extended Passive Mode (|||%d|)\r\n", port)
		case "PASV":
			if options.rejectPassive {
				_, _ = fmt.Fprint(conn, "500 PASV unsupported\r\n")
				continue
			}
			port, openErr := openData()
			if openErr != nil {
				_, _ = fmt.Fprint(conn, "425 cannot open data\r\n")
				continue
			}
			host := options.pasvAdvertisedHost
			if host == "" {
				host = "127,0,0,1"
			}
			_, _ = fmt.Fprintf(conn, "227 Entering Passive Mode (%s,%d,%d)\r\n", host, port/256, port%256)
		case "EPRT":
			if options.rejectEPRT {
				_, _ = fmt.Fprint(conn, "500 EPRT unsupported\r\n")
				continue
			}
			fields := strings.Split(argument, "|")
			if len(fields) != 5 || fields[2] == "" || fields[3] == "" {
				_, _ = fmt.Fprint(conn, "501 invalid EPRT\r\n")
				continue
			}
			activeDataAddr = net.JoinHostPort(fields[2], fields[3])
			_, _ = fmt.Fprint(conn, "200 EPRT accepted\r\n")
		case "PORT":
			fields := strings.Split(argument, ",")
			if len(fields) != 6 {
				_, _ = fmt.Fprint(conn, "501 invalid PORT\r\n")
				continue
			}
			p1, _ := strconv.Atoi(fields[4])
			p2, _ := strconv.Atoi(fields[5])
			activeDataAddr = net.JoinHostPort(strings.Join(fields[:4], "."), strconv.Itoa(p1*256+p2))
			_, _ = fmt.Fprint(conn, "200 PORT accepted\r\n")
		case "NLST":
			_, _ = fmt.Fprint(conn, "150 opening data\r\n")
			dataConn, acceptErr := openDataConn()
			if acceptErr == nil {
				_, _ = fmt.Fprint(dataConn, "/archive/hello.txt\r\n")
				_ = dataConn.Close()
			}
			closeData()
			_, _ = fmt.Fprint(conn, "226 complete\r\n")
		case "RETR":
			switch options.retrMode {
			case "reject":
				closeData()
				_, _ = fmt.Fprint(conn, "550 file unavailable\r\n")
			case "partial":
				_, _ = fmt.Fprint(conn, "150 opening data\r\n")
				dataConn, acceptErr := openDataConn()
				if acceptErr == nil {
					_, _ = fmt.Fprint(dataConn, "PARTIAL")
					_ = dataConn.Close()
				}
				closeData()
				_, _ = fmt.Fprint(conn, "426 transfer aborted\r\n")
			default:
				_, _ = fmt.Fprint(conn, "150 opening data\r\n")
				dataConn, acceptErr := openDataConn()
				if acceptErr == nil {
					if argument == "hello.txt" {
						_, _ = fmt.Fprint(dataConn, "HELLO_COMPAT\n")
					}
					_ = dataConn.Close()
				}
				closeData()
				_, _ = fmt.Fprint(conn, "226 complete\r\n")
			}
		case "QUIT":
			_, _ = fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			_, _ = fmt.Fprintf(conn, "500 unsupported %s\r\n", command)
		}
	}
}

func (s *ftpCompatServer) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *ftpCompatServer) Close() {
	_ = s.listener.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
}

func containsFTPCommand(commands []string, command string) bool {
	for _, line := range commands {
		if strings.EqualFold(strings.Fields(line)[0], command) {
			return true
		}
	}
	return false
}
