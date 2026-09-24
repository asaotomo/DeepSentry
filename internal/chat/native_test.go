package chat

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mockedSocket(t *testing.T, s *Service, handler func(*websocket.Conn)) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	t.Cleanup(server.Close)
	s.socketDial = func(ctx context.Context, _ string) (*socketPeer, error) {
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			return nil, err
		}
		return &socketPeer{conn: conn}, nil
	}
}
func TestWecomWebSocketAuthenticationReceiveAndReplyAck(t *testing.T) {
	s := testService(t, nilRunner)
	sent := make(chan map[string]any, 1)
	mockedSocket(t, s, func(conn *websocket.Conn) {
		var sub struct {
			Cmd     string            `json:"cmd"`
			Headers map[string]string `json:"headers"`
			Body    map[string]string `json:"body"`
		}
		if conn.ReadJSON(&sub) != nil {
			return
		}
		if sub.Cmd != "aibot_subscribe" || sub.Body["bot_id"] != "bot" || sub.Body["secret"] != "secret" {
			sent <- map[string]any{"error": "bad subscribe"}
			return
		}
		_ = conn.WriteJSON(map[string]any{"headers": sub.Headers, "errcode": 0})
		_ = conn.WriteJSON(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "incoming"}, "body": map[string]any{"msgid": "m1", "msgtype": "text", "chattype": "single", "from": map[string]string{"userid": "alice"}, "text": map[string]string{"content": "/ds help"}}})
		var reply map[string]any
		if conn.ReadJSON(&reply) != nil {
			return
		}
		sent <- reply
		_ = conn.WriteJSON(map[string]any{"headers": reply["headers"], "errcode": 0})
		_, _, _ = conn.ReadMessage()
	})
	c := config.ChatChannel{Name: "wecom-quick", Platform: "wecom_ws", AppID: "bot", AppSecret: "secret", AllowedUsers: []string{"alice"}}
	s.Connect(c)
	var d delivery
	select {
	case d = <-s.outbound:
	case <-time.After(3 * time.Second):
		t.Fatal("no incoming command processed")
	}
	if err := s.send(d); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-sent:
		if reply["cmd"] != "aibot_send_msg" {
			t.Fatal(reply)
		}
		body := reply["body"].(map[string]any)
		if body["chatid"] != "alice" {
			t.Fatal(body)
		}
	case <-time.After(time.Second):
		t.Fatal("reply missing")
	}
	s.Disconnect(c.Name)
}
func TestQQGatewayReceivesCommandAndUsesNativeReply(t *testing.T) {
	s := testService(t, nilRunner)
	identify := make(chan int, 1)
	mockedSocket(t, s, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 30000}})
		var frame struct {
			Op   int            `json:"op"`
			Data map[string]any `json:"d"`
		}
		if conn.ReadJSON(&frame) != nil {
			return
		}
		identify <- frame.Op
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]string{"session_id": "session"}})
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": 2, "t": "C2C_MESSAGE_CREATE", "d": map[string]any{"id": "m1", "content": "/ds help", "author": map[string]string{"user_openid": "alice"}}})
		_, _, _ = conn.ReadMessage()
	})
	sent := make(chan map[string]any, 1)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			return jsonResponse(map[string]string{"access_token": "access"}), nil
		case "/gateway":
			return jsonResponse(map[string]string{"url": "wss://api.sgroup.qq.com/websocket"}), nil
		case "/v2/users/alice/messages":
			if r.Header.Get("Authorization") != "QQBot access" {
				return nil, errors.New("missing QQ token")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent <- body
			return jsonResponse(map[string]string{"id": "sent"}), nil
		}
		return nil, errors.New("unexpected request")
	})}
	c := config.ChatChannel{Name: "qq-quick", Platform: "qqbot", AppID: "bot", AppSecret: "secret", AllowedUsers: []string{"alice"}}
	s.Connect(c)
	select {
	case op := <-identify:
		if op != 2 {
			t.Fatal(op)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no identify")
	}
	var d delivery
	select {
	case d = <-s.outbound:
	case <-time.After(3 * time.Second):
		t.Fatal("no command")
	}
	if err := s.send(d); err != nil {
		t.Fatal(err)
	}
	body := <-sent
	if body["msg_id"] != "m1" || !strings.Contains(body["content"].(string), "/ds new") {
		t.Fatal(body)
	}
	s.Disconnect(c.Name)
}
func TestWeixinReceiveCursorAndReplyContext(t *testing.T) {
	s := testService(t, nilRunner)
	sent := make(chan map[string]any, 1)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "getupdates") {
			return jsonResponse(map[string]any{"ret": 0, "get_updates_buf": "next-cursor", "msgs": []any{map[string]any{"message_id": 123, "message_type": 1, "from_user_id": "alice", "context_token": "context", "item_list": []any{map[string]any{"type": 1, "text_item": map[string]string{"text": "/ds help"}}}}}}), nil
		}
		if strings.HasSuffix(r.URL.Path, "sendmessage") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent <- body
			return jsonResponse(map[string]int{"ret": 0}), nil
		}
		return nil, errors.New("unexpected request")
	})}
	c := config.ChatChannel{Name: "wechat-quick", Platform: "weixin", AppID: "bot", AppSecret: "secret", AllowedUsers: []string{"alice"}, APIURL: weixinBase}
	s.Connect(c)
	var d delivery
	select {
	case d = <-s.outbound:
	case <-time.After(3 * time.Second):
		t.Fatal("no command")
	}
	if err := s.send(d); err != nil {
		t.Fatal(err)
	}
	body := <-sent
	msg := body["msg"].(map[string]any)
	if msg["context_token"] != "context" || msg["to_user_id"] != "alice" {
		t.Fatal(msg)
	}
	s.Disconnect(c.Name)
}
func TestWeixinReplyUsesLatestContextToken(t *testing.T) {
	s := testService(t, nilRunner)
	sent := make(chan map[string]any, 1)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "sendmessage") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent <- body
			return jsonResponse(map[string]int{"ret": 0}), nil
		}
		return nil, errors.New("unexpected request")
	})}
	c := config.ChatChannel{Name: "wechat-quick", Platform: "weixin", AppID: "bot", AppSecret: "secret", AllowedUsers: []string{"alice"}, APIURL: weixinBase}
	m := Message{ID: "m2", User: "alice", Chat: "alice", ContextToken: "stale"}
	s.mu.Lock()
	s.replyContext[owner(c, m)] = Message{ContextToken: "fresh"}
	s.mu.Unlock()
	if err := s.sendWeixin(context.Background(), c, m, "hi"); err != nil {
		t.Fatal(err)
	}
	body := <-sent
	msg := body["msg"].(map[string]any)
	if msg["context_token"] != "fresh" {
		t.Fatalf("stale reply token used: %v", msg)
	}
}
func TestQQExpiredPassiveWindowFallsBackToActive(t *testing.T) {
	s := testService(t, nilRunner)
	passive := make(chan bool, 4)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			return jsonResponse(map[string]string{"access_token": "access"}), nil
		case "/v2/users/alice/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, hasID := body["msg_id"]
			passive <- hasID
			if hasID {
				return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"code":22009}`)), Header: make(http.Header)}, nil
			}
			return jsonResponse(map[string]string{"id": "sent"}), nil
		}
		return nil, errors.New("unexpected request")
	})}
	c := config.ChatChannel{Name: "qq-quick", Platform: "qqbot", AppID: "bot", AppSecret: "secret", AllowedUsers: []string{"alice"}}
	m := Message{ID: "m1", User: "alice", Chat: "alice"}
	if err := s.sendQQ(context.Background(), c, m, "任务已完成"); err != nil {
		t.Fatalf("active fallback should recover: %v", err)
	}
	if got := <-passive; !got {
		t.Fatal("first attempt should be a passive reply")
	}
	if got := <-passive; got {
		t.Fatal("fallback should drop msg_id for an active push")
	}
}
func TestQQUncertainReplyDoesNotDuplicate(t *testing.T) {
	s := testService(t, nilRunner)
	var calls int
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/app/getAppAccessToken" {
			return jsonResponse(map[string]string{"access_token": "access"}), nil
		}
		calls++
		return nil, errors.New("connection reset")
	})}
	c := config.ChatChannel{Name: "qq-quick", Platform: "qqbot", AppID: "bot", AppSecret: "secret", AllowedUsers: []string{"alice"}}
	m := Message{ID: "m1", User: "alice", Chat: "alice"}
	if err := s.sendQQ(context.Background(), c, m, "任务已完成"); err == nil {
		t.Fatal("network-uncertain send should surface an error")
	}
	if calls != 1 {
		t.Fatalf("uncertain failure must not resend, calls=%d", calls)
	}
}
func TestPlatformBusinessErrorsAndRedirects(t *testing.T) {
	s := testService(t, nilRunner)
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(map[string]int{"errcode": 40014}), nil
	})}
	if _, err := s.request(context.Background(), "POST", "https://example.test", "", nil); err == nil {
		t.Fatal("business error ignored")
	}
	if _, err := apiJSON(context.Background(), s.client, "POST", "https://example.test", nil, nil); err == nil {
		t.Fatal("native business error ignored")
	}
}
