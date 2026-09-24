package builtin

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSocks5ProxyConnectNoAuth(t *testing.T) {
	target := startTestTCPResponder(t)
	out, err := Socks5Proxy(Runtime{}, "start", "127.0.0.1", "0", "", "", false)
	if err != nil {
		if isListenUnavailable(err) {
			t.Skipf("local proxy listen unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	listen := findForwardListen(t, "socks5_proxy")
	t.Cleanup(func() { _, _ = stopForward(Runtime{}, listenPort(listen)) })
	if !strings.Contains(out, "SOCKS5") {
		t.Fatalf("unexpected start output: %s", out)
	}

	conn, err := net.DialTimeout("tcp", listen, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string([]byte{0x05, 0x00}) {
		t.Fatalf("bad method reply: %v", buf)
	}
	host, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)
	ip := net.ParseIP(host).To4()
	req := []byte{0x05, 0x01, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("bad connect reply: %v", reply)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "pong" {
		t.Fatalf("got %q, want pong", got)
	}
}

func TestSocks5ProxyBindAndUDP(t *testing.T) {
	out, err := Socks5Proxy(Runtime{}, "start", "127.0.0.1", "0", "", "", false)
	if err != nil {
		if isListenUnavailable(err) {
			t.Skipf("local proxy listen unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	if !strings.Contains(out, "BIND") || !strings.Contains(out, "UDP") {
		t.Fatalf("start output=%s", out)
	}
	listen := findForwardListen(t, "socks5_proxy")
	t.Cleanup(func() { _, _ = stopForward(Runtime{}, listenPort(listen)) })

	t.Run("bind", func(t *testing.T) {
		conn := dialSocks(t, listen)
		defer conn.Close()
		bound := socksCommand(t, conn, 0x02, net.IPv4(0, 0, 0, 0), 0)
		peer, err := net.DialTimeout("tcp", bound, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer peer.Close()
		reply := make([]byte, 10)
		if _, err := io.ReadFull(conn, reply); err != nil {
			t.Fatal(err)
		}
		if reply[1] != 0x00 {
			t.Fatalf("second bind reply: %v", reply)
		}
		if _, err := peer.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 4)
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
		if string(got) != "ping" {
			t.Fatalf("bind payload=%q", got)
		}
	})

	t.Run("udp", func(t *testing.T) {
		echo := startTestUDPEcho(t)
		conn := dialSocks(t, listen)
		defer conn.Close()
		relayAddr := socksCommand(t, conn, 0x03, net.IPv4(0, 0, 0, 0), 0)
		client, err := net.DialUDP("udp", nil, mustUDPAddr(t, relayAddr))
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		host, portStr, _ := net.SplitHostPort(echo)
		port, _ := strconv.Atoi(portStr)
		ip := net.ParseIP(host).To4()
		packet := []byte{0x00, 0x00, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port), 'p', 'i', 'n', 'g'}
		if _, err := client.Write(packet); err != nil {
			t.Fatal(err)
		}
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 64)
		n, err := client.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if n < 10 || string(buf[n-4:n]) != "ping" {
			t.Fatalf("udp reply=%q", buf[:n])
		}
	})
}

func TestHTTPProxyForwardsUserTraffic(t *testing.T) {
	out, err := HTTPProxy(Runtime{}, "start", "127.0.0.1", "0", "", "", false)
	if err != nil {
		if isListenUnavailable(err) {
			t.Skipf("local proxy listen unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	if !strings.Contains(out, "HTTP 代理已启动") || strings.Contains(out, "-proxy") && !strings.Contains(out, "不是控制端出站") {
		t.Fatalf("start output=%s", out)
	}
	listen := findForwardListen(t, "http_proxy")
	t.Cleanup(func() { _, _ = stopForward(Runtime{}, listenPort(listen)) })

	target := startTestTCPResponder(t)
	conn, err := net.DialTimeout("tcp", listen, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("connect status=%q", status)
	}
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "pong" {
		t.Fatalf("connect payload=%q", got)
	}

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("page-ok"))
	}))
	t.Cleanup(page.Close)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: listen})}, Timeout: 3 * time.Second}
	resp, err := client.Get(page.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "page-ok" {
		t.Fatalf("http via proxy status=%d body=%q", resp.StatusCode, body)
	}
}

func TestHTTPProxyRejectsLanAndMissingAuth(t *testing.T) {
	if _, err := HTTPProxy(Runtime{}, "start", "0.0.0.0", "0", "", "", false); err == nil || !strings.Contains(err.Error(), "allow_lan") {
		t.Fatalf("expected allow_lan error, got %v", err)
	}
	out, err := HTTPProxy(Runtime{}, "start", "127.0.0.1", "0", "user", "secret", false)
	if err != nil {
		if isListenUnavailable(err) {
			t.Skipf("local proxy listen unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	if !strings.Contains(out, "auth: basic") {
		t.Fatalf("auth output=%s", out)
	}
	listen := findForwardListen(t, "http_proxy")
	t.Cleanup(func() { _, _ = stopForward(Runtime{}, listenPort(listen)) })
	resp, err := http.Get("http://" + listen + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestSocks5ProxyRejectsLanListenByDefault(t *testing.T) {
	_, err := Socks5Proxy(Runtime{}, "start", "0.0.0.0", "0", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "allow_lan") {
		t.Fatalf("expected allow_lan error, got %v", err)
	}
}

func TestTCPForwardSupportsEphemeralListenPort(t *testing.T) {
	target := startTestTCPResponder(t)
	host, port, _ := net.SplitHostPort(target)
	out, err := TCPForward(Runtime{}, "start", "127.0.0.1", "0", host, port)
	if err != nil {
		if isListenUnavailable(err) {
			t.Skipf("local forward listen unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	listen := findForwardListen(t, "tcp_forward")
	t.Cleanup(func() { _, _ = stopForward(Runtime{}, listenPort(listen)) })
	if !strings.Contains(out, "TCP 转发已启动") || strings.Contains(out, "listen: 127.0.0.1:0") {
		t.Fatalf("unexpected forward output: %s", out)
	}
	conn, err := net.DialTimeout("tcp", listen, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "pong" {
		t.Fatalf("got %q, want pong", got)
	}
}

func dialSocks(t *testing.T, listen string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", listen, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		t.Fatalf("bad method reply: %v", buf)
	}
	return conn
}

func socksCommand(t *testing.T, conn net.Conn, cmd byte, ip net.IP, port int) string {
	t.Helper()
	ip = ip.To4()
	req := []byte{0x05, cmd, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("command %d reply: %v", cmd, reply)
	}
	gotPort := int(reply[8])<<8 | int(reply[9])
	return net.JoinHostPort(net.IP(reply[4:8]).String(), strconv.Itoa(gotPort))
}

func startTestUDPEcho(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		if isListenUnavailable(err) {
			t.Skipf("local udp listen unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(append([]byte(nil), buf[:n]...), addr)
		}
	}()
	return conn.LocalAddr().String()
}

func mustUDPAddr(t *testing.T, addr string) *net.UDPAddr {
	t.Helper()
	parsed, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func startTestTCPResponder(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if isListenUnavailable(err) {
			t.Skipf("local listen unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4)
				_, _ = io.ReadFull(c, buf)
				_, _ = c.Write([]byte("pong"))
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func findForwardListen(t *testing.T, kind string) string {
	t.Helper()
	forwardManager.Lock()
	defer forwardManager.Unlock()
	for _, s := range forwardManager.items {
		if s.Kind == kind {
			return s.Listen
		}
	}
	t.Fatalf("no forward session kind=%s; sessions=%v", kind, forwardManager.items)
	return ""
}

func listenPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}

func isListenUnavailable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "operation not permitted") || strings.Contains(s, "permission denied")
}
