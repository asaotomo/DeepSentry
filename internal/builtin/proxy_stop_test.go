package builtin

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestStopClosesEstablishedProxyConnections(t *testing.T) {
	for _, kind := range []string{"http_proxy", "tcp_forward", "socks5_proxy"} {
		t.Run(kind, func(t *testing.T) {
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				c, e := target.Accept()
				if e == nil {
					accepted <- c
					_, _ = io.Copy(c, c)
					c.Close()
				}
			}()
			host, port, _ := net.SplitHostPort(target.Addr().String())
			switch kind {
			case "http_proxy":
				_, err = HTTPProxy(Runtime{}, "start", "127.0.0.1", "0", "", "", false)
			case "tcp_forward":
				_, err = TCPForward(Runtime{}, "start", "127.0.0.1", "0", host, port)
			case "socks5_proxy":
				_, err = Socks5Proxy(Runtime{}, "start", "127.0.0.1", "0", "", "", false)
			}
			if err != nil {
				t.Fatal(err)
			}
			listen := findForwardListen(t, kind)
			defer func() { _, _ = stopForward(Runtime{}, listenPort(listen)) }()
			var c net.Conn
			if kind == "socks5_proxy" {
				c = dialSocks(t, listen)
				p, _ := strconv.Atoi(port)
				socksCommand(t, c, 1, net.ParseIP(host), p)
			} else {
				c, err = net.DialTimeout("tcp", listen, time.Second)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			reader := bufio.NewReader(c)
			if kind == "http_proxy" {
				fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Addr(), target.Addr())
				response, e := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
				if e != nil {
					t.Fatal(e)
				}
				if response.StatusCode != 200 {
					t.Fatal(response.Status)
				}
			}
			if _, err = c.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4)
			if _, err = io.ReadFull(reader, buf); err != nil || string(buf) != "ping" {
				t.Fatal(string(buf), err)
			}
			var peer net.Conn
			select {
			case peer = <-accepted:
			case <-time.After(time.Second):
				t.Fatal("no upstream connection")
			}
			defer peer.Close()
			if _, err = stopForward(Runtime{}, listenPort(listen)); err != nil {
				t.Fatal(err)
			}
			assertProxyClosed(t, c)
		})
	}
}

func assertProxyClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("connection still open")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("stop did not close connection")
	}
}

func TestStopClosesSocksBindAndUDPResources(t *testing.T) {
	for _, cmd := range []byte{2, 3} {
		t.Run(fmt.Sprint(cmd), func(t *testing.T) {
			if _, err := Socks5Proxy(Runtime{}, "start", "127.0.0.1", "0", "", "", false); err != nil {
				t.Fatal(err)
			}
			listen := findForwardListen(t, "socks5_proxy")
			defer func() { _, _ = stopForward(Runtime{}, listenPort(listen)) }()
			c := dialSocks(t, listen)
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			bound := socksCommand(t, c, cmd, net.IPv4zero, 0)
			if _, err := stopForward(Runtime{}, listenPort(listen)); err != nil {
				t.Fatal(err)
			}
			// BIND may send a failure response while tearing down. Drain it to EOF.
			_, err := io.ReadAll(c)
			if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("control connection still open")
			}
			if cmd == 2 {
				ln, err := net.Listen("tcp", bound)
				if err != nil {
					t.Fatal("BIND socket still bound:", err)
				}
				ln.Close()
			} else {
				ln, err := net.ListenPacket("udp", bound)
				if err != nil {
					t.Fatal("UDP relay still bound:", err)
				}
				ln.Close()
			}
		})
	}
}
