package builtin

import (
	"fmt"
	"strings"
	"sync"

	"ai-edr/internal/config"
	"ai-edr/internal/executor"
)

func FleetInventory(rt Runtime, selector string) (string, error) {
	targets := executor.MatchTargets(config.GlobalConfig.Targets, selector)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Fleet Inventory\n", rt.tag()))
	if len(targets) == 0 {
		b.WriteString(fmt.Sprintf("(selector=%s 无匹配目标；请检查选择器或 config.yaml targets)\n", emptyDefault(selector, "all")))
		return b.String(), nil
	}
	b.WriteString(fmt.Sprintf("selector=%s matched=%d\n", emptyDefault(selector, "all"), len(targets)))
	for _, t := range targets {
		b.WriteString(fmt.Sprintf("- %s protocol=%s host=%s user=%s tags=%s\n",
			executor.TargetDisplayName(t), t.Protocol, t.Host, t.User, strings.Join(t.Tags, ",")))
	}
	b.WriteString(fmt.Sprintf("下一步: inventory 只确认了 %d 个目标，尚未采集健康/日志证据；同项批量巡检请继续调用 fleet_exec(selector=%q, command=<只读巡检命令>)，文件取证用 fleet_file。\n",
		len(targets), emptyDefault(selector, "all")))
	return b.String(), nil
}

func FleetExec(rt Runtime, selector, command string, concurrency int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command 必填")
	}
	targets := executor.MatchTargets(config.GlobalConfig.Targets, selector)
	if len(targets) == 0 {
		return "", fmt.Errorf("无匹配目标: %s", selector)
	}
	results := executor.RunFleet(config.GlobalConfig.Targets, selector, command, concurrency)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Fleet Exec selector=%s command=%s\n", rt.tag(), emptyDefault(selector, "all"), command))
	b.WriteString(executor.FormatFleetResults(results))
	return b.String(), nil
}

func FleetFile(rt Runtime, selector, action, remotePath, localPath string, concurrency int) (string, error) {
	targets := executor.MatchTargets(config.GlobalConfig.Targets, selector)
	if len(targets) == 0 {
		return "", fmt.Errorf("无匹配目标: %s", selector)
	}
	if action != "ls" && action != "read" && action != "download" && action != "upload" {
		return "", fmt.Errorf("fleet_file action 仅支持 ls|read|download|upload")
	}
	if strings.TrimSpace(remotePath) == "" || ((action == "download" || action == "upload") && strings.TrimSpace(localPath) == "") {
		return "", fmt.Errorf("remote_path 必填；download/upload 还需 local_path")
	}
	if concurrency <= 0 {
		concurrency = 5
	}
	if concurrency > 20 {
		concurrency = 20
	}
	type fileResult struct {
		out string
		err error
	}
	results := make([]fileResult, len(targets))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target config.TargetConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			destination := localPath
			if action == "download" && len(targets) > 1 {
				// The index makes names unique even when inventory names repeat.
				destination = fmt.Sprintf("%s.%03d-%s", localPath, i+1, fleetFileLabel(target.Name))
			}
			results[i].out, results[i].err = executor.FleetFile(target, action, remotePath, destination)
		}(i, target)
	}
	wg.Wait()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Fleet File action=%s selector=%s\n", rt.tag(), action, emptyDefault(selector, "all")))
	succeeded := 0
	for i, t := range targets {
		out, err := results[i].out, results[i].err
		name := executor.TargetDisplayName(t)
		if err != nil {
			b.WriteString(fmt.Sprintf("[ERR] %s: %v\n\n", name, err))
			continue
		}
		succeeded++
		b.WriteString(fmt.Sprintf("[OK] %s\n%s\n\n", name, truncate(out, 4000)))
	}
	b.WriteString(fmt.Sprintf("汇总: %d/%d 成功", succeeded, len(targets)))
	return b.String(), nil
}

func fleetFileLabel(name string) string {
	if name == "" {
		return "target"
	}
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 48 {
			break
		}
	}
	return b.String()
}
