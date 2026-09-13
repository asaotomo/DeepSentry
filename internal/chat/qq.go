package chat

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

func (s *Service) qqToken(ctx context.Context, c config.ChatChannel) (string, error) {
	d, err := apiJSON(ctx, s.client, "POST", "https://bots.qq.com/app/getAppAccessToken", nil, map[string]string{"appId": c.AppID, "clientSecret": secret(c)})
	if err != nil {
		return "", err
	}
	token := jsonString(d, "access_token")
	if token == "" {
		return "", errors.New("QQ 未返回访问令牌")
	}
	return token, nil
}
func (s *Service) runQQ(ctx context.Context, c config.ChatChannel, update func(string)) {
	session := ""
	var seq atomic.Int64
	for ctx.Err() == nil {
		token, err := s.qqToken(ctx, c)
		if err == nil {
			var data map[string]json.RawMessage
			data, err = apiJSON(ctx, s.client, "GET", "https://api.sgroup.qq.com/gateway", map[string]string{"Authorization": "QQBot " + token}, nil)
			if err == nil {
				gateway := jsonString(data, "url")
				if !trustedURL(gateway, "wss", "qq.com") {
					update("connection_failed")
					return
				}
				s.qqSession(ctx, c, gateway, token, &session, &seq, update)
			}
		}
		if ctx.Err() != nil {
			return
		}
		update("reconnecting")
		if !pause(ctx, 5*time.Second) {
			return
		}
	}
}
func (s *Service) qqSession(ctx context.Context, c config.ChatChannel, gateway, token string, session *string, seq *atomic.Int64, update func(string)) {
	peer, err := s.socketDial(ctx, gateway)
	if err != nil {
		return
	}
	defer peer.conn.Close()
	live, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-live.Done(); _ = peer.conn.Close() }()
	heartbeatStarted := false
	for live.Err() == nil {
		var e struct {
			Op   int             `json:"op"`
			Seq  *int64          `json:"s"`
			Type string          `json:"t"`
			Data json.RawMessage `json:"d"`
		}
		if peer.read(&e) != nil {
			return
		}
		if e.Seq != nil {
			seq.Store(*e.Seq)
		}
		switch e.Op {
		case 10:
			var hello struct {
				Interval int `json:"heartbeat_interval"`
			}
			if json.Unmarshal(e.Data, &hello) != nil || hello.Interval < 1000 || hello.Interval > 120000 {
				return
			}
			if !heartbeatStarted {
				heartbeatStarted = true
				go func() {
					for pause(live, time.Duration(hello.Interval)*time.Millisecond) {
						if peer.write(map[string]any{"op": 1, "d": seq.Load()}) != nil {
							_ = peer.conn.Close()
							return
						}
					}
				}()
			}
			payload := map[string]any{"token": "QQBot " + token, "intents": 1 << 25, "shard": []int{0, 1}, "properties": map[string]string{"$os": "deepsentry", "$browser": "deepsentry", "$device": "deepsentry"}}
			op := 2
			if *session != "" {
				op = 6
				payload = map[string]any{"token": "QQBot " + token, "session_id": *session, "seq": seq.Load()}
			}
			if peer.write(map[string]any{"op": op, "d": payload}) != nil {
				return
			}
		case 7:
			return
		case 9:
			*session = ""
			seq.Store(0)
			return
		case 0:
			if e.Type == "READY" {
				var d struct {
					Session string `json:"session_id"`
				}
				_ = json.Unmarshal(e.Data, &d)
				*session = d.Session
				update("connected")
				continue
			}
			if e.Type == "RESUMED" {
				update("connected")
				continue
			}
			if e.Type != "C2C_MESSAGE_CREATE" && e.Type != "GROUP_AT_MESSAGE_CREATE" {
				continue
			}
			var d struct {
				ID          string `json:"id"`
				Content     string `json:"content"`
				Attachments []struct {
					URL  string `json:"url"`
					Name string `json:"filename"`
					Type string `json:"content_type"`
				} `json:"attachments"`
				Group  string `json:"group_openid"`
				Author struct {
					User   string `json:"user_openid"`
					Member string `json:"member_openid"`
				} `json:"author"`
			}
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			m := Message{ID: d.ID, User: d.Author.User, Chat: d.Author.User, Text: d.Content}
			for _, a := range d.Attachments {
				kind := "file"
				if strings.HasPrefix(a.Type, "image/") {
					kind = "image"
				} else if strings.HasPrefix(a.Type, "audio/") {
					kind = "audio"
				} else if a.URL == "" {
					kind = "unsupported"
				}
				target := a.URL
				if strings.HasPrefix(target, "//") {
					target = "https:" + target
				}
				m.Attachments = append(m.Attachments, Attachment{Kind: kind, URL: target, Name: a.Name})
			}
			if e.Type == "GROUP_AT_MESSAGE_CREATE" {
				m.User = d.Author.Member
				m.Chat = d.Group
				m.Group = true
			}
			if ctx.Err() != nil {
				return
			}
			reply, err := s.command(c, m)
			if err == nil && reply != "" {
				s.enqueue(delivery{channel: c, message: m, text: reply})
			}
		}
	}
}

var qqMessageSeq atomic.Uint32

func (s *Service) sendQQ(ctx context.Context, c config.ChatChannel, m Message, text string) error {
	token, err := s.qqToken(ctx, c)
	if err != nil {
		return preflightError(err)
	}
	path := "/v2/users/" + url.PathEscape(m.User) + "/messages"
	if m.Group {
		path = "/v2/groups/" + url.PathEscape(m.Chat) + "/messages"
	}
	seq := m.DeliverySeq
	if seq == 0 {
		seq = qqMessageSeq.Add(1)
	}
	_, err = apiJSON(ctx, s.client, "POST", "https://api.sgroup.qq.com"+path, map[string]string{"Authorization": "QQBot " + token}, map[string]any{"content": text, "msg_type": 0, "msg_id": m.ID, "msg_seq": seq})
	return err
}
