package executor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"ai-edr/internal/config"
)

const (
	telnetIAC  = byte(255)
	telnetDONT = byte(254)
	telnetDO   = byte(253)
	telnetWONT = byte(252)
	telnetWILL = byte(251)
	telnetSB   = byte(250)
	telnetSE   = byte(240)
)

var (
	usernamePromptRE = regexp.MustCompile(`(?i)(?:^|[\r\n])\s*(?:login|username|user\s*name)\s*[:：]\s*$`)
	passwordPromptRE = regexp.MustCompile(`(?i)(?:^|[\r\n])\s*password\s*[:：]?\s*$`)
	authFailureRE    = regexp.MustCompile(`(?i)(authentication\s+fail|password\s+authentication\s+fail|login\s+incorrect|invalid\s+(?:password|user)|wrong\s+password|incorrect\s+password|super\s+password[^\r\n]*(?:fail|incorrect|wrong|not\s+set)|access\s+denied|permission\s+denied|认证失败|密码错误|用户名错误)`)
	ansiEscapeRE     = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	networkPromptRE  = regexp.MustCompile(`^(?:<[^<>\r\n]{1,160}>|\[[^\[\]\r\n]{1,160}\]|[A-Za-z0-9_.:/()~@-]{1,160}(?:\([^\r\n()]{1,80}\))?[>#])$`)
	shellPromptRE    = regexp.MustCompile(`^[^\r\n]{1,160}[$#%]$`)
	pagerRE          = regexp.MustCompile(`(?i)(?:----?\s*more\s*----?|--more--|\bmore:\s*$|press\s+(?:any\s+key|space|enter|return)\s+to\s+continue|press\s+q\s+to\s+break)`)
	pagerEnterRE     = regexp.MustCompile(`(?i)press\s+(?:enter|return)\s+to\s+continue`)
)

type NetworkDeviceInfo struct {
	Vendor string
	Prompt string
	Banner string
}

type NetworkDeviceReporter interface {
	NetworkDeviceInfo() NetworkDeviceInfo
}

type TelnetExecutor struct {
	conn           net.Conn
	reader         *bufio.Reader
	promptSpec     string
	promptRE       *regexp.Regexp
	authPromptRE   *regexp.Regexp
	prompt         string
	deviceType     string
	banner         string
	enablePassword string
	loginTimeout   time.Duration
	commandTimeout time.Duration
	mu             sync.Mutex
}

func newTelnetExecutor(cfg config.Config) (*TelnetExecutor, error) {
	host := normalizeHostPort(cfg.TelnetHost, "23")
	connectTimeout := secondsOrDefault(cfg.TelnetConnectTimeoutSec, 10)
	conn, err := config.ControllerDialTimeout("tcp", host, connectTimeout)
	if err != nil {
		return nil, fmt.Errorf("建立 Telnet TCP 连接失败 (%s, timeout=%s): %w", host, connectTimeout, err)
	}
	t := &TelnetExecutor{
		conn: conn, reader: bufio.NewReader(conn), promptSpec: strings.TrimSpace(cfg.TelnetPrompt),
		deviceType: normalizeDeviceType(cfg.TelnetDeviceType), enablePassword: cfg.TelnetEnablePassword,
		loginTimeout:   secondsOrDefault(cfg.TelnetLoginTimeoutSec, 20),
		commandTimeout: secondsOrDefault(cfg.TelnetCommandTimeoutSec, max(config.GlobalConfig.EffectiveSSHTimeout(), 30)),
	}
	if t.promptSpec != "" {
		t.promptRE, err = compilePromptSpec(t.promptSpec)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("telnet_prompt 无效: %w", err)
		}
	}
	if raw := strings.TrimSpace(cfg.TelnetAuthPromptRegex); raw != "" {
		t.authPromptRE, err = regexp.Compile("(?i)" + raw)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("telnet_auth_prompt_regex 无效: %w", err)
		}
	}
	if err := t.login(cfg.TelnetUser, cfg.TelnetPassword); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("Telnet 登录失败 [%s]: %w", host, err)
	}
	t.deviceType = detectTelnetDeviceType(t.deviceType, t.banner, t.prompt)
	if err := t.enterPrivilegedMode(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("Telnet enable 失败: %w", err)
	}
	t.disablePaging()
	return t, nil
}

func secondsOrDefault(value, fallback int) time.Duration {
	if value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}

func normalizeDeviceType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "auto"
	case "vrp", "usg", "huawei":
		return "huawei"
	case "comware", "secpath", "dptech", "h3c":
		return "h3c"
	case "rgos", "red-giant", "redgiant", "ruijie":
		return "ruijie"
	case "ios", "ios-xe", "nxos", "nx-os", "ios-xr", "cisco":
		return "cisco"
	case "asa", "cisco-asa", "pix":
		return "asa"
	case "junos", "screenos", "netscreen", "juniper":
		return "juniper"
	case "fortios", "fortigate", "fortinet":
		return "fortinet"
	case "panos", "palo-alto", "paloalto":
		return "paloalto"
	case "stoneos", "hillstone":
		return "hillstone"
	case "sangfor":
		return "sangfor"
	case "gaia", "checkpoint":
		return "checkpoint"
	case "venustech", "topsec", "leadsec":
		return "generic"
	case "linux", "generic":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// networkPrivilegeCommand returns the vendor CLI privilege elevation command.
// Huawei VRP and H3C Comware use `super` (defaulting to the highest configured
// level), while Cisco IOS and Ruijie RGOS use `enable`. Entering configuration
// views such as system-view/configure terminal is deliberately not automatic.
func networkPrivilegeCommand(deviceType string) string {
	switch normalizeDeviceType(deviceType) {
	case "huawei", "h3c":
		return "super"
	case "ruijie", "cisco", "asa", "hillstone", "sangfor":
		return "enable"
	default:
		return ""
	}
}

func networkPrivilegeAlreadyActive(deviceType, prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	switch normalizeDeviceType(deviceType) {
	case "huawei", "h3c":
		// A bracketed VRP/Comware prompt is already inside a configuration view.
		return strings.HasPrefix(prompt, "[")
	case "ruijie", "cisco", "asa", "hillstone", "sangfor":
		return strings.HasSuffix(prompt, "#")
	default:
		return true
	}
}

func networkPrivilegePromptAccepted(deviceType, prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return false
	}
	switch normalizeDeviceType(deviceType) {
	case "huawei", "h3c":
		// VRP/Comware `super` raises the user level without changing <hostname>.
		return networkPromptRE.MatchString(prompt)
	case "ruijie", "cisco", "asa", "hillstone", "sangfor":
		return strings.HasSuffix(prompt, "#")
	default:
		return false
	}
}

func networkPagerAdvance(text string) (string, bool) {
	if !pagerRE.MatchString(text) {
		return "", false
	}
	if pagerEnterRE.MatchString(text) {
		return "\r", true
	}
	return " ", true
}

func compilePromptSpec(spec string) (*regexp.Regexp, error) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(strings.ToLower(spec), "regex:") {
		return regexp.Compile("(?m)" + strings.TrimSpace(spec[len("regex:"):]) + `\s*$`)
	}
	return regexp.Compile(`(?m)` + regexp.QuoteMeta(spec) + `\s*$`)
}

// login is an authentication state machine. Authentication prompts and the
// post-login command prompt are deliberately separate; telnet_prompt is only
// used to recognize a completed login and later command boundaries.
func (t *TelnetExecutor) login(user, pass string) error {
	deadline := time.Now().Add(t.loginTimeout)
	var transcript strings.Builder
	var fullTranscript strings.Builder
	sentUser, sentPass := false, false
	for time.Now().Before(deadline) {
		chunk, err := t.readChunk(deadline)
		if len(chunk) > 0 {
			transcript.WriteString(chunk)
			fullTranscript.WriteString(chunk)
		}
		clean := cleanTerminalText(transcript.String())
		if authFailureRE.MatchString(clean) {
			return fmt.Errorf("设备拒绝认证；最后响应: %s", diagnosticTail(clean, 320))
		}
		if !sentUser && usernamePromptRE.MatchString(clean) {
			if strings.TrimSpace(user) == "" {
				return errors.New("设备请求用户名，但 telnet_user 为空")
			}
			if err := t.writeLine(user); err != nil {
				return fmt.Errorf("发送用户名失败: %w", err)
			}
			sentUser = true
			transcript.Reset()
			continue
		}
		passwordRequested := passwordPromptRE.MatchString(clean) || (t.authPromptRE != nil && t.authPromptRE.MatchString(clean))
		if !sentPass && passwordRequested {
			if err := t.writeLine(pass); err != nil {
				return fmt.Errorf("发送密码失败: %w", err)
			}
			sentPass = true
			transcript.Reset()
			continue
		}
		if prompt := t.detectPrompt(clean); prompt != "" {
			t.prompt = prompt
			t.banner = strings.TrimSpace(cleanTerminalText(fullTranscript.String()))
			return nil
		}
		if err != nil && !isTimeout(err) {
			return fmt.Errorf("读取认证响应失败: %w；最后响应: %s", err, diagnosticTail(clean, 320))
		}
	}
	clean := cleanTerminalText(transcript.String())
	stage := "等待登录提示"
	if sentUser && !sentPass {
		stage = "用户名已发送，等待密码提示"
	} else if sentPass {
		stage = "密码已发送，等待命令提示符"
	}
	return fmt.Errorf("%s超时 (%s)；请检查 telnet_auth_prompt_regex/telnet_prompt，最后响应: %s", stage, t.loginTimeout, diagnosticTail(clean, 400))
}

func (t *TelnetExecutor) writeLine(value string) error {
	_, err := io.WriteString(t.conn, value+"\r\n")
	return err
}

func (t *TelnetExecutor) Run(cmd string) (string, error) {
	return t.RunWithStreaming(cmd, nil)
}

func (t *TelnetExecutor) RunWithStreaming(cmd string, onLine func(string)) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "(空命令)", nil
	}
	if strings.HasPrefix(cmd, "local_run ") {
		return (&LocalExecutor{}).RunWithStreaming(strings.TrimPrefix(cmd, "local_run "), onLine)
	}
	if CommandUsesSudo(cmd) && t.deviceType == "linux" {
		cmd = ForceNonInteractiveSudo(cmd)
	}
	return t.runCLICommand(cmd, onLine, t.commandTimeout)
}

func (t *TelnetExecutor) runCLICommand(cmd string, onLine func(string), timeout time.Duration) (string, error) {
	if err := t.writeLine(cmd); err != nil {
		return "", fmt.Errorf("发送 Telnet 命令失败: %w", err)
	}
	deadline := time.Now().Add(timeout)
	var raw strings.Builder
	matchTail := ""
	maxOutput := effectiveMaxOutputBytes()
	truncated := false
	lastStreamed := 0
	for time.Now().Before(deadline) {
		chunk, err := t.readChunk(deadline)
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
			if _, writeErr := io.WriteString(t.conn, advance); writeErr != nil {
				return finalizeTelnetOutput(clean, cmd, t.prompt, truncated, maxOutput, false), writeErr
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
		if prompt := t.detectPrompt(cleanTail); prompt != "" && commandResponseHasBoundary(cleanTail, cmd, prompt) {
			t.prompt = prompt
			return finalizeTelnetOutput(clean, cmd, prompt, truncated, maxOutput, true), nil
		}
		if err != nil && !isTimeout(err) {
			return finalizeTelnetOutput(clean, cmd, t.prompt, truncated, maxOutput, false), fmt.Errorf("Telnet 命令读取失败: %w", err)
		}
	}
	partial := finalizeTelnetOutput(cleanTerminalText(raw.String()), cmd, t.prompt, truncated, maxOutput, false)
	return partial, fmt.Errorf("等待设备命令提示符超时 (%s, device=%s, prompt=%q)；已保留部分输出", timeout, t.deviceType, t.prompt)
}

func finalizeTelnetOutput(output, cmd, prompt string, truncated bool, maxOutput int, promptSeen bool) string {
	out := cleanCommandOutput(output, cmd, prompt)
	if truncated {
		out += fmt.Sprintf("\n\n[DeepSentry完整性] transport_drained=%t, prompt_seen=%t, output_truncated=true；设备输出超过 %d 字节，仅保留前缀。请用厂商 include/exclude/section/count 或快诊 focus 缩小范围。", promptSeen, promptSeen, maxOutput)
	} else if promptSeen {
		if notice := networkCLIProjectionNotice(cmd); notice != "" {
			out += "\n\n" + notice
		}
	}
	return out
}

func networkCLIProjectionNotice(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	filterAt := strings.Index(lower, " | ")
	if filterAt < 0 {
		return ""
	}
	filter := lower[filterAt:]
	if !strings.Contains(filter, " include ") && !strings.Contains(filter, " exclude ") && !strings.Contains(filter, " begin ") && !strings.Contains(filter, " section ") {
		return ""
	}
	base := strings.TrimSpace(command[:filterAt])
	notice := "[DeepSentry完整性] transport=complete, prompt_seen=true, output_truncated=false, projection=filtered；当前是交换机按正则返回的匹配行，不是传输截断。多数 VRP/Comware 过滤区分大小写，未匹配字段不代表设备没有该数据。"
	if strings.HasPrefix(lower, "display interface ") && !strings.Contains(lower, " interface brief") {
		notice += fmt.Sprintf("单接口完整诊断建议直接执行 %q，避免丢失状态上下文。", base)
	}
	return notice
}

func completedLines(text string, offset *int) []string {
	if *offset >= len(text) {
		return nil
	}
	segment := text[*offset:]
	last := strings.LastIndex(segment, "\n")
	if last < 0 {
		return nil
	}
	*offset += last + 1
	return strings.Split(strings.TrimSuffix(segment[:last+1], "\n"), "\n")
}

func commandResponseHasBoundary(output, cmd, prompt string) bool {
	return strings.TrimSpace(output) != "" && strings.TrimSpace(prompt) != ""
}

func (t *TelnetExecutor) detectPrompt(text string) string {
	line := lastTerminalLine(text)
	if line == "" {
		return ""
	}
	if t.promptRE != nil && t.promptRE.MatchString(strings.TrimSpace(text)) {
		return line
	}
	if t.prompt != "" && line == t.prompt {
		return line
	}
	if networkPromptRE.MatchString(line) || shellPromptRE.MatchString(line) {
		return line
	}
	return ""
}

func lastTerminalLine(text string) string {
	text = strings.ReplaceAll(cleanTerminalText(text), "\r", "\n")
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func cleanCommandOutput(output, cmd, prompt string) string {
	output = cleanTerminalText(output)
	output = pagerRE.ReplaceAllString(output, "")
	lines := strings.Split(strings.ReplaceAll(output, "\r", ""), "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(cleaned) == 0 && (trimmed == cmd || strings.HasSuffix(trimmed, cmd)) {
			continue
		}
		if trimmed == prompt {
			continue
		}
		cleaned = append(cleaned, line)
	}
	out := strings.TrimSpace(strings.Join(cleaned, "\n"))
	if out == "" {
		return "(执行成功，无输出)"
	}
	return out
}

func cleanTerminalText(text string) string {
	text = stripTelnetIAC(text)
	text = ansiEscapeRE.ReplaceAllString(text, "")
	for strings.Contains(text, "\b") {
		idx := strings.Index(text, "\b")
		if idx <= 0 {
			text = text[idx+1:]
			continue
		}
		text = text[:idx-1] + text[idx+1:]
	}
	return text
}

func diagnosticTail(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\x00", ""))
	if text == "" {
		return "(无响应)"
	}
	if len(text) > limit {
		text = text[len(text)-limit:]
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")
}

func (t *TelnetExecutor) readChunk(deadline time.Time) (string, error) {
	if err := t.conn.SetReadDeadline(minTime(deadline, time.Now().Add(250*time.Millisecond))); err != nil {
		return "", err
	}
	var b strings.Builder
	for b.Len() < 4096 {
		value, err := t.readDataByte()
		if err != nil {
			return b.String(), err
		}
		b.WriteByte(value)
		if t.reader.Buffered() == 0 {
			return b.String(), nil
		}
	}
	return b.String(), nil
}

func (t *TelnetExecutor) readDataByte() (byte, error) {
	for {
		value, err := t.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if value != telnetIAC {
			return value, nil
		}
		command, err := t.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch command {
		case telnetIAC:
			return telnetIAC, nil
		case telnetDO, telnetDONT, telnetWILL, telnetWONT:
			option, readErr := t.reader.ReadByte()
			if readErr != nil {
				return 0, readErr
			}
			reply := telnetWONT
			if command == telnetWILL || command == telnetWONT {
				reply = telnetDONT
			}
			_, _ = t.conn.Write([]byte{telnetIAC, reply, option})
		case telnetSB:
			previous := byte(0)
			for {
				current, readErr := t.reader.ReadByte()
				if readErr != nil {
					return 0, readErr
				}
				if previous == telnetIAC && current == telnetSE {
					break
				}
				previous = current
			}
		default:
			// Single-byte Telnet control command; consume it and continue.
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func detectTelnetDeviceType(configured, banner, prompt string) string {
	configured = normalizeDeviceType(configured)
	if configured != "auto" {
		return configured
	}
	lower := strings.ToLower(banner + "\n" + prompt)
	switch {
	case strings.Contains(lower, "h3c"), strings.Contains(lower, "comware"), strings.Contains(lower, "secpath"), strings.Contains(lower, "dptech"), strings.Contains(lower, "迪普"):
		return "h3c"
	case strings.Contains(lower, "huawei"), strings.Contains(lower, "versatile routing platform"), strings.Contains(lower, "vrp"), strings.Contains(lower, "huawei usg"):
		return "huawei"
	case strings.Contains(lower, "ruijie"), strings.Contains(lower, "rgos"), strings.Contains(lower, "red-giant"):
		return "ruijie"
	case strings.Contains(lower, "cisco adaptive security"), strings.Contains(lower, "cisco asa"), strings.Contains(lower, "pix"):
		return "asa"
	case strings.Contains(lower, "cisco ios"), strings.Contains(lower, "cisco internetwork"), strings.Contains(lower, "nx-os"), strings.Contains(lower, "ios-xe"):
		return "cisco"
	case strings.Contains(lower, "junos"), strings.Contains(lower, "juniper"), strings.Contains(lower, "screenos"), strings.Contains(lower, "netscreen"):
		return "juniper"
	case strings.Contains(lower, "fortigate"), strings.Contains(lower, "fortios"), strings.Contains(lower, "fortinet"):
		return "fortinet"
	case strings.Contains(lower, "palo alto"), strings.Contains(lower, "pan-os"), strings.Contains(lower, "panos"):
		return "paloalto"
	case strings.Contains(lower, "hillstone"), strings.Contains(lower, "stoneos"), strings.Contains(lower, "山石"):
		return "hillstone"
	case strings.Contains(lower, "sangfor"), strings.Contains(lower, "深信服"):
		return "sangfor"
	case strings.Contains(lower, "checkpoint"), strings.Contains(lower, "check point"), strings.Contains(lower, "gaia"):
		return "checkpoint"
	case strings.HasSuffix(strings.TrimSpace(prompt), "$"), strings.Contains(lower, "linux"), strings.Contains(lower, "ubuntu"):
		return "linux"
	case networkPromptRE.MatchString(strings.TrimSpace(prompt)):
		return "generic"
	default:
		return "linux"
	}
}

func (t *TelnetExecutor) enterPrivilegedMode() error {
	command := networkPrivilegeCommand(t.deviceType)
	if strings.TrimSpace(t.enablePassword) == "" || command == "" || networkPrivilegeAlreadyActive(t.deviceType, t.prompt) {
		return nil
	}
	if err := t.writeLine(command); err != nil {
		return err
	}
	deadline := time.Now().Add(minDuration(t.loginTimeout, 15*time.Second))
	var transcript strings.Builder
	sentPassword := false
	for time.Now().Before(deadline) {
		chunk, err := t.readChunk(deadline)
		transcript.WriteString(chunk)
		clean := cleanTerminalText(transcript.String())
		if authFailureRE.MatchString(clean) {
			return fmt.Errorf("设备拒绝 %s 认证: %s", command, diagnosticTail(clean, 240))
		}
		if !sentPassword && passwordPromptRE.MatchString(clean) {
			if err := t.writeLine(t.enablePassword); err != nil {
				return err
			}
			sentPassword = true
			transcript.Reset()
			continue
		}
		if prompt := t.detectPrompt(clean); prompt != "" {
			if !networkPrivilegePromptAccepted(t.deviceType, prompt) {
				return fmt.Errorf("%s 后未进入预期特权 prompt: %s", command, prompt)
			}
			t.prompt = prompt
			return nil
		}
		if err != nil && !isTimeout(err) {
			return err
		}
	}
	return fmt.Errorf("等待 %s 特权 prompt 超时；最后响应: %s", command, diagnosticTail(transcript.String(), 240))
}

func networkPagingCommands(deviceType, prompt string) []string {
	switch normalizeDeviceType(deviceType) {
	case "huawei", "h3c":
		return []string{"screen-length 0 temporary"}
	case "ruijie", "cisco", "hillstone", "sangfor":
		return []string{"terminal length 0"}
	case "asa":
		return []string{"terminal pager 0"}
	case "juniper":
		return []string{"set cli screen-length 0"}
	case "paloalto":
		return []string{"set cli pager off"}
	case "checkpoint":
		return []string{"set clienv rows 0"}
	case "generic":
		if strings.HasPrefix(strings.TrimSpace(prompt), "<") || strings.HasPrefix(strings.TrimSpace(prompt), "[") {
			return []string{"screen-length 0 temporary"}
		}
		if strings.HasSuffix(strings.TrimSpace(prompt), "#") || strings.HasSuffix(strings.TrimSpace(prompt), ">") {
			return []string{"terminal length 0"}
		}
	}
	return nil
}

func (t *TelnetExecutor) disablePaging() {
	for _, command := range networkPagingCommands(t.deviceType, t.prompt) {
		_, _ = t.runCLICommand(command, nil, minDuration(t.commandTimeout, 8*time.Second))
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (t *TelnetExecutor) ReadTargetFile(path string) ([]byte, error) {
	if t.deviceType != "linux" {
		return nil, fmt.Errorf("%s 网络设备 CLI 不支持通用文件读取；请执行厂商 display/show 命令采集配置和日志", t.deviceType)
	}
	out, err := t.Run("cat " + shellQuotePath(path))
	return []byte(out), err
}

func (t *TelnetExecutor) ListTargetDir(path string) ([]string, error) {
	if t.deviceType != "linux" {
		return nil, fmt.Errorf("%s 网络设备 CLI 不支持通用目录枚举", t.deviceType)
	}
	out, err := t.Run("ls -1 " + shellQuotePath(path))
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

func (t *TelnetExecutor) NetworkDeviceInfo() NetworkDeviceInfo {
	banner := t.banner
	if len(banner) > 2048 {
		banner = banner[len(banner)-2048:]
	}
	return NetworkDeviceInfo{Vendor: t.deviceType, Prompt: t.prompt, Banner: banner}
}

func (t *TelnetExecutor) IsRemote() bool { return true }
func (t *TelnetExecutor) Mode() string   { return "telnet" }
func (t *TelnetExecutor) Close()         { _ = t.conn.Close() }

func stripTelnetIAC(s string) string {
	var out []byte
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if b[i] != telnetIAC {
			out = append(out, b[i])
			continue
		}
		if i+1 >= len(b) {
			break
		}
		command := b[i+1]
		if command == telnetIAC {
			out = append(out, telnetIAC)
			i++
			continue
		}
		if command == telnetDO || command == telnetDONT || command == telnetWILL || command == telnetWONT {
			i += 2
			continue
		}
		if command == telnetSB {
			i += 2
			for i+1 < len(b) && !(b[i] == telnetIAC && b[i+1] == telnetSE) {
				i++
			}
			i++
			continue
		}
		i++
	}
	return string(out)
}

func normalizeHostPort(host, defPort string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if strings.Count(host, ":") > 1 {
		return net.JoinHostPort(strings.Trim(host, "[]"), defPort)
	}
	if strings.Contains(host, ":") {
		return host
	}
	return net.JoinHostPort(host, defPort)
}
