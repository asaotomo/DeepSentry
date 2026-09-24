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
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
)

const maxMediaBytes = 20 << 20

var trustedMediaSuffixes = []string{
	".weixin.qq.com", ".wechat.com", ".qpic.cn", ".qlogo.cn",
	".gtimg.cn", ".idqqimg.com", ".myqcloud.com",
	".dingtalk.com", ".alicdn.com",
}

type Attachment struct {
	ResourceKey string   `json:"resource_key,omitempty"`
	Kind        string   `json:"kind"`
	URL         string   `json:"url,omitempty"`
	Name        string   `json:"name,omitempty"`
	Key         string   `json:"key,omitempty"`
	Cipher      string   `json:"cipher,omitempty"`
	Transcript  string   `json:"transcript,omitempty"`
	Fallbacks   []string `json:"fallbacks,omitempty"`
	// LocalPath is set only after an authorized prefetch. It is omitted from
	// JSON so inbound callbacks cannot point the Agent at arbitrary files.
	LocalPath string `json:"-"`
}

func publicMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443")
}
func trustedMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.Contains(host, "..") || net.ParseIP(host) != nil {
		return false
	}
	// QQ's newer attachment hosts are outside qpic.cn. Match these exact
	// service hosts, not the entire qq.com.cn/ugcimg.cn namespaces.
	switch host {
	case "multimedia.nt.qq.com.cn", "qqbot.ugcimg.cn", "grouptalk.c2c.qq.com":
		return true
	}
	for _, suffix := range trustedMediaSuffixes {
		name := strings.TrimPrefix(suffix, ".")
		if host == name || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
func platformMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || !trustedMediaHost(u.Hostname()) {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Port() == "" || u.Port() == "443"
	case "http":
		return u.Port() == "" || u.Port() == "80"
	default:
		return false
	}
}
func canonicalMediaURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	if u.Scheme == "http" && trustedMediaHost(u.Hostname()) {
		u.Scheme = "https"
		if u.Port() == "80" {
			u.Host = u.Hostname()
		}
		return u.String()
	}
	return raw
}
func publicMediaIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if netip.MustParsePrefix(cidr).Contains(addr) {
			return false
		}
	}
	return true
}
func newMediaClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errors.New("附件域名解析失败")
		}
		if len(ips) == 0 {
			return nil, errors.New("附件地址无效")
		}
		want4 := !strings.HasSuffix(network, "6")
		want6 := strings.HasSuffix(network, "6") || network == "tcp"
		var ordered []net.IPAddr
		if want4 {
			for _, ip := range ips {
				if ip.IP.To4() != nil && publicMediaIP(ip.IP) {
					ordered = append(ordered, ip)
				}
			}
		}
		if want6 {
			for _, ip := range ips {
				if ip.IP.To4() == nil && publicMediaIP(ip.IP) {
					ordered = append(ordered, ip)
				}
			}
		}
		if len(ordered) == 0 {
			for _, ip := range ips {
				if !publicMediaIP(ip.IP) {
					return nil, errors.New("附件不允许访问本地或内网地址")
				}
			}
			return nil, errors.New("附件地址无效")
		}
		var last error
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ordered {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last != nil {
			return nil, last
		}
		return nil, errors.New("附件下载失败，请重新发送")
	}
	return &http.Client{Transport: t, Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || !publicMediaURL(r.URL.String()) {
			return errors.New("附件重定向被拒绝")
		}
		return nil
	}}
}
func newPlatformMediaClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: t, Timeout: 40 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || !platformMediaURL(r.URL.String()) {
			return errors.New("附件重定向被拒绝")
		}
		return nil
	}}
}
func (s *Service) mediaClientFor(raw string) *http.Client {
	if platformMediaURL(raw) && s.platformMedia != nil {
		return s.platformMedia
	}
	if s.mediaClient != nil {
		return s.mediaClient
	}
	return newMediaClient()
}
func mediaCandidateURLs(a Attachment) []string {
	var out []string
	for _, raw := range append([]string{a.URL}, a.Fallbacks...) {
		raw = canonicalMediaURL(raw)
		if raw == "" {
			continue
		}
		dup := false
		for _, e := range out {
			if e == raw {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, raw)
		}
	}
	return out
}
func looksLikeImage(b []byte) bool {
	if len(b) < 12 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(b), ";")[0])) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
func (s *Service) fetchMediaBytes(ctx context.Context, raw string) ([]byte, error) {
	if !platformMediaURL(raw) && !publicMediaURL(raw) {
		return nil, errors.New("附件需要 HTTPS 公网下载地址")
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
		if err != nil {
			return nil, errors.New("附件地址无效")
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; DeepSentryChat/1.0; MicroMessenger)")
		resp, err := s.mediaClientFor(raw).Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			last = fmt.Errorf("附件下载 HTTP %d", resp.StatusCode)
			if resp.StatusCode >= 500 && attempt == 0 {
				continue
			}
			return nil, last
		}
		if readErr != nil || len(body) == 0 || len(body) > maxMediaBytes {
			return nil, errors.New("附件为空、读取失败或超过 20 MiB")
		}
		return body, nil
	}
	if last != nil {
		u, _ := url.Parse(raw)
		// Never log the URL/error verbatim: signed attachment queries contain
		// credentials. Host and an allowlisted error class are sufficient.
		reason := "network_or_tls"
		var netErr net.Error
		if errors.As(last, &netErr) && netErr.Timeout() {
			reason = "timeout"
		} else if strings.Contains(last.Error(), "附件不允许访问本地或内网地址") {
			reason = "non_public_dns"
		} else if strings.Contains(last.Error(), "附件域名解析失败") {
			reason = "dns"
		} else if strings.Contains(last.Error(), "附件重定向被拒绝") {
			reason = "redirect_rejected"
		}
		log.Printf("chat media download failed host=%s reason=%s", u.Hostname(), reason)
	}
	return nil, errors.New("附件下载失败，请重新发送")
}
func (s *Service) downloadMedia(ctx context.Context, a Attachment) ([]byte, error) {
	var last error
	for _, raw := range mediaCandidateURLs(a) {
		b, err := s.fetchMediaBytes(ctx, raw)
		if err != nil {
			last = err
			continue
		}
		if a.Cipher == "" {
			return b, nil
		}
		plain, err := decryptMedia(b, a.Key, a.Cipher)
		if err == nil {
			return plain, nil
		}
		if looksLikeImage(b) {
			return b, nil
		}
		last = err
	}
	if last != nil {
		if errors.Is(last, context.Canceled) || errors.Is(last, context.DeadlineExceeded) {
			return nil, last
		}
		if strings.Contains(last.Error(), "HTTPS") || strings.Contains(last.Error(), "HTTP ") || strings.Contains(last.Error(), "20 MiB") || strings.Contains(last.Error(), "解密") {
			return nil, last
		}
	}
	return nil, errors.New("附件下载失败，请重新发送")
}
func decryptMedia(b []byte, keyText, mode string) ([]byte, error) {
	fail := errors.New("附件解密失败")
	key, err := hex.DecodeString(keyText)
	if err != nil || len(key) != 16 {
		key, err = base64.StdEncoding.DecodeString(keyText)
		if err != nil {
			key, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(keyText, "="))
			if err != nil {
				return nil, fail
			}
		}
	}
	if len(key) == 32 && mode == "ecb" {
		if decoded, e := hex.DecodeString(string(key)); e == nil {
			key = decoded
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil || len(b) == 0 || len(b)%aes.BlockSize != 0 {
		return nil, fail
	}
	out := make([]byte, len(b))
	switch mode {
	case "ecb":
		for i := 0; i < len(b); i += aes.BlockSize {
			block.Decrypt(out[i:i+aes.BlockSize], b[i:i+aes.BlockSize])
		}
	case "cbc":
		cipher.NewCBCDecrypter(block, key[:16]).CryptBlocks(out, b)
	default:
		return nil, fail
	}
	n := int(out[len(out)-1])
	if n < 1 || n > 32 || n > len(out) {
		return nil, fail
	}
	for _, v := range out[len(out)-n:] {
		if int(v) != n {
			return nil, fail
		}
	}
	return out[:len(out)-n], nil
}
func encryptMedia(b, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, errors.New("附件加密失败")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("附件加密失败")
	}
	n := aes.BlockSize - len(b)%aes.BlockSize
	padded := append(append([]byte{}, b...), bytes.Repeat([]byte{byte(n)}, n)...)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return out, nil
}
func (s *Service) prepareMedia(ctx context.Context, m Message, dir, prompt string) (string, []analyzer.ImageAttachment, error) {
	return s.prepareChannelMedia(ctx, config.ChatChannel{}, m, dir, prompt)
}

func (s *Service) prepareChannelMedia(ctx context.Context, c config.ChatChannel, m Message, dir, prompt string) (string, []analyzer.ImageAttachment, error) {
	if len(m.Attachments) > 8 {
		return "", nil, errors.New("每条消息最多 8 个图片/语音/文件附件")
	}
	var images []analyzer.ImageAttachment
	total := 0
	mediaDir := filepath.Join(filepath.Dir(s.cfg.Store), "media", filepath.Base(dir))
	for n, a := range m.Attachments {
		if a.Kind == "audio" && strings.TrimSpace(a.Transcript) != "" {
			prompt += "\n用户语音：" + a.Transcript
			continue
		}
		if a.Kind != "image" && a.Kind != "audio" && a.Kind != "file" {
			return "", nil, errors.New("暂不支持该附件类型，请发送图片、语音或文件")
		}
		if a.Kind == "audio" && s.cfg.TranscriptionURL == "" {
			return "", nil, errors.New("收到语音，但尚未配置 chat.transcription_url 转写服务；请先发送文字")
		}
		if strings.TrimSpace(a.URL) == "" && len(a.Fallbacks) == 0 && a.ResourceKey == "" && a.LocalPath == "" {
			return "", nil, errors.New("附件还在传输中，请稍后再发一次")
		}
		b, err := s.downloadChannelMedia(ctx, c, m, a)
		if err != nil {
			return "", nil, err
		}
		total += len(b)
		if total > 40<<20 {
			return "", nil, errors.New("附件总大小超过 40 MiB")
		}
		if a.Kind == "audio" {
			text, err := s.transcribe(ctx, b, a.Name)
			if err != nil {
				return "", nil, err
			}
			prompt += "\n用户语音：" + text
			continue
		}
		if err := os.MkdirAll(mediaDir, 0700); err != nil {
			return "", nil, err
		}
		name := safeFileName(a.Name)
		if a.Kind == "image" {
			name = fmt.Sprintf("image-%d", n)
		} else if name == "file.bin" {
			name = fmt.Sprintf("file-%d", n)
		}
		attachmentDir := filepath.Join(mediaDir, fmt.Sprintf("%02d", n))
		if err := os.MkdirAll(attachmentDir, 0700); err != nil {
			return "", nil, err
		}
		path := filepath.Join(attachmentDir, name)
		if err := os.WriteFile(path, b, 0600); err != nil {
			return "", nil, err
		}
		if a.Kind == "image" || looksLikeImage(b) {
			image, err := analyzer.PrepareImageAttachment(path)
			if err != nil {
				if a.Kind == "image" {
					_ = os.Remove(path)
					return "", nil, errors.New("图片格式不支持或内容无效（支持 PNG/JPEG/GIF/WebP）")
				}
			} else {
				images = append(images, image)
			}
		}
		if a.Kind == "file" {
			prompt += "\n用户发送了文件「" + name + "」，已保存到本机 " + path + "。请直接读取或用 document_parse 解析后再回答。\n" + filePreview(name, b)
		}
	}
	return prompt, images, nil
}

func (s *Service) prefetchRoot() string {
	return filepath.Join(filepath.Dir(s.cfg.Store), "media", "prefetch")
}

// prefetchPendingMedia downloads authorized inbound attachments as soon as a
// task is queued. Signed chat URLs often expire before a busy owner is free.
func (s *Service) prefetchPendingMedia(id string, c config.ChatChannel, m Message) {
	if id == "" || len(m.Attachments) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	attachments := append([]Attachment(nil), m.Attachments...)
	root := s.prefetchRoot()
	dir := filepath.Join(root, filepath.Base(id))
	for i, a := range attachments {
		if a.LocalPath != "" || (a.Kind != "image" && a.Kind != "audio" && a.Kind != "file") {
			continue
		}
		if strings.TrimSpace(a.URL) == "" && len(a.Fallbacks) == 0 && a.ResourceKey == "" {
			continue
		}
		b, err := s.downloadChannelMedia(ctx, c, m, a)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return
		}
		name := safeFileName(a.Name)
		if a.Kind == "image" {
			name = fmt.Sprintf("image-%d", i)
		} else if name == "file.bin" {
			name = fmt.Sprintf("file-%d", i)
		}
		path := filepath.Join(dir, fmt.Sprintf("%02d-%s", i, name))
		// #nosec G703 -- path is rooted in operator-configured chat store media/prefetch/<job>.
		if err := os.WriteFile(path, b, 0600); err != nil {
			return
		}
		attachments[i].LocalPath = path
		attachments[i].URL = ""
		attachments[i].Fallbacks = nil
		attachments[i].ResourceKey = ""
		attachments[i].Key = ""
		attachments[i].Cipher = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return
	}
	p.message.Attachments = attachments
	s.pending[id] = p
}

func (s *Service) readPrefetchedMedia(path string) ([]byte, error) {
	root, err := filepath.Abs(s.prefetchRoot())
	if err != nil {
		return nil, errors.New("预取附件目录不可用")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("预取附件目录不可用")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("预取附件路径无效")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, errors.New("预取附件不可用")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("预取附件不在缓存目录内")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, errors.New("预取附件目录不可用")
	}
	defer rootHandle.Close()
	f, err := rootHandle.Open(rel)
	if err != nil {
		return nil, errors.New("读取预取附件失败")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("预取附件不是普通文件")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxMediaBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxMediaBytes {
		return nil, errors.New("预取附件为空、读取失败或超过 20 MiB")
	}
	return b, nil
}

func (s *Service) transcribe(ctx context.Context, b []byte, name string) (string, error) {
	if !publicMediaURL(s.cfg.TranscriptionURL) {
		return "", errors.New("chat.transcription_url 必须为 HTTPS 接口")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.NewReplacer("\r", "", "\n", "").Replace(name)
	if name == "." || name == "" {
		name = "audio.bin"
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	_, _ = part.Write(b)
	_ = w.WriteField("model", firstNonEmpty(s.cfg.TranscriptionModel, "whisper-1"))
	_ = w.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", s.cfg.TranscriptionURL, &body)
	if err != nil {
		return "", errors.New("转写地址无效")
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if key := os.Getenv(s.cfg.TranscriptionKeyEnv); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", errors.New("语音转写请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("语音转写 HTTP %d；请检查服务或音频格式", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64001))
	if err != nil || len(raw) > 64000 {
		return "", errors.New("语音转写结果过大或读取失败")
	}
	var result struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &result) != nil || strings.TrimSpace(result.Text) == "" {
		return "", errors.New("语音转写没有返回文字")
	}
	return result.Text, nil
}
func LoadControlImages(dir string) ([]analyzer.ImageAttachment, error) {
	var images []analyzer.ImageAttachment
	b, err := os.ReadFile(filepath.Join(dir, "images.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(b, &images)
	return images, err
}

// ApplyControlImages attaches inbound chat images to the current user turn.
func ApplyControlImages(history *[]analyzer.Message, images []analyzer.ImageAttachment, task string) {
	if history == nil || len(images) == 0 {
		return
	}
	h := *history
	if len(h) == 0 || h[len(h)-1].Role != "user" {
		text := strings.TrimSpace(task)
		if text == "" {
			text = "请分析所附图片，并结合当前任务继续。"
		}
		h = append(h, analyzer.Message{Role: "user", Content: text})
	}
	h[len(h)-1].Attachments = append(h[len(h)-1].Attachments, images...)
	*history = h
}

func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(name)
	if name == "." || name == ".." || name == "" {
		return "file.bin"
	}
	return name
}

func filePreview(name string, b []byte) string {
	if looksLikeImage(b) {
		return "该文件是图片，已作为视觉附件。"
	}
	if utf8.Valid(b) && mostlyPrintable(b) {
		return "文件内容预览：\n" + clip(string(b), 4000)
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".docx", ".xlsx", ".xls", ".csv", ".rtf", ".pptx":
		return "办公文档，请用 document_parse 读取上述路径。"
	default:
		return "二进制文件，可用 file_ident、file_strings 或 document_parse 分析上述路径。"
	}
}

func mostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	bad := 0
	n := min(len(b), 4096)
	for _, c := range b[:n] {
		if c == 0 || (c < 32 && c != '\t' && c != '\n' && c != '\r') {
			bad++
		}
	}
	return bad*20 < n
}

func FileInstructions() string {
	return "\n当前是聊天任务。用户发来的文件已保存到本机路径，请直接读取或用 document_parse 解析，不要说收不到文件。若要把本机文件发给用户（例如桌面、下载目录或刚生成的报告），调用 chat_send_file，path 使用本机绝对路径；不要只在文字里贴路径。"
}
