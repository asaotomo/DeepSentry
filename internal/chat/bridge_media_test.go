package chat

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestOneBotCQAndArrayAttachments(t *testing.T) {
	for _, raw := range []string{`[{"type":"text","data":{"text":"看图"}},{"type":"image","data":{"file":"img","sub_type":0,"url":"https://example.com/i?a=1&b=2"}},{"type":"record","data":{"file":"voice"}}]`, `"看图[CQ:image,file=img,url=https://example.com/i?a=1&amp;b=2][CQ:record,file=voice]"`} {
		text, a := parseOneBotMedia(json.RawMessage(raw), "")
		if text != "看图" || len(a) != 2 || a[0].URL != "https://example.com/i?a=1&b=2" || a[1].ResourceKey != "voice" {
			t.Fatalf("%q %+v", text, a)
		}
	}
	text, a := parseOneBotMedia(json.RawMessage(`"&#91;CQ:image,file=literal&#93;"`), "")
	if len(a) != 0 || text != "[CQ:image,file=literal]" {
		t.Fatalf("%s %+v", text, a)
	}
}

func TestWecomAppMediaDownloadAndBusinessError(t *testing.T) {
	s := testService(t, nil)
	fail := false
	c := config.ChatChannel{Platform: "wecom", AppID: "corp", AppSecret: "secret"}
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			return jsonResponse(map[string]string{"access_token": "token"}), nil
		case "/cgi-bin/media/get":
			if r.URL.Query().Get("media_id") != "media" || r.URL.Query().Get("access_token") != "token" {
				t.Error("missing credentials or media ID")
			}
			if fail {
				return jsonResponse(map[string]any{"errcode": 40007, "errmsg": "invalid media_id"}), nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
		}
		t.Fatal(r.URL.Path)
		return nil, nil
	})}
	m := Message{Attachments: []Attachment{{Kind: "image", ResourceKey: "media"}}}
	_, images, err := s.prepareChannelMedia(context.Background(), c, m, t.TempDir(), "看图")
	if err != nil || len(images) != 1 {
		t.Fatalf("%+v %v", images, err)
	}
	fail = true
	if _, err := s.downloadChannelMedia(context.Background(), c, m, Attachment{Kind: "file", ResourceKey: "media"}); err == nil {
		t.Fatal("JSON error became document")
	}
}

func TestOneBotSharedMediaAndEscape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "voice.mp3")
	_ = os.WriteFile(path, []byte("voice"), 0600)
	s := testService(t, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/get_record" {
			t.Error(r.URL.Path)
		}
		return jsonResponse(map[string]any{"retcode": 0, "data": map[string]string{"file": path}}), nil
	})}
	c := config.ChatChannel{Platform: "onebot", APIURL: "http://localhost:3000", MediaRoot: dir}
	b, err := s.downloadChannelMedia(context.Background(), c, Message{}, Attachment{Kind: "audio", ResourceKey: "id"})
	if err != nil || string(b) != "voice" {
		t.Fatalf("%s %v", b, err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(outside, []byte("secret"), 0600)
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := readBridgeMediaFile(dir, link); err == nil {
			t.Fatal("symlink escaped")
		}
	}
	if _, err := readBridgeMediaFile("", path); err == nil {
		t.Fatal("unconfigured local read")
	}
}

func TestBridgeMediaSendingAndExplicitAcknowledgement(t *testing.T) {
	for _, platform := range []string{"onebot", "wechat"} {
		for _, group := range []bool{false, true} {
			s := testService(t, nil)
			s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				if platform == "onebot" {
					want := "/upload_private_file"
					if group {
						want = "/upload_group_file"
					}
					if r.URL.Path != want || !strings.HasPrefix(jsonString(body, "file"), "base64://") {
						t.Errorf("%s %v", r.URL, body)
					}
					return jsonResponse(map[string]int{"retcode": 0}), nil
				}
				if len(body["attachments"]) == 0 {
					t.Error("missing attachment")
				}
				return jsonResponse(map[string]bool{"media_accepted": true}), nil
			})}
			c := config.ChatChannel{Platform: platform, APIURL: "http://localhost:3000"}
			if err := s.sendBridgeFile(context.Background(), c, Message{User: "123", Chat: "456", Group: group}, []byte("report"), "报告.txt"); err != nil {
				t.Fatal(err)
			}
		}
	}
	s := testService(t, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(map[string]int{"code": 0}), nil })}
	if err := s.sendBridgeFile(context.Background(), config.ChatChannel{Platform: "wechat", APIURL: "http://localhost:3000"}, Message{}, []byte("x"), "x.txt"); err == nil {
		t.Fatal("text-only bridge claimed file delivery")
	}
}

func TestOneBotResourceBase64LargerThanTextReceipt(t *testing.T) {
	s := testService(t, nil)
	payload := bytes.Repeat([]byte("a"), 2<<20)
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(map[string]any{"retcode": 0, "data": map[string]string{"base64": base64.StdEncoding.EncodeToString(payload)}}), nil
	})}
	b, err := s.downloadChannelMedia(context.Background(), config.ChatChannel{Platform: "onebot", APIURL: "http://localhost:3000"}, Message{}, Attachment{Kind: "file", ResourceKey: "id"})
	if err != nil || !bytes.Equal(b, payload) {
		t.Fatalf("bytes=%d err=%v", len(b), err)
	}
}

func TestWecomEncryptedAttachmentCallback(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	t.Setenv("WC_AES_MEDIA", base64.RawStdEncoding.EncodeToString(key))
	t.Setenv("WC_TOKEN_MEDIA", "token")
	c := config.ChatChannel{Platform: "wecom", AppID: "corp", AgentID: 7, EncryptKeyEnv: "WC_AES_MEDIA", TokenEnv: "WC_TOKEN_MEDIA"}
	for _, kind := range []string{"image", "file", "voice"} {
		xml := fmt.Sprintf(`<xml><ToUserName>corp</ToUserName><FromUserName>alice</FromUserName><MsgId>123</MsgId><MsgType>%s</MsgType><MediaId>media</MediaId><Recognition>hello</Recognition><FileName>报告.pdf</FileName><AgentID>7</AgentID></xml>`, kind)
		plain := make([]byte, 20)
		binary.BigEndian.PutUint32(plain[16:], uint32(len(xml)))
		plain = append(plain, []byte(xml+"corp")...)
		encrypted := base64.StdEncoding.EncodeToString(encryptCBC(key, key[:16], plain, 32))
		req := httptest.NewRequest("POST", "/", nil)
		q := req.URL.Query()
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		q.Set("timestamp", ts)
		q.Set("nonce", "n")
		parts := []string{"token", ts, "n", encrypted}
		sort.Strings(parts)
		sum := sha1.Sum([]byte(strings.Join(parts, "")))
		q.Set("msg_signature", hex.EncodeToString(sum[:]))
		req.URL.RawQuery = q.Encode()
		m, _, err := decode(c, req, []byte("<xml><Encrypt>"+encrypted+"</Encrypt></xml>"))
		if err != nil || len(m.Attachments) != 1 || m.Attachments[0].ResourceKey != "media" {
			t.Fatalf("%+v %v", m, err)
		}
		if kind == "voice" && m.Attachments[0].Transcript != "hello" {
			t.Fatal("lost recognition")
		}
	}
}

func TestAuthorizedGroupAttachmentStartsTask(t *testing.T) {
	s := testService(t, nilRunner)
	c := testChannel()
	c.AllowedChats = []string{"group"}
	m := testMessage("group-file", "")
	m.Group = true
	m.Chat = "group"
	m.Attachments = []Attachment{{Kind: "audio", Transcript: "解释收到的附件"}}
	if _, err := s.command(c, m); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, "completed")
	m.ID = "unauthorized"
	m.Chat = "other"
	if _, err := s.command(c, m); err == nil {
		t.Fatal("unlisted group accepted")
	}
}
