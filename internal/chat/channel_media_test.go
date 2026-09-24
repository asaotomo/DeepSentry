package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestFeishuContentMediaAndPost(t *testing.T) {
	for _, kind := range []string{"image", "file", "audio"} {
		_, a, err := parseFeishuContent(kind, `{"image_key":"img","file_key":"file","file_name":"报告.pdf"}`)
		if err != nil || len(a) != 1 || a[0].ResourceKey == "" {
			t.Fatalf("%s: %+v %v", kind, a, err)
		}
	}
	text, a, err := parseFeishuContent("post", `{"zh_cn":{"title":"看图","content":[[{"tag":"text","text":"分析"},{"tag":"img","image_key":"img"}]]},"en_us":{"title":"ignore"}}`)
	if err != nil || !strings.Contains(text, "分析") || strings.Contains(text, "ignore") || len(a) != 1 {
		t.Fatalf("%s %+v %v", text, a, err)
	}
}

func TestFeishuAuthenticatedImageAndDuplicateFilenames(t *testing.T) {
	s := testService(t, nil)
	c := config.ChatChannel{Platform: "feishu", AppID: "app", AppSecret: "secret"}
	hits := 0
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			return jsonResponse(map[string]any{"code": 0, "tenant_access_token": "token"}), nil
		}
		hits++
		if r.Header.Get("Authorization") != "Bearer token" || !strings.Contains(r.URL.Path, "/messages/msg/resources/") {
			t.Errorf("bad resource request %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
	})}
	m := Message{ID: "msg", Attachments: []Attachment{{Kind: "file", Name: "same.png", ResourceKey: "one"}, {Kind: "file", Name: "same.png", ResourceKey: "two"}}}
	_, images, err := s.prepareChannelMedia(context.Background(), c, m, t.TempDir(), "compare")
	if err != nil || len(images) != 2 || hits != 2 {
		t.Fatalf("%+v %v", images, err)
	}
	if images[0].Path == images[1].Path {
		t.Fatal("same filename overwrote attachment")
	}
	for _, img := range images {
		if _, err := os.Stat(img.Path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFeishuFileUploadAndSend(t *testing.T) {
	s := testService(t, nil)
	uploaded, sent := false, false
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			return jsonResponse(map[string]any{"code": 0, "tenant_access_token": "token"}), nil
		case strings.HasSuffix(r.URL.Path, "/files"):
			uploaded = true
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			defer r.MultipartForm.RemoveAll()
			f, h, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			if h.Filename != "报告.txt" || string(b) != "report" || r.FormValue("file_type") != "stream" {
				t.Fatal("invalid upload")
			}
			return jsonResponse(map[string]any{"code": 0, "data": map[string]string{"file_key": "resource"}}), nil
		case strings.HasSuffix(r.URL.Path, "/messages"):
			sent = true
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !uploaded || body["receive_id"] != "chat" || body["msg_type"] != "file" || !strings.Contains(body["content"], "resource") {
				t.Fatalf("%v", body)
			}
			return jsonResponse(map[string]any{"code": 0}), nil
		}
		t.Fatalf("unexpected %s", r.URL)
		return nil, nil
	})}
	if err := s.sendFeishuFile(context.Background(), config.ChatChannel{}, Message{Chat: "chat"}, []byte("report"), "报告.txt"); err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatal("not sent")
	}
}

func TestFileOutboxSurvivesTaskCleanupAndRestart(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	m := testMessage("file", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	_ = os.WriteFile(path, []byte("report"), 0600)
	if err := s.deliverOutboundFiles(context.Background(), c, m, dir); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(dir)
	var item *OutboxItem
	for _, i := range s.box.items {
		if i.File != nil {
			item = i
		}
	}
	if item == nil {
		t.Fatal("no durable file")
	}
	b, err := os.ReadFile(item.File.Path)
	if err != nil || string(b) != "report" {
		t.Fatalf("%s %v", b, err)
	}
	s.box.items = map[string]*OutboxItem{}
	if err := s.restoreOutbox(); err != nil {
		t.Fatal(err)
	}
	restored := s.box.items[item.ID]
	if restored == nil || restored.File == nil || restored.partCount() != 1 {
		t.Fatal("lost file")
	}
}

func TestWecomFileChunkUploadAndReceipt(t *testing.T) {
	s := testService(t, nil)
	payload := bytes.Repeat([]byte("x"), 600000)
	received := make(chan []byte, 1)
	mockedSocket(t, s, func(conn *websocket.Conn) {
		var collected []byte
		for {
			var frame struct {
				Cmd     string                     `json:"cmd"`
				Headers map[string]string          `json:"headers"`
				Body    map[string]json.RawMessage `json:"body"`
			}
			if conn.ReadJSON(&frame) != nil {
				return
			}
			body := map[string]string{}
			switch frame.Cmd {
			case "aibot_subscribe":
			case "aibot_upload_media_init":
				body["upload_id"] = "upload"
			case "aibot_upload_media_chunk":
				chunk, err := base64.StdEncoding.DecodeString(jsonString(frame.Body, "base64_data"))
				if err != nil {
					t.Error(err)
					return
				}
				collected = append(collected, chunk...)
			case "aibot_upload_media_finish":
				body["media_id"] = "media"
			case "aibot_send_msg":
				if jsonString(frame.Body, "msgtype") != "file" || jsonString(frame.Body, "chatid") != "alice" {
					t.Error("bad file message")
				}
				received <- collected
			default:
				t.Errorf("unexpected %s", frame.Cmd)
			}
			if conn.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0, "body": body}) != nil {
				return
			}
		}
	})
	c := config.ChatChannel{Name: "wecom-media", Platform: "wecom_ws", AppID: "bot", AppSecret: "secret", AllowedUsers: []string{"alice"}}
	s.Connect(c)
	defer s.Disconnect(c.Name)
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		p := s.mediaPeers[c.Name]
		active := s.channels[c.Name]
		s.mu.Unlock()
		if p != nil {
			c = active
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.sendWecomSocketFile(context.Background(), c, Message{Chat: "alice"}, payload, "report.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-received:
		if !bytes.Equal(b, payload) {
			t.Fatal("chunk corruption")
		}
	case <-time.After(time.Second):
		t.Fatal("missing send")
	}
}

func TestFileOutboxUploadFailureAndRetry(t *testing.T) {
	s := testService(t, nil)
	c := testChannel()
	c.Platform = "feishu"
	s.channels[c.Name] = c
	m := testMessage("file", "")
	path := filepath.Join(t.TempDir(), "report.txt")
	_ = os.WriteFile(path, []byte("report"), 0600)
	if err := s.queueOutboundFile(c, m, sendFile{Path: path, Name: "report.txt"}); err != nil {
		t.Fatal(err)
	}
	var item *OutboxItem
	for _, i := range s.box.items {
		item = i
	}
	fail := true
	sent := 0
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			return jsonResponse(map[string]string{"tenant_access_token": "token"}), nil
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			if fail {
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return jsonResponse(map[string]any{"code": 0, "data": map[string]string{"file_key": "file"}}), nil
		}
		sent++
		return jsonResponse(map[string]int{"code": 0}), nil
	})}
	s.deliverOutbox(item)
	if item.Status != "pending" || item.Next != 0 || sent != 0 {
		t.Fatalf("%+v sent=%d", item, sent)
	}
	fail = false
	s.deliverOutbox(item)
	if item.Status != "delivered" || item.Next != 1 || sent != 1 {
		t.Fatalf("%+v sent=%d", item, sent)
	}
	s.deliverOutbox(item)
	if sent != 1 {
		t.Fatal("duplicated delivered file")
	}
}

func TestInboundPrefetchSurvivesExpiredURL(t *testing.T) {
	s := testService(t, nil)
	hits := 0
	s.mediaClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		if hits > 1 {
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
	})}
	m := Message{ID: "msg", Attachments: []Attachment{{Kind: "image", URL: "https://example.com/i.png"}}}
	s.pending["job1"] = pendingRun{message: m}
	s.prefetchPendingMedia("job1", config.ChatChannel{}, m)
	cached := s.pending["job1"].message.Attachments
	if len(cached) != 1 || cached[0].LocalPath == "" || cached[0].URL != "" {
		t.Fatalf("%+v", cached)
	}
	_, images, err := s.prepareChannelMedia(context.Background(), config.ChatChannel{}, s.pending["job1"].message, t.TempDir(), "看图")
	if err != nil || len(images) != 1 {
		t.Fatalf("%+v %v", images, err)
	}
	if hits != 1 {
		t.Fatalf("prepare re-downloaded expired URL: hits=%d", hits)
	}
}

func TestInboundJSONCannotSetLocalPath(t *testing.T) {
	var a Attachment
	if err := json.Unmarshal([]byte(`{"kind":"file","local_path":"/etc/passwd","url":"https://example.com/a"}`), &a); err != nil {
		t.Fatal(err)
	}
	if a.LocalPath != "" {
		t.Fatal("inbound local_path was accepted")
	}
}

func TestPrefetchRejectsOutsideCache(t *testing.T) {
	s := testService(t, nil)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readPrefetchedMedia(outside); err == nil {
		t.Fatal("escaped prefetch root")
	}
}

func TestFeishuVideoResourcesAreNotSilentlyDropped(t *testing.T) {
	for _, kind := range []string{"media", "post"} {
		raw := `{"file_key":"video","file_name":"clip.mp4"}`
		if kind == "post" {
			raw = `{"content":[[{"tag":"text","text":"看附件"},{"tag":"media","file_key":"video"}]]}`
		}
		_, a, err := parseFeishuContent(kind, raw)
		if err != nil || len(a) != 1 || a[0].ResourceKey != "video" || a[0].Kind != "file" {
			t.Fatalf("%s %+v %v", kind, a, err)
		}
	}
}

func TestFeishuResourceRejectsCredentialRedirectAndPreservesCancel(t *testing.T) {
	s := testService(t, nil)
	c := config.ChatChannel{Platform: "feishu_ws", AppID: "app", AppSecret: "secret"}
	var cancelled bool
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			return jsonResponse(map[string]any{"code": 0, "tenant_access_token": "token"}), nil
		}
		if r.URL.Host != "open.feishu.cn" {
			t.Fatal("credential redirect followed")
		}
		if cancelled {
			return nil, context.Canceled
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.test/steal"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	_, err := s.downloadChannelMedia(context.Background(), c, Message{ID: "message"}, Attachment{Kind: "image", ResourceKey: "key"})
	if err == nil {
		t.Fatal("redirect accepted as attachment")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled = true
	_, err = s.downloadChannelMedia(ctx, c, Message{ID: "message"}, Attachment{Kind: "image", ResourceKey: "key"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}
