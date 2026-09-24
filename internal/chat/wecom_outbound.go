package chat

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"

	"ai-edr/internal/config"
)

func (s *Service) sendWecomSocketFile(ctx context.Context, c config.ChatChannel, m Message, b []byte, name string) error {
	s.mu.Lock()
	p := s.mediaPeers[c.Name]
	current := s.generations[c.Name] == c.Generation
	s.mu.Unlock()
	if p == nil || !current {
		return &platformError{Message: "企业微信尚未连接", Retryable: true}
	}
	kind := "file"
	if looksLikeImage(b) && len(b) <= 2<<20 {
		kind = "image"
	}
	const chunkSize = 512 * 1024
	sum := md5.Sum(b)
	raw, err := p.requestBody(ctx, "aibot_upload_media_init", map[string]any{"type": kind, "filename": name, "total_size": len(b), "total_chunks": (len(b) + chunkSize - 1) / chunkSize, "md5": hex.EncodeToString(sum[:])})
	if err != nil {
		return preflightError(err)
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || jsonString(data, "upload_id") == "" {
		return errors.New("企业微信未返回上传 ID")
	}
	id := jsonString(data, "upload_id")
	for off, index := 0, 0; off < len(b); off, index = off+chunkSize, index+1 {
		err = p.request(ctx, "aibot_upload_media_chunk", map[string]any{"upload_id": id, "chunk_index": index, "base64_data": base64.StdEncoding.EncodeToString(b[off:min(off+chunkSize, len(b))])})
		if err != nil {
			return preflightError(err)
		}
	}
	raw, err = p.requestBody(ctx, "aibot_upload_media_finish", map[string]string{"upload_id": id})
	if err != nil {
		return preflightError(err)
	}
	if json.Unmarshal(raw, &data) != nil || jsonString(data, "media_id") == "" {
		return errors.New("企业微信未返回素材 ID")
	}
	return p.request(ctx, "aibot_send_msg", map[string]any{"chatid": m.Chat, "msgtype": kind, kind: map[string]string{"media_id": jsonString(data, "media_id")}})
}

func (s *Service) sendWecomAppFile(ctx context.Context, c config.ChatChannel, m Message, b []byte, name string) error {
	data, err := s.request(ctx, "GET", "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid="+url.QueryEscape(c.AppID)+"&corpsecret="+url.QueryEscape(secret(c)), "", nil)
	if err != nil {
		return preflightError(err)
	}
	token := jsonString(data, "access_token")
	if token == "" {
		return errors.New("企业微信未返回访问令牌")
	}
	kind := "file"
	if looksLikeImage(b) && len(b) <= 2<<20 {
		kind = "image"
	}
	data, err = s.uploadMultipart(ctx, "https://qyapi.weixin.qq.com/cgi-bin/media/upload?access_token="+url.QueryEscape(token)+"&type="+kind, "", "media", name, b, nil)
	if err != nil {
		return err
	}
	id := jsonString(data, "media_id")
	if id == "" {
		return errors.New("企业微信未返回素材 ID")
	}
	_, err = s.request(ctx, "POST", "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token="+url.QueryEscape(token), "", map[string]any{"touser": m.User, "agentid": c.AgentID, "msgtype": kind, kind: map[string]string{"media_id": id}})
	return err
}
