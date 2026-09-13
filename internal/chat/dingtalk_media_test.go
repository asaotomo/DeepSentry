package chat

import (
	"ai-edr/internal/config"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDingAttachmentCallbackAndVision(t *testing.T) {
	t.Setenv("DING_TEST_SECRET", "secret")
	c := config.ChatChannel{Platform: "dingtalk", AppID: "robot", SecretEnv: "DING_TEST_SECRET"}
	r := httptest.NewRequest("POST", "/", nil)
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(ts + "\nsecret"))
	r.Header.Set("timestamp", ts)
	r.Header.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	m, _, err := decode(c, r, []byte(`{"msgId":"id","senderStaffId":"user","conversationId":"chat","msgtype":"picture","content":{"pictureDownloadCode":"code"},"sessionWebhook":"https://oapi.dingtalk.com/robot/sendBySession?session=x","sessionWebhookExpiredTime":123}`))
	if err != nil || len(m.Attachments) != 1 || m.Attachments[0].ResourceKey != "code" || m.ReplyExpires != 123 {
		t.Fatalf("%+v %v", m, err)
	}
	s := testService(t, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "accessToken") {
			return jsonResponse(map[string]string{"accessToken": "token"}), nil
		}
		if r.URL.Path != "/v1.0/robot/messageFiles/download" || r.Header.Get("x-acs-dingtalk-access-token") != "token" {
			t.Fatalf("invalid API request %s", r.URL.Path)
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["robotCode"] != "robot" || b["downloadCode"] != "code" {
			t.Fatalf("%v", b)
		}
		return jsonResponse(map[string]string{"downloadUrl": "https://static.dingtalk.com/image?rkey=unchanged"}), nil
	})}
	s.platformMedia = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-acs-dingtalk-access-token") != "" {
			t.Error("credential sent to CDN")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
	})}
	_, images, err := s.prepareChannelMedia(context.Background(), c, m, filepath.Join(t.TempDir(), "job"), "看图")
	if err != nil || len(images) != 1 {
		t.Fatalf("images=%d err=%v", len(images), err)
	}
	for _, kind := range []string{"file", "audio", "unknown"} {
		_, a, e := parseDingContent(kind, json.RawMessage(`{"downloadCode":"file","recognition":"你好","fileName":"报告.pdf"}`))
		if e != nil || len(a) != 1 {
			t.Fatalf("%s %+v %v", kind, a, e)
		}
	}
	text, a, err := parseDingContent("richText", json.RawMessage(`{"richText":[{"text":"看图"},{"type":"picture","pictureDownloadCode":"code"}]}`))
	if err != nil || text != "看图" || len(a) != 1 {
		t.Fatalf("%s %+v %v", text, a, err)
	}
}

func TestDingFileTemplatesAndExpiredWebhook(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(strconv.FormatBool(group), func(t *testing.T) {
			s := testService(t, nil)
			c := config.ChatChannel{Platform: "dingtalk", AppID: "robot", AppSecret: "secret"}
			var keys []string
			s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v1.0/oauth2/accessToken":
					return jsonResponse(map[string]string{"accessToken": "token"}), nil
				case "/media/upload":
					if r.URL.Query().Get("access_token") != "token" {
						t.Error("upload token missing")
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					defer r.MultipartForm.RemoveAll()
					f, _, err := r.FormFile("media")
					if err != nil {
						t.Fatal(err)
					}
					f.Close()
					return jsonResponse(map[string]any{"errcode": 0, "media_id": "media"}), nil
				default:
					if strings.Contains(r.URL.Path, "sendBySession") {
						t.Fatal("expired webhook used")
					}
					var b map[string]json.RawMessage
					_ = json.NewDecoder(r.Body).Decode(&b)
					keys = append(keys, jsonString(b, "msgKey"))
					if group && jsonString(b, "openConversationId") != "chat" {
						t.Fatal("wrong group")
					}
					if !group && string(b["userIds"]) != `["user"]` {
						t.Fatal("wrong recipient")
					}
					if r.Header.Get("x-acs-dingtalk-access-token") != "token" {
						t.Fatal("missing API token")
					}
					return jsonResponse(map[string]string{"processQueryKey": "accepted"}), nil
				}
			})}
			m := Message{User: "user", Chat: "chat", Group: group, ReplyExpires: 1}
			for _, b := range [][]byte{[]byte("document"), pngFixture(t)} {
				if err := s.sendDingFile(context.Background(), c, m, b, "test.txt"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.sendChunk(context.Background(), c, m, "done"); err != nil {
				t.Fatal(err)
			}
			if strings.Join(keys, ",") != "sampleFile,sampleImageMsg,sampleText" {
				t.Fatalf("%v", keys)
			}
		})
	}
}
