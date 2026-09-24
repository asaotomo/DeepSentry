package builtin

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// /proc/net 解析器 — 纯 Go 实现，适用于 Linux 极简系统（通过 SFTP/直读获取数据）

var tcpStates = map[string]string{
	"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV",
	"04": "FIN_WAIT1", "05": "FIN_WAIT2", "06": "TIME_WAIT",
	"07": "CLOSE", "08": "CLOSE_WAIT", "09": "LAST_ACK",
	"0A": "LISTEN", "0B": "CLOSING",
}

type socketEntry struct {
	Proto      string
	LocalIP    string
	LocalPort  int
	RemoteIP   string
	RemotePort int
	State      string
	Inode      string
}

type routeEntry struct {
	Iface   string
	Dest    string
	Gateway string
	Flags   string
	Mask    string
}

type arpEntry struct {
	IP        string
	HWType    string
	Flags     string
	HWAddress string
	Device    string
}

func parseHexIPPort(hexAddr string) (string, int, error) {
	parts := strings.Split(hexAddr, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid addr: %s", hexAddr)
	}
	ip := parseHexIPv4LE(parts[0])
	port64, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil {
		return "", 0, err
	}
	return ip, int(port64), nil
}

func parseHexIPv4LE(hex string) string {
	hex = strings.TrimSpace(hex)
	if len(hex) > 8 {
		hex = hex[len(hex)-8:]
	}
	hex = strings.Repeat("0", 8-len(hex)) + hex
	o0, _ := strconv.ParseUint(hex[6:8], 16, 8)
	o1, _ := strconv.ParseUint(hex[4:6], 16, 8)
	o2, _ := strconv.ParseUint(hex[2:4], 16, 8)
	o3, _ := strconv.ParseUint(hex[0:2], 16, 8)
	return net.IP([]byte{byte(o0), byte(o1), byte(o2), byte(o3)}).String()
}

func parseHexIPv4(hex string) string {
	return parseHexIPv4LE(hex)
}

func parseProcNet(content, proto string) ([]socketEntry, error) {
	var entries []socketEntry
	sc := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0
	for sc.Scan() {
		lineNum++
		if lineNum == 1 {
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		localIP, localPort, err := parseHexIPPort(fields[1])
		if err != nil {
			continue
		}
		remoteIP, remotePort, err := parseHexIPPort(fields[2])
		if err != nil {
			continue
		}
		state := tcpStates[strings.ToUpper(fields[3])]
		if state == "" {
			state = fields[3]
		}
		entries = append(entries, socketEntry{
			Proto: proto, LocalIP: localIP, LocalPort: localPort,
			RemoteIP: remoteIP, RemotePort: remotePort, State: state, Inode: fields[9],
		})
	}
	return entries, sc.Err()
}

func formatSocket(e socketEntry) string {
	if e.RemotePort == 0 && (e.RemoteIP == "0.0.0.0" || e.RemoteIP == "::") {
		return fmt.Sprintf("%-5s %-21s %-15s %s", e.Proto, fmt.Sprintf("%s:%d", e.LocalIP, e.LocalPort), e.State, e.Inode)
	}
	return fmt.Sprintf("%-5s %-21s -> %-21s %-15s %s",
		e.Proto, fmt.Sprintf("%s:%d", e.LocalIP, e.LocalPort),
		fmt.Sprintf("%s:%d", e.RemoteIP, e.RemotePort), e.State, e.Inode)
}

func readProcNetTCP() ([]socketEntry, error) {
	data, err := readTarget("/proc/net/tcp")
	if err != nil {
		return nil, err
	}
	return parseProcNet(string(data), "tcp")
}

func readProcNetUDP() ([]socketEntry, error) {
	data, err := readTarget("/proc/net/udp")
	if err != nil {
		return nil, err
	}
	entries, err := parseProcNet(string(data), "udp")
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].State = "UDP"
	}
	return entries, nil
}

func readAllSockets() ([]socketEntry, error) {
	tcp, err1 := readProcNetTCP()
	udp, err2 := readProcNetUDP()
	if err1 != nil && err2 != nil {
		return nil, fmt.Errorf("无法读取 /proc/net: tcp=%v udp=%v", err1, err2)
	}
	return append(tcp, udp...), nil
}

func parseProcRouteFixed(content string) ([]routeEntry, error) {
	var routes []routeEntry
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		routes = append(routes, routeEntry{
			Iface: fields[0], Dest: parseHexIPv4(fields[1]), Gateway: parseHexIPv4(fields[2]),
			Flags: fields[3], Mask: parseHexIPv4(fields[7]),
		})
	}
	return routes, nil
}

func parseProcARP(content string) ([]arpEntry, error) {
	var entries []arpEntry
	sc := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0
	for sc.Scan() {
		lineNum++
		if lineNum == 1 {
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 {
			continue
		}
		entries = append(entries, arpEntry{
			IP: fields[0], HWType: fields[1], Flags: fields[2],
			HWAddress: fields[3], Device: fields[5],
		})
	}
	return entries, sc.Err()
}
