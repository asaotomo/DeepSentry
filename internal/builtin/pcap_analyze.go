package builtin

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

const (
	maxPcapAnalyzeRead = 64 << 20
	maxPcapEvents      = 80
)

type pcapAnalysis struct {
	Path          string
	LinkType      string
	Packets       int
	Bytes         int
	FirstSeen     time.Time
	LastSeen      time.Time
	Truncated     bool
	Protocols     map[string]int
	Endpoints     map[string]int
	Conversations map[string]int
	DNS           []string
	HTTP          []string
	TLS           []string
	SMB           []string
	PacketLines   []string
	DecodeErrors  int
}

// PcapAnalyze parses offline pcap traffic with gopacket/pcapgo. It is a
// read-only protocol analysis helper, not a live capture or packet injection tool.
func PcapAnalyze(rt Runtime, filePath, mode string, limit int) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	mode = normalizePcapMode(mode)
	if limit <= 0 {
		limit = 5000
	}
	if limit > 50000 {
		limit = 50000
	}
	data, err := readTargetLimited(filePath, maxPcapAnalyzeRead)
	if err != nil {
		return "", err
	}
	a, err := analyzePcapBytes(filePath, data, limit, mode)
	if err != nil {
		return "", err
	}
	return formatPcapAnalysis(rt, a, mode, limit), nil
}

func normalizePcapMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "summary", "auto":
		return "summary"
	case "dns":
		return "dns"
	case "http":
		return "http"
	case "tls":
		return "tls"
	case "smb", "ntlm":
		return "smb"
	case "flows":
		return "flows"
	case "packets":
		return "packets"
	default:
		return "summary"
	}
}

func analyzePcapBytes(filePath string, data []byte, limit int, mode string) (*pcapAnalysis, error) {
	reader, err := pcapgo.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("pcap 解析失败: %w", err)
	}
	a := &pcapAnalysis{
		Path:          filePath,
		LinkType:      reader.LinkType().String(),
		Protocols:     map[string]int{},
		Endpoints:     map[string]int{},
		Conversations: map[string]int{},
	}
	for {
		if a.Packets >= limit {
			a.Truncated = true
			break
		}
		packetData, ci, err := reader.ReadPacketData()
		if err == io.EOF {
			break
		}
		if err != nil {
			a.DecodeErrors++
			continue
		}
		a.Packets++
		a.Bytes += len(packetData)
		if a.FirstSeen.IsZero() || ci.Timestamp.Before(a.FirstSeen) {
			a.FirstSeen = ci.Timestamp
		}
		if ci.Timestamp.After(a.LastSeen) {
			a.LastSeen = ci.Timestamp
		}
		packet := gopacket.NewPacket(packetData, reader.LinkType(), gopacket.DecodeOptions{Lazy: true, NoCopy: true})
		summarizePacket(a, packet, ci, mode)
	}
	return a, nil
}

func summarizePacket(a *pcapAnalysis, packet gopacket.Packet, ci gopacket.CaptureInfo, mode string) {
	for _, layer := range packet.Layers() {
		name := layer.LayerType().String()
		if name != "" {
			a.Protocols[name]++
		}
	}

	src, dst := "", ""
	if ip4Layer := packet.Layer(layers.LayerTypeIPv4); ip4Layer != nil {
		ip := ip4Layer.(*layers.IPv4)
		src, dst = ip.SrcIP.String(), ip.DstIP.String()
	} else if ip6Layer := packet.Layer(layers.LayerTypeIPv6); ip6Layer != nil {
		ip := ip6Layer.(*layers.IPv6)
		src, dst = ip.SrcIP.String(), ip.DstIP.String()
	} else if arpLayer := packet.Layer(layers.LayerTypeARP); arpLayer != nil {
		arp := arpLayer.(*layers.ARP)
		src = net.IP(arp.SourceProtAddress).String()
		dst = net.IP(arp.DstProtAddress).String()
		a.PacketLines = appendLimited(a.PacketLines, fmt.Sprintf("%s ARP %s -> %s", tsShort(ci.Timestamp), src, dst), maxPcapEvents)
	}
	if src != "" {
		a.Endpoints[src]++
	}
	if dst != "" {
		a.Endpoints[dst]++
	}

	proto, sport, dport := "", "", ""
	var payload []byte
	if tcpLayer := packet.Layer(layers.LayerTypeTCP); tcpLayer != nil {
		tcp := tcpLayer.(*layers.TCP)
		proto = "tcp"
		sport, dport = tcp.SrcPort.String(), tcp.DstPort.String()
		payload = tcp.Payload
	} else if udpLayer := packet.Layer(layers.LayerTypeUDP); udpLayer != nil {
		udp := udpLayer.(*layers.UDP)
		proto = "udp"
		sport, dport = udp.SrcPort.String(), udp.DstPort.String()
		payload = udp.Payload
	} else if icmpLayer := packet.Layer(layers.LayerTypeICMPv4); icmpLayer != nil {
		proto = "icmp"
	}
	if proto != "" && src != "" && dst != "" {
		conv := fmt.Sprintf("%s %s:%s -> %s:%s", proto, src, sport, dst, dport)
		a.Conversations[conv]++
		a.PacketLines = appendLimited(a.PacketLines, fmt.Sprintf("%s %s len=%d", tsShort(ci.Timestamp), conv, ci.Length), maxPcapEvents)
	}

	if dnsLayer := packet.Layer(layers.LayerTypeDNS); dnsLayer != nil {
		dns := dnsLayer.(*layers.DNS)
		a.DNS = appendLimited(a.DNS, formatDNSEvent(dns, src, dst), maxPcapEvents)
	}
	if len(payload) > 0 {
		if h := parseHTTPPayload(payload, src, dst, sport, dport); h != "" {
			a.HTTP = appendLimited(a.HTTP, h, maxPcapEvents)
		}
		if sni, ok := parseTLSSNI(payload); ok {
			a.TLS = appendLimited(a.TLS, fmt.Sprintf("%s:%s -> %s:%s SNI=%s", src, sport, dst, dport, sni), maxPcapEvents)
		}
		for _, hint := range parseSMBNTLMHints(payload, src, dst, sport, dport) {
			a.SMB = appendLimited(a.SMB, hint, maxPcapEvents)
		}
	}
}

func formatPcapAnalysis(rt Runtime, a *pcapAnalysis, mode string, limit int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s gopacket PCAP Analyze\nPath: %s\nLinkType: %s\nPackets: %d\nBytes: %d\nMode: %s\nLimit: %d\n",
		rt.tag(), a.Path, a.LinkType, a.Packets, a.Bytes, mode, limit))
	if !a.FirstSeen.IsZero() {
		b.WriteString(fmt.Sprintf("Time: %s -> %s\n", a.FirstSeen.Format(time.RFC3339), a.LastSeen.Format(time.RFC3339)))
	}
	if a.Truncated {
		b.WriteString("Warning: packet limit reached, output is truncated\n")
	}
	if a.DecodeErrors > 0 {
		b.WriteString(fmt.Sprintf("DecodeErrors: %d\n", a.DecodeErrors))
	}

	if mode == "summary" || mode == "flows" || mode == "packets" {
		b.WriteString("\nProtocols:\n")
		writeTopCounts(&b, a.Protocols, 20)
		b.WriteString("\nTop Endpoints:\n")
		writeTopCounts(&b, a.Endpoints, 20)
		b.WriteString("\nTop Conversations:\n")
		writeTopCounts(&b, a.Conversations, 30)
	}
	if mode == "summary" || mode == "dns" {
		writeEventSection(&b, "DNS", a.DNS)
	}
	if mode == "summary" || mode == "http" {
		writeEventSection(&b, "HTTP", a.HTTP)
	}
	if mode == "summary" || mode == "tls" {
		writeEventSection(&b, "TLS SNI", a.TLS)
	}
	if mode == "summary" || mode == "smb" {
		writeEventSection(&b, "SMB/NTLM Hints", a.SMB)
	}
	if mode == "packets" {
		writeEventSection(&b, "Packets", a.PacketLines)
	}
	return truncate(b.String(), 120000)
}

func writeEventSection(b *strings.Builder, title string, items []string) {
	b.WriteString("\n" + title + ":\n")
	if len(items) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, item := range items {
		b.WriteString("  - " + item + "\n")
	}
}

func writeTopCounts(b *strings.Builder, counts map[string]int, n int) {
	items := sortedCountItems(counts)
	if len(items) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	if len(items) > n {
		items = items[:n]
	}
	for _, item := range items {
		b.WriteString(fmt.Sprintf("  - %-55s %d\n", truncateOneLine(item.Key, 55), item.Count))
	}
}

type countItem struct {
	Key   string
	Count int
}

func sortedCountItems(counts map[string]int) []countItem {
	items := make([]countItem, 0, len(counts))
	for k, v := range counts {
		if strings.TrimSpace(k) != "" {
			items = append(items, countItem{Key: k, Count: v})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Key < items[j].Key
		}
		return items[i].Count > items[j].Count
	})
	return items
}

func formatDNSEvent(dns *layers.DNS, src, dst string) string {
	prefix := "Q"
	if dns.QR {
		prefix = "R"
	}
	var parts []string
	for _, q := range dns.Questions {
		parts = append(parts, fmt.Sprintf("%s %s", q.Type.String(), strings.TrimRight(string(q.Name), ".")))
	}
	for _, ans := range dns.Answers {
		switch ans.Type {
		case layers.DNSTypeA, layers.DNSTypeAAAA:
			parts = append(parts, fmt.Sprintf("%s=%s", ans.Name, ans.IP))
		case layers.DNSTypeCNAME, layers.DNSTypeNS:
			parts = append(parts, fmt.Sprintf("%s=%s", ans.Name, ans.CNAME))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("id=%d", dns.ID))
	}
	return fmt.Sprintf("%s %s -> %s %s", prefix, src, dst, strings.Join(parts, "; "))
}

func parseHTTPPayload(payload []byte, src, dst, sport, dport string) string {
	if len(payload) == 0 {
		return ""
	}
	text := strings.ToValidUTF8(string(payload[:min(len(payload), 4096)]), "")
	line := firstLine(text)
	methods := []string{"GET ", "POST ", "HEAD ", "PUT ", "DELETE ", "OPTIONS ", "PATCH ", "TRACE ", "CONNECT "}
	isReq := false
	for _, m := range methods {
		if strings.HasPrefix(line, m) {
			isReq = true
			break
		}
	}
	if !isReq && !strings.HasPrefix(line, "HTTP/") {
		return ""
	}
	host := headerValue(text, "Host")
	if host != "" {
		return fmt.Sprintf("%s:%s -> %s:%s Host=%s %s", src, sport, dst, dport, host, truncateOneLine(line, 180))
	}
	return fmt.Sprintf("%s:%s -> %s:%s %s", src, sport, dst, dport, truncateOneLine(line, 220))
}

func parseTLSSNI(data []byte) (string, bool) {
	if len(data) < 5 || data[0] != 22 {
		return "", false
	}
	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	if recordLen <= 0 || len(data) < 5+min(recordLen, len(data)-5) {
		return "", false
	}
	hs := data[5 : 5+min(recordLen, len(data)-5)]
	if len(hs) < 42 || hs[0] != 1 {
		return "", false
	}
	idx := 4 + 2 + 32
	if idx >= len(hs) {
		return "", false
	}
	sessionLen := int(hs[idx])
	idx += 1 + sessionLen
	if idx+2 > len(hs) {
		return "", false
	}
	cipherLen := int(binary.BigEndian.Uint16(hs[idx : idx+2]))
	idx += 2 + cipherLen
	if idx >= len(hs) {
		return "", false
	}
	compLen := int(hs[idx])
	idx += 1 + compLen
	if idx+2 > len(hs) {
		return "", false
	}
	extLen := int(binary.BigEndian.Uint16(hs[idx : idx+2]))
	idx += 2
	end := idx + extLen
	if end > len(hs) {
		end = len(hs)
	}
	for idx+4 <= end {
		extType := binary.BigEndian.Uint16(hs[idx : idx+2])
		extDataLen := int(binary.BigEndian.Uint16(hs[idx+2 : idx+4]))
		idx += 4
		if idx+extDataLen > end {
			return "", false
		}
		ext := hs[idx : idx+extDataLen]
		idx += extDataLen
		if extType != 0 || len(ext) < 5 {
			continue
		}
		listLen := int(binary.BigEndian.Uint16(ext[0:2]))
		pos := 2
		listEnd := min(len(ext), 2+listLen)
		for pos+3 <= listEnd {
			nameType := ext[pos]
			nameLen := int(binary.BigEndian.Uint16(ext[pos+1 : pos+3]))
			pos += 3
			if pos+nameLen > listEnd {
				break
			}
			if nameType == 0 {
				name := strings.TrimSpace(string(ext[pos : pos+nameLen]))
				if name != "" {
					return name, true
				}
			}
			pos += nameLen
		}
	}
	return "", false
}

func parseSMBNTLMHints(payload []byte, src, dst, sport, dport string) []string {
	var out []string
	prefix := fmt.Sprintf("%s:%s -> %s:%s", src, sport, dst, dport)
	off := smbPayloadOffset(payload)
	if off >= 0 && off+4 <= len(payload) {
		switch {
		case bytes.HasPrefix(payload[off:], []byte{0xfe, 'S', 'M', 'B'}):
			cmd := "unknown"
			if off+14 <= len(payload) {
				cmd = smb2CommandName(binary.LittleEndian.Uint16(payload[off+12 : off+14]))
			}
			out = append(out, fmt.Sprintf("%s SMB2/3 command=%s", prefix, cmd))
		case bytes.HasPrefix(payload[off:], []byte{0xff, 'S', 'M', 'B'}):
			cmd := "unknown"
			if off+5 <= len(payload) {
				cmd = smb1CommandName(payload[off+4])
			}
			out = append(out, fmt.Sprintf("%s SMB1 command=%s", prefix, cmd))
		}
	}
	idx := bytes.Index(payload, []byte("NTLMSSP\x00"))
	if idx >= 0 && idx+12 <= len(payload) {
		msgType := binary.LittleEndian.Uint32(payload[idx+8 : idx+12])
		out = append(out, fmt.Sprintf("%s NTLMSSP %s", prefix, ntlmMessageName(msgType)))
	}
	return out
}

func smbPayloadOffset(payload []byte) int {
	if len(payload) >= 4 && (bytes.HasPrefix(payload, []byte{0xfe, 'S', 'M', 'B'}) || bytes.HasPrefix(payload, []byte{0xff, 'S', 'M', 'B'})) {
		return 0
	}
	if len(payload) >= 8 && payload[0] == 0x00 {
		n := int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
		if n > 0 && 4+n <= len(payload) &&
			(bytes.HasPrefix(payload[4:], []byte{0xfe, 'S', 'M', 'B'}) || bytes.HasPrefix(payload[4:], []byte{0xff, 'S', 'M', 'B'})) {
			return 4
		}
	}
	return -1
}

func smb2CommandName(cmd uint16) string {
	names := map[uint16]string{
		0: "NEGOTIATE", 1: "SESSION_SETUP", 2: "LOGOFF", 3: "TREE_CONNECT", 4: "TREE_DISCONNECT",
		5: "CREATE", 6: "CLOSE", 7: "FLUSH", 8: "READ", 9: "WRITE", 10: "LOCK", 11: "IOCTL",
		12: "CANCEL", 13: "ECHO", 14: "QUERY_DIRECTORY", 15: "CHANGE_NOTIFY", 16: "QUERY_INFO",
		17: "SET_INFO", 18: "OPLOCK_BREAK",
	}
	if s := names[cmd]; s != "" {
		return s
	}
	return strconv.Itoa(int(cmd))
}

func smb1CommandName(cmd byte) string {
	names := map[byte]string{0x72: "NEGOTIATE", 0x73: "SESSION_SETUP_ANDX", 0x75: "TREE_CONNECT_ANDX", 0x2e: "READ_ANDX", 0x2f: "WRITE_ANDX"}
	if s := names[cmd]; s != "" {
		return s
	}
	return fmt.Sprintf("0x%02x", cmd)
}

func ntlmMessageName(t uint32) string {
	switch t {
	case 1:
		return "NEGOTIATE"
	case 2:
		return "CHALLENGE"
	case 3:
		return "AUTHENTICATE"
	default:
		return fmt.Sprintf("type=%d", t)
	}
}

func firstLine(s string) string {
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}

func headerValue(s, name string) string {
	name = strings.ToLower(name)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.ToLower(strings.TrimSpace(k)) == name {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func appendLimited(items []string, item string, limit int) []string {
	item = strings.TrimSpace(item)
	if item == "" || len(items) >= limit {
		return items
	}
	return append(items, item)
}

func tsShort(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("15:04:05.000")
}
