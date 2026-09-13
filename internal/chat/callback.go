package chat

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-edr/internal/config"
)

func equal(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func fresh(v string, millis bool) bool {
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil {
		return false
	}
	if millis {
		n /= 1000
	}
	d := time.Now().Unix() - n
	return d >= -300 && d <= 300
}
func (s *Service) callback(c config.ChatChannel, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && c.Platform == "wecom" {
		plain, err := wecomDecrypt(c, r, r.URL.Query().Get("echostr"))
		if err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(plain)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	m, challenge, err := decode(c, r, raw)
	if err != nil {
		http.Error(w, "invalid callback", http.StatusUnauthorized)
		return
	}
	if challenge != "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge})
		return
	}
	if m.ID == "" {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	reply, err := s.command(c, m)
	if err != nil {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	if reply != "" {
		s.enqueue(delivery{channel: c, message: m, text: reply})
	}
	if c.Platform == "wecom" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("success"))
		return
	}
	_, _ = w.Write([]byte(`{}`))
}
func decode(c config.ChatChannel, r *http.Request, raw []byte) (Message, string, error) {
	fail := errors.New("invalid event")
	switch c.Platform {
	case "feishu":
		ts, nonce := r.Header.Get("X-Lark-Request-Timestamp"), r.Header.Get("X-Lark-Request-Nonce")
		key := os.Getenv(c.EncryptKeyEnv)
		sum := sha256.Sum256(append([]byte(ts+nonce+key), raw...))
		if !fresh(ts, false) || nonce == "" || !equal(hex.EncodeToString(sum[:]), r.Header.Get("X-Lark-Signature")) {
			return Message{}, "", fail
		}
		var envelope struct {
			Encrypt string `json:"encrypt"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return Message{}, "", fail
		}
		if envelope.Encrypt != "" {
			b, e := base64.StdEncoding.DecodeString(envelope.Encrypt)
			if e != nil || len(b) < 32 {
				return Message{}, "", fail
			}
			k := sha256.Sum256([]byte(key))
			raw, e = decryptCBC(k[:], b[:16], b[16:])
			if e != nil {
				return Message{}, "", e
			}
		}
		var e struct {
			Type      string `json:"type"`
			Token     string `json:"token"`
			Challenge string `json:"challenge"`
			Header    struct {
				Token     string `json:"token"`
				AppID     string `json:"app_id"`
				EventType string `json:"event_type"`
			} `json:"header"`
			Event struct {
				Sender struct {
					SenderType string `json:"sender_type"`
					SenderID   struct {
						OpenID string `json:"open_id"`
					} `json:"sender_id"`
				} `json:"sender"`
				Message struct {
					ID       string `json:"message_id"`
					ChatID   string `json:"chat_id"`
					ChatType string `json:"chat_type"`
					Type     string `json:"message_type"`
					Content  string `json:"content"`
					Mentions []struct {
						Key string `json:"key"`
					} `json:"mentions"`
				} `json:"message"`
			} `json:"event"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Message{}, "", fail
		}
		if e.Type == "url_verification" && equal(e.Token, os.Getenv(c.TokenEnv)) {
			return Message{}, e.Challenge, nil
		}
		if !equal(e.Header.Token, os.Getenv(c.TokenEnv)) || e.Header.AppID != c.AppID {
			return Message{}, "", fail
		}
		if e.Header.EventType != "im.message.receive_v1" || e.Event.Sender.SenderType != "user" {
			return Message{}, "", nil
		}
		text, attachments, err := parseFeishuContent(e.Event.Message.Type, e.Event.Message.Content)
		if err != nil {
			return Message{}, "", fail
		}
		for _, mention := range e.Event.Message.Mentions {
			text = strings.ReplaceAll(text, mention.Key, "")
		}
		return Message{ID: e.Event.Message.ID, User: e.Event.Sender.SenderID.OpenID, Chat: e.Event.Message.ChatID, Text: text, Attachments: attachments, Group: e.Event.Message.ChatType == "group"}, "", nil
	case "dingtalk":
		ts := r.Header.Get("timestamp")
		secret := secret(c)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "\n" + secret))
		sign := r.Header.Get("sign")
		if decoded, e := url.QueryUnescape(sign); e == nil && strings.Contains(sign, "%") {
			sign = decoded
		}
		if !fresh(ts, true) || !equal(sign, base64.StdEncoding.EncodeToString(mac.Sum(nil))) {
			return Message{}, "", fail
		}
		var e struct {
			ID               string          `json:"msgId"`
			Content          json.RawMessage `json:"content"`
			ReplyExpires     int64           `json:"sessionWebhookExpiredTime"`
			User             string          `json:"senderStaffId"`
			Chat             string          `json:"conversationId"`
			Type             string          `json:"msgtype"`
			ConversationType string          `json:"conversationType"`
			Reply            string          `json:"sessionWebhook"`
			Text             struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Message{}, "", fail
		}
		var attachments []Attachment
		if e.Type != "text" {
			text, media, err := parseDingContent(e.Type, e.Content)
			if err != nil {
				return Message{}, "", fail
			}
			e.Text.Content, attachments = text, media
		}
		u, err := url.Parse(e.Reply)
		if err != nil || u.Scheme != "https" || u.Host != "oapi.dingtalk.com" || u.Path != "/robot/sendBySession" || u.User != nil || u.Fragment != "" {
			return Message{}, "", fail
		}
		return Message{ID: e.ID, User: e.User, Chat: e.Chat, Text: e.Text.Content, ReplyURL: e.Reply, ReplyExpires: e.ReplyExpires, Attachments: attachments, Group: e.ConversationType == "2"}, "", nil
	case "wecom":
		var envelope struct {
			Encrypt string `xml:"Encrypt"`
		}
		if xml.Unmarshal(raw, &envelope) != nil {
			return Message{}, "", fail
		}
		plain, err := wecomDecrypt(c, r, envelope.Encrypt)
		if err != nil {
			return Message{}, "", err
		}
		var e struct {
			To          string `xml:"ToUserName"`
			User        string `xml:"FromUserName"`
			ID          string `xml:"MsgId"`
			Type        string `xml:"MsgType"`
			Content     string `xml:"Content"`
			MediaID     string `xml:"MediaId"`
			Recognition string `xml:"Recognition"`
			FileName    string `xml:"FileName"`
			Format      string `xml:"Format"`
			AgentID     int64  `xml:"AgentID"`
		}
		if xml.Unmarshal(plain, &e) != nil || e.To != c.AppID || e.AgentID != c.AgentID {
			return Message{}, "", fail
		}
		m := Message{ID: e.ID, User: e.User, Chat: e.User, Text: e.Content}
		switch e.Type {
		case "text":
		case "image", "file":
			m.Attachments = []Attachment{{Kind: e.Type, ResourceKey: e.MediaID, Name: e.FileName}}
		case "voice":
			m.Attachments = []Attachment{{Kind: "audio", ResourceKey: e.MediaID, Name: "voice." + safeFileName(e.Format), Transcript: e.Recognition}}
		case "event":
			return Message{}, "", nil
		default:
			m.Attachments = []Attachment{{Kind: "unsupported"}}
		}
		return m, "", nil
	case "onebot":
		mac := hmac.New(sha1.New, []byte(os.Getenv(c.SecretEnv)))
		mac.Write(raw)
		if !equal(r.Header.Get("X-Signature"), "sha1="+hex.EncodeToString(mac.Sum(nil))) {
			return Message{}, "", fail
		}
		var e struct {
			Time   int64       `json:"time"`
			Self   json.Number `json:"self_id"`
			ID     json.Number `json:"message_id"`
			User   json.Number `json:"user_id"`
			Group  json.Number `json:"group_id"`
			Notice string      `json:"notice_type"`
			File   struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"file"`
			Post    string          `json:"post_type"`
			Type    string          `json:"message_type"`
			Raw     string          `json:"raw_message"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Message{}, "", fail
		}
		isFileNotice := e.Post == "notice" && (e.Notice == "group_upload" || e.Notice == "offline_file")
		if (e.Post != "message" && !isFileNotice) || e.User == e.Self {
			return Message{}, "", nil
		}
		if !fresh(strconv.FormatInt(e.Time, 10), false) {
			return Message{}, "", fail
		}
		text, attachments := parseOneBotMedia(e.Message, e.Raw)
		if isFileNotice {
			attachments = []Attachment{{Kind: "file", Name: e.File.Name, URL: e.File.URL}}
			if e.File.URL == "" {
				attachments[0].ResourceKey = e.File.ID
			}
		}
		chat := e.User.String()
		group := e.Type == "group" || e.Notice == "group_upload"
		if group {
			chat = e.Group.String()
		}
		return Message{ID: e.ID.String(), User: e.User.String(), Chat: chat, Text: text, Attachments: attachments, Group: group}, "", nil
	case "wechat":
		if !equal(r.Header.Get("Authorization"), "Bearer "+os.Getenv(c.SecretEnv)) {
			return Message{}, "", fail
		}
		var e struct {
			ID          string       `json:"id"`
			User        string       `json:"user_id"`
			Chat        string       `json:"chat_id"`
			Text        string       `json:"text"`
			Attachments []Attachment `json:"attachments"`
			Group       bool         `json:"group"`
			Timestamp   int64        `json:"timestamp"`
			Self        bool         `json:"self"`
		}
		if json.Unmarshal(raw, &e) != nil || !fresh(strconv.FormatInt(e.Timestamp, 10), false) {
			return Message{}, "", fail
		}
		if e.Self {
			return Message{}, "", nil
		}
		for i := range e.Attachments {
			e.Attachments[i].LocalPath = ""
		}
		return Message{ID: e.ID, User: e.User, Chat: e.Chat, Text: e.Text, Group: e.Group, Attachments: e.Attachments}, "", nil
	}
	return Message{}, "", fail
}
func decryptCBC(key, iv, data []byte) ([]byte, error) {
	fail := errors.New("invalid encrypted payload")
	block, err := aes.NewCipher(key)
	if err != nil || len(iv) != 16 || len(data) == 0 || len(data)%16 != 0 {
		return nil, fail
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	padding := int(out[len(out)-1])
	if padding < 1 || padding > 32 || padding > len(out) {
		return nil, fail
	}
	for _, b := range out[len(out)-padding:] {
		if int(b) != padding {
			return nil, fail
		}
	}
	return out[:len(out)-padding], nil
}
func wecomDecrypt(c config.ChatChannel, r *http.Request, encrypted string) ([]byte, error) {
	fail := errors.New("invalid wecom callback")
	q := r.URL.Query()
	if encrypted == "" || q.Get("nonce") == "" || !fresh(q.Get("timestamp"), false) {
		return nil, fail
	}
	parts := []string{os.Getenv(c.TokenEnv), q.Get("timestamp"), q.Get("nonce"), encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	if !equal(q.Get("msg_signature"), hex.EncodeToString(sum[:])) {
		return nil, fail
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(os.Getenv(c.EncryptKeyEnv), "="))
	if err != nil || len(key) != 32 {
		return nil, fail
	}
	b, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, fail
	}
	plain, err := decryptCBC(key, key[:16], b)
	if err != nil || len(plain) < 20 {
		return nil, fail
	}
	n := int(binary.BigEndian.Uint32(plain[16:20]))
	if n > len(plain)-20 || string(plain[20+n:]) != c.AppID {
		return nil, fail
	}
	return plain[20 : 20+n], nil
}
