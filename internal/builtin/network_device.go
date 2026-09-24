package builtin

import (
	"fmt"
	"strings"

	"ai-edr/internal/executor"
)

// NetworkDeviceBaseline collects a deterministic, read-only evidence set for
// common switch/router CLIs. Unsupported commands do not abort the workflow.
func NetworkDeviceBaseline(rt Runtime, requestedProfile string) (string, error) {
	return runNetworkDeviceCommands(rt, requestedProfile, "full", networkBaselineCommands)
}

// NetworkDeviceDiagnose is the competition/incident fast path. It keeps the
// evidence set deterministic while avoiding a full seven-command baseline
// when the question is clearly about interfaces, routing, L2, or logs.
func NetworkDeviceDiagnose(rt Runtime, requestedProfile, focus string) (string, error) {
	focus = strings.ToLower(strings.TrimSpace(focus))
	if focus == "" {
		focus = "overview"
	}
	return runNetworkDeviceCommands(rt, requestedProfile, focus, func(profile string) []string {
		return networkDiagnosticCommands(profile, focus)
	})
}

func runNetworkDeviceCommands(rt Runtime, requestedProfile, focus string, commandsFor func(string) []string) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	profile := strings.ToLower(strings.TrimSpace(requestedProfile))
	prompt := ""
	if reporter, ok := rt.Exec.(executor.NetworkDeviceReporter); ok {
		info := reporter.NetworkDeviceInfo()
		if profile == "" || profile == "auto" {
			profile = info.Vendor
		}
		prompt = info.Prompt
	}
	if profile == "" || profile == "auto" || profile == "generic" {
		if strings.HasPrefix(prompt, "<") || strings.HasPrefix(prompt, "[") {
			profile = "huawei"
		} else {
			profile = "ruijie"
		}
	}
	commands := commandsFor(profile)
	if len(commands) == 0 {
		return "", fmt.Errorf("不支持的网络设备 profile/focus: %q/%q", profile, focus)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【网络设备只读证据】profile=%s focus=%s prompt=%s\n", profile, focus, prompt)
	for _, command := range commands {
		fmt.Fprintf(&b, "\n=== %s ===\n", command)
		out, err := rt.Exec.Run(command)
		if err != nil {
			fmt.Fprintf(&b, "采集失败: %v\n", err)
			if strings.TrimSpace(out) != "" {
				b.WriteString(out + "\n")
			}
			continue
		}
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteByte('\n')
		}
	}
	return truncate(b.String(), 180000), nil
}

func networkDiagnosticCommands(profile, focus string) []string {
	profile = strings.ToLower(strings.TrimSpace(profile))
	focus = strings.ToLower(strings.TrimSpace(focus))
	display := profile == "huawei" || profile == "h3c"
	iosLike := profile == "ruijie" || profile == "cisco" || profile == "asa" || profile == "hillstone" || profile == "sangfor"
	switch focus {
	case "overview":
		if display {
			return []string{"display version", "display device", "display interface brief"}
		}
		if profile == "ruijie" {
			return []string{"show version", "show device", "show interfaces status"}
		}
		if profile == "cisco" || profile == "asa" {
			return []string{"show version", "show inventory", "show interfaces status"}
		}
		if extra := extraVendorCommands(profile, focus); extra != nil {
			return extra
		}
	case "interfaces":
		if display {
			return []string{"display interface brief", "display ip interface brief", "display interface"}
		}
		if iosLike {
			return []string{"show interfaces status", "show ip interface brief", "show interfaces"}
		}
		if extra := extraVendorCommands(profile, focus); extra != nil {
			return extra
		}
	case "routing":
		if display {
			return []string{"display ip routing-table", "display ospf peer brief", "display ip interface brief"}
		}
		if iosLike {
			return []string{"show ip route", "show ip ospf neighbor", "show ip interface brief"}
		}
		if extra := extraVendorCommands(profile, focus); extra != nil {
			return extra
		}
	case "l2":
		if display {
			return []string{"display vlan summary", "display stp brief", "display mac-address"}
		}
		if iosLike {
			return []string{"show vlan brief", "show spanning-tree summary", "show mac address-table dynamic"}
		}
		if extra := extraVendorCommands(profile, focus); extra != nil {
			return extra
		}
	case "logs":
		if display {
			return []string{"display logbuffer", "display alarm active"}
		}
		if iosLike {
			return []string{"show logging", "show clock"}
		}
		if extra := extraVendorCommands(profile, focus); extra != nil {
			return extra
		}
	case "full":
		return networkBaselineCommands(profile)
	}
	return nil
}

func networkBaselineCommands(profile string) []string {
	switch strings.ToLower(profile) {
	case "huawei", "h3c":
		return []string{"display version", "display device", "display interface brief", "display ip interface brief", "display ip routing-table", "display stp brief", "display logbuffer"}
	case "ruijie":
		return []string{"show version", "show device", "show interfaces status", "show ip interface brief", "show ip route", "show spanning-tree summary", "show logging"}
	case "cisco", "asa":
		return []string{"show version", "show inventory", "show interfaces status", "show ip interface brief", "show ip route", "show spanning-tree summary", "show logging"}
	case "hillstone", "sangfor":
		return []string{"show version", "show interface", "show ip route", "show logging"}
	case "juniper":
		return []string{"show version", "show chassis hardware", "show interfaces terse", "show route", "show log messages"}
	case "fortinet":
		return []string{"get system status", "get system interface", "get router info routing-table all"}
	case "paloalto":
		return []string{"show system info", "show interface all", "show routing route"}
	case "checkpoint":
		return []string{"show version all", "show interface all", "show route"}
	default:
		return nil
	}
}

func extraVendorCommands(profile, focus string) []string {
	switch profile {
	case "juniper":
		switch focus {
		case "overview":
			return []string{"show version", "show chassis hardware", "show interfaces terse"}
		case "interfaces":
			return []string{"show interfaces terse", "show interfaces"}
		case "routing":
			return []string{"show route", "show ospf neighbor"}
		case "l2":
			return []string{"show vlans", "show spanning-tree"}
		case "logs":
			return []string{"show log messages"}
		}
	case "fortinet":
		switch focus {
		case "overview":
			return []string{"get system status", "get system interface"}
		case "interfaces":
			return []string{"get system interface"}
		case "routing":
			return []string{"get router info routing-table all"}
		case "logs":
			return []string{"get log event"}
		}
	case "paloalto":
		switch focus {
		case "overview":
			return []string{"show system info", "show interface all"}
		case "interfaces":
			return []string{"show interface all"}
		case "routing":
			return []string{"show routing route"}
		case "logs":
			return []string{"show log system"}
		}
	case "checkpoint":
		switch focus {
		case "overview":
			return []string{"show version all", "show interface all"}
		case "interfaces":
			return []string{"show interface all"}
		case "routing":
			return []string{"show route"}
		}
	}
	return nil
}
