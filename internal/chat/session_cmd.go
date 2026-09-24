package chat

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ai-edr/internal/harness"
)

const (
	maxListedSessions = 8
	sessionPickTTL    = 10 * time.Minute
)

type chatSessionInfo struct {
	Gen     int
	ID      string
	Goal    string
	SavedAt time.Time
	Current bool
}

type sessionPick struct {
	items []chatSessionInfo
	at    time.Time
}

func lookupChatSessions(prefix string) []chatSessionInfo {
	summaries, err := harness.ListSessionSummaries()
	if err != nil {
		return nil
	}
	out := make([]chatSessionInfo, 0, len(summaries))
	for _, sum := range summaries {
		gen, ok := parseChatSessionGen(sum.ID, prefix)
		if !ok {
			continue
		}
		goal := strings.TrimSpace(sum.Goal)
		if goal == "" {
			goal = "（未命名会话）"
		}
		out = append(out, chatSessionInfo{Gen: gen, ID: sum.ID, Goal: clipRunes(goal, 36), SavedAt: sum.SavedAt})
	}
	return out
}

func sessionOwnerPrefix(own string) string {
	sum := sha256.Sum256([]byte(own))
	return fmt.Sprintf("session_chat_%x_g", sum[:8])
}

func sessionIDFor(own string, gen int) string {
	return fmt.Sprintf("%s%d", sessionOwnerPrefix(own), gen)
}

func parseChatSessionGen(id, prefix string) (int, bool) {
	if !strings.HasPrefix(id, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(id[len(prefix):])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (s *Service) sessionIDLocked(own string) string {
	return sessionIDFor(own, s.sessions[own])
}

func (s *Service) handleSessionTalkLocked(own, text string) (bool, string) {
	talk := normalizeSessionTalk(text)
	if talk == "" {
		return false, ""
	}
	if pick, ok := s.picks[own]; ok && time.Since(pick.at) > sessionPickTTL {
		delete(s.picks, own)
	}
	if isRestartTalk(talk) {
		return true, s.restartSessionLocked(own)
	}
	if rest, ok := matchSwitchTalk(talk); ok {
		if n, ok := parsePickIndex(rest); ok {
			items := s.listOwnerSessionsLocked(own)
			if len(items) == 0 {
				return true, "还没有可切换的会话。直接发消息开始，或说「重启会话」开新的。"
			}
			s.picks[own] = sessionPick{items: items, at: time.Now()}
			return true, s.switchListedLocked(own, n)
		}
		return true, s.promptSessionSwitchLocked(own)
	}
	if pick, ok := s.picks[own]; ok {
		if n, ok := parsePickIndex(talk); ok {
			s.picks[own] = pick
			return true, s.switchListedLocked(own, n)
		}
		delete(s.picks, own)
	}
	return false, ""
}

func (s *Service) restartSessionLocked(own string) string {
	delete(s.picks, own)
	if err := s.setSessionGenLocked(own, s.maxGenLocked(own)+1); err != nil {
		return "新建会话失败：无法保存会话状态，请检查存储目录写入权限后重试。"
	}
	delete(s.approvals, own)
	s.stopOwnerLocked(own, time.Now())
	return "已开启新会话，旧任务及排队消息已请求取消。下一条消息将使用全新上下文。"
}

func (s *Service) maxGenLocked(own string) int {
	max := s.sessions[own]
	prefix := sessionOwnerPrefix(own)
	for _, info := range s.lookupSessions(prefix) {
		if info.Gen > max {
			max = info.Gen
		}
	}
	for _, j := range s.jobs {
		if j.Owner != own {
			continue
		}
		if gen, ok := parseChatSessionGen(j.SessionID, prefix); ok && gen > max {
			max = gen
		}
	}
	return max
}

func (s *Service) promptSessionSwitchLocked(own string) string {
	items := s.listOwnerSessionsLocked(own)
	if len(items) == 0 {
		delete(s.picks, own)
		return "还没有可切换的会话。直接发消息开始，或说「重启会话」开新的。"
	}
	s.picks[own] = sessionPick{items: items, at: time.Now()}
	var b strings.Builder
	b.WriteString("近期会话，回复序号即可切换：\n")
	for i, item := range items {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		if item.Current {
			b.WriteString("【当前】")
		}
		b.WriteString(formatSessionTime(item.SavedAt))
		b.WriteString(" · ")
		b.WriteString(item.Goal)
		b.WriteByte('\n')
	}
	b.WriteString("说「重启会话」会新开一条。")
	return strings.TrimSpace(b.String())
}

func (s *Service) switchListedLocked(own string, n int) string {
	pick, ok := s.picks[own]
	if !ok || n < 1 || n > len(pick.items) {
		return fmt.Sprintf("序号无效，请回复 1 到 %d，或重新发送「切换会话」。", max(1, len(pick.items)))
	}
	item := pick.items[n-1]
	if err := s.setSessionGenLocked(own, item.Gen); err != nil {
		return "切换会话失败：无法保存会话状态，请检查存储目录写入权限后重试。"
	}
	delete(s.picks, own)
	if item.Current {
		return "当前已经在这个会话里：" + item.Goal
	}
	delete(s.approvals, own)
	s.stopOwnerLocked(own, time.Now())
	return fmt.Sprintf("已切换到第 %d 个会话：%s\n下一条消息会接着这个上下文聊。", n, item.Goal)
}

func (s *Service) listOwnerSessionsLocked(own string) []chatSessionInfo {
	prefix := sessionOwnerPrefix(own)
	byGen := map[int]chatSessionInfo{}
	for _, info := range s.lookupSessions(prefix) {
		byGen[info.Gen] = info
	}
	for _, j := range s.jobs {
		if j.Owner != own {
			continue
		}
		gen, ok := parseChatSessionGen(j.SessionID, prefix)
		if !ok {
			continue
		}
		cur := byGen[gen]
		if cur.ID == "" {
			cur.ID = j.SessionID
			cur.Gen = gen
		}
		if strings.TrimSpace(cur.Goal) == "" || cur.Goal == "（未命名会话）" || cur.Goal == "（当前会话，尚无记录）" {
			if prompt := strings.TrimSpace(j.Prompt); prompt != "" {
				cur.Goal = clipRunes(prompt, 36)
			}
		}
		if j.Updated.After(cur.SavedAt) {
			cur.SavedAt = j.Updated
		}
		byGen[gen] = cur
	}
	current := s.sessions[own]
	if cur, ok := byGen[current]; ok {
		cur.Current = true
		if strings.TrimSpace(cur.Goal) == "" {
			cur.Goal = "（当前会话，尚无记录）"
		}
		byGen[current] = cur
	} else {
		byGen[current] = chatSessionInfo{
			Gen:     current,
			ID:      sessionIDFor(own, current),
			Goal:    "（当前会话，尚无记录）",
			Current: true,
		}
	}
	items := make([]chatSessionInfo, 0, len(byGen))
	for _, info := range byGen {
		if strings.TrimSpace(info.Goal) == "" {
			info.Goal = "（未命名会话）"
		}
		items = append(items, info)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Current != items[j].Current {
			return items[i].Current
		}
		if !items[i].SavedAt.Equal(items[j].SavedAt) {
			return items[i].SavedAt.After(items[j].SavedAt)
		}
		return items[i].Gen > items[j].Gen
	})
	if len(items) > maxListedSessions {
		items = items[:maxListedSessions]
	}
	return items
}

func (s *Service) lookupSessions(prefix string) []chatSessionInfo {
	if s.listSessions != nil {
		return s.listSessions(prefix)
	}
	return lookupChatSessions(prefix)
}

func normalizeSessionTalk(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsPunct(r) || r == '。' || r == '！' || r == '？' || r == '，'
	})
	for _, prefix := range []string{"请帮我", "麻烦帮我", "帮我", "请"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimSpace(s[len(prefix):])
		}
	}
	return s
}

func isRestartTalk(s string) bool {
	switch strings.ToLower(s) {
	case "重启会话", "重启对话", "重启聊天", "重新开始会话", "重新开始对话",
		"重置会话", "新开会话", "新开对话", "新建会话", "开启新会话", "开始新会话",
		"换个新会话", "new", "reset", "新建", "restart session", "reset session",
		"new session", "new chat":
		return true
	default:
		return false
	}
}

func matchSwitchTalk(s string) (string, bool) {
	lower := strings.ToLower(s)
	for _, phrase := range []string{
		"切换会话", "切换对话", "更换会话", "换个会话", "换一下会话", "换会话",
		"会话列表", "列出会话", "历史会话", "看看会话",
		"switch session", "list sessions", "list session", "sessions", "switch", "切换",
	} {
		if !strings.HasPrefix(lower, phrase) {
			continue
		}
		rest := strings.TrimSpace(s[len(phrase):])
		if rest == "" {
			return "", true
		}
		if _, ok := parsePickIndex(rest); ok {
			return rest, true
		}
	}
	return "", false
}

func parsePickIndex(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, prefix := range []string{"切换到", "恢复到", "选择", "选"} {
		s = strings.TrimPrefix(s, prefix)
		s = strings.TrimSpace(s)
	}
	s = strings.TrimPrefix(s, "第")
	s = strings.TrimSuffix(s, "个会话")
	s = strings.TrimSuffix(s, "会话")
	s = strings.TrimSuffix(s, "个")
	s = strings.TrimSuffix(s, "号")
	s = strings.TrimSpace(s)
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 99 {
		return 0, false
	}
	return n, true
}

func formatSessionTime(t time.Time) string {
	if t.IsZero() {
		return "尚未开始"
	}
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("1月2日 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if s == "" || n <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Publish the new selection only after its durable state was written.
func (s *Service) setSessionGenLocked(own string, gen int) error {
	next := make(map[string]int, len(s.sessions)+1)
	for k, v := range s.sessions {
		next[k] = v
	}
	next[own] = gen
	if err := saveSessionGens(s.cfg, next); err != nil {
		return err
	}
	s.sessions = next
	return nil
}

func normalizeChatCommand(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return text
	}
	switch strings.ToLower(fields[0]) {
	case "/delivery", "/retry", "/steer", "/stop", "/cancel", "/new", "/reset", "/status", "/result", "/help", "/sessions":
		cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
		if cmd == "cancel" && len(fields) == 1 {
			cmd = "stop"
		}
		return strings.TrimSpace("/ds " + cmd + " " + strings.TrimSpace(text[len(fields[0]):]))
	default:
		return text
	}
}

// Stop both active and queued turns for this sender and conversation only.
func (s *Service) stopOwnerLocked(own string, now time.Time) string {
	n := 0
	for _, j := range s.jobs {
		if j.Owner != own || !jobLive(j) {
			continue
		}
		n++
		if j.Status == "queued" {
			s.cancelPendingLocked(j.ID, true)
			j.Status = "cancelled"
		} else if cancel := s.cancel[j.ID]; cancel != nil {
			cancel()
			j.Status = "cancelling"
		}
		j.Updated = now
	}
	if n == 0 {
		return "当前没有正在执行或排队的任务。"
	}
	if err := s.saveLocked(); err != nil {
		return "已请求停止任务，但保存状态失败，请检查存储目录写入权限。"
	}
	return fmt.Sprintf("已请求停止当前对话的 %d 个任务，排队消息已取消。", n)
}
