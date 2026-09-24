package executor

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/config"

	"golang.org/x/crypto/ssh"
)

// SSHNetworkExecutor drives appliances that expose an interactive vendor CLI
// over SSH instead of a POSIX exec shell. Huawei VRP, H3C Comware, Ruijie RGOS
// and many Cisco IOS devices reject /bin/bash, SFTP, or shell marker syntax.
type SSHNetworkExecutor struct {
	client         *ssh.Client
	session        *ssh.Session
	stdin          io.WriteCloser
	chunks         chan sshCLIChunk
	promptRE       *regexp.Regexp
	prompt         string
	deviceType     string
	banner         string
	enablePassword string
	loginTimeout   time.Duration
	commandTimeout time.Duration
	mu             sync.Mutex
	closeOnce      sync.Once
}

type sshCLIChunk struct {
	data string
	err  error
}

type sshCLIReadTimeout struct{}

func (sshCLIReadTimeout) Error() string   { return "SSH CLI read timeout" }
func (sshCLIReadTimeout) Timeout() bool   { return true }
func (sshCLIReadTimeout) Temporary() bool { return true }

func newSSHNetworkExecutor(client *ssh.Client, cfg config.Config) (*SSHNetworkExecutor, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("创建 SSH CLI Session 失败: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	session.Stderr = session.Stdout
	if err := requestNetworkPTY(session); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("网络设备 SSH PTY 请求失败: %w", err)
	}
	if err := session.Shell(); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("启动网络设备 SSH CLI 失败: %w", err)
	}

	exe := &SSHNetworkExecutor{
		client: client, session: session, stdin: stdin, chunks: make(chan sshCLIChunk, 32),
		deviceType: normalizeDeviceType(cfg.SSHDeviceType), enablePassword: cfg.SSHEnablePassword,
		loginTimeout: 25 * time.Second, commandTimeout: secondsOrDefault(cfg.SSHCommandTimeoutSec, 90),
	}
	if raw := strings.TrimSpace(cfg.SSHPrompt); raw != "" {
		exe.promptRE, err = compilePromptSpec(raw)
		if err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("ssh_prompt 无效: %w", err)
		}
	}
	go exe.pump(stdout)
	if err := exe.awaitInitialPrompt(); err != nil {
		_ = session.Close()
		return nil, err
	}
	exe.deviceType = detectTelnetDeviceType(exe.deviceType, exe.banner, exe.prompt)
	if err := exe.enterPrivilegedMode(); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("SSH enable 失败: %w", err)
	}
	exe.disablePaging()
	return exe, nil
}

func (s *SSHNetworkExecutor) pump(reader io.Reader) {
	buffered := bufio.NewReader(reader)
	buf := make([]byte, 4096)
	for {
		n, err := buffered.Read(buf)
		chunk := sshCLIChunk{err: err}
		if n > 0 {
			chunk.data = string(append([]byte(nil), buf[:n]...))
		}
		s.chunks <- chunk
		if err != nil {
			close(s.chunks)
			return
		}
	}
}

func (s *SSHNetworkExecutor) readChunk(deadline time.Time) (string, error) {
	wait := time.Until(deadline)
	if wait <= 0 {
		return "", sshCLIReadTimeout{}
	}
	if wait > 250*time.Millisecond {
		wait = 250 * time.Millisecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case chunk, ok := <-s.chunks:
		if !ok {
			return "", io.EOF
		}
		return chunk.data, chunk.err
	case <-timer.C:
		return "", sshCLIReadTimeout{}
	}
}

func (s *SSHNetworkExecutor) writeLine(value string) error {
	_, err := io.WriteString(s.stdin, value+"\r\n")
	return err
}

func (s *SSHNetworkExecutor) awaitInitialPrompt() error {
	deadline := time.Now().Add(s.loginTimeout)
	started := time.Now()
	nudged := false
	var transcript strings.Builder
	for time.Now().Before(deadline) {
		chunk, err := s.readChunk(deadline)
		transcript.WriteString(chunk)
		clean := cleanTerminalText(transcript.String())
		if prompt := s.detectPrompt(clean); prompt != "" {
			s.prompt = prompt
			s.banner = strings.TrimSpace(clean)
			return nil
		}
		if !nudged && time.Since(started) >= 750*time.Millisecond {
			_, _ = io.WriteString(s.stdin, "\r\n")
			nudged = true
		}
		if err != nil && !isTimeout(err) {
			return fmt.Errorf("读取 SSH 设备 CLI 失败: %w；最后响应: %s", err, diagnosticTail(clean, 320))
		}
	}
	return fmt.Errorf("等待 SSH 设备命令提示符超时 (%s)；请检查 ssh_device_type/ssh_prompt，最后响应: %s", s.loginTimeout, diagnosticTail(transcript.String(), 400))
}

func (s *SSHNetworkExecutor) detectPrompt(text string) string {
	line := lastTerminalLine(text)
	if line == "" {
		return ""
	}
	if s.promptRE != nil && s.promptRE.MatchString(strings.TrimSpace(text)) {
		return line
	}
	if s.prompt != "" && line == s.prompt {
		return line
	}
	if networkPromptRE.MatchString(line) || shellPromptRE.MatchString(line) {
		return line
	}
	return ""
}

func (s *SSHNetworkExecutor) Run(cmd string) (string, error) {
	return s.RunWithStreaming(cmd, nil)
}

func (s *SSHNetworkExecutor) RunWithStreaming(cmd string, onLine func(string)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "(空命令)", nil
	}
	if strings.HasPrefix(cmd, "local_run ") {
		return (&LocalExecutor{}).RunWithStreaming(strings.TrimPrefix(cmd, "local_run "), onLine)
	}
	if CommandUsesSudo(cmd) && s.deviceType == "linux" {
		cmd = ForceNonInteractiveSudo(cmd)
	}
	return s.runCLICommand(cmd, onLine, s.commandTimeout)
}

func (s *SSHNetworkExecutor) runCLICommand(cmd string, onLine func(string), timeout time.Duration) (string, error) {
	if err := s.writeLine(cmd); err != nil {
		return "", fmt.Errorf("发送 SSH CLI 命令失败: %w", err)
	}
	deadline := time.Now().Add(timeout)
	var raw strings.Builder
	matchTail := ""
	maxOutput := effectiveMaxOutputBytes()
	truncated := false
	lastStreamed := 0
	for time.Now().Before(deadline) {
		chunk, err := s.readChunk(deadline)
		if len(chunk) > 0 {
			matchTail += chunk
			if len(matchTail) > 32768 {
				matchTail = matchTail[len(matchTail)-32768:]
			}
			remaining := maxOutput - raw.Len()
			if remaining > 0 {
				if len(chunk) > remaining {
					raw.WriteString(safeUTF8BytePrefix(chunk, remaining))
					truncated = true
				} else {
					raw.WriteString(chunk)
				}
			} else {
				truncated = true
			}
		}
		clean := cleanTerminalText(raw.String())
		cleanTail := cleanTerminalText(matchTail)
		if advance, matched := networkPagerAdvance(cleanTail); matched {
			if _, writeErr := io.WriteString(s.stdin, advance); writeErr != nil {
				return finalizeTelnetOutput(clean, cmd, s.prompt, truncated, maxOutput, false), writeErr
			}
			matchTail = pagerRE.ReplaceAllString(cleanTail, "")
		}
		if onLine != nil {
			for _, line := range completedLines(clean, &lastStreamed) {
				if !pagerRE.MatchString(line) && strings.TrimSpace(line) != "" {
					onLine(line)
				}
			}
		}
		if prompt := s.detectPrompt(cleanTail); prompt != "" && commandResponseHasBoundary(cleanTail, cmd, prompt) {
			s.prompt = prompt
			return finalizeTelnetOutput(clean, cmd, prompt, truncated, maxOutput, true), nil
		}
		if err != nil && !isTimeout(err) {
			return finalizeTelnetOutput(clean, cmd, s.prompt, truncated, maxOutput, false), fmt.Errorf("SSH CLI 读取失败: %w", err)
		}
	}
	partial := finalizeTelnetOutput(cleanTerminalText(raw.String()), cmd, s.prompt, truncated, maxOutput, false)
	return partial, fmt.Errorf("等待 SSH 设备命令提示符超时 (%s, device=%s, prompt=%q)；已保留部分输出", timeout, s.deviceType, s.prompt)
}

func (s *SSHNetworkExecutor) enterPrivilegedMode() error {
	command := networkPrivilegeCommand(s.deviceType)
	if strings.TrimSpace(s.enablePassword) == "" || command == "" || networkPrivilegeAlreadyActive(s.deviceType, s.prompt) {
		return nil
	}
	if err := s.writeLine(command); err != nil {
		return err
	}
	deadline := time.Now().Add(minDuration(s.loginTimeout, 15*time.Second))
	var transcript strings.Builder
	sentPassword := false
	for time.Now().Before(deadline) {
		chunk, err := s.readChunk(deadline)
		transcript.WriteString(chunk)
		clean := cleanTerminalText(transcript.String())
		if authFailureRE.MatchString(clean) {
			return fmt.Errorf("设备拒绝 %s 认证: %s", command, diagnosticTail(clean, 240))
		}
		if !sentPassword && passwordPromptRE.MatchString(clean) {
			if err := s.writeLine(s.enablePassword); err != nil {
				return err
			}
			sentPassword = true
			transcript.Reset()
			continue
		}
		if prompt := s.detectPrompt(clean); prompt != "" {
			if !networkPrivilegePromptAccepted(s.deviceType, prompt) {
				return fmt.Errorf("%s 后未进入预期特权 prompt: %s", command, prompt)
			}
			s.prompt = prompt
			return nil
		}
		if err != nil && !isTimeout(err) {
			return err
		}
	}
	return fmt.Errorf("等待 %s 特权 prompt 超时；最后响应: %s", command, diagnosticTail(transcript.String(), 240))
}

func requestNetworkPTY(session *ssh.Session) error {
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	var last error
	for _, term := range []string{"vt100", "xterm", "vt220", "ansi", "dumb"} {
		if err := session.RequestPty(term, 80, 240, modes); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return last
}

func (s *SSHNetworkExecutor) disablePaging() {
	for _, command := range networkPagingCommands(s.deviceType, s.prompt) {
		_, _ = s.runCLICommand(command, nil, minDuration(s.commandTimeout, 8*time.Second))
	}
}

func (s *SSHNetworkExecutor) ReadTargetFile(path string) ([]byte, error) {
	if s.deviceType != "linux" {
		return nil, fmt.Errorf("%s 网络设备 CLI 不支持通用文件读取；请执行厂商 display/show 命令", s.deviceType)
	}
	out, err := s.Run("cat " + shellQuotePath(path))
	return []byte(out), err
}

func (s *SSHNetworkExecutor) ListTargetDir(path string) ([]string, error) {
	if s.deviceType != "linux" {
		return nil, fmt.Errorf("%s 网络设备 CLI 不支持通用目录枚举", s.deviceType)
	}
	out, err := s.Run("ls -1 " + shellQuotePath(path))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

func (s *SSHNetworkExecutor) NetworkDeviceInfo() NetworkDeviceInfo {
	banner := s.banner
	if len(banner) > 2048 {
		banner = banner[len(banner)-2048:]
	}
	return NetworkDeviceInfo{Vendor: s.deviceType, Prompt: s.prompt, Banner: banner}
}

func (s *SSHNetworkExecutor) IsRemote() bool { return true }
func (s *SSHNetworkExecutor) Mode() string   { return "ssh" }

func (s *SSHNetworkExecutor) Close() {
	s.closeOnce.Do(func() {
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.session != nil {
			_ = s.session.Close()
		}
		if s.client != nil {
			_ = s.client.Close()
		}
	})
}

var _ Executor = (*SSHNetworkExecutor)(nil)
var _ StreamingExecutor = (*SSHNetworkExecutor)(nil)
var _ NetworkDeviceReporter = (*SSHNetworkExecutor)(nil)
