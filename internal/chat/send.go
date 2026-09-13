package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"ai-edr/internal/config"
)

// post checks transport status and platform business errors, without logging tokens or response bodies.
func (s *Service) request(ctx context.Context, method, target, token string, body any) (map[string]json.RawMessage, error) {
	return s.requestLimited(ctx, method, target, token, body, 1<<20)
}

func (s *Service) requestLimited(ctx context.Context, method, target, token string, body any, limit int64) (map[string]json.RawMessage, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	// #nosec G704 -- callers use fixed platform endpoints, operator-configured bridge URLs, or the DingTalk callback URL validated by decode.
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("invalid reply endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// #nosec G704 -- endpoint origins are constrained at call sites; private operator-configured bridges are intentional.
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, &platformError{Message: "回复网络中断，送达结果未知", Uncertain: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, &platformError{Message: "平台回执读取失败，结果未知", Uncertain: true}
	}
	data := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &data) != nil {
		return nil, &platformError{Message: "平台返回无效回执，送达结果未知", Uncertain: true}
	}
	for _, key := range []string{"code", "errcode", "retcode"} {
		if b, ok := data[key]; ok && string(b) != "0" {
			return nil, fmt.Errorf("platform rejected reply (%s)", key)
		}
	}
	if b, ok := data["status"]; ok && string(b) == `"failed"` {
		return nil, errors.New("platform reply failed")
	}
	return data, nil
}
func jsonString(m map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return s
}
func (s *Service) send(d delivery) error {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	c, m := d.channel, d.message
	s.mu.Lock()
	stale := isQuick(c.Platform) && (s.connections[c.Name] == nil || s.generations[c.Name] != c.Generation)
	s.mu.Unlock()
	if stale {
		return errors.New("连接已断开")
	}
	chunks := deliveryChunks(c.Platform, d.text)
	for i, chunk := range chunks {
		if err := s.sendChunk(ctx, c, m, chunk); err != nil {
			return err
		}
		if i < len(chunks)-1 && !pause(ctx, 250*time.Millisecond) {
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) sendChunk(ctx context.Context, c config.ChatChannel, m Message, text string) error {
	text = clip(text, 12000)
	switch c.Platform {
	case "qqbot":
		return s.sendQQ(ctx, c, m, text)
	case "weixin":
		return s.sendWeixin(ctx, c, m, text)
	case "wecom_ws":
		s.mu.Lock()
		sender := s.senders[c.Name]
		s.mu.Unlock()
		if sender == nil {
			return &platformError{Message: "企业微信尚未连接", Retryable: true}
		}
		return sender(ctx, m, text)

	case "feishu", "feishu_ws":
		token, err := s.feishuToken(ctx, c)
		if err != nil {
			return err
		}
		content, _ := json.Marshal(map[string]string{"text": text})
		_, err = s.request(ctx, "POST", "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=chat_id", token, map[string]string{"receive_id": m.Chat, "msg_type": "text", "content": string(content)})
		return err
	case "dingtalk":
		if m.ReplyExpires > 0 && time.Now().UnixMilli() >= m.ReplyExpires {
			token, err := s.dingToken(ctx, c)
			if err != nil {
				return err
			}
			return s.sendDingTemplate(ctx, c, m, token, "sampleText", map[string]string{"content": text})
		}
		_, err := s.request(ctx, "POST", m.ReplyURL, "", map[string]any{"msgtype": "text", "text": map[string]string{"content": text}})
		return err
	case "wecom":
		endpoint := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=" + url.QueryEscape(c.AppID) + "&corpsecret=" + url.QueryEscape(os.Getenv(c.SecretEnv))
		data, err := s.request(ctx, "GET", endpoint, "", nil)
		if err != nil {
			return preflightError(err)
		}
		token := jsonString(data, "access_token")
		if token == "" {
			return errors.New("missing WeCom access token")
		}
		_, err = s.request(ctx, "POST", "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token="+url.QueryEscape(token), "", map[string]any{"touser": m.User, "msgtype": "text", "agentid": c.AgentID, "text": map[string]string{"content": text}})
		return err
	case "onebot":
		body := map[string]any{"message": text, "auto_escape": true, "message_type": "private", "user_id": m.User}
		if m.Group {
			body = map[string]any{"message": text, "auto_escape": true, "message_type": "group", "group_id": m.Chat}
		}
		_, err := s.request(ctx, "POST", strings.TrimRight(c.APIURL, "/")+"/send_msg", os.Getenv(c.AccessTokenEnv), body)
		return err
	case "wechat":
		_, err := s.request(ctx, "POST", c.APIURL, os.Getenv(c.AccessTokenEnv), map[string]any{"chat_id": m.Chat, "user_id": m.User, "group": m.Group, "text": text})
		return err
	}
	return errors.New("unknown reply channel")
}

func deliveryChunks(platform, text string) []string {
	text = formatMobileText(text)
	if text == "" {
		return nil
	}
	chunks := chunkMobile(text, platformBubbleLimit(platform))
	if len(chunks) == 0 {
		chunks = []string{clip(text, 12000)}
	}
	// The application API limits text by UTF-8 bytes, unlike our rune-based bubbles.
	if platform == "wecom" {
		var bounded []string
		for _, chunk := range chunks {
			bounded = append(bounded, textPages(chunk, 1800)...)
		}
		chunks = bounded
	}
	return chunks
}
