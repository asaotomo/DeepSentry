package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func apiJSON(ctx context.Context, client *http.Client, method, target string, headers map[string]string, body any) (map[string]json.RawMessage, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("invalid API endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &platformError{Message: "平台网络中断，送达结果未知", Uncertain: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp)
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, &platformError{Message: "平台回执读取失败，结果未知", Uncertain: true}
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || data == nil {
		return nil, &platformError{Message: "平台返回无效 JSON，结果未知", Uncertain: true}
	}
	for _, k := range []string{"ret", "retcode", "errcode", "code"} {
		if v, ok := data[k]; ok && string(v) != "0" {
			return nil, fmt.Errorf("平台接口错误 %s=%s", k, clip(string(v), 32))
		}
	}
	return data, nil
}
func trustedURL(raw, scheme string, hosts ...string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.User != nil || u.Fragment != "" || u.Host == "" || u.Port() != "" && u.Port() != "443" {
		return false
	}
	for _, h := range hosts {
		if u.Hostname() == h || strings.HasSuffix(u.Hostname(), "."+h) {
			return true
		}
	}
	return false
}
func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
