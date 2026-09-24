package chat

import (
	"ai-edr/internal/config"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOneBotSignatureSegmentDecodeAndReplayWindow(t *testing.T) {
	t.Setenv("OB_SECRET", "event-secret")
	c := config.ChatChannel{Platform: "onebot", SecretEnv: "OB_SECRET"}
	raw := []byte(fmt.Sprintf(`{"time":%d,"self_id":111,"user_id":222,"group_id":333,"message_id":444,"post_type":"message","message_type":"group","message":[{"type":"at","data":{"qq":"111"}},{"type":"text","data":{"text":" /ds help"}}]}`, time.Now().Unix()))
	req := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
	mac := hmac.New(sha1.New, []byte("event-secret"))
	mac.Write(raw)
	req.Header.Set("X-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
	m, _, err := decode(c, req, raw)
	if err != nil || m.User != "222" || m.Chat != "333" || m.ID != "444" || !m.Group || strings.TrimSpace(m.Text) != "/ds help" {
		t.Fatalf("decode=%+v %v", m, err)
	}
	raw[len(raw)-3] = 'X'
	if _, _, err = decode(c, req, raw); err == nil {
		t.Fatal("tampered message accepted")
	}
}
func TestDingTalkRejectsStaleSignatureAndReplySSRF(t *testing.T) {
	t.Setenv("DING_SECRET", "app-secret")
	c := config.ChatChannel{Platform: "dingtalk", SecretEnv: "DING_SECRET"}
	req := httptest.NewRequest("POST", "/", nil)
	sign := func(ts string) {
		mac := hmac.New(sha256.New, []byte("app-secret"))
		mac.Write([]byte(ts + "\napp-secret"))
		req.Header.Set("timestamp", ts)
		req.Header.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}
	sign(strconv.FormatInt(time.Now().UnixMilli(), 10))
	raw := []byte(`{"msgId":"m1","senderStaffId":"u","conversationId":"c","conversationType":"2","msgtype":"text","text":{"content":"/ds help"},"sessionWebhook":"https://oapi.dingtalk.com/robot/sendBySession?session=example"}`)
	m, _, err := decode(c, req, raw)
	if err != nil || m.User != "u" || !m.Group {
		t.Fatalf("decode=%+v %v", m, err)
	}
	for _, bad := range []string{"http://127.0.0.1/admin", "https://oapi.dingtalk.com.evil.test/robot/sendBySession", "https://oapi.dingtalk.com/other"} {
		changed := bytes.Replace(raw, []byte("https://oapi.dingtalk.com/robot/sendBySession?session=example"), []byte(bad), 1)
		if _, _, err := decode(c, req, changed); err == nil {
			t.Fatal("untrusted reply URL accepted")
		}
	}
	sign("1000")
	if _, _, err = decode(c, req, raw); err == nil {
		t.Fatal("stale callback accepted")
	}
}
func encryptCBC(key, iv, raw []byte, blockSize int) []byte {
	pad := blockSize - len(raw)%blockSize
	raw = append(append([]byte(nil), raw...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(raw))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, raw)
	return out
}
func TestFeishuEncryptedEventAndChallenge(t *testing.T) {
	t.Setenv("FS_TOKEN", "verify")
	t.Setenv("FS_KEY", "encrypt-key")
	c := config.ChatChannel{Platform: "feishu", AppID: "cli_app", TokenEnv: "FS_TOKEN", EncryptKeyEnv: "FS_KEY"}
	payload := []byte(`{"header":{"token":"verify","app_id":"cli_app","event_type":"im.message.receive_v1"},"event":{"sender":{"sender_type":"user","sender_id":{"open_id":"ou_user"}},"message":{"message_id":"om_msg","chat_id":"oc_chat","chat_type":"p2p","message_type":"text","content":"{\"text\":\"@_user_1 /ds help\"}","mentions":[{"key":"@_user_1"}]}}}`)
	key := sha256.Sum256([]byte("encrypt-key"))
	iv := bytes.Repeat([]byte{1}, 16)
	encrypted := append(iv, encryptCBC(key[:], iv, payload, 16)...)
	raw, _ := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(encrypted)})
	req := httptest.NewRequest("POST", "/", nil)
	sign := func(raw []byte) {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set("X-Lark-Request-Timestamp", ts)
		req.Header.Set("X-Lark-Request-Nonce", "nonce")
		sum := sha256.Sum256(append([]byte(ts+"nonceencrypt-key"), raw...))
		req.Header.Set("X-Lark-Signature", hex.EncodeToString(sum[:]))
	}
	sign(raw)
	m, _, err := decode(c, req, raw)
	if err != nil || m.User != "ou_user" || strings.TrimSpace(m.Text) != "/ds help" {
		t.Fatalf("decode=%+v %v", m, err)
	}
	req.Header.Set("X-Lark-Signature", "bad")
	if _, _, err = decode(c, req, raw); err == nil {
		t.Fatal("bad signature accepted")
	}
	raw = []byte(`{"type":"url_verification","token":"verify","challenge":"hello"}`)
	sign(raw)
	_, challenge, err := decode(c, req, raw)
	if err != nil || challenge != "hello" {
		t.Fatal(challenge, err)
	}
}
func TestWecomEncryptedXMLAndReceiverValidation(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	t.Setenv("WC_AES", base64.RawStdEncoding.EncodeToString(key))
	t.Setenv("WC_TOKEN", "token")
	c := config.ChatChannel{Platform: "wecom", AppID: "corp", AgentID: 7, EncryptKeyEnv: "WC_AES", TokenEnv: "WC_TOKEN"}
	xml := `<xml><ToUserName>corp</ToUserName><FromUserName>alice</FromUserName><MsgId>123</MsgId><MsgType>text</MsgType><Content>/ds help</Content><AgentID>7</AgentID></xml>`
	plain := make([]byte, 20)
	binary.BigEndian.PutUint32(plain[16:], uint32(len(xml)))
	plain = append(plain, []byte(xml+"corp")...)
	encrypted := base64.StdEncoding.EncodeToString(encryptCBC(key, key[:16], plain, 32))
	req := httptest.NewRequest("POST", "/", nil)
	q := req.URL.Query()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	q.Set("timestamp", ts)
	q.Set("nonce", "nonce")
	parts := []string{"token", ts, "nonce", encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	q.Set("msg_signature", hex.EncodeToString(sum[:]))
	req.URL.RawQuery = q.Encode()
	raw := []byte("<xml><Encrypt>" + encrypted + "</Encrypt></xml>")
	m, _, err := decode(c, req, raw)
	if err != nil || m.User != "alice" || m.Text != "/ds help" {
		t.Fatalf("decode=%+v %v", m, err)
	}
	c.AppID = "other"
	if _, _, err = decode(c, req, raw); err == nil {
		t.Fatal("receiver mismatch accepted")
	}
	if _, err := decryptCBC(key, key[:16], []byte("bad")); err == nil {
		t.Fatal("bad padding accepted")
	}
}
func TestCallbackAuthenticationAndBodyLimit(t *testing.T) {
	s := testService(t, nilRunner)
	c := testChannel()
	c.SecretEnv = "BRIDGE_TOKEN"
	t.Setenv("BRIDGE_TOKEN", "secret")
	s.cfg.Channels = []config.ChatChannel{c}
	h := s.Handler("test-token")
	raw := fmt.Sprintf(`{"id":"1","user_id":"alice","chat_id":"private","text":"/ds help","timestamp":%d}`, time.Now().Unix())
	req := httptest.NewRequest("POST", "/chat/test", strings.NewReader(raw))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	req = httptest.NewRequest("POST", "/chat/test", strings.NewReader(raw))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	req = httptest.NewRequest("POST", "/chat/test", strings.NewReader(strings.Repeat("x", 300<<10)))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatal(rec.Code)
	}
}
