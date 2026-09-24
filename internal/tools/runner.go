package tools

import (
	"ai-edr/internal/builtin"
	"ai-edr/internal/executor"
	"fmt"
)

// Run 执行内置工具（Go 原生 BusyBox 实现，不依赖目标系统 nmap/ping 等 CLI）
func Run(name string, args map[string]string, isWindows bool) (string, string, error) {
	return RunWithExecutor(name, args, isWindows, executor.Current)
}

func RunWithExecutor(name string, args map[string]string, isWindows bool, ex executor.Executor) (string, string, error) {
	t, ok := Get(name)
	if !ok {
		names := ListNames()
		return "", "", fmt.Errorf("未知工具: %s。可用: %s", name, joinNames(names))
	}
	if args == nil {
		args = map[string]string{}
	}

	isRemote := ex != nil && ex.IsRemote()
	rt := builtin.NewRuntime(windowsHint(isWindows), isRemote)
	rt = builtin.WithExecutor(rt, ex)

	out, err := builtin.Run(name, args, rt)
	if err != nil {
		return "", t.RiskLevel, err
	}
	return out, t.RiskLevel, nil
}

func windowsHint(isWindows bool) string {
	if isWindows {
		return "windows"
	}
	return "linux"
}

func joinNames(names []string) string {
	if len(names) > 8 {
		return fmt.Sprintf("%s ... (%d total)", names[:8], len(names))
	}
	s := ""
	for i, n := range names {
		if i > 0 {
			s += ", "
		}
		s += n
	}
	return s
}
