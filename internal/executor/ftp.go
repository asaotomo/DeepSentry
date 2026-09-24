package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/config"
	"ai-edr/internal/ui"
)

type FTPExecutor struct {
	conn            net.Conn
	reader          *bufio.Reader
	host            string
	connectTimeout  time.Duration
	commandTimeout  time.Duration
	transferTimeout time.Duration
	tlsMode         string
	tlsConfig       *tls.Config
	dataMode        string
	activeAddress   string
	activeBlocked   bool
	mu              sync.Mutex
}

func newFTPExecutor(cfg config.Config) (*FTPExecutor, error) {
	tlsMode := strings.ToLower(strings.TrimSpace(cfg.FTPTLSMode))
	if tlsMode == "" {
		tlsMode = "plain"
	}
	if tlsMode != "plain" && tlsMode != "explicit" && tlsMode != "implicit" {
		return nil, fmt.Errorf("FTP TLS 模式无效: %s", cfg.FTPTLSMode)
	}
	defaultPort := "21"
	if tlsMode == "implicit" {
		defaultPort = "990"
	}
	host := normalizeHostPort(cfg.FTPHost, defaultPort)
	connectTimeout := ftpTimeout(cfg.FTPConnectTimeoutSec, 10*time.Second)
	commandTimeout := ftpTimeout(cfg.FTPCommandTimeoutSec, 30*time.Second)
	transferTimeout := ftpTimeout(cfg.FTPTransferTimeoutSec, 90*time.Second)
	dataMode := strings.ToLower(strings.TrimSpace(cfg.FTPDataMode))
	if dataMode == "" {
		dataMode = "passive"
	}
	if dataMode != "passive" && dataMode != "active" && dataMode != "auto" {
		return nil, fmt.Errorf("FTP 数据模式无效: %s", cfg.FTPDataMode)
	}
	if dataMode == "active" && strings.TrimSpace(cfg.ControllerProxy) != "" {
		return nil, fmt.Errorf("FTP 主动模式不能经由 controller_proxy；请使用 passive 或直连")
	}
	tlsConfig, err := buildFTPTLSConfig(cfg, host)
	if err != nil {
		return nil, err
	}
	conn, err := config.ControllerDialTimeout("tcp", host, connectTimeout)
	if err != nil {
		return nil, fmt.Errorf("FTP 连接失败: %v", err)
	}
	f := &FTPExecutor{
		conn: conn, reader: bufio.NewReader(conn), host: host,
		connectTimeout: connectTimeout, commandTimeout: commandTimeout, transferTimeout: transferTimeout,
		tlsMode: tlsMode, tlsConfig: tlsConfig, dataMode: dataMode,
		activeAddress: strings.TrimSpace(cfg.FTPActiveAddress),
		activeBlocked: strings.TrimSpace(cfg.ControllerProxy) != "",
	}
	if tlsMode == "implicit" {
		if err := f.upgradeControlTLS(); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("FTPS 隐式 TLS 握手失败: %w", err)
		}
	}
	greeting, err := f.readReadyGreeting()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("FTP 服务端问候失败: %w", err)
	}
	if code := ftpReplyCode(greeting); code != 220 {
		conn.Close()
		return nil, fmt.Errorf("FTP 服务端未就绪: %s", strings.TrimSpace(greeting))
	}
	if tlsMode == "explicit" {
		authResp, authErr := f.cmd("AUTH TLS")
		if authErr != nil || ftpReplyCode(authResp) != 234 {
			_ = conn.Close()
			if authErr != nil {
				return nil, fmt.Errorf("FTPS 服务端拒绝 AUTH TLS: %w", authErr)
			}
			return nil, fmt.Errorf("FTPS 服务端拒绝 AUTH TLS: %s", strings.TrimSpace(authResp))
		}
		if err := f.upgradeControlTLS(); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("FTPS 显式 TLS 握手失败: %w", err)
		}
	}
	user := cfg.FTPUser
	if user == "" {
		user = "anonymous"
	}
	pass := cfg.FTPPassword
	if pass == "" && user == "anonymous" {
		pass = "deepsentry@example.local"
	}
	userResp, err := f.cmd("USER " + user)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("FTP 用户认证失败: %w", err)
	}
	switch ftpReplyCode(userResp) {
	case 230:
		// Some anonymous/allow-listed appliances authenticate on USER and reject
		// an unnecessary PASS command.
	case 331:
		passResp, passErr := f.cmd("PASS " + pass)
		if passErr != nil {
			conn.Close()
			return nil, fmt.Errorf("FTP 密码认证失败: %w", passErr)
		}
		if code := ftpReplyCode(passResp); code < 200 || code >= 300 {
			conn.Close()
			return nil, fmt.Errorf("FTP 密码认证未完成: %s", strings.TrimSpace(passResp))
		}
	case 332:
		conn.Close()
		return nil, fmt.Errorf("FTP 服务端要求 ACCT 账户信息，当前配置未提供")
	default:
		conn.Close()
		return nil, fmt.Errorf("FTP USER 返回了不支持的状态: %s", strings.TrimSpace(userResp))
	}
	if tlsMode != "plain" {
		if err := f.enablePrivateDataProtection(); err != nil {
			f.Close()
			return nil, err
		}
	}
	if typeResp, typeErr := f.cmd("TYPE I"); typeErr != nil || ftpReplyCode(typeResp)/100 != 2 {
		conn.Close()
		if typeErr != nil {
			return nil, fmt.Errorf("FTP 无法切换二进制传输模式: %w", typeErr)
		}
		return nil, fmt.Errorf("FTP 无法切换二进制传输模式: %s", strings.TrimSpace(typeResp))
	}
	// UTF-8 negotiation is optional. The failure response is consumed so it
	// cannot contaminate the next transfer command.
	_, _ = f.cmd("OPTS UTF8 ON")
	return f, nil
}

func buildFTPTLSConfig(cfg config.Config, hostPort string) (*tls.Config, error) {
	if strings.EqualFold(strings.TrimSpace(cfg.FTPTLSMode), "plain") || strings.TrimSpace(cfg.FTPTLSMode) == "" {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, fmt.Errorf("解析 FTPS 主机失败: %w", err)
	}
	serverName := strings.TrimSpace(cfg.FTPTLSServerName)
	if serverName == "" {
		serverName = host
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		// #nosec G402 -- explicit operator-controlled compatibility opt-out;
		// validation, docs and the default keep certificate verification enabled.
		InsecureSkipVerify: cfg.FTPTLSInsecureSkipVerify,
	}
	if caPath := strings.TrimSpace(cfg.FTPTLSCAFile); caPath != "" {
		pemData, readErr := os.ReadFile(expandLocalPath(caPath))
		if readErr != nil {
			return nil, fmt.Errorf("读取 FTPS CA 文件失败: %w", readErr)
		}
		roots, rootErr := x509.SystemCertPool()
		if rootErr != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("FTPS CA 文件不包含有效 PEM 证书")
		}
		tlsConfig.RootCAs = roots
	}
	return tlsConfig, nil
}

func (f *FTPExecutor) upgradeControlTLS() error {
	if f == nil || f.conn == nil || f.tlsConfig == nil {
		return fmt.Errorf("FTPS TLS 配置缺失")
	}
	tlsConn := tls.Client(f.conn, f.tlsConfig.Clone())
	ctx, cancel := context.WithTimeout(context.Background(), f.connectTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return err
	}
	f.conn = tlsConn
	f.reader = bufio.NewReader(tlsConn)
	return nil
}

func (f *FTPExecutor) enablePrivateDataProtection() error {
	for _, command := range []string{"PBSZ 0", "PROT P"} {
		resp, err := f.cmd(command)
		if err != nil {
			return fmt.Errorf("FTPS %s 失败: %w", command, err)
		}
		if ftpReplyCode(resp)/100 != 2 {
			return fmt.Errorf("FTPS %s 未生效: %s", command, strings.TrimSpace(resp))
		}
	}
	return nil
}

func ftpTimeout(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// FTP transfer pseudo-commands use the same quoting as local and SSH
// transfers. In particular, shellQuote emits '\” for apostrophes in paths.
func splitFTPCommandLine(command string) ([]string, error) {
	return splitShellFields(command)
}

func (f *FTPExecutor) Run(cmd string) (string, error) {
	trimmed := strings.TrimSpace(cmd)
	if strings.HasPrefix(trimmed, "local_run ") {
		localCommand := strings.TrimSpace(strings.TrimPrefix(trimmed, "local_run "))
		if localCommand == "" {
			return "", fmt.Errorf("local_run 缺少控制端命令")
		}
		return (&LocalExecutor{}).Run(localCommand)
	}
	parts, err := splitFTPCommandLine(cmd)
	if err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", nil
	}
	switch parts[0] {
	case "download":
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return "", fmt.Errorf("用法: download <远程文件> <本地路径>")
		}
		return f.downloadFile(parts[1], parts[2])
	case "upload":
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return "", fmt.Errorf("用法: upload <本地文件> <远程路径>")
		}
		return f.uploadFile(parts[1], parts[2])
	case "pwd", "noop":
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.cmd(strings.ToUpper(parts[0]))
	default:
		return "", fmt.Errorf("FTP 模式不支持 shell 命令: %s；请使用 file_download/file_upload/ListTargetDir/ReadTargetFile", parts[0])
	}
}

func (f *FTPExecutor) ReadTargetFile(path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dataConn, preliminary, err := f.openTransfer("RETR " + path)
	if err != nil {
		return nil, err
	}
	data, readErr := readFTPDataLimited(dataConn, maxReadSize)
	_ = dataConn.Close()
	respErr := f.finishTransfer(preliminary)
	if readErr != nil {
		return nil, readErr
	}
	return data, respErr
}

// writeTargetFile implements the generic target-file contract over FTP STOR.
// Keeping this on the executor avoids materializing model-generated content in
// a local temporary file and ensures the same transfer state machine, command
// validation, deadlines, and final 2xx acknowledgement are applied.
func (f *FTPExecutor) writeTargetFile(remotePath string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	dataConn, preliminary, err := f.openTransfer("STOR " + remotePath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dataConn, bytes.NewReader(content))
	_ = dataConn.Close()
	finishErr := f.finishTransfer(preliminary)
	if copyErr != nil {
		return copyErr
	}
	return finishErr
}

func (f *FTPExecutor) ListTargetDir(path string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := "NLST"
	if strings.TrimSpace(path) != "" {
		cmd += " " + path
	}
	dataConn, preliminary, err := f.openTransfer(cmd)
	if err != nil {
		return nil, err
	}
	raw, readErr := readFTPDataLimited(dataConn, maxReadSize)
	_ = dataConn.Close()
	respErr := f.finishTransfer(preliminary)
	if readErr != nil {
		return nil, readErr
	}
	if respErr != nil {
		return nil, respErr
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			line = strings.TrimSuffix(strings.ReplaceAll(line, `\`, "/"), "/")
			names = append(names, pathpkg.Base(line))
		}
	}
	return names, nil
}

func (f *FTPExecutor) IsRemote() bool { return true }
func (f *FTPExecutor) Mode() string   { return "ftp" }
func (f *FTPExecutor) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn == nil {
		return
	}
	previousTimeout := f.commandTimeout
	if previousTimeout <= 0 || previousTimeout > 2*time.Second {
		f.commandTimeout = 2 * time.Second
	}
	_, _ = f.cmd("QUIT")
	f.commandTimeout = previousTimeout
	_ = f.conn.Close()
	f.conn = nil
}

func (f *FTPExecutor) uploadFile(localPath, remotePath string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	localPath = expandLocalPath(localPath)

	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dataConn, preliminary, err := f.openTransfer("STOR " + remotePath)
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(dataConn, src)
	_ = dataConn.Close()
	respErr := f.finishTransfer(preliminary)
	if copyErr != nil {
		return "", copyErr
	}
	if respErr != nil {
		return "", respErr
	}
	return fmt.Sprintf("%sFTP 上传成功 (Bytes: %d): %s -> %s", ui.Prefix("✅", "[OK]"), n, localPath, remotePath), nil
}

func (f *FTPExecutor) downloadFile(remotePath, localPath string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	localPath = expandLocalPath(localPath)

	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		return "", err
	}
	dataConn, preliminary, err := f.openTransfer("RETR " + remotePath)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".deepsentry-ftp-download-*.tmp")
	if err != nil {
		dataConn.Close()
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = dataConn.Close()
		return "", err
	}
	n, copyErr := io.Copy(tmp, dataConn)
	if syncErr := tmp.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	_ = dataConn.Close()
	respErr := f.finishTransfer(preliminary)
	if copyErr != nil {
		return "", copyErr
	}
	if respErr != nil {
		return "", respErr
	}
	if err := replaceDownloadedFile(tmpPath, localPath); err != nil {
		return "", err
	}
	return fmt.Sprintf("%sFTP 下载成功 (Bytes: %d): %s -> %s", ui.Prefix("✅", "[OK]"), n, remotePath, localPath), nil
}

func replaceDownloadedFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	// Windows cannot replace an existing destination atomically. Preserve the
	// old destination unless the temporary file is complete and ready.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func (f *FTPExecutor) cmd(command string) (string, error) {
	if _, err := f.cmdNoRead(command); err != nil {
		return "", err
	}
	return f.readResponse()
}

func (f *FTPExecutor) cmdNoRead(command string) (string, error) {
	if f == nil || f.conn == nil {
		return "", fmt.Errorf("FTP 连接已关闭")
	}
	if err := validateFTPCommand(command); err != nil {
		return "", err
	}
	_ = f.conn.SetWriteDeadline(time.Now().Add(f.commandTimeout))
	defer f.conn.SetWriteDeadline(time.Time{})
	_, err := fmt.Fprintf(f.conn, "%s\r\n", command)
	return command, err
}

func validateFTPCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("FTP 命令不能为空")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		return fmt.Errorf("FTP 命令参数包含禁止的换行或 NUL 字符")
	}
	if len(command) > 4096 {
		return fmt.Errorf("FTP 命令超过 4096 字节")
	}
	return nil
}

func (f *FTPExecutor) readResponse() (string, error) {
	if f == nil || f.conn == nil {
		return "", fmt.Errorf("FTP 连接已关闭")
	}
	_ = f.conn.SetReadDeadline(time.Now().Add(f.commandTimeout))
	defer f.conn.SetReadDeadline(time.Time{})
	line, err := f.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	code := ""
	if len(line) >= 3 {
		code = line[:3]
	}
	var b strings.Builder
	b.WriteString(line)
	if len(line) > 3 && line[3] == '-' {
		for {
			l, err := f.reader.ReadString('\n')
			if err != nil {
				return b.String(), err
			}
			b.WriteString(l)
			if strings.HasPrefix(l, code+" ") {
				break
			}
		}
	}
	codeNumber := ftpReplyCode(b.String())
	if codeNumber == 0 {
		return b.String(), fmt.Errorf("FTP 响应缺少合法三位状态码: %q", strings.TrimSpace(b.String()))
	}
	if codeNumber >= 400 {
		return b.String(), fmt.Errorf("%s", strings.TrimSpace(b.String()))
	}
	return b.String(), nil
}

func (f *FTPExecutor) readReadyGreeting() (string, error) {
	var combined strings.Builder
	for replies := 0; replies < 4; replies++ {
		resp, err := f.readResponse()
		combined.WriteString(resp)
		if err != nil {
			return combined.String(), err
		}
		code := ftpReplyCode(resp)
		if code < 100 || code >= 200 {
			return resp, nil
		}
	}
	return combined.String(), fmt.Errorf("FTP 服务端持续返回 preliminary greeting")
}

func ftpReplyCode(resp string) int {
	resp = strings.TrimSpace(resp)
	if len(resp) < 3 {
		return 0
	}
	code, err := strconv.Atoi(resp[:3])
	if err != nil {
		return 0
	}
	return code
}

// beginTransfer consumes the command reply before reading the data socket.
// This detects immediate 4xx/5xx failures instead of waiting indefinitely on
// a data connection the server will never use.
func (f *FTPExecutor) beginTransfer(command string) (bool, error) {
	if _, err := f.cmdNoRead(command); err != nil {
		return false, err
	}
	resp, err := f.readResponse()
	if err != nil {
		return false, err
	}
	code := ftpReplyCode(resp)
	if code >= 100 && code < 200 {
		return true, nil
	}
	return false, fmt.Errorf("FTP 传输未开始: %s", strings.TrimSpace(resp))
}

func (f *FTPExecutor) finishTransfer(preliminary bool) error {
	if !preliminary {
		return nil
	}
	resp, err := f.readResponse()
	if err != nil {
		return err
	}
	if code := ftpReplyCode(resp); code < 200 || code >= 300 {
		return fmt.Errorf("FTP 传输未完成: %s", strings.TrimSpace(resp))
	}
	return nil
}

func readFTPDataLimited(conn net.Conn, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(conn, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("FTP 数据超过安全读取上限 %d 字节，请使用 file_download 保存完整文件", limit)
	}
	return data, nil
}

type ftpPreparedData struct {
	conn         net.Conn
	listener     *net.TCPListener
	expectedPeer net.IP
}

func (d *ftpPreparedData) close() {
	if d == nil {
		return
	}
	if d.conn != nil {
		_ = d.conn.Close()
		d.conn = nil
	}
	if d.listener != nil {
		_ = d.listener.Close()
		d.listener = nil
	}
}

// openTransfer prepares either a passive outbound connection or an active
// listener, starts the FTP command, then returns the established data stream.
// Keeping this ordering centralized is important for active mode, where the
// server only connects after RETR/STOR/NLST receives a preliminary reply.
func (f *FTPExecutor) openTransfer(command string) (net.Conn, bool, error) {
	// Validate before EPSV/PASV/EPRT/PORT so rejected model input cannot open
	// or advertise a data endpoint as a side effect.
	if err := validateFTPCommand(command); err != nil {
		return nil, false, err
	}
	prepared, err := f.prepareData()
	if err != nil {
		return nil, false, err
	}
	preliminary, err := f.beginTransfer(command)
	if err != nil {
		prepared.close()
		return nil, false, err
	}
	dataConn, err := prepared.open(f)
	if err != nil {
		prepared.close()
		finishErr := f.finishTransfer(preliminary)
		if finishErr != nil {
			return nil, false, fmt.Errorf("FTP 数据通道失败: %v；传输终态: %w", err, finishErr)
		}
		return nil, false, fmt.Errorf("FTP 数据通道失败: %w", err)
	}
	return dataConn, preliminary, nil
}

func (d *ftpPreparedData) open(f *FTPExecutor) (net.Conn, error) {
	if d == nil {
		return nil, fmt.Errorf("FTP 数据通道未准备")
	}
	conn := d.conn
	if conn != nil {
		d.conn = nil
	} else {
		if d.listener == nil {
			return nil, fmt.Errorf("FTP 主动数据监听器缺失")
		}
		_ = d.listener.SetDeadline(time.Now().Add(f.transferTimeout))
		accepted, err := d.listener.AcceptTCP()
		_ = d.listener.Close()
		d.listener = nil
		if err != nil {
			return nil, fmt.Errorf("等待 FTP 服务端主动回连失败: %w", err)
		}
		conn = accepted
		if d.expectedPeer != nil {
			peer, _ := accepted.RemoteAddr().(*net.TCPAddr)
			if peer == nil || !peer.IP.Equal(d.expectedPeer) {
				_ = accepted.Close()
				return nil, fmt.Errorf("FTP 主动数据连接来自非预期地址 %v", accepted.RemoteAddr())
			}
		}
	}
	_ = conn.SetDeadline(time.Now().Add(f.transferTimeout))
	if f.tlsMode != "plain" {
		tlsConn := tls.Client(conn, f.tlsConfig.Clone())
		ctx, cancel := context.WithTimeout(context.Background(), f.transferTimeout)
		err := tlsConn.HandshakeContext(ctx)
		cancel()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("FTPS 数据通道 TLS 握手失败: %w", err)
		}
		conn = tlsConn
	}
	return conn, nil
}

func (f *FTPExecutor) prepareData() (*ftpPreparedData, error) {
	switch f.dataMode {
	case "active":
		return f.activeData()
	case "auto":
		passive, passiveErr := f.passiveData()
		if passiveErr == nil {
			return passive, nil
		}
		active, activeErr := f.activeData()
		if activeErr == nil {
			return active, nil
		}
		return nil, fmt.Errorf("FTP 被动/主动数据通道均不可用（passive: %v；active: %v）", passiveErr, activeErr)
	default:
		return f.passiveData()
	}
}

func (f *FTPExecutor) passiveData() (*ftpPreparedData, error) {
	host, err := f.controlPeerHost()
	if err != nil {
		return nil, err
	}
	var epsvFailure error
	if resp, epsvErr := f.cmd("EPSV"); epsvErr == nil {
		port, parseErr := parseEPSVPort(resp)
		if parseErr == nil {
			if dataConn, dialErr := f.dialData(host, port); dialErr == nil {
				return &ftpPreparedData{conn: dataConn}, nil
			} else {
				epsvFailure = dialErr
			}
		} else {
			epsvFailure = parseErr
		}
	} else {
		epsvFailure = epsvErr
	}
	resp, err := f.cmd("PASV")
	if err != nil {
		return nil, fmt.Errorf("FTP EPSV/PASV 均不可用（EPSV: %v；PASV: %w）", epsvFailure, err)
	}
	port, err := parsePASVPort(resp)
	if err != nil {
		return nil, err
	}
	// Deliberately use the control connection's peer IP instead of the address
	// advertised by PASV. This works through common NAT misconfiguration and
	// prevents a malicious FTP server from turning DeepSentry into a bounce/
	// SSRF client to an unrelated host.
	dataConn, err := f.dialData(host, port)
	if err != nil {
		return nil, err
	}
	return &ftpPreparedData{conn: dataConn}, nil
}

func (f *FTPExecutor) activeData() (*ftpPreparedData, error) {
	if f.activeBlocked {
		return nil, fmt.Errorf("controller_proxy 下无法接收 FTP 服务端主动回连")
	}
	localAddr, ok := f.conn.LocalAddr().(*net.TCPAddr)
	if !ok || localAddr.IP == nil || localAddr.IP.IsUnspecified() {
		return nil, fmt.Errorf("无法确定 FTP 主动模式的本地网卡地址")
	}
	bindIP := append(net.IP(nil), localAddr.IP...)
	advertiseIP := bindIP
	if f.activeAddress != "" {
		advertiseIP = net.ParseIP(f.activeAddress)
		if advertiseIP == nil {
			return nil, fmt.Errorf("FTP 主动广播地址无效: %s", f.activeAddress)
		}
	}
	network := "tcp6"
	if bindIP.To4() != nil {
		network = "tcp4"
		bindIP = bindIP.To4()
	}
	listener, err := net.ListenTCP(network, &net.TCPAddr{IP: bindIP, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("创建 FTP 主动数据监听器失败: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	protocolFamily := 2
	formattedIP := advertiseIP.String()
	if ip4 := advertiseIP.To4(); ip4 != nil {
		protocolFamily = 1
		formattedIP = ip4.String()
	}
	eprt := fmt.Sprintf("EPRT |%d|%s|%d|", protocolFamily, formattedIP, port)
	if _, eprtErr := f.cmd(eprt); eprtErr != nil {
		ip4 := advertiseIP.To4()
		if ip4 == nil {
			_ = listener.Close()
			return nil, fmt.Errorf("FTP EPRT 不可用: %w", eprtErr)
		}
		portCommand := fmt.Sprintf("PORT %d,%d,%d,%d,%d,%d", ip4[0], ip4[1], ip4[2], ip4[3], port/256, port%256)
		if _, portErr := f.cmd(portCommand); portErr != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("FTP EPRT/PORT 均不可用（EPRT: %v；PORT: %w）", eprtErr, portErr)
		}
	}
	var expectedPeer net.IP
	if remote, ok := f.conn.RemoteAddr().(*net.TCPAddr); ok {
		expectedPeer = append(net.IP(nil), remote.IP...)
	}
	return &ftpPreparedData{listener: listener, expectedPeer: expectedPeer}, nil
}

func (f *FTPExecutor) controlPeerHost() (string, error) {
	// A proxied connection's RemoteAddr is the proxy endpoint, not the FTP
	// server. Use the configured target host so EPSV/PASV data channels follow
	// the same route while still ignoring an attacker-controlled PASV host.
	if f != nil && strings.TrimSpace(f.host) != "" {
		if host, _, err := net.SplitHostPort(f.host); err == nil && host != "" {
			return host, nil
		}
	}
	if f == nil || f.conn == nil || f.conn.RemoteAddr() == nil {
		return "", fmt.Errorf("FTP 控制连接缺少对端地址")
	}
	host, _, err := net.SplitHostPort(f.conn.RemoteAddr().String())
	if err != nil {
		return "", fmt.Errorf("解析 FTP 控制连接对端失败: %w", err)
	}
	return host, nil
}

func (f *FTPExecutor) dialData(host string, port int) (net.Conn, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("FTP 被动端口非法: %d", port)
	}
	conn, err := config.ControllerDialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), f.connectTimeout)
	if err != nil {
		return nil, fmt.Errorf("FTP 数据连接失败 %s:%d: %w", host, port, err)
	}
	_ = conn.SetDeadline(time.Now().Add(f.transferTimeout))
	return conn, nil
}

func parseEPSVPort(resp string) (int, error) {
	start := strings.Index(resp, "(")
	end := strings.Index(resp[start+1:], ")")
	if start < 0 || end < 0 {
		return 0, fmt.Errorf("无法解析 EPSV 响应: %s", strings.TrimSpace(resp))
	}
	inside := resp[start+1 : start+1+end]
	if len(inside) < 5 {
		return 0, fmt.Errorf("非法 EPSV 响应: %s", strings.TrimSpace(resp))
	}
	delimiter := inside[0]
	parts := strings.Split(inside, string(delimiter))
	if len(parts) < 5 {
		return 0, fmt.Errorf("非法 EPSV 响应: %s", strings.TrimSpace(resp))
	}
	port, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-2]))
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("非法 EPSV 端口: %s", strings.TrimSpace(resp))
	}
	return port, nil
}

func parsePASVPort(resp string) (int, error) {
	start := strings.Index(resp, "(")
	end := strings.Index(resp, ")")
	if start < 0 || end < start {
		return 0, fmt.Errorf("无法解析 PASV 响应: %s", strings.TrimSpace(resp))
	}
	parts := strings.Split(resp[start+1:end], ",")
	if len(parts) != 6 {
		return 0, fmt.Errorf("非法 PASV 响应: %s", strings.TrimSpace(resp))
	}
	for i := range parts {
		value, parseErr := strconv.Atoi(strings.TrimSpace(parts[i]))
		if parseErr != nil || value < 0 || value > 255 {
			return 0, fmt.Errorf("非法 PASV 响应: %s", strings.TrimSpace(resp))
		}
	}
	p1, _ := strconv.Atoi(strings.TrimSpace(parts[4]))
	p2, _ := strconv.Atoi(strings.TrimSpace(parts[5]))
	port := p1*256 + p2
	if port <= 0 {
		return 0, fmt.Errorf("非法 PASV 端口: %s", strings.TrimSpace(resp))
	}
	return port, nil
}
