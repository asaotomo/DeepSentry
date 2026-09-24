package chat

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"time"
)

func (s *Service) runWecom(ctx context.Context, c config.ChatChannel, update func(string)) {
	for ctx.Err() == nil {
		s.wecomSession(ctx, c, update)
		if ctx.Err() != nil {
			return
		}
		update("reconnecting")
		if !pause(ctx, 5*time.Second) {
			return
		}
	}
}
func (s *Service) wecomSession(ctx context.Context, c config.ChatChannel, update func(string)) {
	p, err := s.socketDial(ctx, "wss://openws.work.weixin.qq.com")
	if err != nil {
		return
	}
	defer p.conn.Close()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.mediaPeers[c.Name] == p {
			delete(s.mediaPeers, c.Name)
			delete(s.senders, c.Name)
		}
	}()
	live, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-live.Done(); _ = p.conn.Close() }()
	subID := "aibot_subscribe_" + randomID()
	if p.write(map[string]any{"cmd": "aibot_subscribe", "headers": map[string]string{"req_id": subID}, "body": map[string]string{"bot_id": c.AppID, "secret": secret(c)}}) != nil {
		return
	}
	authenticated := false
	for live.Err() == nil {
		var frame struct {
			Cmd     string `json:"cmd"`
			Headers struct {
				ID string `json:"req_id"`
			} `json:"headers"`
			Code *int            `json:"errcode"`
			Body json.RawMessage `json:"body"`
		}
		if p.read(&frame) != nil {
			return
		}
		if frame.Headers.ID == subID {
			if frame.Code == nil || *frame.Code != 0 {
				return
			}
			authenticated = true
			s.mu.Lock()
			if ctx.Err() != nil || s.generations[c.Name] != c.Generation {
				s.mu.Unlock()
				return
			}
			if s.mediaPeers == nil {
				s.mediaPeers = map[string]*socketPeer{}
			}
			s.mediaPeers[c.Name] = p
			s.senders[c.Name] = func(callCtx context.Context, m Message, text string) error {
				return p.request(callCtx, "aibot_send_msg", map[string]any{"chatid": m.Chat, "msgtype": "markdown", "markdown": map[string]string{"content": text}})
			}
			s.mu.Unlock()
			update("connected")
			go func() {
				for pause(live, 30*time.Second) {
					if p.request(live, "ping", nil) != nil {
						_ = p.conn.Close()
						return
					}
				}
			}()
			continue
		}
		if frame.Cmd == "" {
			p.ackBody(frame.Headers.ID, frame.Code, frame.Body)
			continue
		}
		if !authenticated || frame.Cmd != "aibot_msg_callback" {
			continue
		}
		var e struct {
			ID       string `json:"msgid"`
			Type     string `json:"msgtype"`
			Chat     string `json:"chatid"`
			ChatType string `json:"chattype"`
			From     struct {
				User string `json:"userid"`
			} `json:"from"`
			Image struct {
				URL string `json:"url"`
				Key string `json:"aeskey"`
			} `json:"image"`
			Voice struct {
				Content string `json:"content"`
			} `json:"voice"`
			File struct {
				URL  string `json:"url"`
				Name string `json:"filename"`
				Key  string `json:"aeskey"`
			} `json:"file"`
			Mixed struct {
				Items []struct {
					Type string `json:"msgtype"`
					Text struct {
						Content string `json:"content"`
					} `json:"text"`
					Image struct {
						URL string `json:"url"`
						Key string `json:"aeskey"`
					} `json:"image"`
				} `json:"msg_item"`
			} `json:"mixed"`
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		if json.Unmarshal(frame.Body, &e) != nil {
			continue
		}
		chat := e.From.User
		if e.ChatType == "group" {
			chat = e.Chat
		}
		m := Message{ID: e.ID, User: e.From.User, Chat: chat, Group: e.ChatType == "group", Text: e.Text.Content}
		switch e.Type {
		case "text":
		case "voice":
			m.Attachments = []Attachment{{Kind: "audio", Transcript: e.Voice.Content}}
		case "image":
			m.Attachments = []Attachment{{Kind: "image", URL: e.Image.URL, Key: e.Image.Key, Cipher: "cbc"}}
		case "file":
			a := Attachment{Kind: "file", URL: e.File.URL, Name: e.File.Name}
			if e.File.Key != "" {
				a.Key = e.File.Key
				a.Cipher = "cbc"
			}
			m.Attachments = []Attachment{a}
		case "mixed":
			for _, part := range e.Mixed.Items {
				if part.Type == "text" {
					m.Text += "\n" + part.Text.Content
				} else if part.Type == "image" {
					m.Attachments = append(m.Attachments, Attachment{Kind: "image", URL: part.Image.URL, Key: part.Image.Key, Cipher: "cbc"})
				}
			}
		default:
			m.Attachments = []Attachment{{Kind: "unsupported"}}
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
