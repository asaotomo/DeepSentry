package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"ai-edr/internal/config"
)

// parseFeishuContent is shared by webhook and WebSocket ingress. Resource keys
// remain opaque until the authorized task downloads them using its own account.
func parseFeishuContent(kind, raw string) (string, []Attachment, error) {
	var body struct {
		Text     string `json:"text"`
		ImageKey string `json:"image_key"`
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return "", nil, err
	}
	switch kind {
	case "text":
		return body.Text, nil, nil
	case "image":
		return "", []Attachment{{Kind: "image", ResourceKey: body.ImageKey}}, nil
	case "file", "media":
		return "", []Attachment{{Kind: "file", ResourceKey: body.FileKey, Name: body.FileName}}, nil
	case "audio":
		return "", []Attachment{{Kind: "audio", ResourceKey: body.FileKey, Name: "voice.opus"}}, nil
	case "post":
		type element struct {
			Tag      string `json:"tag"`
			Text     string `json:"text"`
			Href     string `json:"href"`
			ImageKey string `json:"image_key"`
			FileKey  string `json:"file_key"`
		}
		type post struct {
			Title   string      `json:"title"`
			Content [][]element `json:"content"`
		}
		var p post
		_ = json.Unmarshal([]byte(raw), &p)
		if p.Content == nil {
			var localized map[string]post
			if err := json.Unmarshal([]byte(raw), &localized); err != nil {
				return "", nil, err
			}
			// Stable preference; never concatenate translations or duplicate images.
			for _, locale := range []string{"zh_cn", "en_us", "ja_jp"} {
				if v, ok := localized[locale]; ok {
					p = v
					break
				}
			}
			if p.Content == nil {
				locales := make([]string, 0, len(localized))
				for locale := range localized {
					locales = append(locales, locale)
				}
				sort.Strings(locales)
				if len(locales) > 0 {
					p = localized[locales[0]]
				}
			}
		}
		var lines []string
		var attachments []Attachment
		if p.Title != "" {
			lines = append(lines, p.Title)
		}
		for _, row := range p.Content {
			var line strings.Builder
			for _, e := range row {
				switch e.Tag {
				case "text":
					line.WriteString(e.Text)
				case "a":
					line.WriteString(e.Text + " (" + e.Href + ")")
				case "media":
					attachments = append(attachments, Attachment{Kind: "file", ResourceKey: e.FileKey})
				case "img":
					attachments = append(attachments, Attachment{Kind: "image", ResourceKey: e.ImageKey})
				}
			}
			lines = append(lines, line.String())
		}
		return strings.TrimSpace(strings.Join(lines, "\n")), attachments, nil
	default:
		return "", []Attachment{{Kind: "unsupported"}}, nil
	}
}

func (s *Service) feishuToken(ctx context.Context, c config.ChatChannel) (string, error) {
	data, err := s.request(ctx, "POST", "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", "", map[string]string{"app_id": c.AppID, "app_secret": secret(c)})
	if err != nil {
		return "", preflightError(err)
	}
	token := jsonString(data, "tenant_access_token")
	if token == "" {
		return "", errors.New("飞书未返回访问令牌")
	}
	return token, nil
}

func (s *Service) downloadChannelMedia(ctx context.Context, c config.ChatChannel, m Message, a Attachment) ([]byte, error) {
	if a.LocalPath != "" {
		b, err := s.readPrefetchedMedia(a.LocalPath)
		if err == nil {
			return b, nil
		}
		if a.URL == "" && len(a.Fallbacks) == 0 && a.ResourceKey == "" {
			return nil, err
		}
	}
	if a.ResourceKey == "" {
		return s.downloadMedia(ctx, a)
	}
	if c.Platform == "dingtalk" {
		return s.downloadDingMedia(ctx, c, a)
	}
	if c.Platform == "wecom" {
		return s.downloadWecomAppMedia(ctx, c, a)
	}
	if c.Platform == "onebot" {
		return s.downloadOneBotMedia(ctx, c, a)
	}
	if c.Platform != "feishu" && c.Platform != "feishu_ws" {
		return nil, errors.New("当前渠道不支持该附件资源")
	}
	if m.ID == "" {
		return nil, errors.New("缺少附件所属消息 ID")
	}
	token, err := s.feishuToken(ctx, c)
	if err != nil {
		return nil, err
	}
	kind := "file"
	if a.Kind == "image" {
		kind = "image"
	}
	target := "https://open.feishu.cn/open-apis/im/v1/messages/" + url.PathEscape(m.ID) + "/resources/" + url.PathEscape(a.ResourceKey) + "?type=" + kind
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Authenticated resource requests must never redirect credentials to a CDN.
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("飞书附件下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("飞书附件下载 HTTP %d；请检查消息资源权限", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxMediaBytes {
		return nil, errors.New("附件为空、读取失败或超过 20 MiB")
	}
	return b, nil
}

func (s *Service) uploadMultipart(ctx context.Context, target, token, field, name string, b []byte, fields map[string]string) (map[string]json.RawMessage, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(b); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("附件上传失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("附件上传回执读取失败")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return nil, errors.New("附件上传回执无效")
	}
	for _, key := range []string{"code", "errcode"} {
		if value, ok := data[key]; ok && string(value) != "0" {
			return nil, errors.New("平台拒绝附件上传，请检查权限、大小和文件格式")
		}
	}
	return data, nil
}

func (s *Service) sendFeishuFile(ctx context.Context, c config.ChatChannel, m Message, b []byte, name string) error {
	token, err := s.feishuToken(ctx, c)
	if err != nil {
		return err
	}
	kind, field, key := "file", "file", "file_key"
	fields := map[string]string{"file_type": "stream", "file_name": name}
	endpoint := "https://open.feishu.cn/open-apis/im/v1/files"
	if looksLikeImage(b) {
		kind, field, key = "image", "image", "image_key"
		fields = map[string]string{"image_type": "message"}
		endpoint = "https://open.feishu.cn/open-apis/im/v1/images"
	}
	data, err := s.uploadMultipart(ctx, endpoint, token, field, name, b, fields)
	if err != nil {
		return err
	}
	var resource map[string]json.RawMessage
	if json.Unmarshal(data["data"], &resource) != nil || jsonString(resource, key) == "" {
		return errors.New("飞书未返回附件资源标识")
	}
	content, _ := json.Marshal(map[string]string{key: jsonString(resource, key)})
	_, err = s.request(ctx, "POST", "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=chat_id", token, map[string]string{"receive_id": m.Chat, "msg_type": kind, "content": string(content)})
	return err
}
