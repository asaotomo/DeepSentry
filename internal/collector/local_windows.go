//go:build windows

package collector

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// localWindowsContext reads the local fingerprint through Win32 APIs. The old
// path launched cmd and three cold PowerShell/CIM processes before the TUI
// could draw, which left a black console for many seconds on Windows 10.
func localWindowsContext() (SystemContext, bool) {
	ctx := SystemContext{
		OS:             windowsVersionString(),
		Arch:           windowsArch(),
		Shell:          "cmd.exe / powershell.exe",
		PackageManager: "winget/choco",
	}
	ctx.KernelVersion = ctx.OS
	ctx.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		ctx.Username = strings.ToLower(u.Username)
	}
	ctx.MemoryStatus = windowsMemoryStatus()
	ctx.DiskStatus = windowsDiskStatus()
	ctx.CPUInfo = windowsCPUName()
	ctx.LocalIPs = []string{strings.Join(localIPv4s(), " ")}
	ctx.IsRoot = windows.GetCurrentProcessToken().IsElevated()
	return ctx, true
}

func windowsVersionString() string {
	v := windows.RtlGetVersion()
	if v == nil {
		return "Microsoft Windows (Local)"
	}
	ubr := uint64(0)
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		ubr, _, _ = k.GetIntegerValue("UBR")
		k.Close()
	}
	return fmt.Sprintf("Microsoft Windows [Version %d.%d.%d.%d]", v.MajorVersion, v.MinorVersion, v.BuildNumber, ubr)
}

func windowsArch() string {
	if arch := strings.TrimSpace(os.Getenv("PROCESSOR_ARCHITECTURE")); arch != "" {
		return arch
	}
	return strings.ToUpper(runtime.GOARCH)
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func windowsMemoryStatus() string {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return ""
	}
	return fmt.Sprintf("TotalVisibleMemorySize %d FreePhysicalMemory %d", st.TotalPhys/1024, st.AvailPhys/1024)
}

func windowsDiskStatus() string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return ""
	}
	var rows []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rootPtr, err := windows.UTF16PtrFromString(root)
		if err != nil || windows.GetDriveType(rootPtr) != windows.DRIVE_FIXED {
			continue
		}
		var free, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(rootPtr, &free, &total, &totalFree); err != nil {
			continue
		}
		rows = append(rows, fmt.Sprintf("%s FreeSpace %d Size %d", root[:2], totalFree, total))
	}
	return strings.Join(rows, "; ")
}

func windowsCPUName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	name, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

func localIPv4s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		out = append(out, ipNet.IP.String())
	}
	return out
}
