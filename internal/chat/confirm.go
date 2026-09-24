package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type confirmDirKey struct{}

const chatConfirmEnv = "DEEPSENTRY_CHAT_CONFIRM_DIR"
const chatSendDirEnv = "DEEPSENTRY_CHAT_SEND_DIR"

type ConfirmRequest struct {
	Kind       string `json:"kind,omitempty"`
	Seq        int64  `json:"seq"`
	Summary    string `json:"summary"`
	Command    string `json:"command,omitempty"`
	Reason     string `json:"reason"`
	ScopeKey   string `json:"scope_key,omitempty"`
	ScopeLabel string `json:"scope_label,omitempty"`
}

type ConfirmReply struct {
	Text     string `json:"text,omitempty"`
	Seq      int64  `json:"seq"`
	Decision string `json:"decision"`
}

func WithConfirmDir(ctx context.Context, dir string) context.Context {
	if strings.TrimSpace(dir) == "" {
		return ctx
	}
	return context.WithValue(ctx, confirmDirKey{}, dir)
}

func ConfirmDirFromContext(ctx context.Context) string {
	dir, _ := ctx.Value(confirmDirKey{}).(string)
	return strings.TrimSpace(dir)
}

// ParseConfirmText only accepts wording that cannot be an ordinary chat reply
// for approvals. "好", "是", "ok" and "y" are deliberately absent: a casual
// acknowledgement must never release a high-risk operation.
func ParseConfirmText(text string) (string, bool) {
	switch normalizeConfirmText(text) {
	case "允许本次", "仅允许本次", "允许", "allow", "allow once", "yes":
		return "allow_once", true
	case "本次会话允许所有高危操作", "本会话允许所有高危操作", "本会话全部":
		return "allow_all_session", true
	case "本会话允许同类操作", "本会话同类", "allow session":
		return "allow_session", true
	case "拒绝", "否", "不", "不允许", "no", "n", "deny", "cancel":
		return "deny", true
	default:
		return "", false
	}
}

// IsExplicitConfirmPhrase reports wording that only makes sense as an answer
// to an approval prompt, so it must not start a new task when nothing waits.
func IsExplicitConfirmPhrase(text string) bool {
	switch normalizeConfirmText(text) {
	case "允许本次", "仅允许本次", "allow once",
		"本次会话允许所有高危操作", "本会话允许所有高危操作", "本会话全部",
		"本会话允许同类操作", "本会话同类", "allow session":
		return true
	}
	return false
}

func normalizeConfirmText(text string) string {
	s := strings.ToLower(strings.TrimSpace(text))
	s = strings.TrimPrefix(s, "/ds ")
	s = strings.Trim(strings.TrimSpace(s), "。.!！")
	return strings.Join(strings.Fields(s), " ")
}

// PendingConfirmReminder answers any non-command message while an approval
// waits, so an unrelated reply cannot queue a task and let the prompt time out.
func PendingConfirmReminder(req ConfirmRequest) string {
	return "还有一项操作在等你确认，没收到明确答复前不会执行。\n\n" + FormatConfirmPrompt(req) + "\n\n不想继续就发 /stop。"
}

func FormatConfirmPrompt(req ConfirmRequest) string {
	if req.Kind == "input" {
		return "需要补充信息：\n" + req.Summary + "\n直接回复即可继续原任务；/stop 停止，/new 开新会话。"
	}
	var b strings.Builder
	b.WriteString("已拦截，尚未执行。\n")
	body := strings.TrimSpace(req.Command)
	if body == "" {
		body = strings.TrimSpace(req.Summary)
	}
	if body != "" {
		b.WriteString("\n要执行：\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	if reason := strings.TrimSpace(req.Reason); reason != "" && reason != body {
		b.WriteString("\n拦截理由：\n")
		b.WriteString(reason)
		b.WriteString("\n")
	}
	if req.ScopeLabel != "" {
		b.WriteString("\n同类授权范围：" + req.ScopeLabel + "\n")
	}
	b.WriteString("\n请原样回复其中一项：允许本次 / 本会话同类 / 本次会话允许所有高危操作 / 拒绝\n10 分钟内未回复按拒绝处理。")
	return b.String()
}

func WriteConfirmRequest(dir string, req ConfirmRequest) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, "reply.json"))
	if req.Seq == 0 {
		req.Seq = time.Now().UnixNano()
	}
	return writeConfirmJSON(filepath.Join(dir, "request.json"), req)
}

func ReadConfirmRequest(dir string) (ConfirmRequest, error) {
	var req ConfirmRequest
	data, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		return req, err
	}
	err = json.Unmarshal(data, &req)
	return req, err
}

func WriteConfirmReply(dir string, decision string) error {
	if decision != "allow_once" && decision != "allow_session" && decision != "allow_all_session" && decision != "deny" {
		return errors.New("invalid confirmation decision")
	}
	req, err := ReadConfirmRequest(dir)
	if err != nil || req.Seq == 0 || req.Kind == "input" {
		return errors.New("no pending confirmation")
	}
	return writeConfirmJSON(filepath.Join(dir, "reply.json"), ConfirmReply{Seq: req.Seq, Decision: decision})
}

func WaitConfirmReply(dir string, stop <-chan struct{}, timeout time.Duration) (ConfirmReply, bool) {
	defer os.Remove(filepath.Join(dir, "request.json"))
	defer os.Remove(filepath.Join(dir, "reply.json"))
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if reply, ok := readConfirmReply(dir); ok {
			return reply, true
		}
		if stop != nil {
			select {
			case <-stop:
				return ConfirmReply{Decision: "deny"}, false
			default:
			}
		}
		if time.Now().After(deadline) {
			return ConfirmReply{Decision: "deny"}, false
		}
		select {
		case <-tick.C:
		case <-stop:
			return ConfirmReply{Decision: "deny"}, false
		}
	}
}

func readConfirmReply(dir string) (ConfirmReply, bool) {
	var reply ConfirmReply
	data, err := os.ReadFile(filepath.Join(dir, "reply.json"))
	if err != nil {
		return reply, false
	}
	if json.Unmarshal(data, &reply) != nil || strings.TrimSpace(reply.Decision) == "" {
		return reply, false
	}
	req, err := ReadConfirmRequest(dir)
	if err != nil || req.Seq == 0 || reply.Seq != req.Seq {
		return reply, false
	}
	if req.Kind == "input" {
		return reply, reply.Decision == "user_input" && strings.TrimSpace(reply.Text) != ""
	}
	if reply.Decision != "allow_once" && reply.Decision != "allow_session" && reply.Decision != "allow_all_session" && reply.Decision != "deny" {
		return reply, false
	}
	return reply, true
}

func writeConfirmJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".confirm-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err = tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func WriteInputReply(dir, text string) error {
	req, err := ReadConfirmRequest(dir)
	if err != nil || req.Kind != "input" || req.Seq == 0 || strings.TrimSpace(text) == "" {
		return errors.New("no pending input question")
	}
	return writeConfirmJSON(filepath.Join(dir, "reply.json"), ConfirmReply{Seq: req.Seq, Decision: "user_input", Text: text})
}
