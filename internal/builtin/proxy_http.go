package builtin

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func HTTPProxy(rt Runtime, action, listenHost, listenPort, username, password string, allowLAN bool) (string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "start":
		return startHTTPProxy(rt, listenHost, listenPort, username, password, allowLAN)
	case "list":
		return listForwards(rt), nil
	case "stop":
		return stopForward(rt, listenPort)
	default:
		return "", fmt.Errorf("action 仅支持 start|list|stop")
	}
}

func startHTTPProxy(rt Runtime, listenHost, listenPort, username, password string, allowLAN bool) (string, error) {
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	if !allowLAN && !isLoopbackListenHost(listenHost) {
		return "", fmt.Errorf("HTTP 代理默认仅允许监听本机地址；如需局域网监听请显式设置 allow_lan=true")
	}
	if strings.TrimSpace(listenPort) == "" {
		listenPort = "8080"
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
	id := "http:" + actualListen
	auth := "none"
	if username != "" {
		auth = "basic"
	}
	sess := &forwardSession{ID: id, Kind: "http_proxy", Listen: actualListen, Auth: auth, StartedAt: time.Now(), Listener: ln}
	forwardManager.Lock()
	sess.Listener = forwardListener{Listener: ln, session: sess}
	forwardManager.items[id] = sess
	forwardManager.Unlock()
	server := &http.Server{
		Handler:           httpProxyHandler{sess: sess, username: username, password: password},
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() { _ = server.Serve(sess.Listener) }()
	return fmt.Sprintf("%s HTTP 代理已启动\nid: %s\nlisten: %s\nauth: %s\n说明: 这是给用户程序用的本地代理，支持 CONNECT 和普通 HTTP 转发；不是控制端出站 -proxy。进程退出或 stop 后关闭；无持久化/无反连控制面。", rt.tag(), id, actualListen, auth), nil
}

type httpProxyHandler struct {
	sess     *forwardSession
	username string
	password string
}

func (h httpProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !proxyBasicOK(r, h.username, h.password) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="DeepSentry"`)
		http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		h.handleConnect(w, r)
		return
	}
	if r.URL == nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.Host == "" {
		http.Error(w, "只接受发往该代理的 HTTP 请求或 CONNECT", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	if err := validateHost(host); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHopHeaders(out.Header)
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	forwardManager.Lock()
	h.sess.ConnCount++
	h.sess.BytesIn += n
	forwardManager.Unlock()
}

func (h httpProxyHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitProxyHost(r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// #nosec G704 -- CONNECT intentionally dials the proxy client-selected target; listener defaults to loopback, LAN exposure requires allow_lan, and configured proxy authentication is checked first.
	dst, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !h.sess.track(dst) {
		return
	}
	defer h.sess.closeResource(dst)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = dst.Close()
		http.Error(w, "无法建立 CONNECT 隧道", http.StatusInternalServerError)
		return
	}
	client, bufrw, err := hijacker.Hijack()
	if err != nil {
		_ = dst.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()
	defer dst.Close()
	if _, err := bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := bufrw.Flush(); err != nil {
		return
	}
	noteForwardConn(h.sess)
	if n := bufrw.Reader.Buffered(); n > 0 {
		peek, _ := bufrw.Reader.Peek(n)
		_, _ = dst.Write(peek)
	}
	spliceConns(h.sess, dst, client)
}

func splitProxyHost(raw string) (string, string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		host = raw
		port = "443"
	}
	if err := validateHost(host); err != nil {
		return "", "", err
	}
	if _, err := parseTCPPort(port); err != nil {
		return "", "", err
	}
	return host, port, nil
}

func proxyBasicOK(r *http.Request, username, password string) bool {
	if username == "" {
		return true
	}
	header := r.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return false
	}
	gotUser, gotPass, ok := strings.Cut(string(raw), ":")
	return ok && gotUser == username && gotPass == password
}

func stripHopHeaders(h http.Header) {
	if h == nil {
		return
	}
	if c := h.Get("Connection"); c != "" {
		for _, name := range strings.Split(c, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(name)
	}
}
