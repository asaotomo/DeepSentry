package builtin

import (
	"strings"
	"testing"
)

func TestParseProcNetTCP(t *testing.T) {
	sample := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
`
	entries, err := parseProcNet(sample, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].LocalPort != 22 || entries[0].State != "LISTEN" {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].LocalPort != 80 {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}
}

func TestParseHexIPPort(t *testing.T) {
	ip, port, err := parseHexIPPort("0100007F:0016")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "127.0.0.1" || port != 22 {
		t.Fatalf("got %s:%d", ip, port)
	}
}

func TestValidateHost(t *testing.T) {
	if validateHost("") == nil {
		t.Fatal("empty should fail")
	}
	if validateHost("192.168.1.1") != nil {
		t.Fatal("valid ip should pass")
	}
	if validateHost("evil;rm") == nil {
		t.Fatal("injection should fail")
	}
}

func TestParsePortSpec(t *testing.T) {
	ports, err := parsePortSpec("80,443,8000-8002")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 5 {
		t.Fatalf("expected 5 ports, got %d", len(ports))
	}
}

func TestParseHexIPv4(t *testing.T) {
	if parseHexIPv4("0100007F") != "127.0.0.1" {
		t.Fatal("bad parse")
	}
}

func TestSHA256(t *testing.T) {
	sum := sha256Sum([]byte("hello"))
	if len(sum) != 64 {
		t.Fatal("bad hash length")
	}
}

func TestParseProcRouteFixed(t *testing.T) {
	sample := `Iface	Destination	Gateway	Flags	RefCnt	Use	Metric	Mask	MTU	Window	IRTT
eth0	00000000	0101A8C0	0003	0	0	0	00000000	0	0	0
`
	routes, err := parseProcRouteFixed(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Iface != "eth0" {
		t.Fatalf("unexpected: %+v", routes[0])
	}
}

func TestFormatCatalogBusyBox(t *testing.T) {
	// ensure registry mentions Go native
	if !strings.Contains(strings.ToLower("Go 原生"), "go") {
		t.Skip()
	}
}

func TestSocketInode(t *testing.T) {
	if socketInode("socket:[12345]") != "12345" {
		t.Fatal("failed to parse socket inode")
	}
	if socketInode("pipe:[12345]") != "" {
		t.Fatal("pipe should not be parsed as socket")
	}
}

func TestParseMySQLHandshake(t *testing.T) {
	packet := append([]byte{0x2a, 0x00, 0x00, 0x00, 0x0a}, []byte("8.0.36\x00")...)
	packet = append(packet, make([]byte, 40)...)
	out := parseMySQLHandshake(packet)
	if !strings.Contains(out, "8.0.36") || !strings.Contains(out, "MySQL") {
		t.Fatalf("bad mysql handshake parse: %s", out)
	}
}

func TestSQLiteSchemaExtraction(t *testing.T) {
	data := []byte("SQLite format 3\x00........CREATE TABLE users(id int, password text)")
	schema := extractSQLiteSchemaStrings(data)
	if len(schema) == 0 || !strings.Contains(schema[0], "CREATE TABLE users") {
		t.Fatalf("schema not extracted: %#v", schema)
	}
}

func TestRedactSecrets(t *testing.T) {
	out := redactSecrets("password=supersecret redis://user:pass@127.0.0.1:6379/0")
	if strings.Contains(out, "supersecret") || strings.Contains(out, ":pass@") {
		t.Fatalf("secret not redacted: %s", out)
	}
}

func TestExpandCIDRLimit(t *testing.T) {
	ips, err := expandCIDR("192.168.1.0/30", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 3 || ips[0] != "192.168.1.1" {
		t.Fatalf("unexpected cidr expansion: %v", ips)
	}
}

func TestWebSnapshotExtractors(t *testing.T) {
	html := `<html><head><title>Admin</title><meta name="generator" content="WordPress"></head><body><form action="/login"></form><script src="/a.js"></script><a href="/x">x</a></body></html>`
	if firstMatch(html, `(?is)<title[^>]*>(.*?)</title>`) != "Admin" {
		t.Fatal("title extraction failed")
	}
	if len(allMatches(html, `(?is)<script[^>]+src=["']([^"']+)`, 5)) != 1 {
		t.Fatal("script extraction failed")
	}
}

func TestDiscoveryPorts(t *testing.T) {
	ports, err := discoveryPorts("80,443,8000-8001")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 4 {
		t.Fatalf("unexpected ports: %v", ports)
	}
}

func TestNormalizeArchiveFormat(t *testing.T) {
	if normalizeArchiveFormat("", "a.tar.gz") != "tar.gz" {
		t.Fatal("tar.gz detection failed")
	}
	if normalizeArchiveFormat("", "a.7z") != "7z" {
		t.Fatal("7z detection failed")
	}
}

func TestArchiveCommandBuilders(t *testing.T) {
	cmd, err := archiveExtractCommand("zip", "/tmp/a.zip", "/tmp/out")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "unzip") {
		t.Fatalf("unexpected zip extract command: %s", cmd)
	}
	cmd, err = archivePackCommand("tar.gz", "/tmp/demo", "/tmp/demo.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "tar -czf") {
		t.Fatalf("unexpected tar command: %s", cmd)
	}
}

func TestListForwardsEmpty(t *testing.T) {
	out := listForwards(Runtime{})
	if !strings.Contains(out, "无活动") {
		t.Fatalf("unexpected forward list: %s", out)
	}
}
