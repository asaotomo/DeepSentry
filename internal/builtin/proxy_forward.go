package builtin

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type forwardSession struct {
	ID          string
	Kind        string
	Listen      string
	Target      string
	Auth        string
	StartedAt   time.Time
	BytesIn     int64
	BytesOut    int64
	ConnCount   int64
	Listener    net.Listener
	resourcesMu sync.Mutex
	stopped     bool
	resources   map[io.Closer]struct{}
}

// Track every socket, including accepted connections and SOCKS BIND/UDP
// resources. Registering after stop closes it immediately, covering dial races.
func (s *forwardSession) track(c io.Closer) bool {
	s.resourcesMu.Lock()
	defer s.resourcesMu.Unlock()
	if s.stopped {
		_ = c.Close()
		return false
	}
	if s.resources == nil {
		s.resources = make(map[io.Closer]struct{})
	}
	s.resources[c] = struct{}{}
	return true
}
func (s *forwardSession) forget(c io.Closer) {
	s.resourcesMu.Lock()
	delete(s.resources, c)
	s.resourcesMu.Unlock()
}
func (s *forwardSession) closeResource(c io.Closer) { _ = c.Close(); s.forget(c) }
func (s *forwardSession) stop() {
	s.resourcesMu.Lock()
	s.stopped = true
	resources := s.resources
	s.resources = nil
	s.resourcesMu.Unlock()
	_ = s.Listener.Close()
	for c := range resources {
		_ = c.Close()
	}
}

// Wrapping accepted connections also covers HTTP keep-alive and hijacking.
type forwardListener struct {
	net.Listener
	session *forwardSession
}

func (l forwardListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if !l.session.track(c) {
		return nil, net.ErrClosed
	}
	return &forwardConn{Conn: c, session: l.session}, nil
}

type forwardConn struct {
	net.Conn
	session *forwardSession
}

func (c *forwardConn) Close() error {
	err := c.Conn.Close()
	c.session.forget(c.Conn)
	return err
}

var forwardManager = struct {
	sync.Mutex
	items map[string]*forwardSession
}{items: map[string]*forwardSession{}}

func TCPForward(rt Runtime, action, listenHost, listenPort, targetHost, targetPort string) (string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "start":
		return startForward(rt, listenHost, listenPort, targetHost, targetPort)
	case "list":
		return listForwards(rt), nil
	case "stop":
		return stopForward(rt, listenPort)
	default:
		return "", fmt.Errorf("action 仅支持 start|list|stop")
	}
}

func startForward(rt Runtime, listenHost, listenPort, targetHost, targetPort string) (string, error) {
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	lp, err := parseListenPort(listenPort)
	if err != nil {
		return "", err
	}
	tp, err := parseTCPPort(targetPort)
	if err != nil {
		return "", err
	}
	if err := validateHost(targetHost); err != nil {
		return "", err
	}
	listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(lp))
	targetAddr := net.JoinHostPort(targetHost, strconv.Itoa(tp))
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", err
	}
	actualListen := ln.Addr().String()
	id := fmt.Sprintf("tcp:%s->%s", actualListen, targetAddr)
	sess := &forwardSession{ID: id, Kind: "tcp_forward", Listen: actualListen, Target: targetAddr, StartedAt: time.Now(), Listener: ln}
	forwardManager.Lock()
	sess.Listener = forwardListener{Listener: ln, session: sess}
	forwardManager.items[id] = sess
	forwardManager.Unlock()
	go serveForward(sess)
	return fmt.Sprintf("%s TCP 转发已启动\nid: %s\nlisten: %s\ntarget: %s\n说明: 进程退出或 stop 后关闭；无持久化/无反连控制面。", rt.tag(), id, actualListen, targetAddr), nil
}

func serveForward(sess *forwardSession) {
	for {
		conn, err := sess.Listener.Accept()
		if err != nil {
			return
		}
		go handleForwardConn(sess, conn)
	}
}

func handleForwardConn(sess *forwardSession, src net.Conn) {
	defer src.Close()
	dst, err := net.DialTimeout("tcp", sess.Target, 8*time.Second)
	if err != nil {
		return
	}
	if !sess.track(dst) {
		return
	}
	defer sess.closeResource(dst)
	forwardManager.Lock()
	sess.ConnCount++
	forwardManager.Unlock()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(dst, src)
		forwardManager.Lock()
		sess.BytesOut += n
		forwardManager.Unlock()
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(src, dst)
		forwardManager.Lock()
		sess.BytesIn += n
		forwardManager.Unlock()
	}()
	wg.Wait()
}

func listForwards(rt Runtime) string {
	forwardManager.Lock()
	defer forwardManager.Unlock()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 代理/转发会话列表\n", rt.tag()))
	if len(forwardManager.items) == 0 {
		b.WriteString("(无活动代理/转发)\n")
		return b.String()
	}
	keys := make([]string, 0, len(forwardManager.items))
	for k := range forwardManager.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := forwardManager.items[k]
		target := s.Target
		if target == "" {
			target = "dynamic"
		}
		auth := ""
		if s.Auth != "" {
			auth = " auth=" + s.Auth
		}
		b.WriteString(fmt.Sprintf("- id=%s kind=%s listen=%s target=%s%s uptime=%s conns=%d bytes_in=%d bytes_out=%d\n",
			s.ID, s.Kind, s.Listen, target, auth, time.Since(s.StartedAt).Round(time.Second), s.ConnCount, s.BytesIn, s.BytesOut))
	}
	return b.String()
}

func stopForward(rt Runtime, idOrPort string) (string, error) {
	forwardManager.Lock()
	for id, s := range forwardManager.items {
		_, port, _ := net.SplitHostPort(s.Listen)
		if id == idOrPort || (idOrPort != "" && port == idOrPort) {
			delete(forwardManager.items, id)
			forwardManager.Unlock()
			s.stop()
			return fmt.Sprintf("%s 代理/转发已停止\nid: %s\nkind: %s\nlisten: %s\ntarget: %s", rt.tag(), id, s.Kind, s.Listen, s.Target), nil
		}
	}
	forwardManager.Unlock()
	return "", fmt.Errorf("未找到转发: %s", idOrPort)
}

func Socks5Proxy(rt Runtime, action, listenHost, listenPort, username, password string, allowLAN bool) (string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "start":
		return startSocks5Proxy(rt, listenHost, listenPort, username, password, allowLAN)
	case "list":
		return listForwards(rt), nil
	case "stop":
		return stopForward(rt, listenPort)
	default:
		return "", fmt.Errorf("action 仅支持 start|list|stop")
	}
}

func startSocks5Proxy(rt Runtime, listenHost, listenPort, username, password string, allowLAN bool) (string, error) {
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	if !allowLAN && !isLoopbackListenHost(listenHost) {
		return "", fmt.Errorf("SOCKS5 默认仅允许监听本机地址；如需局域网监听请显式设置 allow_lan=true")
	}
	if strings.TrimSpace(listenPort) == "" {
		listenPort = "1080"
	}
	if (username == "") != (password == "") {
		return "", fmt.Errorf("username/password 必须同时提供或同时留空")
	}
	lp, err := parseListenPort(listenPort)
	if err != nil {
		return "", err
	}
	listenAddr := net.JoinHostPort(listenHost, strconv.Itoa(lp))
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", err
	}
	actualListen := ln.Addr().String()
	id := "socks5:" + actualListen
	auth := "none"
	if username != "" {
		auth = "username"
	}
	sess := &forwardSession{ID: id, Kind: "socks5_proxy", Listen: actualListen, Auth: auth, StartedAt: time.Now(), Listener: ln}
	forwardManager.Lock()
	sess.Listener = forwardListener{Listener: ln, session: sess}
	forwardManager.items[id] = sess
	forwardManager.Unlock()
	go serveSocks5Proxy(sess, username, password)
	return fmt.Sprintf("%s SOCKS5 代理已启动\nid: %s\nlisten: %s\nauth: %s\n说明: 支持 CONNECT、BIND 和 UDP，供用户程序把流量指到这个地址；进程退出或 stop 后关闭；无持久化/无反连控制面。", rt.tag(), id, actualListen, auth), nil
}

func serveSocks5Proxy(sess *forwardSession, username, password string) {
	for {
		conn, err := sess.Listener.Accept()
		if err != nil {
			return
		}
		go handleSocks5Conn(sess, conn, username, password)
	}
}

func handleSocks5Conn(sess *forwardSession, src net.Conn, username, password string) {
	defer src.Close()
	_ = src.SetDeadline(time.Now().Add(15 * time.Second))
	if err := socks5Handshake(src, username, password); err != nil {
		return
	}
	cmd, host, port, err := readSocks5Request(src)
	if err != nil {
		return
	}
	switch cmd {
	case 0x01:
		relaySocks5Connect(sess, src, net.JoinHostPort(host, strconv.Itoa(port)))
	case 0x02:
		relaySocks5Bind(sess, src, host, port)
	case 0x03:
		relaySocks5UDP(sess, src, host, port)
	default:
		_, _ = src.Write(socks5Reply(0x07, nil))
	}
}

func relaySocks5Connect(sess *forwardSession, src net.Conn, targetAddr string) {
	dst, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		_, _ = src.Write(socks5Reply(0x05, nil))
		return
	}
	if !sess.track(dst) {
		return
	}
	defer sess.closeResource(dst)
	if _, err := src.Write(socks5Reply(0x00, dst.LocalAddr())); err != nil {
		return
	}
	_ = src.SetDeadline(time.Time{})
	_ = dst.SetDeadline(time.Time{})
	noteForwardConn(sess)
	spliceConns(sess, dst, src)
}

func relaySocks5Bind(sess *forwardSession, src net.Conn, expectHost string, expectPort int) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bindHostFor(sess.Listen), "0"))
	if err != nil {
		_, _ = src.Write(socks5Reply(0x01, nil))
		return
	}
	if !sess.track(ln) {
		return
	}
	defer sess.closeResource(ln)
	if _, err := src.Write(socks5Reply(0x00, ln.Addr())); err != nil {
		return
	}
	_ = src.SetDeadline(time.Time{})
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	go func() {
		_, err := io.Copy(pw, src)
		_ = pw.CloseWithError(err)
		_ = ln.Close()
	}()
	if tcpLn, ok := ln.(*net.TCPListener); ok {
		_ = tcpLn.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	peer, err := ln.Accept()
	if err != nil {
		_, _ = src.Write(socks5Reply(0x04, nil))
		return
	}
	if !sess.track(peer) {
		return
	}
	defer sess.closeResource(peer)
	if !sourceAllowed(peer.RemoteAddr(), expectHost, expectPort) {
		_, _ = src.Write(socks5Reply(0x02, nil))
		return
	}
	if _, err := src.Write(socks5Reply(0x00, peer.RemoteAddr())); err != nil {
		return
	}
	noteForwardConn(sess)
	spliceConnReader(sess, peer, pr, src)
}

func relaySocks5UDP(sess *forwardSession, src net.Conn, expectHost string, expectPort int) {
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIPFor(sess.Listen), Port: 0})
	if err != nil {
		_, _ = src.Write(socks5Reply(0x01, nil))
		return
	}
	if !sess.track(relay) {
		return
	}
	defer sess.closeResource(relay)
	if _, err := src.Write(socks5Reply(0x00, relay.LocalAddr())); err != nil {
		return
	}
	_ = src.SetDeadline(time.Time{})
	noteForwardConn(sess)
	go func() {
		_, _ = io.Copy(io.Discard, src)
		_ = relay.Close()
	}()
	expectIP := net.ParseIP(expectHost)
	if expectIP == nil || expectIP.IsUnspecified() {
		if tcp, ok := src.RemoteAddr().(*net.TCPAddr); ok {
			expectIP = tcp.IP
		}
	}
	var clientAddr *net.UDPAddr
	buf := make([]byte, 65535)
	for {
		n, from, err := relay.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !udpFromClient(from, expectIP, expectPort, clientAddr) {
			if clientAddr == nil {
				continue
			}
			header := socksUDPHeader(from)
			packet := append(header, buf[:n]...)
			_, _ = relay.WriteToUDP(packet, clientAddr)
			forwardManager.Lock()
			sess.BytesIn += int64(n)
			forwardManager.Unlock()
			continue
		}
		if clientAddr == nil {
			clientAddr = &net.UDPAddr{IP: append(net.IP(nil), from.IP...), Port: from.Port}
		}
		host, port, headerLen, frag, err := parseSocksUDP(buf[:n])
		if err != nil || frag != 0 {
			continue
		}
		target, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		payload := append([]byte(nil), buf[headerLen:n]...)
		if _, err := relay.WriteToUDP(payload, target); err != nil {
			continue
		}
		forwardManager.Lock()
		sess.BytesOut += int64(len(payload))
		forwardManager.Unlock()
	}
}

func noteForwardConn(sess *forwardSession) {
	forwardManager.Lock()
	sess.ConnCount++
	forwardManager.Unlock()
}

func spliceConns(sess *forwardSession, dst, src net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(dst, src)
		forwardManager.Lock()
		sess.BytesOut += n
		forwardManager.Unlock()
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(src, dst)
		forwardManager.Lock()
		sess.BytesIn += n
		forwardManager.Unlock()
	}()
	wg.Wait()
}

func spliceConnReader(sess *forwardSession, dst net.Conn, src io.Reader, back net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(dst, src)
		forwardManager.Lock()
		sess.BytesOut += n
		forwardManager.Unlock()
		_ = dst.Close()
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(back, dst)
		forwardManager.Lock()
		sess.BytesIn += n
		forwardManager.Unlock()
	}()
	wg.Wait()
}

func bindHostFor(listen string) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return "127.0.0.1"
	}
	return host
}

func bindIPFor(listen string) net.IP {
	host := bindHostFor(listen)
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	return net.IPv4(127, 0, 0, 1)
}

func sourceAllowed(addr net.Addr, expectHost string, expectPort int) bool {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	if ip := net.ParseIP(expectHost); ip != nil && !ip.IsUnspecified() {
		if !tcp.IP.Equal(ip) {
			return false
		}
	}
	if expectPort != 0 && tcp.Port != expectPort {
		return false
	}
	return true
}

func udpFromClient(from *net.UDPAddr, expectIP net.IP, expectPort int, locked *net.UDPAddr) bool {
	if from == nil {
		return false
	}
	if locked != nil {
		return from.IP.Equal(locked.IP) && from.Port == locked.Port
	}
	if expectIP != nil && !expectIP.IsUnspecified() && !from.IP.Equal(expectIP) {
		return false
	}
	if expectPort != 0 && from.Port != expectPort {
		return false
	}
	return true
}

func socks5Handshake(conn net.Conn, username, password string) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[0] != 0x05 {
		return fmt.Errorf("not socks5")
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	wantAuth := username != ""
	method := byte(0x00)
	if wantAuth {
		method = 0x02
	}
	if !bytesContains(methods, method) {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return fmt.Errorf("unsupported auth method")
	}
	if _, err := conn.Write([]byte{0x05, method}); err != nil {
		return err
	}
	if !wantAuth {
		return nil
	}
	authHead := make([]byte, 2)
	if _, err := io.ReadFull(conn, authHead); err != nil {
		return err
	}
	if authHead[0] != 0x01 {
		return fmt.Errorf("unsupported username auth version")
	}
	user := make([]byte, int(authHead[1]))
	if _, err := io.ReadFull(conn, user); err != nil {
		return err
	}
	passLen := make([]byte, 1)
	if _, err := io.ReadFull(conn, passLen); err != nil {
		return err
	}
	pass := make([]byte, int(passLen[0]))
	if _, err := io.ReadFull(conn, pass); err != nil {
		return err
	}
	if string(user) != username || string(pass) != password {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return fmt.Errorf("invalid username/password")
	}
	_, err := conn.Write([]byte{0x01, 0x00})
	return err
}

func readSocks5Request(conn net.Conn) (byte, string, int, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, "", 0, err
	}
	if head[0] != 0x05 {
		return 0, "", 0, fmt.Errorf("invalid socks version")
	}
	cmd := head[1]
	if cmd != 0x01 && cmd != 0x02 && cmd != 0x03 {
		_, _ = conn.Write(socks5Reply(0x07, nil))
		return 0, "", 0, fmt.Errorf("unsupported socks command")
	}
	host, port, _, _, err := readSocksAddr(head[3], conn, true)
	if err != nil {
		return 0, "", 0, err
	}
	return cmd, host, port, nil
}

func readSocksAddr(atyp byte, r io.Reader, replyOnError bool) (string, int, int, byte, error) {
	writeFail := func(code byte, err error) (string, int, int, byte, error) {
		if replyOnError {
			if conn, ok := r.(io.Writer); ok {
				_, _ = conn.Write(socks5Reply(code, nil))
			}
		}
		return "", 0, 0, 0, err
	}
	var host string
	headerLen := 4
	switch atyp {
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", 0, 0, 0, err
		}
		host = net.IP(ip).String()
		headerLen += 4
	case 0x03:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(r, lb); err != nil {
			return "", 0, 0, 0, err
		}
		name := make([]byte, int(lb[0]))
		if _, err := io.ReadFull(r, name); err != nil {
			return "", 0, 0, 0, err
		}
		host = string(name)
		if err := validateHost(host); err != nil && host != "0.0.0.0" {
			return writeFail(0x04, err)
		}
		headerLen += 1 + len(name)
	case 0x04:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", 0, 0, 0, err
		}
		host = net.IP(ip).String()
		headerLen += 16
	default:
		return writeFail(0x08, fmt.Errorf("unsupported address type"))
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(r, portBytes); err != nil {
		return "", 0, 0, 0, err
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])
	return host, port, headerLen + 2, 0, nil
}

func parseSocksUDP(pkt []byte) (string, int, int, byte, error) {
	if len(pkt) < 4 || pkt[0] != 0x00 || pkt[1] != 0x00 {
		return "", 0, 0, 0, fmt.Errorf("invalid udp header")
	}
	frag := pkt[2]
	host, port, headerLen, _, err := readSocksAddr(pkt[3], bytes.NewReader(pkt[4:]), false)
	if err != nil {
		return "", 0, 0, frag, err
	}
	return host, port, headerLen, frag, nil
}

func socksUDPHeader(addr *net.UDPAddr) []byte {
	if addr == nil {
		return []byte{0x00, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	}
	port := addr.Port
	if port < 0 || port > 65535 {
		port = 0
	}
	if ip4 := addr.IP.To4(); ip4 != nil {
		return []byte{0x00, 0x00, 0x00, 0x01, ip4[0], ip4[1], ip4[2], ip4[3], byte((port >> 8) & 0xff), byte(port & 0xff)}
	}
	ip6 := addr.IP.To16()
	if ip6 == nil {
		ip6 = net.IPv6zero
	}
	out := []byte{0x00, 0x00, 0x00, 0x04}
	out = append(out, ip6...)
	return append(out, byte((port>>8)&0xff), byte(port&0xff))
}

func socks5Reply(code byte, addr net.Addr) []byte {
	ip, port := net.IPv4(0, 0, 0, 0), 0
	if addr != nil {
		switch a := addr.(type) {
		case *net.TCPAddr:
			if a.IP != nil {
				ip = a.IP
			}
			port = a.Port
		case *net.UDPAddr:
			if a.IP != nil {
				ip = a.IP
			}
			port = a.Port
		}
	}
	if ip4 := ip.To4(); ip4 != nil {
		return []byte{0x05, code, 0x00, 0x01, ip4[0], ip4[1], ip4[2], ip4[3], byte(port >> 8), byte(port)}
	}
	ip6 := ip.To16()
	if ip6 == nil {
		ip6 = net.IPv6zero
	}
	out := []byte{0x05, code, 0x00, 0x04}
	out = append(out, ip6...)
	return append(out, byte(port>>8), byte(port))
}

func isLoopbackListenHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func bytesContains(xs []byte, x byte) bool {
	for _, b := range xs {
		if b == x {
			return true
		}
	}
	return false
}

func parseListenPort(port string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || p < 0 || p > 65535 {
		return 0, fmt.Errorf("非法 listen_port: %s", port)
	}
	return p, nil
}
