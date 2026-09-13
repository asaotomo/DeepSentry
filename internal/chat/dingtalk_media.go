package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"ai-edr/internal/config"
)

func parseDingContent(kind string, raw json.RawMessage) (string, []Attachment, error) {
	var c struct {
		DownloadCode        string `json:"downloadCode"`
		PictureDownloadCode string `json:"pictureDownloadCode"`
		FileName            string `json:"fileName"`
		Recognition         string `json:"recognition"`
		RichText            []struct {
			Text                string `json:"text"`
			Type                string `json:"type"`
			PictureDownloadCode string `json:"pictureDownloadCode"`
			DownloadCode        string `json:"downloadCode"`
		} `json:"richText"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &c) != nil {
		return "", nil, errors.New("钉钉附件格式无效")
	}
	switch kind {
	case "picture":
		return "", []Attachment{{Kind: "image", ResourceKey: firstNonEmpty(c.PictureDownloadCode, c.DownloadCode)}}, nil
	case "file":
		return "", []Attachment{{Kind: "file", ResourceKey: c.DownloadCode, Name: c.FileName}}, nil
	case "audio":
		return "", []Attachment{{Kind: "audio", ResourceKey: c.DownloadCode, Transcript: c.Recognition, Name: "voice.amr"}}, nil
	case "richText":
		var text strings.Builder
		var attachments []Attachment
		for _, e := range c.RichText {
			if e.Text != "" {
				text.WriteString(e.Text)
			}
			if e.Type == "picture" || e.PictureDownloadCode != "" {
				attachments = append(attachments, Attachment{Kind: "image", ResourceKey: firstNonEmpty(e.PictureDownloadCode, e.DownloadCode)})
			}
		}
		if strings.TrimSpace(text.String()) == "" && len(attachments) == 0 {
			attachments = []Attachment{{Kind: "unsupported"}}
		}
		return text.String(), attachments, nil
	default:
		return "", []Attachment{{Kind: "unsupported"}}, nil
	}
}

func (s *Service) dingToken(ctx context.Context, c config.ChatChannel) (string, error) {
	if c.AppID == "" || secret(c) == "" {
		return "", errors.New("钉钉媒体及主动回复需要配置 app_id（机器人 AppKey）和应用 AppSecret")
	}
	d, err := apiJSON(ctx, s.client, "POST", "https://api.dingtalk.com/v1.0/oauth2/accessToken", nil, map[string]string{"appKey": c.AppID, "appSecret": secret(c)})
	if err != nil {
		return "", preflightError(err)
	}
	token := jsonString(d, "accessToken")
	if token == "" {
		return "", errors.New("钉钉未返回访问令牌")
	}
	return token, nil
}
func (s *Service) downloadDingMedia(ctx context.Context, c config.ChatChannel, a Attachment) ([]byte, error) {
	token, err := s.dingToken(ctx, c)
	if err != nil {
		return nil, err
	}
	d, err := apiJSON(ctx, s.client, "POST", "https://api.dingtalk.com/v1.0/robot/messageFiles/download", map[string]string{"x-acs-dingtalk-access-token": token}, map[string]string{"robotCode": c.AppID, "downloadCode": a.ResourceKey})
	if err != nil {
		return nil, preflightError(err)
	}
	target := jsonString(d, "downloadUrl")
	if target == "" {
		return nil, errors.New("钉钉未返回附件下载地址；请检查下载码有效期与应用权限")
	}
	// The token is used only at the fixed OpenAPI endpoint, never on the CDN.
	return s.downloadMedia(ctx, Attachment{URL: target})
}
func (s *Service) sendDingTemplate(ctx context.Context, c config.ChatChannel, m Message, token, key string, params map[string]string) error {
	encoded, _ := json.Marshal(params)
	body := map[string]any{"robotCode": c.AppID, "msgKey": key, "msgParam": string(encoded)}
	endpoint := "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend"
	if m.Group {
		endpoint = "https://api.dingtalk.com/v1.0/robot/groupMessages/send"
		body["openConversationId"] = m.Chat
	} else {
		body["userIds"] = []string{m.User}
	}
	d, err := apiJSON(ctx, s.client, "POST", endpoint, map[string]string{"x-acs-dingtalk-access-token": token}, body)
	if err != nil {
		return err
	}
	var invalid []string
	_ = json.Unmarshal(d["invalidStaffIdList"], &invalid)
	var forbidden []string
	_ = json.Unmarshal(d["flowControlledStaffIdList"], &forbidden)
	if len(invalid) > 0 || len(forbidden) > 0 {
		return errors.New("钉钉未向接收人送达，请检查用户标识或限流状态")
	}
	if jsonString(d, "processQueryKey") == "" {
		return &platformError{Message: "钉钉未返回消息投递标识，请查询投递状态", Uncertain: true}
	}
	return nil
}
func (s *Service) sendDingFile(ctx context.Context, c config.ChatChannel, m Message, b []byte, name string) error {
	token, err := s.dingToken(ctx, c)
	if err != nil {
		return err
	}
	kind := "file"
	if looksLikeImage(b) {
		kind = "image"
	}
	// Legacy media upload takes the token in the query; uploadMultipart never logs it or follows redirects.
	d, err := s.uploadMultipart(ctx, "https://oapi.dingtalk.com/media/upload?access_token="+url.QueryEscape(token)+"&type="+kind, "", "media", name, b, nil)
	if err != nil {
		return err
	}
	media := jsonString(d, "media_id")
	if media == "" {
		return errors.New("钉钉未返回媒体标识")
	}
	key := "sampleFile"
	params := map[string]string{"mediaId": media, "fileName": name, "fileType": strings.TrimPrefix(filepath.Ext(name), ".")}
	if kind == "image" {
		key = "sampleImageMsg"
		params = map[string]string{"photoURL": media}
	}
	return s.sendDingTemplate(ctx, c, m, token, key, params)
}
