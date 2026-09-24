package executor

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ai-edr/internal/config"
)

type FleetResult struct {
	Target   config.TargetConfig
	Success  bool
	Output   string
	Error    string
	Duration time.Duration
}

type FleetProgress struct {
	Target config.TargetConfig
	Status string
	Output string
	Error  string
}

func MatchTargets(targets []config.TargetConfig, selector string) []config.TargetConfig {
	selector = strings.TrimSpace(selector)
	if selector == "" || strings.EqualFold(selector, "all") {
		return targets
	}
	var out []config.TargetConfig
	for _, t := range targets {
		// Commas narrow a target set; explicit | joins alternative sets.
		// A union for "prod,ssh" can accidentally run a command on every SSH host.
		for _, alternative := range strings.Split(selector, "|") {
			matched := true
			for _, part := range strings.Split(alternative, ",") {
				part = strings.TrimSpace(part)
				if part == "" || !targetMatches(t, part) {
					matched = false
					break
				}
			}
			if matched {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

func targetMatches(t config.TargetConfig, selector string) bool {
	field, value, qualified := strings.Cut(selector, ":")
	if qualified && (field == "name" || field == "host" || field == "protocol" || field == "tag") {
		selector = value
	} else {
		field = ""
	}
	host, _, err := net.SplitHostPort(t.Host)
	if err != nil {
		host = t.Host
	}
	if (field == "" || field == "name") && strings.EqualFold(selector, t.Name) {
		return true
	}
	if (field == "" || field == "host") && (strings.EqualFold(selector, t.Host) || strings.EqualFold(selector, host)) {
		return true
	}
	if (field == "" || field == "protocol") && strings.EqualFold(selector, t.Protocol) {
		return true
	}
	if field == "" || field == "tag" {
		for _, tag := range t.Tags {
			if strings.EqualFold(selector, tag) {
				return true
			}
		}
	}
	return false
}

func RunFleet(targets []config.TargetConfig, selector, command string, concurrency int) []FleetResult {
	return RunFleetWithProgress(targets, selector, command, concurrency, nil)
}

func RunFleetWithProgress(targets []config.TargetConfig, selector, command string, concurrency int, onProgress func(FleetProgress)) []FleetResult {
	return RunFleetWithProgressAndStop(targets, selector, command, concurrency, onProgress, nil)
}

// RunFleetWithProgressAndStop propagates an interactive stop request both to
// queued targets and to executors that are already running.
func RunFleetWithProgressAndStop(targets []config.TargetConfig, selector, command string, concurrency int, onProgress func(FleetProgress), stop <-chan struct{}) []FleetResult {
	selected := MatchTargets(targets, selector)
	if concurrency <= 0 {
		concurrency = 5
	}
	if concurrency > 20 {
		concurrency = 20
	}
	results := make([]FleetResult, len(selected))
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	recordCanceled := func(index int, target config.TargetConfig) {
		res := FleetResult{Target: target, Error: "已按用户请求取消"}
		if onProgress != nil {
			onProgress(FleetProgress{Target: target, Status: "canceled", Error: res.Error})
		}
		results[index] = res
	}
	for index, target := range selected {
		index := index
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			if isStopped(stop) {
				recordCanceled(index, target)
				return
			}
			select {
			case sem <- struct{}{}:
			case <-stop:
				recordCanceled(index, target)
				return
			}
			defer func() { <-sem }()
			if isStopped(stop) {
				recordCanceled(index, target)
				return
			}
			start := time.Now()
			if onProgress != nil {
				onProgress(FleetProgress{Target: target, Status: "running"})
			}
			var out string
			var err error
			if strings.EqualFold(target.Protocol, "ftp") {
				err = fmt.Errorf("FTP 目标不支持 shell 命令，请使用 fleet_file")
			} else {
				out, err = RunOnTargetWithStop(target, command, stop)
			}
			res := FleetResult{Target: target, Success: err == nil, Output: out, Duration: time.Since(start)}
			if err != nil {
				res.Error = err.Error()
			}
			if onProgress != nil {
				status := "ok"
				if err != nil {
					status = "error"
					if isStopped(stop) {
						status = "canceled"
					}
				}
				onProgress(FleetProgress{Target: target, Status: status, Output: out, Error: res.Error})
			}
			results[index] = res
		}()
	}
	wg.Wait()
	return results
}

func RunOnTarget(target config.TargetConfig, command string) (string, error) {
	return RunOnTargetWithStop(target, command, nil)
}

func RunOnTargetWithStop(target config.TargetConfig, command string, stop <-chan struct{}) (string, error) {
	exe, err := NewEphemeralExecutor(target)
	if err != nil {
		return "", err
	}
	defer exe.Close()
	if stoppable, ok := exe.(StoppableStreamingExecutor); ok {
		return stoppable.RunWithStreamingAndStop(command, nil, stop)
	}
	if stop != nil {
		type runResult struct {
			out string
			err error
		}
		done := make(chan runResult, 1)
		go func() {
			out, runErr := exe.Run(command)
			done <- runResult{out: out, err: runErr}
		}()
		select {
		case result := <-done:
			return result.out, result.err
		case <-stop:
			exe.Close()
			result := <-done
			if result.err != nil {
				return result.out, fmt.Errorf("已按用户请求取消: %w", result.err)
			}
			return result.out, fmt.Errorf("已按用户请求取消")
		}
	}
	return exe.Run(command)
}

func isStopped(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func NewEphemeralExecutor(target config.TargetConfig) (Executor, error) {
	cfg := config.GlobalConfig
	switch strings.ToLower(strings.TrimSpace(target.Protocol)) {
	case "ssh":
		cfg.TargetProtocol = "ssh"
		cfg.SSHHost = target.Host
		cfg.SSHUser = firstNonEmpty(target.User, cfg.SSHUser, "root")
		cfg.SSHPassword = target.Password
		cfg.SSHKeyPath = target.KeyPath
		cfg.SSHKeyPassphrase = target.KeyPassphrase
		cfg.SSHPrompt = target.Prompt
		cfg.SSHDeviceType = target.DeviceType
		cfg.SSHEnablePassword = target.EnablePassword
		return newSSHExecutor(cfg)
	case "telnet":
		cfg.TargetProtocol = "telnet"
		cfg.TelnetHost = target.Host
		cfg.TelnetUser = firstNonEmpty(target.User, cfg.TelnetUser, "root")
		cfg.TelnetPassword = target.Password
		cfg.TelnetPrompt = target.Prompt
		cfg.TelnetAuthPromptRegex = target.AuthPromptRegex
		cfg.TelnetDeviceType = target.DeviceType
		cfg.TelnetEnablePassword = target.EnablePassword
		return newTelnetExecutor(cfg)
	case "ftp":
		cfg.TargetProtocol = "ftp"
		cfg.FTPHost = target.Host
		cfg.FTPUser = firstNonEmpty(target.User, cfg.FTPUser, "anonymous")
		cfg.FTPPassword = target.Password
		cfg.FTPTLSMode = target.FTPTLSMode
		cfg.FTPTLSServerName = target.FTPTLSServerName
		cfg.FTPTLSCAFile = target.FTPTLSCAFile
		cfg.FTPTLSInsecureSkipVerify = target.FTPTLSInsecureSkipVerify
		cfg.FTPDataMode = target.FTPDataMode
		cfg.FTPActiveAddress = target.FTPActiveAddress
		return newFTPExecutor(cfg)
	default:
		return nil, fmt.Errorf("目标 %s 协议不支持: %s", target.Name, target.Protocol)
	}
}

func FleetFile(target config.TargetConfig, action, remotePath, localPath string) (string, error) {
	exe, err := NewEphemeralExecutor(target)
	if err != nil {
		return "", err
	}
	defer exe.Close()
	switch action {
	case "ls":
		names, err := exe.ListTargetDir(remotePath)
		return strings.Join(names, "\n"), err
	case "read":
		data, err := exe.ReadTargetFile(remotePath)
		return string(data), err
	case "download":
		return downloadWithExecutor(exe, remotePath, localPath)
	case "upload":
		return uploadWithExecutor(exe, localPath, remotePath)
	default:
		return "", fmt.Errorf("fleet file action 仅支持 ls|read|download|upload")
	}
}

func downloadWithExecutor(exe Executor, remotePath, localPath string) (string, error) {
	switch e := exe.(type) {
	case *SSHExecutor:
		return e.downloadFile(remotePath, localPath)
	case *FTPExecutor:
		return e.downloadFile(remotePath, localPath)
	case *LocalExecutor:
		return copyLocalFile(remotePath, localPath)
	default:
		return "", fmt.Errorf("%s 不支持 download", CurrentModeOf(exe))
	}
}

func uploadWithExecutor(exe Executor, localPath, remotePath string) (string, error) {
	switch e := exe.(type) {
	case *SSHExecutor:
		return e.uploadFile(localPath, remotePath)
	case *FTPExecutor:
		return e.uploadFile(localPath, remotePath)
	case *LocalExecutor:
		return copyLocalFile(localPath, remotePath)
	default:
		return "", fmt.Errorf("%s 不支持 upload", CurrentModeOf(exe))
	}
}

func FormatFleetResults(results []FleetResult) string {
	var b strings.Builder
	success := 0
	for _, r := range results {
		if r.Success {
			success++
		}
	}
	b.WriteString(fmt.Sprintf("Fleet 执行完成: %d/%d 成功\n\n", success, len(results)))
	for _, r := range results {
		status := "OK"
		if !r.Success {
			status = "ERR"
		}
		name := firstNonEmpty(r.Target.Name, r.Target.Host)
		b.WriteString(fmt.Sprintf("[%s] %s (%s %s) %s\n", status, name, r.Target.Protocol, r.Target.Host, r.Duration.Round(time.Millisecond)))
		if r.Error != "" {
			b.WriteString("error: " + r.Error + "\n")
		}
		if strings.TrimSpace(r.Output) != "" {
			b.WriteString(truncateFleetOutput(r.Output, 2000) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncateFleetOutput(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n] + "\n...(单目标输出已截断)..."
}

func TargetDisplayName(t config.TargetConfig) string {
	name := firstNonEmpty(t.Name, filepath.Base(t.Host), t.Host)
	if len(t.Tags) > 0 {
		return fmt.Sprintf("%s [%s]", name, strings.Join(t.Tags, ","))
	}
	return name
}
