package chat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
)

func pngFixture(t *testing.T) []byte {
	t.Helper()
	b, e := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestMediaRejectsPrivateAndUnsafeAddresses(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "::ffff:192.168.1.1"} {
		if publicMediaIP(net.ParseIP(ip)) {
			t.Fatal(ip)
		}
	}
	if !publicMediaIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP rejected")
	}
	for _, raw := range []string{"file:///etc/passwd", "http://example.com/a", "https://user:pass@example.com/a", "https://example.com:1234/a"} {
		if publicMediaURL(raw) {
			t.Fatal(raw)
		}
	}
	if !trustedMediaHost("novac2c.cdn.weixin.qq.com") || !trustedMediaHost("wx.qpic.cn") || trustedMediaHost("198.18.1.81") || trustedMediaHost("evil.example") {
		t.Fatal("trusted host")
	}
	if !platformMediaURL("https://novac2c.cdn.weixin.qq.com/c2c/download?x=1") || platformMediaURL("https://cdn.example/a") {
		t.Fatal("platform url")
	}
}
func TestMediaDecryptsWeixinAndWecomWithStrictPadding(t *testing.T) {
	for _, mode := range []string{"ecb", "cbc"} {
		key := bytes.Repeat([]byte{7}, 16)
		if mode == "cbc" {
			key = bytes.Repeat([]byte{9}, 32)
		}
		plain := pngFixture(t)
		n := 16 - len(plain)%16
		padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(n)}, n)...)
		block, _ := aes.NewCipher(key)
		enc := make([]byte, len(padded))
		keyText := base64.StdEncoding.EncodeToString(key)
		if mode == "cbc" {
			cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(enc, padded)
		} else {
			for off := 0; off < len(enc); off += 16 {
				block.Encrypt(enc[off:off+16], padded[off:off+16])
			}
			keyText = base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
		}
		got, err := decryptMedia(enc, keyText, mode)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%s %v", mode, err)
		}
		if _, err := decryptMedia(enc[:len(enc)-1], keyText, mode); err == nil {
			t.Fatal("partial block accepted")
		}
	}
}
func TestWeixinMediaParsingPreservesTranscriptAndImage(t *testing.T) {
	raw := `[{"type":1,"text_item":{"text":"看图"}},{"type":2,"image_item":{"aeskey":"abcd","media":{"full_url":"https://cdn.example/image"}}},{"type":3,"voice_item":{"text":"描述这张图片"}}]`
	var items []weixinItem
	if json.Unmarshal([]byte(raw), &items) != nil {
		t.Fatal("fixture")
	}
	text, attachments := parseWeixinItems(items)
	if text != "看图" || len(attachments) != 2 || attachments[0].Cipher != "ecb" || attachments[1].Transcript != "描述这张图片" {
		t.Fatalf("%s %+v", text, attachments)
	}
}
func TestWeixinPrefersOfficialCDNAndQuotedImage(t *testing.T) {
	raw := `[{"type":2,"image_item":{"aeskey":"00112233445566778899aabbccddeeff","media":{"encrypt_query_param":"AA==","full_url":"https://novac2c.cdn.weixin.qq.com/c2c/download?encrypted_query_param=alt"}}}]`
	var items []weixinItem
	if json.Unmarshal([]byte(raw), &items) != nil {
		t.Fatal("fixture")
	}
	_, attachments := parseWeixinItems(items)
	if len(attachments) != 1 || attachments[0].URL != weixinCDNBase+"/download?encrypted_query_param="+url.QueryEscape("AA==") || len(attachments[0].Fallbacks) != 1 {
		t.Fatalf("%+v", attachments)
	}
	quoted := `[{"type":1,"text_item":{"text":"这个图片是什么"},"ref_msg":{"message_item":{"type":2,"image_item":{"media":{"encrypt_query_param":"QQ=="}}}}}]`
	if json.Unmarshal([]byte(quoted), &items) != nil {
		t.Fatal("quoted")
	}
	text, attachments := parseWeixinItems(items)
	if text != "这个图片是什么" || len(attachments) != 1 || !strings.Contains(attachments[0].URL, "QQ") {
		t.Fatalf("%s %+v", text, attachments)
	}
}
func TestWeixinParsesFileAndQuotedFile(t *testing.T) {
	raw := `[{"type":4,"file_item":{"file_name":"报价单.pdf","media":{"encrypt_query_param":"AA==","aes_key":"abcd"}}}]`
	var items []weixinItem
	if json.Unmarshal([]byte(raw), &items) != nil {
		t.Fatal("fixture")
	}
	_, attachments := parseWeixinItems(items)
	if len(attachments) != 1 || attachments[0].Kind != "file" || attachments[0].Name != "报价单.pdf" || attachments[0].Cipher != "ecb" {
		t.Fatalf("%+v", attachments)
	}
	quoted := `[{"type":1,"text_item":{"text":"看看这个"},"ref_msg":{"message_item":{"type":4,"file_item":{"file_name":"ref.txt","media":{"encrypt_query_param":"BB=="}}}}}]`
	if json.Unmarshal([]byte(quoted), &items) != nil {
		t.Fatal("quoted")
	}
	text, attachments := parseWeixinItems(items)
	if text != "看看这个" || len(attachments) != 1 || attachments[0].Name != "ref.txt" {
		t.Fatalf("%s %+v", text, attachments)
	}
}
func TestEncryptMediaRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 16)
	plain := pngFixture(t)
	enc, err := encryptMedia(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptMedia(enc, hex.EncodeToString(key), "ecb")
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("%v", err)
	}
}
func TestWeixinCoalesceAndSkipGenerating(t *testing.T) {
	if weixinShouldHandle(weixinInbound{Type: 1, From: "alice", State: 1}, "bot") {
		t.Fatal("generating accepted")
	}
	msgs := coalesceWeixinInbound([]weixinInbound{
		{From: "alice", Type: 1, Items: []weixinItem{{Type: 2}}, ID: []byte("1")},
		{From: "alice", Type: 1, Items: []weixinItem{{Type: 1}}, ID: []byte("2"), Context: "ctx"},
	})
	if len(msgs) != 1 || len(msgs[0].Items) != 2 || msgs[0].Context != "ctx" {
		t.Fatalf("%+v", msgs)
	}
}
func TestTrustedWeixinCDNUsesPlatformClientAndFallbacks(t *testing.T) {
	s := testService(t, nil)
	s.mediaClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("strict SSRF client used for weixin CDN")
		return nil, nil
	})}
	hits := 0
	s.platformMedia = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing UA")
		}
		if strings.Contains(r.URL.Path, "bad") {
			return nil, errors.New("first url down")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
	})}
	got, err := s.downloadMedia(context.Background(), Attachment{
		Kind: "image", URL: "https://novac2c.cdn.weixin.qq.com/c2c/bad",
		Fallbacks: []string{"https://novac2c.cdn.weixin.qq.com/c2c/good"},
	})
	if err != nil || !bytes.Equal(got, pngFixture(t)) || hits < 2 {
		t.Fatalf("hits=%d err=%v", hits, err)
	}
}
func TestEncryptedDownloadFallsBackToPlainImage(t *testing.T) {
	s := testService(t, nil)
	s.platformMedia = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
	})}
	got, err := s.downloadMedia(context.Background(), Attachment{Kind: "image", URL: "https://wx.qpic.cn/a", Cipher: "ecb", Key: "not-a-key"})
	if err != nil || !bytes.Equal(got, pngFixture(t)) {
		t.Fatalf("%v %v", err, len(got))
	}
}
func TestApplyControlImagesAttachesToCurrentUserTurn(t *testing.T) {
	img := analyzer.ImageAttachment{Path: "/tmp/a.png", MediaType: "image/png"}
	var history []analyzer.Message
	ApplyControlImages(&history, []analyzer.ImageAttachment{img}, "看图")
	if len(history) != 1 || history[0].Role != "user" || len(history[0].Attachments) != 1 {
		t.Fatalf("%+v", history)
	}
	history = []analyzer.Message{{Role: "assistant", Content: "ok"}, {Role: "user", Content: "这个图片是什么"}}
	ApplyControlImages(&history, []analyzer.ImageAttachment{img}, "ignore")
	if len(history) != 2 || history[0].Role != "assistant" || len(history[1].Attachments) != 1 {
		t.Fatalf("%+v", history)
	}
	if !strings.Contains(FileInstructions(), "chat_send_file") {
		t.Fatal("missing send instruction")
	}
}
func TestPureImageReachesRunnerManifestAndPersistsForCheckpoint(t *testing.T) {
	var imagePath string
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		images, err := LoadControlImages(ConfirmDirFromContext(ctx))
		if err != nil || len(images) != 1 {
			return "missing image", err
		}
		imagePath = images[0].Path
		if images[0].MediaType != "image/png" {
			t.Error("wrong image type")
		}
		return "saw image", nil
	})
	s.mediaClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
	})}
	m := testMessage("img", "")
	m.Attachments = []Attachment{{Kind: "image", URL: "https://cdn.example/a"}}
	if _, err := s.command(testChannel(), m); err != nil {
		t.Fatal(err)
	}
	j := waitStatus(t, s, "completed")
	if j.Result != "saw image" {
		t.Fatal(j.Result)
	}
	s.stop()
	s.wg.Wait()
	if !filepath.IsAbs(imagePath) {
		t.Fatal("relative image")
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatal("checkpoint image removed", err)
	}
}
func TestVoiceTranscriptSkipsDownloadAndRawAudioUsesConfiguredService(t *testing.T) {
	s := testService(t, nil)
	s.mediaClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("transcript triggered download"); return nil, nil })}
	m := Message{Attachments: []Attachment{{Kind: "audio", Transcript: "你好"}}}
	prompt, _, err := s.prepareMedia(context.Background(), m, t.TempDir(), "语音")
	if err != nil || !strings.Contains(prompt, "你好") {
		t.Fatalf("%q %v", prompt, err)
	}
	m.Attachments[0].Transcript = ""
	m.Attachments[0].URL = "https://cdn.example/a"
	if _, _, err := s.prepareMedia(context.Background(), m, t.TempDir(), "语音"); err == nil || !strings.Contains(err.Error(), "transcription_url") {
		t.Fatal(err)
	}
	s.cfg.TranscriptionURL = "https://speech.example/v1/audio/transcriptions"
	s.cfg.TranscriptionKeyEnv = "CHAT_TEST_STT_KEY"
	t.Setenv("CHAT_TEST_STT_KEY", "test-key")
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing transcription auth")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		b, _ := io.ReadAll(file)
		if string(b) != "audio" || strings.ContainsAny(header.Filename, "\r\n") || r.FormValue("model") != "whisper-1" {
			t.Error("invalid multipart")
		}
		return jsonResponse(map[string]string{"text": "转写成功"}), nil
	})}
	got, err := s.transcribe(context.Background(), []byte("audio"), "voice\r\n.mp3")
	if err != nil || got != "转写成功" {
		t.Fatalf("%s %v", got, err)
	}
}
func TestUnauthorizedAttachmentNeverStartsDownload(t *testing.T) {
	s := testService(t, nil)
	s.mediaClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unauthorized download"); return nil, nil })}
	m := testMessage("bad", "")
	m.User = "mallory"
	m.Attachments = []Attachment{{Kind: "image", URL: "https://cdn.example/a"}}
	if _, err := s.command(testChannel(), m); err == nil {
		t.Fatal("unauthorized media accepted")
	}
}
func TestInboundFileReachesPromptAndPersists(t *testing.T) {
	var saved string
	s := testService(t, func(ctx context.Context, prompt, session string) (string, error) {
		saved = prompt
		return "saw file", nil
	})
	s.mediaClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("hello file"))}, nil
	})}
	m := testMessage("file1", "看看这个")
	m.Attachments = []Attachment{{Kind: "file", URL: "https://cdn.example/a", Name: "hello.txt"}}
	if _, err := s.command(testChannel(), m); err != nil {
		t.Fatal(err)
	}
	j := waitStatus(t, s, "completed")
	if j.Result != "saw file" {
		t.Fatal(j.Result)
	}
	if !strings.Contains(saved, "hello.txt") || !strings.Contains(saved, "hello file") {
		t.Fatalf("%s", saved)
	}
}
func TestListSendFilesSkipsSent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt.sent"), []byte("b"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := listSendFiles(dir)
	if err != nil || len(files) != 1 || files[0].Name != "a.txt" {
		t.Fatalf("%+v %v", files, err)
	}
}
func TestSendWeixinFileUploadsThenSends(t *testing.T) {
	s := testService(t, nil)
	var uploaded, sent bool
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "getuploadurl") {
			return jsonResponse(map[string]any{"ret": 0, "upload_param": "up"}), nil
		}
		if strings.HasSuffix(r.URL.Path, "sendmessage") {
			sent = true
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			msg := body["msg"].(map[string]any)
			items := msg["item_list"].([]any)
			item := items[0].(map[string]any)
			if item["type"].(float64) != 4 {
				t.Fatalf("%v", item["type"])
			}
			return jsonResponse(map[string]int{"ret": 0}), nil
		}
		t.Fatalf("unexpected %s", r.URL)
		return nil, nil
	})}
	s.platformMedia = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		uploaded = true
		if r.Method != "POST" || !strings.Contains(r.URL.Path, "/c2c/upload") {
			t.Fatalf("%s %s", r.Method, r.URL)
		}
		resp := jsonResponse(map[string]string{})
		resp.Header.Set("x-encrypted-param", "down")
		return resp, nil
	})}
	c := config.ChatChannel{Name: "wechat-quick", Platform: "weixin", AppID: "bot", AppSecret: "secret", APIURL: weixinBase}
	m := Message{User: "alice", Chat: "alice", ContextToken: "ctx"}
	if err := s.sendWeixinFile(context.Background(), c, m, []byte("report-body"), "报告.txt"); err != nil {
		t.Fatal(err)
	}
	if !uploaded || !sent {
		t.Fatalf("uploaded=%v sent=%v", uploaded, sent)
	}
}

func TestQQAttachmentCDNUsesProxyCompatibleDownloadAndVision(t *testing.T) {
	s := testService(t, nil)
	s.mediaClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("QQ attachment routed through fake-IP rejecting generic downloader")
		return nil, errors.New("blocked")
	})}
	s.platformMedia = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.RawQuery != "rkey=a%2Bb&fileid=fixture" {
			t.Errorf("signed query changed: %s", r.URL.RawQuery)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngFixture(t)))}, nil
	})}
	for _, host := range []string{"multimedia.nt.qq.com.cn", "qqbot.ugcimg.cn", "grouptalk.c2c.qq.com"} {
		_, images, err := s.prepareChannelMedia(context.Background(), config.ChatChannel{Platform: "qqbot"}, Message{Attachments: []Attachment{{Kind: "image", URL: "https://" + host + "/download?rkey=a%2Bb&fileid=fixture"}}}, filepath.Join(t.TempDir(), host), "看图")
		if err != nil || len(images) != 1 {
			t.Fatalf("%s images=%d err=%v", host, len(images), err)
		}
	}
	for _, host := range []string{"evil.qq.com.cn", "multimedia.nt.qq.com.cn.evil.test", "evil.ugcimg.cn"} {
		if trustedMediaHost(host) {
			t.Fatalf("overbroad trust: %s", host)
		}
	}
	if newPlatformMediaClient().CheckRedirect(&http.Request{URL: &url.URL{Scheme: "https", Host: "evil.test"}}, nil) == nil {
		t.Fatal("untrusted redirect allowed")
	}
}

func TestMediaCancellationPreserved(t *testing.T) {
	s := testService(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.downloadMedia(ctx, Attachment{URL: "https://multimedia.nt.qq.com.cn/download"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
