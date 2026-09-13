package chat

import (
	"ai-edr/internal/config"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"sync"
	"time"
)

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type socketPeer struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	mu        sync.Mutex
	responses map[string]chan socketResponse
}

func socketDialer() *websocket.Dialer {
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment, NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return config.ControllerDialContext(ctx, network, address)
	}}
	if config.GlobalConfig.ControllerProxy != "" {
		d.Proxy = nil
	}
	return &d
}
func dialSocket(ctx context.Context, target string) (*socketPeer, error) {
	conn, _, err := socketDialer().DialContext(ctx, target, nil)
	if err != nil {
		return nil, errors.New("长连接建立失败")
	}
	conn.SetReadLimit(1 << 20)
	return &socketPeer{conn: conn}, nil
}
func (p *socketPeer) write(v any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	return p.conn.WriteJSON(v)
}

type socketResponse struct {
	body json.RawMessage
	err  error
}

func (p *socketPeer) request(ctx context.Context, cmd string, body any) error {
	_, err := p.requestBody(ctx, cmd, body)
	return err
}
func (p *socketPeer) requestBody(ctx context.Context, cmd string, body any) (json.RawMessage, error) {
	id := cmd + "_" + randomID()
	ch := make(chan socketResponse, 1)
	p.mu.Lock()
	if p.responses == nil {
		p.responses = map[string]chan socketResponse{}
	}
	p.responses[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.responses, id); p.mu.Unlock() }()
	if err := p.write(map[string]any{"cmd": cmd, "headers": map[string]string{"req_id": id}, "body": body}); err != nil {
		return nil, &platformError{Message: "企业微信连接写入失败，送达结果未知", Uncertain: true}
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response.body, response.err
	case <-ctx.Done():
		return nil, &platformError{Message: "企业微信等待回执被取消，送达结果未知", Uncertain: true}
	case <-timer.C:
		return nil, &platformError{Message: "企业微信消息回执超时，送达结果未知", Uncertain: true}
	}
}
func (p *socketPeer) ackBody(id string, code *int, body json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch := p.responses[id]; ch != nil {
		var err error
		if code == nil || *code != 0 {
			err = errors.New("企业微信拒绝消息")
		}
		select {
		case ch <- socketResponse{body: body, err: err}:
		default:
		}
	}
}
func (p *socketPeer) read(v any) error {
	_ = p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	_, b, err := p.conn.ReadMessage()
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
