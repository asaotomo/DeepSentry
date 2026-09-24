//go:build !windows
// +build !windows

package main

import (
	"os/exec"
	"syscall"
)

// Mac/Linux 原生支持 ANSI 颜色，不需要做任何事
func enableWindowsANSI() {
	// 空函数，仅为了兼容接口
}

func useSurveyConsoleInput() func() { return func() {} }

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
