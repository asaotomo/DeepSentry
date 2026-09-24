package scheduler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-edr/internal/config"
)

func SendDingTalkMarkdown(webhook, secret, title, markdown string) error {
	webhook = strings.TrimSpace(webhook)
	if webhook == "" {
		return fmt.Errorf("dingtalk_webhook 未配置")
	}
	if title == "" {
		title = "DeepSentry 定时任务通知"
	}
	if len(markdown) > 12000 {
		markdown = safeTextPrefix(markdown, 12000) + "\n\n...(内容过长已截断，请查看本地报告)..."
	}
	target := webhook
	if strings.TrimSpace(secret) != "" {
		signed, err := signDingTalkURL(webhook, secret, time.Now())
		if err != nil {
			return err
		}
		target = signed
	}
	body := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": title,
			"text":  markdown,
		},
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := config.HTTPClient(12 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("钉钉返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func SendFeishuMarkdown(webhook, secret, title, markdown string) error {
	webhook = strings.TrimSpace(webhook)
	if webhook == "" {
		return fmt.Errorf("feishu_webhook 未配置")
	}
	if title == "" {
		title = "DeepSentry 定时任务通知"
	}
	if len(markdown) > 12000 {
		markdown = safeTextPrefix(markdown, 12000) + "\n\n...(内容过长已截断，请查看本地报告)..."
	}
	text := title + "\n\n" + markdown
	body := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	if strings.TrimSpace(secret) != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		body["timestamp"] = ts
		body["sign"] = signFeishu(secret, ts)
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, webhook, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := config.HTTPClient(12 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("飞书返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if len(raw) > 0 && !feishuResponseOK(raw) {
		return fmt.Errorf("飞书返回异常: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func SendEmailGatewayMarkdown(gatewayURL, token, headerName, to, from, subject, markdown string) error {
	gatewayURL = strings.TrimSpace(gatewayURL)
	if gatewayURL == "" {
		return fmt.Errorf("email_gateway_url 未配置")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("email_to 未配置")
	}
	if subject == "" {
		subject = "DeepSentry 定时任务通知"
	}
	if len(markdown) > 30000 {
		markdown = safeTextPrefix(markdown, 30000) + "\n\n...(内容过长已截断，请查看本地报告)..."
	}
	body := map[string]any{
		"to":       splitRecipients(to),
		"from":     strings.TrimSpace(from),
		"subject":  subject,
		"markdown": markdown,
		"text":     markdownToText(markdown),
		"source":   "DeepSentry",
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, gatewayURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	applyGatewayAuth(req, token, headerName)
	resp, err := config.HTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("邮件网关返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func signDingTalkURL(webhook, secret string, now time.Time) (string, error) {
	u, err := url.Parse(webhook)
	if err != nil {
		return "", err
	}
	ts := strconv.FormatInt(now.UnixNano()/int64(time.Millisecond), 10)
	message := ts + "\n" + secret
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func signFeishu(secret, timestamp string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func feishuResponseOK(raw []byte) bool {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return true
	}
	for _, key := range []string{"code", "StatusCode"} {
		if v, ok := obj[key]; ok {
			switch x := v.(type) {
			case float64:
				return x == 0
			case string:
				return x == "" || x == "0"
			}
		}
	}
	return true
}

func splitRecipients(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == ' '
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func applyGatewayAuth(req *http.Request, token, headerName string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	headerName = strings.TrimSpace(headerName)
	if headerName == "" {
		headerName = "Authorization"
	}
	if strings.Contains(headerName, ":") {
		parts := strings.SplitN(headerName, ":", 2)
		name := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if name != "" {
			if value == "" {
				value = token
			} else if strings.Contains(value, "{token}") {
				value = strings.ReplaceAll(value, "{token}", token)
			}
			req.Header.Set(name, value)
		}
		return
	}
	if strings.EqualFold(headerName, "Authorization") {
		if strings.HasPrefix(strings.ToLower(token), "bearer ") || strings.HasPrefix(strings.ToLower(token), "basic ") {
			req.Header.Set(headerName, token)
		} else {
			req.Header.Set(headerName, "Bearer "+token)
		}
		return
	}
	req.Header.Set(headerName, token)
}

func markdownToText(markdown string) string {
	replacer := strings.NewReplacer("#", "", "*", "", "`", "", ">", "", "|", " ")
	return strings.TrimSpace(replacer.Replace(markdown))
}
