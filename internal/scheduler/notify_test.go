package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendFeishuMarkdown(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	if err := SendFeishuMarkdown(srv.URL, "secret", "标题", "正文"); err != nil {
		t.Fatal(err)
	}
	if got["msg_type"] != "text" || got["timestamp"] == "" || got["sign"] == "" {
		t.Fatalf("unexpected feishu body: %#v", got)
	}
	content := got["content"].(map[string]any)
	if !strings.Contains(content["text"].(string), "标题") || !strings.Contains(content["text"].(string), "正文") {
		t.Fatalf("unexpected text: %#v", content)
	}
}

func TestSendEmailGatewayMarkdown(t *testing.T) {
	var auth string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("X-API-Key")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	err := SendEmailGatewayMarkdown(srv.URL, "token123", "X-API-Key", "a@example.com,b@example.com", "robot@example.com", "主题", "# 正文")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "token123" {
		t.Fatalf("auth=%q", auth)
	}
	recipients := got["to"].([]any)
	if len(recipients) != 2 || recipients[0] != "a@example.com" {
		t.Fatalf("to=%#v", recipients)
	}
	if got["subject"] != "主题" || !strings.Contains(got["markdown"].(string), "正文") {
		t.Fatalf("body=%#v", got)
	}
}
