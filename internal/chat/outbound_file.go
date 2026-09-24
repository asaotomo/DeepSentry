package chat

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-edr/internal/config"
)

type sendFile struct {
	Path string
	Name string
}

func listSendFiles(dir string) ([]sendFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []sendFile
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".sent") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxMediaBytes {
			continue
		}
		out = append(out, sendFile{Path: filepath.Join(dir, e.Name()), Name: e.Name()})

	}
	return out, nil
}

func (s *Service) deliverOutboundFiles(ctx context.Context, c config.ChatChannel, m Message, dir string) error {
	s.fileQueueMu.Lock()
	defer s.fileQueueMu.Unlock()
	files, err := listSendFiles(dir)
	if err != nil || len(files) == 0 {
		return err
	}
	var failed []string
	for _, f := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.queueOutboundFile(c, m, f); err != nil {
			failed = append(failed, f.Name+"："+err.Error())
			continue
		}
		_ = os.Rename(f.Path, f.Path+".sent")
	}
	if len(failed) > 0 {
		return errors.New("部分文件未能加入发送队列：" + strings.Join(failed, "；"))
	}
	return nil
}

func (s *Service) sendOutboundFile(ctx context.Context, c config.ChatChannel, m Message, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("读取待发送文件失败")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMediaBytes+1))
	if err != nil || len(b) == 0 {
		return errors.New("读取待发送文件失败")
	}
	if len(b) > maxMediaBytes {
		return errors.New("发送文件不能超过 20 MiB")
	}
	name = safeFileName(firstNonEmpty(name, filepath.Base(path)))
	switch c.Platform {
	case "onebot", "wechat":
		return s.sendBridgeFile(ctx, c, m, b, name)
	case "dingtalk":
		return s.sendDingFile(ctx, c, m, b, name)
	case "feishu", "feishu_ws":
		return s.sendFeishuFile(ctx, c, m, b, name)
	case "wecom_ws":
		return s.sendWecomSocketFile(ctx, c, m, b, name)
	case "wecom":
		return s.sendWecomAppFile(ctx, c, m, b, name)
	case "weixin":
		return s.sendWeixinFile(ctx, c, m, b, name)
	case "qqbot":
		return s.sendQQFile(ctx, c, m, b, name)
	default:
		return fmt.Errorf("%s 当前通道暂不支持直接发送文件", c.Platform)
	}
}

func (s *Service) sendWeixinFile(ctx context.Context, c config.ChatChannel, m Message, plain []byte, name string) error {
	m = s.latestReplyContext(c, m)
	if m.ContextToken == "" {
		return errors.New("缺少微信回复上下文，请用户重新发消息")
	}
	base := c.APIURL
	if base == "" {
		base = weixinBase
	}
	key := make([]byte, 16)
	filekeyBytes := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if _, err := rand.Read(filekeyBytes); err != nil {
		return err
	}
	cipherText, err := encryptMedia(plain, key)
	if err != nil {
		return err
	}
	filekey := hex.EncodeToString(filekeyBytes)
	sum := md5.Sum(plain)
	mediaType := 3
	if looksLikeImage(plain) {
		mediaType = 1
	}
	data, err := weixinAPI(ctx, s.client, base, secret(c), "getuploadurl", map[string]any{
		"filekey": filekey, "media_type": mediaType, "to_user_id": m.User,
		"rawsize": len(plain), "rawfilemd5": hex.EncodeToString(sum[:]), "filesize": len(cipherText),
		"no_need_thumb": true, "aeskey": hex.EncodeToString(key), "base_info": weixinMetadata(),
	})
	if err != nil {
		return err
	}
	uploadParam := jsonString(data, "upload_param")
	if uploadParam == "" {
		return errors.New("微信未返回上传参数")
	}
	downloadParam, err := s.postWeixinCDN(ctx, uploadParam, filekey, cipherText)
	if err != nil {
		return err
	}
	aesKeyJSON := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
	media := map[string]any{"encrypt_query_param": downloadParam, "aes_key": aesKeyJSON, "encrypt_type": 1}
	var item map[string]any
	if mediaType == 1 {
		item = map[string]any{"type": 2, "image_item": map[string]any{"media": media, "mid_size": len(cipherText)}}
	} else {
		item = map[string]any{"type": 4, "file_item": map[string]any{"media": media, "file_name": name, "md5": hex.EncodeToString(sum[:]), "len": fmt.Sprintf("%d", len(plain))}}
	}
	body := map[string]any{"from_user_id": "", "to_user_id": m.User, "client_id": randomID(), "message_type": 2, "message_state": 2, "context_token": m.ContextToken, "item_list": []any{item}}
	if m.Group {
		body["group_id"] = m.Chat
	}
	_, err = weixinAPI(ctx, s.client, base, secret(c), "sendmessage", map[string]any{"msg": body, "base_info": weixinMetadata()})
	return err
}

func (s *Service) postWeixinCDN(ctx context.Context, uploadParam, filekey string, cipherText []byte) (string, error) {
	target := weixinCDNBase + "/upload?encrypted_query_param=" + url.QueryEscape(uploadParam) + "&filekey=" + url.QueryEscape(filekey)
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(cipherText))
	if err != nil {
		return "", errors.New("微信上传地址无效")
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; DeepSentryChat/1.0; MicroMessenger)")
	resp, err := s.mediaClientFor(target).Do(req)
	if err != nil {
		return "", errors.New("微信 CDN 上传失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("微信 CDN 上传 HTTP %d", resp.StatusCode)
	}
	param := strings.TrimSpace(resp.Header.Get("x-encrypted-param"))
	if param == "" {
		return "", errors.New("微信 CDN 未返回下载参数")
	}
	return param, nil
}

func (s *Service) sendQQFile(ctx context.Context, c config.ChatChannel, m Message, plain []byte, name string) error {
	token, err := s.qqToken(ctx, c)
	if err != nil {
		return preflightError(err)
	}
	fileType := 4
	if looksLikeImage(plain) {
		fileType = 1
	}
	path := "/v2/users/" + url.PathEscape(m.User) + "/files"
	if m.Group {
		path = "/v2/groups/" + url.PathEscape(m.Chat) + "/files"
	}
	_, err = apiJSON(ctx, s.client, "POST", "https://api.sgroup.qq.com"+path, map[string]string{"Authorization": "QQBot " + token}, map[string]any{
		"file_type": fileType, "file_data": base64.StdEncoding.EncodeToString(plain), "srv_send_msg": true, "file_name": name,
	})
	return err
}

// Store immutable bytes outside the task control directory before acknowledging
// queueing. File delivery uses the same authorization, retry and uncertainty
// policy as text and survives task cleanup and daemon restart.
func (s *Service) queueOutboundFile(c config.ChatChannel, m Message, f sendFile) error {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	if len(b) == 0 || len(b) > maxMediaBytes {
		return errors.New("待发送文件大小无效")
	}
	digest := sha256.Sum256([]byte(f.Path))
	id := "file-" + hex.EncodeToString(digest[:16])
	dir := filepath.Join(s.cfg.Store+".outbox", "files", id)
	path := filepath.Join(dir, safeFileName(f.Name))
	s.box.mu.Lock()
	defer s.box.mu.Unlock()
	if _, ok := s.box.items[id]; ok {
		return nil
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// #nosec G703 -- path is rooted in operator-configured outbox storage; name is reduced to a basename by safeFileName.
	if err = os.WriteFile(path, b, 0600); err != nil {
		return err
	}
	item := &OutboxItem{ID: id, Channel: c, Message: m, File: &sendFile{Path: path, Name: f.Name}, Status: "pending", Created: time.Now()}
	item.Channel.AppSecret = ""
	item.Channel.PairCode = ""
	item.Message.Attachments = nil
	item.Message.Text = ""
	if err = s.saveOutbox(item); err != nil {
		return err
	}
	s.box.items[id] = item
	return nil
}
