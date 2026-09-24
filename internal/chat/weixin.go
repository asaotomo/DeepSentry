package chat

import (
	"ai-edr/internal/config"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const weixinBase = "https://ilinkai.weixin.qq.com"

func weixinHeaders(token string) map[string]string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	h := map[string]string{"AuthorizationType": "ilink_bot_token", "X-WECHAT-UIN": base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(b[:])), 10))), "iLink-App-ClientVersion": "1", "iLink-App-Id": "bot"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return h
}
func weixinAPI(ctx context.Context, client *http.Client, base, token, endpoint string, body any) (map[string]json.RawMessage, error) {
	if !trustedURL(base, "https", "weixin.qq.com") {
		return nil, errors.New("微信返回非受信任服务地址")
	}
	method := "POST"
	if body == nil {
		method = "GET"
	}
	return apiJSON(ctx, client, method, strings.TrimRight(base, "/")+"/ilink/bot/"+endpoint, weixinHeaders(token), body)
}
func weixinMetadata() map[string]string {
	return map[string]string{"channel_version": "1.0.0", "bot_agent": "DeepSentry/1.0.0"}
}
func (s *Service) runWeixin(ctx context.Context, c config.ChatChannel, update func(string)) {
	base := c.APIURL
	if base == "" {
		base = weixinBase
	}
	client := *s.client
	client.Timeout = 40 * time.Second
	cursorPath := s.cfg.Store + "." + c.Name + ".cursor"
	cursor, _ := os.ReadFile(cursorPath)
	for ctx.Err() == nil {
		data, err := weixinAPI(ctx, &client, base, secret(c), "getupdates", map[string]any{"get_updates_buf": string(cursor), "base_info": weixinMetadata()})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			update("reconnecting")
			if !pause(ctx, 5*time.Second) {
				return
			}
			continue
		}
		update("connected")
		var msgs []weixinInbound
		if json.Unmarshal(data["msgs"], &msgs) != nil && string(data["msgs"]) != "null" && len(data["msgs"]) > 0 {
			update("connection_failed")
			return
		}
		for _, m := range coalesceWeixinInbound(msgs) {
			if ctx.Err() != nil {
				return
			}
			if !weixinShouldHandle(m, c.AppID) {
				continue
			}
			text, attachments := parseWeixinItems(m.Items)
			chat := m.From
			if m.Group != "" {
				chat = m.Group
			}
			msg := Message{ID: rawString(m.ID), User: m.From, Chat: chat, Text: text, Group: m.Group != "", ContextToken: m.Context, Attachments: attachments}
			reply, err := s.command(c, msg)
			if err == nil && reply != "" {
				s.enqueue(delivery{channel: c, message: msg, text: reply})
			}
		}
		if next := jsonString(data, "get_updates_buf"); next != "" {
			cursor = []byte(next)
			_ = os.WriteFile(cursorPath, cursor, 0600)
		}
		// Guard against an upstream returning immediately with an empty update batch.
		if !pause(ctx, 300*time.Millisecond) {
			return
		}
	}
}
func (s *Service) sendWeixin(ctx context.Context, c config.ChatChannel, m Message, text string) error {
	m = s.latestReplyContext(c, m)
	if m.ContextToken == "" {
		return errors.New("缺少微信回复上下文，请用户重新发消息")
	}
	base := c.APIURL
	if base == "" {
		base = weixinBase
	}
	body := map[string]any{"from_user_id": "", "to_user_id": m.User, "client_id": firstNonEmpty(m.DeliveryID, randomID()), "message_type": 2, "message_state": 2, "context_token": m.ContextToken, "item_list": []any{map[string]any{"type": 1, "text_item": map[string]string{"text": text}}}}
	if m.Group {
		body["group_id"] = m.Chat
	}
	_, err := weixinAPI(ctx, s.client, base, secret(c), "sendmessage", map[string]any{"msg": body, "base_info": weixinMetadata()})
	return err
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
