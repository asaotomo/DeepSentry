package chat

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/skip2/go-qrcode"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (st *setupState) begin(ctx context.Context, in setupInput) error {
	all, err := loadBindings(st.cfg)
	if err != nil {
		return err
	}
	if _, exists := all[in.Platform]; exists {
		return errors.New("请先断开此平台的现有连接")
	}
	delete(st.pending, in.Platform)
	p := &pendingLogin{Platform: in.Platform, Expires: time.Now().Add(5 * time.Minute), Interval: 3 * time.Second, AllowBatch: in.AllowBatch, State: randomID()}
	switch in.Platform {
	case "feishu":
		data, err := st.registration(ctx, url.Values{"action": {"begin"}, "archetype": {"PersonalAgent"}, "auth_method": {"client_secret"}, "request_user_info": {"open_id tenant_brand"}})
		if err != nil {
			return err
		}
		p.Device = jsonString(data, "device_code")
		user := jsonString(data, "user_code")
		if p.Device == "" || user == "" {
			return errors.New("飞书未返回完整授权码")
		}
		expiry, interval := 600, 5
		_ = json.Unmarshal(data["expire_in"], &expiry)
		if _, ok := data["expire_in"]; !ok {
			_ = json.Unmarshal(data["expires_in"], &expiry)
		}
		_ = json.Unmarshal(data["interval"], &interval)
		if expiry <= 0 || expiry > 1800 {
			expiry = 600
		}
		if interval < 5 {
			interval = 5
		}
		if interval > 60 {
			interval = 60
		}
		p.Expires = time.Now().Add(time.Duration(expiry) * time.Second)
		p.Interval = time.Duration(interval) * time.Second
		p.URL = "https://open.feishu.cn/page/cli?" + url.Values{"user_code": {user}, "from": {"deepsentry"}, "name": {"DeepSentry"}}.Encode()
	case "qq":
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		p.Key = base64.StdEncoding.EncodeToString(key)
		data, err := apiJSON(ctx, st.client, "POST", "https://q.qq.com/lite/create_bind_task", nil, map[string]string{"key": p.Key})
		if err != nil {
			return err
		}
		var task struct {
			ID string `json:"task_id"`
		}
		_ = json.Unmarshal(data["data"], &task)
		if task.ID == "" {
			return errors.New("QQ 未返回绑定任务编号")
		}
		p.Device = task.ID
		p.URL = "https://q.qq.com/qqbot/openclaw/connect.html?" + url.Values{"task_id": {task.ID}, "_wv": {"2"}, "source": {"deepsentry"}}.Encode()
	case "wechat":
		data, err := weixinAPI(ctx, st.client, weixinBase, "", "get_bot_qrcode?bot_type=3", map[string]any{"local_token_list": []string{}})
		if err != nil {
			return err
		}
		p.Device = jsonString(data, "qrcode")
		p.URL = jsonString(data, "qrcode_img_content")
		p.BaseURL = weixinBase
		if p.Device == "" || !trustedURL(p.URL, "https", "weixin.qq.com", "weixin.qq.com.cn", "weixin.com") {
			return errors.New("微信未返回有效授权二维码")
		}
	case "wecom":
		source := st.cfg.WecomSource
		if source == "" {
			source = "deepsentry"
		}
		p.URL = "https://work.weixin.qq.com/ai/qc/gen?" + url.Values{"source": {source}, "state": {p.State}, "timestamp": {strconv.FormatInt(time.Now().UnixMilli(), 10)}}.Encode()
	}
	if in.Platform != "wecom" {
		png, err := qrcode.Encode(p.URL, qrcode.Medium, 320)
		if err != nil {
			return err
		}
		p.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	p.Next = time.Now().Add(p.Interval)
	st.pending[in.Platform] = p
	return nil
}
func (st *setupState) poll(ctx context.Context, in setupInput) error {
	p := st.pending[in.Platform]
	if p == nil || p.Platform == "wecom" {
		return nil
	}
	if time.Now().After(p.Expires) {
		delete(st.pending, in.Platform)
		st.status[in.Platform] = "expired"
		return nil
	}
	if time.Now().Before(p.Next) {
		return nil
	}
	p.Next = time.Now().Add(p.Interval)
	switch p.Platform {
	case "feishu":
		data, err := st.registration(ctx, url.Values{"action": {"poll"}, "device_code": {p.Device}})
		if err != nil {
			return err
		}
		var user struct {
			OpenID string `json:"open_id"`
			Brand  string `json:"tenant_brand"`
		}
		_ = json.Unmarshal(data["user_info"], &user)
		if user.Brand == "lark" {
			return st.fail(p, "当前支持国内飞书，请使用飞书账号扫码")
		}
		switch jsonString(data, "error") {
		case "authorization_pending":
			return nil
		case "slow_down":
			p.Interval = min(p.Interval+5*time.Second, 60*time.Second)
			p.Next = time.Now().Add(p.Interval)
			return nil
		case "":
		case "access_denied":
			return st.fail(p, "用户已取消授权")
		case "expired_token", "invalid_grant":
			return st.fail(p, "二维码已过期，请重新生成")
		default:
			return st.fail(p, "飞书授权失败，请重试")
		}
		app, secret := jsonString(data, "client_id"), jsonString(data, "client_secret")
		if app == "" || secret == "" {
			return nil
		}
		if user.OpenID == "" {
			return st.fail(p, "未返回扫码用户身份，请重新授权")
		}
		return st.bind(p.Platform, binding{Platform: p.Platform, AppID: app, Secret: secret, User: user.OpenID, AllowBatch: p.AllowBatch})
	case "qq":
		data, err := apiJSON(ctx, st.client, "POST", "https://q.qq.com/lite/poll_bind_result", nil, map[string]string{"task_id": p.Device})
		if err != nil {
			return err
		}
		var d struct {
			Status int             `json:"status"`
			AppID  json.RawMessage `json:"bot_appid"`
			Secret string          `json:"bot_encrypt_secret"`
			User   string          `json:"user_openid"`
		}
		if json.Unmarshal(data["data"], &d) != nil {
			return errors.New("QQ 返回无效绑定状态")
		}
		if d.Status == 3 {
			return st.fail(p, "QQ 二维码已过期")
		}
		if d.Status != 2 {
			return nil
		}
		secret, err := decryptQQ(d.Secret, p.Key)
		if err != nil {
			return st.fail(p, "QQ 凭据解密失败")
		}
		app := rawString(d.AppID)
		if app == "" || secret == "" || d.User == "" {
			return st.fail(p, "QQ 返回不完整绑定凭据")
		}
		return st.bind(p.Platform, binding{Platform: p.Platform, AppID: app, Secret: secret, User: d.User, AllowBatch: p.AllowBatch})
	case "wechat":
		endpoint := "get_qrcode_status?qrcode=" + url.QueryEscape(p.Device)
		if in.VerifyCode != "" {
			if !regexp.MustCompile(`^[0-9]{4,10}$`).MatchString(in.VerifyCode) {
				return errors.New("验证码应为 4–10 位数字")
			}
			endpoint += "&verify_code=" + url.QueryEscape(in.VerifyCode)
		}
		data, err := weixinAPI(ctx, st.client, p.BaseURL, "", endpoint, nil)
		if err != nil {
			return err
		}
		switch jsonString(data, "status") {
		case "wait", "scaned":
			return nil
		case "need_verifycode":
			return errors.New("请在手机查看验证码，并在窗口输入后继续")
		case "verify_code_blocked":
			return st.fail(p, "验证码尝试次数过多，请稍后重新扫码")
		case "expired":
			return st.fail(p, "微信二维码已过期")
		case "scaned_but_redirect":
			host := jsonString(data, "redirect_host")
			base := "https://" + host
			if !trustedURL(base, "https", "weixin.qq.com") {
				return st.fail(p, "微信返回非受信任服务地址")
			}
			p.BaseURL = base
			return nil
		case "binded_redirect":
			return st.fail(p, "此机器人已有绑定，请在微信管理中解除旧连接后重试")
		case "confirmed":
			token, id, user, base := jsonString(data, "bot_token"), jsonString(data, "ilink_bot_id"), jsonString(data, "ilink_user_id"), jsonString(data, "baseurl")
			if base == "" {
				base = p.BaseURL
			}
			if !trustedURL(base, "https", "weixin.qq.com") || token == "" || id == "" || user == "" {
				return st.fail(p, "微信返回不完整或无效的绑定信息")
			}
			return st.bind(p.Platform, binding{Platform: p.Platform, AppID: id, Secret: token, User: user, APIURL: base, AllowBatch: p.AllowBatch})
		default:
			return errors.New("微信返回未知授权状态")
		}
	}
	return nil
}
func (st *setupState) completeWecom(in setupInput) error {
	p := st.pending["wecom"]
	if p == nil || time.Now().After(p.Expires) || !equal(in.State, p.State) {
		return errors.New("企业微信授权会话已失效")
	}
	if in.BotID == "" || in.Secret == "" || len(in.BotID) > 256 || len(in.Secret) > 2048 {
		return errors.New("企业微信未返回完整机器人凭据")
	}
	return st.bind("wecom", binding{Platform: "wecom", AppID: in.BotID, Secret: in.Secret, PairCode: randomID()[:16], AllowBatch: p.AllowBatch})
}
func (st *setupState) fail(p *pendingLogin, message string) error {
	delete(st.pending, p.Platform)
	st.status[p.Platform] = "failed"
	return errors.New(message)
}
func decryptQQ(encrypted, key string) (string, error) {
	k, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return "", err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(b)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("invalid QQ ciphertext")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}
func (st *setupState) registration(ctx context.Context, form url.Values) (map[string]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "https://accounts.feishu.cn/oauth/v1/app/registration", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := st.client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接飞书授权服务")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || data == nil {
		return nil, errors.New("飞书授权服务返回无效响应")
	}
	if resp.StatusCode >= 500 {
		return nil, errors.New("飞书授权服务暂不可用")
	}
	return data, nil
}
