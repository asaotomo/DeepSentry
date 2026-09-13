package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"ai-edr/internal/config"
)

type oneBotSegment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

func (p *oneBotSegment) UnmarshalJSON(raw []byte) error {
	var value struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	p.Type = value.Type
	p.Data = map[string]string{}
	for key, b := range value.Data {
		var text string
		if json.Unmarshal(b, &text) == nil {
			p.Data[key] = text
			continue
		}
		var scalar any
		if json.Unmarshal(b, &scalar) == nil {
			switch scalar.(type) {
			case float64, bool:
				p.Data[key] = string(b)
			}
		}
	}
	return nil
}

func parseOneBotMedia(raw json.RawMessage, fallback string) (string, []Attachment) {
	var parts []oneBotSegment
	if json.Unmarshal(raw, &parts) != nil {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			text = fallback
		}
		// CQ decoding is applied after segmentation, so escaped literal CQ remains text.
		for len(text) > 0 {
			start := strings.Index(text, "[CQ:")
			if start < 0 {
				parts = append(parts, oneBotSegment{Type: "text", Data: map[string]string{"text": decodeCQ(text)}})
				break
			}
			if start > 0 {
				parts = append(parts, oneBotSegment{Type: "text", Data: map[string]string{"text": decodeCQ(text[:start])}})
			}
			end := strings.IndexByte(text[start:], ']')
			if end < 0 {
				parts = append(parts, oneBotSegment{Type: "text", Data: map[string]string{"text": decodeCQ(text[start:])}})
				break
			}
			fields := strings.Split(text[start+4:start+end], ",")
			p := oneBotSegment{Type: fields[0], Data: map[string]string{}}
			for _, field := range fields[1:] {
				k, v, ok := strings.Cut(field, "=")
				if ok {
					p.Data[k] = decodeCQ(v)
				}
			}
			parts = append(parts, p)
			text = text[start+end+1:]
		}
	}
	var text strings.Builder
	var attachments []Attachment
	for _, p := range parts {
		switch p.Type {
		case "text":
			text.WriteString(p.Data["text"])
		case "image", "record", "file":
			kind := p.Type
			if kind == "record" {
				kind = "audio"
			}
			a := Attachment{Kind: kind, Name: firstNonEmpty(p.Data["name"], p.Data["file_name"]), URL: p.Data["url"]}
			if a.URL == "" {
				file := p.Data["file"]
				if publicMediaURL(file) || platformMediaURL(file) {
					a.URL = file
				} else {
					a.ResourceKey = firstNonEmpty(p.Data["file_id"], file)
				}
			}
			attachments = append(attachments, a)
		case "video":
			attachments = append(attachments, Attachment{Kind: "unsupported"})
		}
	}
	return text.String(), attachments
}
func decodeCQ(s string) string {
	return strings.NewReplacer("&#91;", "[", "&#93;", "]", "&#44;", ",", "&amp;", "&").Replace(s)
}

func (s *Service) downloadWecomAppMedia(ctx context.Context, c config.ChatChannel, a Attachment) ([]byte, error) {
	data, err := s.request(ctx, "GET", "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid="+url.QueryEscape(c.AppID)+"&corpsecret="+url.QueryEscape(secret(c)), "", nil)
	if err != nil {
		return nil, err
	}
	token := jsonString(data, "access_token")
	if token == "" {
		return nil, errors.New("企业微信未返回访问令牌")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://qyapi.weixin.qq.com/cgi-bin/media/get?access_token="+url.QueryEscape(token)+"&media_id="+url.QueryEscape(a.ResourceKey), nil)
	if err != nil {
		return nil, err
	}
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("企业微信素材下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, responseError(resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxMediaBytes {
		return nil, errors.New("素材为空、读取失败或超过 20 MiB")
	}
	var failure struct {
		Code *int `json:"errcode"`
	}
	if json.Unmarshal(b, &failure) == nil && failure.Code != nil && *failure.Code != 0 {
		return nil, errors.New("企业微信素材不可用，请检查权限或重新发送")
	}
	return b, nil
}

func (s *Service) downloadOneBotMedia(ctx context.Context, c config.ChatChannel, a Attachment) ([]byte, error) {
	method := "get_file"
	body := map[string]string{"file_id": a.ResourceKey}
	if a.Kind == "image" {
		method = "get_image"
		body = map[string]string{"file": a.ResourceKey}
	}
	if a.Kind == "audio" {
		method = "get_record"
		body = map[string]string{"file": a.ResourceKey, "out_format": "mp3"}
	}
	data, err := s.requestLimited(ctx, "POST", strings.TrimRight(c.APIURL, "/")+"/"+method, os.Getenv(c.AccessTokenEnv), body, int64(base64.StdEncoding.EncodedLen(maxMediaBytes))+(64<<10))
	if err != nil {
		return nil, err
	}
	var resource map[string]json.RawMessage
	if json.Unmarshal(data["data"], &resource) != nil {
		return nil, errors.New("桥接器未返回附件资源")
	}
	if encoded := jsonString(resource, "base64"); encoded != "" {
		if len(encoded) > base64.StdEncoding.EncodedLen(maxMediaBytes) {
			return nil, errors.New("桥接附件超过 20 MiB")
		}
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(b) == 0 || len(b) > maxMediaBytes {
			return nil, errors.New("桥接附件 Base64 无效或过大")
		}
		return b, nil
	}
	a.URL = firstNonEmpty(jsonString(resource, "url"), jsonString(resource, "file"))
	// Bridge-local paths must never be read on the Agent host or sent as URLs.
	if !publicMediaURL(a.URL) && !platformMediaURL(a.URL) {
		return readBridgeMediaFile(c.MediaRoot, a.URL)
	}
	a.ResourceKey = ""
	return s.downloadMedia(ctx, a)
}

func (s *Service) sendBridgeFile(ctx context.Context, c config.ChatChannel, m Message, b []byte, name string) error {
	encoded := base64.StdEncoding.EncodeToString(b)
	if c.Platform == "wechat" {
		kind := "file"
		if looksLikeImage(b) {
			kind = "image"
		}
		data, err := s.request(ctx, "POST", c.APIURL, os.Getenv(c.AccessTokenEnv), map[string]any{"chat_id": m.Chat, "user_id": m.User, "group": m.Group, "text": "", "attachments": []any{map[string]string{"kind": kind, "name": name, "encoding": "base64", "data": encoded}}})
		if err != nil {
			return err
		}
		var accepted bool
		if json.Unmarshal(data["media_accepted"], &accepted) != nil || !accepted {
			return errors.New("桥接器未确认附件投递，请实现 media_accepted 回执后重试")
		}
		return nil
	}
	base := strings.TrimRight(c.APIURL, "/")
	if looksLikeImage(b) {
		body := map[string]any{"message_type": "private", "user_id": m.User, "message": []oneBotSegment{{Type: "image", Data: map[string]string{"file": "base64://" + encoded}}}}
		if m.Group {
			delete(body, "user_id")
			body["message_type"] = "group"
			body["group_id"] = m.Chat
		}
		_, err := s.request(ctx, "POST", base+"/send_msg", os.Getenv(c.AccessTokenEnv), body)
		return err
	}
	// NapCat-compatible file extension. Plain OneBot 11 does not specify files.
	method := "upload_private_file"
	body := map[string]string{"user_id": m.User, "file": "base64://" + encoded, "name": name}
	if m.Group {
		method = "upload_group_file"
		delete(body, "user_id")
		body["group_id"] = m.Chat
	}
	_, err := s.request(ctx, "POST", base+"/"+method, os.Getenv(c.AccessTokenEnv), body)
	return err
}

// Only a resource resolved by the authenticated bridge API may reach this path.
// A configured shared directory is required; symlink escapes are rejected.
func readBridgeMediaFile(root, path string) ([]byte, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("桥接器返回本地路径；请配置共享附件目录 media_root 或让桥接器返回 HTTPS 下载地址")
	}
	if strings.HasPrefix(path, "file://") {
		u, err := url.Parse(path)
		if err != nil || u.Host != "" {
			return nil, errors.New("桥接文件路径无效")
		}
		path = u.Path
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("共享附件目录不可用")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("桥接器必须返回绝对文件路径")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, errors.New("共享附件不可用")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("附件不在配置的共享目录内")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootHandle.Close()
	f, err := rootHandle.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("附件不是普通文件")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxMediaBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxMediaBytes {
		return nil, errors.New("附件为空、读取失败或超过 20 MiB")
	}
	return b, nil
}
