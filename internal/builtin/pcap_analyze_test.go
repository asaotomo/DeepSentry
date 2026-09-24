package builtin

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

func TestPcapAnalyzeExtractsDNSHTTPAndSMB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traffic.pcap")
	var buf bytes.Buffer
	w := pcapgo.NewWriter(&buf)
	if err := w.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatal(err)
	}
	writeTestPacket(t, w, buildDNSPacket(t))
	writeTestPacket(t, w, buildTCPPacket(t, 12345, 80, []byte("GET /admin HTTP/1.1\r\nHost: example.test\r\n\r\n")))
	smb := append([]byte{0xfe, 'S', 'M', 'B'}, append(make([]byte, 20), []byte("NTLMSSP\x00\x01\x00\x00\x00")...)...)
	netbiosSMB := append([]byte{0, byte(len(smb) >> 16), byte(len(smb) >> 8), byte(len(smb))}, smb...)
	writeTestPacket(t, w, buildTCPPacket(t, 44444, 445, netbiosSMB))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	withLocalExecutor(t)

	out, err := PcapAnalyze(Runtime{}, path, "summary", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gopacket PCAP Analyze", "DNS", "example.test", "Host=example.test", "SMB2/3", "NTLMSSP NEGOTIATE"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestParseTLSSNI(t *testing.T) {
	payload := buildTLSClientHello("deep.example")
	sni, ok := parseTLSSNI(payload)
	if !ok || sni != "deep.example" {
		t.Fatalf("SNI = %q ok=%v", sni, ok)
	}
}

func TestPcapModeNormalize(t *testing.T) {
	if got := normalizePcapMode("ntlm"); got != "smb" {
		t.Fatalf("ntlm mode = %q", got)
	}
	if got := normalizePcapMode("wat"); got != "summary" {
		t.Fatalf("unknown mode = %q", got)
	}
}

func writeTestPacket(t *testing.T, w *pcapgo.Writer, data []byte) {
	t.Helper()
	if err := w.WritePacket(gopacket.CaptureInfo{
		Timestamp:     time.Unix(1, 0),
		CaptureLength: len(data),
		Length:        len(data),
	}, data); err != nil {
		t.Fatal(err)
	}
}

func buildDNSPacket(t *testing.T) []byte {
	eth := testEthernetLayer()
	ip := testIPv4Layer(layers.IPProtocolUDP)
	udp := &layers.UDP{SrcPort: 53530, DstPort: 53}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	dns := &layers.DNS{
		ID:           1,
		QR:           false,
		OpCode:       layers.DNSOpCodeQuery,
		RD:           true,
		Questions:    []layers.DNSQuestion{{Name: []byte("example.test"), Type: layers.DNSTypeA, Class: layers.DNSClassIN}},
		QDCount:      1,
		ResponseCode: layers.DNSResponseCodeNoErr,
	}
	return serializeLayers(t, eth, ip, udp, dns)
}

func buildTCPPacket(t *testing.T, src, dst layers.TCPPort, payload []byte) []byte {
	eth := testEthernetLayer()
	ip := testIPv4Layer(layers.IPProtocolTCP)
	tcp := &layers.TCP{SrcPort: src, DstPort: dst, Seq: 1, ACK: true, PSH: true, Window: 14600}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	return serializeLayers(t, eth, ip, tcp, gopacket.Payload(payload))
}

func testEthernetLayer() *layers.Ethernet {
	return &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0, 1, 2, 3, 4, 5},
		DstMAC:       net.HardwareAddr{6, 7, 8, 9, 10, 11},
		EthernetType: layers.EthernetTypeIPv4,
	}
}

func testIPv4Layer(proto layers.IPProtocol) *layers.IPv4 {
	return &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{10, 0, 0, 1},
		DstIP:    net.IP{10, 0, 0, 2},
		Protocol: proto,
	}
}

func serializeLayers(t *testing.T, layersIn ...gopacket.SerializableLayer) []byte {
	t.Helper()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf, opts, layersIn...); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildTLSClientHello(host string) []byte {
	var hs bytes.Buffer
	hs.Write([]byte{0x03, 0x03})
	hs.Write(make([]byte, 32))
	hs.WriteByte(0)
	hs.Write([]byte{0x00, 0x02, 0x13, 0x01})
	hs.Write([]byte{0x01, 0x00})

	name := []byte(host)
	serverName := make([]byte, 0, 5+len(name))
	serverName = binary.BigEndian.AppendUint16(serverName, uint16(len(name)+3))
	serverName = append(serverName, 0x00)
	serverName = binary.BigEndian.AppendUint16(serverName, uint16(len(name)))
	serverName = append(serverName, name...)

	var exts bytes.Buffer
	exts.Write(binary.BigEndian.AppendUint16(nil, 0))
	exts.Write(binary.BigEndian.AppendUint16(nil, uint16(len(serverName))))
	exts.Write(serverName)
	hs.Write(binary.BigEndian.AppendUint16(nil, uint16(exts.Len())))
	hs.Write(exts.Bytes())

	body := hs.Bytes()
	var hello bytes.Buffer
	hello.WriteByte(1)
	hello.Write([]byte{byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))})
	hello.Write(body)

	recordBody := hello.Bytes()
	var rec bytes.Buffer
	rec.Write([]byte{22, 0x03, 0x03})
	rec.Write(binary.BigEndian.AppendUint16(nil, uint16(len(recordBody))))
	rec.Write(recordBody)
	return rec.Bytes()
}
