package config

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResolveStartupProxy(t *testing.T) {
	tests := []struct {
		http, socks string
		want        string
		wantErr     bool
	}{
		{http: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{socks: "socks5://127.0.0.1:1080", want: "socks5://127.0.0.1:1080"},
		{socks: "socks5h://user:pass@127.0.0.1:1080", want: "socks5h://user:pass@127.0.0.1:1080"},
		{http: "socks5://127.0.0.1:1080", wantErr: true},
		{socks: "http://127.0.0.1:8080", wantErr: true},
		{http: "http://127.0.0.1", wantErr: true},
		{http: "http://127.0.0.1:8080/path", wantErr: true},
		{http: "http://127.0.0.1:8080", socks: "socks5://127.0.0.1:1080", wantErr: true},
	}
	for _, tc := range tests {
		got, err := ResolveStartupProxy(tc.http, tc.socks)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ResolveStartupProxy(%q,%q) accepted, got %q", tc.http, tc.socks, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("ResolveStartupProxy(%q,%q)=%q,%v want %q", tc.http, tc.socks, got, err, tc.want)
		}
	}
	if got := ControllerProxySummary("http://secret:password@127.0.0.1:8080"); got != "http://127.0.0.1:8080" {
		t.Fatalf("proxy summary leaked or changed endpoint: %q", got)
	}
}

func TestControllerDialTimeoutViaHTTPConnect(t *testing.T) {
	target := listenProxyTestTarget(t, "http-connect-ok")
	defer target.Close()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()
	proxyErr := make(chan error, 1)
	go func() {
		client, err := proxyListener.Accept()
		if err != nil {
			proxyErr <- err
			return
		}
		defer client.Close()
		reader := bufio.NewReader(client)
		req, err := http.ReadRequest(reader)
		if err != nil {
			proxyErr <- err
			return
		}
		if req.Method != http.MethodConnect || req.Host != target.Addr().String() {
			proxyErr <- fmt.Errorf("unexpected CONNECT %s %s", req.Method, req.Host)
			return
		}
		if req.Header.Get("Proxy-Authorization") == "" {
			proxyErr <- fmt.Errorf("missing proxy authorization")
			return
		}
		upstream, err := net.Dial("tcp", req.Host)
		if err != nil {
			proxyErr <- err
			return
		}
		defer upstream.Close()
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			proxyErr <- err
			return
		}
		_, err = io.Copy(client, upstream)
		proxyErr <- err
	}()

	withControllerProxy(t, "http://user:pass@"+proxyListener.Addr().String())
	conn, err := ControllerDialTimeout("tcp", target.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyPayload(t, conn, "http-connect-ok")
	if err := <-proxyErr; err != nil {
		t.Fatal(err)
	}
}

func TestControllerDialTimeoutViaSOCKS5(t *testing.T) {
	target := listenProxyTestTarget(t, "socks5-ok")
	defer target.Close()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()
	proxyErr := make(chan error, 1)
	go func() {
		client, err := proxyListener.Accept()
		if err != nil {
			proxyErr <- err
			return
		}
		defer client.Close()
		reader := bufio.NewReader(client)
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(reader, greeting); err != nil {
			proxyErr <- err
			return
		}
		methods := make([]byte, int(greeting[1]))
		if _, err := io.ReadFull(reader, methods); err != nil {
			proxyErr <- err
			return
		}
		if _, err := client.Write([]byte{5, 0}); err != nil {
			proxyErr <- err
			return
		}
		host, port, err := readSOCKS5Target(reader)
		if err != nil {
			proxyErr <- err
			return
		}
		upstream, err := net.Dial("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
		if err != nil {
			proxyErr <- err
			return
		}
		defer upstream.Close()
		if _, err := client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
			proxyErr <- err
			return
		}
		_, err = io.Copy(client, upstream)
		proxyErr <- err
	}()

	withControllerProxy(t, "socks5://"+proxyListener.Addr().String())
	conn, err := ControllerDialTimeout("tcp", target.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyPayload(t, conn, "socks5-ok")
	if err := <-proxyErr; err != nil {
		t.Fatal(err)
	}
}

func listenProxyTestTarget(t *testing.T, payload string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_, _ = io.WriteString(conn, payload)
			_ = conn.Close()
		}
	}()
	return listener
}

func withControllerProxy(t *testing.T, raw string) {
	t.Helper()
	old := GlobalConfig
	GlobalConfig.ControllerProxy = raw
	t.Cleanup(func() { GlobalConfig = old })
}

func assertProxyPayload(t *testing.T, conn net.Conn, want string) {
	t.Helper()
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil || string(data) != want {
		t.Fatalf("proxy payload=%q err=%v want %q", data, err, want)
	}
}

func readSOCKS5Target(reader *bufio.Reader) (string, uint16, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", 0, err
	}
	if header[0] != 5 || header[1] != 1 {
		return "", 0, fmt.Errorf("unexpected SOCKS5 request: %v", header)
	}
	var host string
	switch header[3] {
	case 1:
		value := make([]byte, 4)
		_, _ = io.ReadFull(reader, value)
		host = net.IP(value).String()
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return "", 0, err
		}
		value := make([]byte, int(length))
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", 0, err
		}
		host = string(value)
	case 4:
		value := make([]byte, 16)
		_, _ = io.ReadFull(reader, value)
		host = net.IP(value).String()
	default:
		return "", 0, fmt.Errorf("unsupported SOCKS5 atyp: %d", header[3])
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(host), binary.BigEndian.Uint16(portBytes), nil
}
