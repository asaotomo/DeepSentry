package chat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func nilRunner(context.Context, string, string) (string, error) { return "", nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(body any) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}
}
func setupFixture(t *testing.T, transport roundTripFunc) *setupState {
	s := testService(t, nilRunner)
	s.stop()
	return &setupState{cfg: s.cfg, service: s, client: &http.Client{Transport: transport}, pending: map[string]*pendingLogin{}, status: map[string]string{}}
}
func TestQQOnboardingDecryptsAndNeverPublishesSecrets(t *testing.T) {
	var key string
	st := setupFixture(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "create_bind_task") {
			var d map[string]string
			_ = json.NewDecoder(r.Body).Decode(&d)
			key = d["key"]
			return jsonResponse(map[string]any{"retcode": 0, "data": map[string]string{"task_id": "task"}}), nil
		}
		rawKey, _ := base64.StdEncoding.DecodeString(key)
		block, _ := aes.NewCipher(rawKey)
		gcm, _ := cipher.NewGCM(block)
		iv := bytes.Repeat([]byte{2}, 12)
		encrypted := append(iv, gcm.Seal(nil, iv, []byte("private-secret"), nil)...)
		return jsonResponse(map[string]any{"retcode": 0, "data": map[string]any{"status": 2, "bot_appid": 123456, "bot_encrypt_secret": base64.StdEncoding.EncodeToString(encrypted), "user_openid": "owner"}}), nil
	})
	in := setupInput{Platform: "qq", AllowBatch: true}
	if err := st.begin(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if st.pending["qq"].QR == "" {
		t.Fatal("missing QR")
	}
	public, _ := json.Marshal(st.publicState("qq"))
	if strings.Contains(string(public), key) {
		t.Fatal("AES key exposed")
	}
	st.pending["qq"].Next = time.Time{}
	if err := st.poll(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	all, err := loadBindings(st.cfg)
	if err != nil || all["qq"].Secret != "private-secret" || all["qq"].User != "owner" || !all["qq"].AllowBatch {
		t.Fatal(all, err)
	}
	public, _ = json.Marshal(st.publicState("qq"))
	if strings.Contains(string(public), "private-secret") {
		t.Fatal("secret exposed")
	}
	if _, err := decryptQQ(base64.StdEncoding.EncodeToString([]byte("tampered")), key); err == nil {
		t.Fatal("invalid GCM accepted")
	}
}
func TestWeixinQRAndConfirmedBinding(t *testing.T) {
	st := setupFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("AuthorizationType") != "ilink_bot_token" {
			t.Fatal("missing protocol header")
		}
		if strings.HasSuffix(r.URL.Path, "get_bot_qrcode") {
			return jsonResponse(map[string]any{"qrcode": "qr-key", "qrcode_img_content": "https://weixin.qq.com/x/test"}), nil
		}
		return jsonResponse(map[string]any{"status": "confirmed", "bot_token": "wx-secret", "ilink_bot_id": "bot", "ilink_user_id": "owner", "baseurl": weixinBase}), nil
	})
	in := setupInput{Platform: "wechat"}
	if err := st.begin(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	st.pending["wechat"].Next = time.Time{}
	if err := st.poll(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	all, _ := loadBindings(st.cfg)
	if all["wechat"].User != "owner" || all["wechat"].APIURL != weixinBase {
		t.Fatal(all)
	}
}
func TestWecomPopupStateExpiresAndRequiresPairing(t *testing.T) {
	st := setupFixture(t, nil)
	in := setupInput{Platform: "wecom", AllowBatch: true}
	if err := st.begin(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	p := st.pending["wecom"]
	if !strings.HasPrefix(p.URL, "https://work.weixin.qq.com/ai/qc/gen?") {
		t.Fatal(p.URL)
	}
	in.BotID = "bot"
	in.Secret = "secret"
	in.State = "wrong"
	if err := st.completeWecom(in); err == nil {
		t.Fatal("wrong state accepted")
	}
	in.State = p.State
	if err := st.completeWecom(in); err != nil {
		t.Fatal(err)
	}
	all, _ := loadBindings(st.cfg)
	if all["wecom"].User != "" || all["wecom"].PairCode == "" {
		t.Fatal("unpaired bot trusted a user")
	}
	if err := st.completeWecom(in); err == nil {
		t.Fatal("popup callback replay accepted")
	}
}
func TestFeishuPendingSlowDownSuccessAndRefresh(t *testing.T) {
	polls := 0
	st := setupFixture(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		if r.Form.Get("action") == "begin" {
			return jsonResponse(map[string]any{"device_code": "private-device", "user_code": "public-code", "expire_in": 600, "interval": 5}), nil
		}
		polls++
		if polls == 1 {
			return jsonResponse(map[string]string{"error": "slow_down"}), nil
		}
		return jsonResponse(map[string]any{"client_id": "cli_app", "client_secret": "secret", "user_info": map[string]string{"open_id": "owner", "tenant_brand": "feishu"}}), nil
	})
	in := setupInput{Platform: "feishu"}
	if err := st.begin(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(st.publicState("feishu"))
	if strings.Contains(string(public), "private-device") {
		t.Fatal("device credential exposed")
	}
	st.pending["feishu"].Next = time.Time{}
	if err := st.poll(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if st.pending["feishu"].Interval != 10*time.Second {
		t.Fatal("slow_down ignored")
	}
	st.pending["feishu"].Next = time.Time{}
	if err := st.poll(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	all, _ := loadBindings(st.cfg)
	if all["feishu"].User != "owner" {
		t.Fatal(all)
	}
	if err := st.begin(context.Background(), in); err == nil {
		t.Fatal("existing binding silently overwritten")
	}
}
func TestUntrustedPlatformEndpointsRejected(t *testing.T) {
	for _, u := range []string{"http://ilinkai.weixin.qq.com", "https://weixin.qq.com.evil.test", "https://user:pass@ilinkai.weixin.qq.com", "https://ilinkai.weixin.qq.com:8000", "https://127.0.0.1"} {
		if trustedURL(u, "https", "weixin.qq.com") {
			t.Fatal(u)
		}
	}
}
