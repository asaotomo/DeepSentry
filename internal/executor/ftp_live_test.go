package executor

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ai-edr/internal/config"
)

// TestFTPLiveServer exercises the real FTP protocol stack against an external
// disposable server. It is skipped during ordinary test runs; set
// DEEPSENTRY_LIVE_FTP_ADDR plus optional user/password variables to enable it.
func TestFTPLiveServer(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("DEEPSENTRY_LIVE_FTP_ADDR"))
	if addr == "" {
		t.Skip("set DEEPSENTRY_LIVE_FTP_ADDR to run the live FTP integration test")
	}
	user := os.Getenv("DEEPSENTRY_LIVE_FTP_USER")
	if user == "" {
		user = "deepsentry"
	}
	password := os.Getenv("DEEPSENTRY_LIVE_FTP_PASSWORD")
	tlsMode := strings.TrimSpace(os.Getenv("DEEPSENTRY_LIVE_FTP_TLS_MODE"))
	dataMode := strings.TrimSpace(os.Getenv("DEEPSENTRY_LIVE_FTP_DATA_MODE"))
	insecureTLS, _ := strconv.ParseBool(os.Getenv("DEEPSENTRY_LIVE_FTP_TLS_INSECURE"))
	baseConfig := config.Config{
		FTPHost: addr, FTPUser: user, FTPPassword: password,
		FTPTLSMode: tlsMode, FTPTLSServerName: os.Getenv("DEEPSENTRY_LIVE_FTP_TLS_SERVER_NAME"),
		FTPTLSCAFile: os.Getenv("DEEPSENTRY_LIVE_FTP_TLS_CA_FILE"), FTPTLSInsecureSkipVerify: insecureTLS,
		FTPDataMode: dataMode, FTPActiveAddress: os.Getenv("DEEPSENTRY_LIVE_FTP_ACTIVE_ADDRESS"),
		FTPConnectTimeoutSec: 3, FTPCommandTimeoutSec: 5, FTPTransferTimeoutSec: 10,
	}
	if tlsMode != "" && !strings.EqualFold(tlsMode, "plain") && !insecureTLS {
		wrongNameConfig := baseConfig
		wrongNameConfig.FTPTLSServerName = "wrong-host.invalid"
		if rejected, verifyErr := newFTPExecutor(wrongNameConfig); verifyErr == nil {
			rejected.Close()
			t.Fatal("FTPS certificate hostname mismatch was accepted")
		}
	}
	badConfig := baseConfig
	badConfig.FTPPassword = password + "-wrong"
	if _, err := newFTPExecutor(badConfig); err == nil || !strings.Contains(strings.ToLower(err.Error()), "认证") {
		t.Fatalf("wrong-password login must fail clearly, err=%v", err)
	}

	ex, err := newFTPExecutor(baseConfig)
	if err != nil {
		t.Fatalf("connect/login: %v", err)
	}
	defer ex.Close()
	if tlsMode != "" && !strings.EqualFold(tlsMode, "plain") {
		tlsConn, ok := ex.conn.(*tls.Conn)
		if !ok {
			t.Fatalf("FTPS control channel is not TLS: %T", ex.conn)
		}
		state := tlsConn.ConnectionState()
		if !state.HandshakeComplete || state.Version < tls.VersionTLS12 || len(state.PeerCertificates) == 0 {
			t.Fatalf("FTPS TLS state incomplete: handshake=%t version=%x peer_certs=%d", state.HandshakeComplete, state.Version, len(state.PeerCertificates))
		}
		if !insecureTLS && len(state.VerifiedChains) == 0 {
			t.Fatal("FTPS certificate chain was not verified")
		}
	}

	if out, err := ex.Run("pwd"); err != nil || !strings.HasPrefix(strings.TrimSpace(out), "257") {
		t.Fatalf("PWD output=%q err=%v", out, err)
	}
	if out, err := ex.Run("noop"); err != nil || !strings.HasPrefix(strings.TrimSpace(out), "200") {
		t.Fatalf("NOOP output=%q err=%v", out, err)
	}

	names, err := ex.ListTargetDir("/")
	if err != nil {
		t.Fatalf("NLST: %v", err)
	}
	if !containsString(names, "seed.txt") {
		t.Fatalf("NLST missing seed.txt: %#v", names)
	}
	seed, err := ex.ReadTargetFile("seed.txt")
	if err != nil || string(seed) != "FTP_LIVE_SEED\n" {
		t.Fatalf("RETR seed data=%q err=%v", seed, err)
	}

	clientDir := t.TempDir()
	uploadPath := filepath.Join(clientDir, "上传 source file.txt")
	uploadData := []byte("FTP_UPLOAD_中文_OK\n")
	if err := os.WriteFile(uploadPath, uploadData, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := ex.Run(fmt.Sprintf("upload %q %q", uploadPath, "uploaded file.txt")); err != nil || !strings.Contains(out, "FTP 上传成功") {
		t.Fatalf("STOR upload output=%q err=%v", out, err)
	}
	if got, err := ex.ReadTargetFile("uploaded file.txt"); err != nil || string(got) != string(uploadData) {
		t.Fatalf("verify upload data=%q err=%v", got, err)
	}

	if err := WriteFileWithExecutor(ex, "direct-generated.txt", []byte("DIRECT_WRITE_OK\n")); err != nil {
		t.Fatalf("generic target write: %v", err)
	}
	if got, err := ex.ReadTargetFile("direct-generated.txt"); err != nil || string(got) != "DIRECT_WRITE_OK\n" {
		t.Fatalf("verify generic target write data=%q err=%v", got, err)
	}

	downloadPath := filepath.Join(clientDir, "nested", "下载 result.txt")
	if err := os.MkdirAll(filepath.Dir(downloadPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(downloadPath, []byte("OLD_DESTINATION"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := ex.Run(fmt.Sprintf("download %q %q", "uploaded file.txt", downloadPath)); err != nil || !strings.Contains(out, "FTP 下载成功") {
		t.Fatalf("download output=%q err=%v", out, err)
	}
	if got, err := os.ReadFile(downloadPath); err != nil || string(got) != string(uploadData) {
		t.Fatalf("downloaded data=%q err=%v", got, err)
	}
	if info, err := os.Stat(downloadPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("download mode=%v err=%v", infoMode(info), err)
	}
	if out, err := ex.Run(fmt.Sprintf("local_run cat %q", downloadPath)); err != nil || strings.TrimSpace(out) != strings.TrimSpace(string(uploadData)) {
		t.Fatalf("FTP control-side local_run output=%q err=%v", out, err)
	}

	if _, err := ex.ReadTargetFile("missing-file.txt"); err == nil || !strings.Contains(err.Error(), "550") {
		t.Fatalf("missing file error=%v", err)
	}
	if out, err := ex.Run("noop"); err != nil || !strings.HasPrefix(strings.TrimSpace(out), "200") {
		t.Fatalf("control channel desynchronized after 550: output=%q err=%v", out, err)
	}
	if _, err := ex.ReadTargetFile("seed.txt\r\nDELE seed.txt"); err == nil || !strings.Contains(err.Error(), "禁止") {
		t.Fatalf("CRLF injection error=%v", err)
	}
	if _, err := ex.Run("rm seed.txt"); err == nil || !strings.Contains(err.Error(), "不支持 shell") {
		t.Fatalf("unsupported shell command error=%v", err)
	}

	// One executor is shared by an Agent turn; concurrent requests must be
	// serialized without corrupting the FTP control channel.
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if index%3 == 0 {
				_, err := ex.Run("noop")
				errs <- err
				return
			}
			if index%3 == 1 {
				_, err := ex.ListTargetDir("/")
				errs <- err
				return
			}
			data, err := ex.ReadTargetFile("seed.txt")
			if err == nil && string(data) != "FTP_LIVE_SEED\n" {
				err = fmt.Errorf("unexpected concurrent RETR data %q", data)
			}
			errs <- err
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent FTP operation: %v", err)
		}
	}

	finalNames, err := ex.ListTargetDir("/")
	if err != nil {
		t.Fatalf("final NLST: %v", err)
	}
	sort.Strings(finalNames)
	for _, want := range []string{"direct-generated.txt", "seed.txt", "uploaded file.txt"} {
		if !containsString(finalNames, want) {
			t.Errorf("final NLST missing %q: %#v", want, finalNames)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
