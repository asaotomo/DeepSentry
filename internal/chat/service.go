// Package chat connects authenticated messaging callbacks to isolated DeepSentry tasks.
package chat

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ai-edr/internal/config"
)

type Message struct {
	ReplyExpires                   int64        `json:"reply_expires,omitempty"`
	Attachments                    []Attachment `json:"attachments,omitempty"`
	ID, User, Chat, Text, ReplyURL string
	ContextToken                   string
	DeliveryID                     string
	DeliverySeq                    uint32
	Group                          bool
}

const maxConcurrentJobs = 5

type Job struct {
	Notification *OutboxItem `json:"notification,omitempty"`
	Progress     string      `json:"progress,omitempty"`
	ID           string      `json:"id"`
	Owner        string      `json:"owner"`
	Event        string      `json:"event"`
	Prompt       string      `json:"prompt"`
	SessionID    string      `json:"session_id,omitempty"`
	Status       string      `json:"status"`
	Result       string      `json:"result"`
	Created      time.Time   `json:"created"`
	Updated      time.Time   `json:"updated"`
}

type pendingRun struct {
	channel                config.ChatChannel
	message                Message
	prompt                 string
	sessionID              string
	steerTarget, steerFile string
}

// Runner executes one Agent turn. sessionID binds multi-turn chat to a checkpoint.
type Runner func(ctx context.Context, prompt, sessionID string) (string, error)
type delivery struct {
	created    time.Time
	id, jobID  string
	ephemeral  bool
	confirmSeq int64
	channel    config.ChatChannel
	message    Message
	text       string
}
type sessionGrant struct {
	all    bool
	scopes map[string]bool
}

type Service struct {
	fileQueueMu   sync.Mutex
	approvals     map[string]*sessionGrant
	mediaClient   *http.Client
	platformMedia *http.Client
	box           outboxState
	channels      map[string]config.ChatChannel
	socketDial    func(context.Context, string) (*socketPeer, error)
	cfg           config.ChatConfig
	run           Runner
	ctx           context.Context
	mu            sync.Mutex
	jobs          map[string]*Job
	pending       map[string]pendingRun
	seen          map[string]time.Time
	cancel        map[string]context.CancelFunc
	outbound      chan delivery
	wg            sync.WaitGroup
	client        *http.Client
	OnStatus      func(string, string)
	Ready         chan struct{}
	connections   map[string]context.CancelFunc
	generations   map[string]int
	sessions      map[string]int
	picks         map[string]sessionPick
	listSessions  func(string) []chatSessionInfo
	liveStatus    map[string]string
	paired        map[string]string
	confirms      map[string]string
	senders       map[string]func(context.Context, Message, string) error
	mediaPeers    map[string]*socketPeer
	stop          context.CancelFunc
}

func New(ctx context.Context, cfg config.ChatConfig, run Runner) (*Service, error) {
	if run == nil {
		return nil, errors.New("chat runner is required")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8787"
	}
	if cfg.Store == "" {
		cfg.Store = "reports/chat/jobs.json"
	}
	if cfg.TaskTimeoutSec <= 0 {
		cfg.TaskTimeoutSec = 1800
	}
	if cfg.MaxConcurrent <= 0 || cfg.MaxConcurrent > maxConcurrentJobs {
		cfg.MaxConcurrent = maxConcurrentJobs
	}
	var err error
	cfg, err = withBinding(cfg)
	if err != nil {
		return nil, err
	}

	names := map[string]bool{}
	for _, c := range cfg.Channels {
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(c.Name) || names[c.Name] {
			return nil, errors.New("chat channel name 必须唯一且只含字母、数字、下划线或短横线")
		}
		names[c.Name] = true
		if len(c.AllowedUsers) == 0 && c.PairCode == "" {
			return nil, fmt.Errorf("%s: allowed_users 必须设置", c.Name)
		}
		required := []string{}
		switch c.Platform {
		case "feishu_ws", "qqbot", "weixin", "wecom_ws":
			if c.AppID == "" || secret(c) == "" {
				return nil, fmt.Errorf("%s: 缺少平台应用凭据", c.Name)
			}
		case "feishu":
			if c.AppID == "" {
				return nil, fmt.Errorf("%s: app_id 必填", c.Name)
			}
			required = []string{c.SecretEnv, c.TokenEnv, c.EncryptKeyEnv}
		case "dingtalk":
			if secret(c) == "" {
				return nil, fmt.Errorf("%s: 缺少钉钉 AppSecret", c.Name)
			}
		case "wecom":
			if c.AppID == "" || c.AgentID <= 0 {
				return nil, fmt.Errorf("%s: app_id (CorpID) 和 agent_id 必填", c.Name)
			}
			required = []string{c.SecretEnv, c.TokenEnv, c.EncryptKeyEnv}
		case "onebot", "wechat":
			required = []string{c.SecretEnv, c.AccessTokenEnv}
			u, err := url.Parse(c.APIURL)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return nil, fmt.Errorf("%s: api_url 必须为无凭据/查询参数的 HTTP(S) 地址", c.Name)
			}
		default:
			return nil, fmt.Errorf("%s: 不支持平台 %q", c.Name, c.Platform)
		}
		for _, env := range required {
			if env == "" || os.Getenv(env) == "" {
				return nil, fmt.Errorf("%s: 必需的密钥环境变量未设置 (%s)", c.Name, env)
			}
		}
	}
	hasCallbacks := false
	for _, c := range cfg.Channels {
		if !isQuick(c.Platform) {
			hasCallbacks = true
		}
	}
	if !hasCallbacks {
		cfg.Listen = "127.0.0.1:0"
	}
	client := *config.HTTPClient(12 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, stop := context.WithCancel(ctx)
	s := &Service{socketDial: dialSocket, Ready: make(chan struct{}), connections: map[string]context.CancelFunc{}, generations: map[string]int{}, sessions: map[string]int{}, picks: map[string]sessionPick{}, listSessions: lookupChatSessions, liveStatus: map[string]string{}, paired: map[string]string{}, confirms: map[string]string{}, senders: map[string]func(context.Context, Message, string) error{}, stop: stop, cfg: cfg, run: run, ctx: ctx, jobs: map[string]*Job{}, pending: map[string]pendingRun{}, seen: map[string]time.Time{}, cancel: map[string]context.CancelFunc{}, outbound: make(chan delivery, 128), client: &client}
	s.mediaClient = newMediaClient()
	s.platformMedia = newPlatformMediaClient()
	s.box = outboxState{items: map[string]*OutboxItem{}, busy: map[string]bool{}}
	s.channels = map[string]config.ChatChannel{}
	for _, c := range cfg.Channels {
		s.channels[c.Name] = c
	}
	watchParent(stop)
	s.sessions = loadSessionGens(cfg)
	data, err := os.ReadFile(cfg.Store)
	if err == nil {
		var jobs []Job
		if err = json.Unmarshal(data, &jobs); err != nil {
			return nil, fmt.Errorf("chat store: %w", err)
		}
		if len(jobs) > 1000 {
			return nil, errors.New("chat store exceeds 1000 jobs")
		}
		for _, j := range jobs {
			if j.Status == "running" || j.Status == "cancelling" || j.Status == "queued" {
				j.Status = "interrupted"
				j.Result = "服务重启，任务已中断；不会自动重放。"
			}
			s.jobs[j.ID] = &j
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *Service) Serve() error {
	defer s.stop()
	listener, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	unlock, err := lockStore(s.cfg.Store)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.saveLocked(); err != nil {
		return err
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	controlToken := hex.EncodeToString(token)
	if err := writeDaemonMeta(s.cfg, daemonMeta{PID: os.Getpid(), Addr: listener.Addr().String(), Token: controlToken}); err != nil {
		return err
	}
	defer clearDaemonMeta(s.cfg)

	server := &http.Server{Handler: s.Handler(controlToken), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	if err := s.restoreOutbox(); err != nil {
		return err
	}
	if err := s.recoverJobReplies(); err != nil {
		return err
	}
	for n := 0; n < 5; n++ {
		s.wg.Add(1)
		go s.outboxWorker()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-s.ctx.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		case <-done:
		}
	}()
	log.Printf("DeepSentry chat listening on %s", listener.Addr())
	for _, c := range s.cfg.Channels {
		if isQuick(c.Platform) {
			s.Connect(c)
		}
	}
	close(s.Ready)
	err = server.Serve(listener)
	close(done)
	s.mu.Lock()
	for _, cancel := range s.cancel {
		cancel()
	}
	s.mu.Unlock()
	// Wait only after the HTTP server has stopped accepting new work.
	s.stop()
	s.wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Service) Handler(controlToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/control/status", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorizeControl(r, controlToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"states": s.connectionStates()})
	})
	mux.HandleFunc("/control/reload", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorizeControl(r, controlToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := s.ReloadBindings(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/control/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorizeControl(r, controlToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.stop()
		}()
	})
	for _, c := range s.cfg.Channels {
		if isQuick(c.Platform) {
			continue
		}
		mux.HandleFunc("/chat/"+c.Name, func(w http.ResponseWriter, r *http.Request) { s.callback(c, w, r) })
	}
	return mux
}

func (s *Service) authorizeControl(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(got) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func (s *Service) connectionStates() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for name, status := range s.liveStatus {
		out[strings.TrimSuffix(name, "-quick")] = status
	}
	for name := range s.connections {
		platform := strings.TrimSuffix(name, "-quick")
		if _, ok := out[platform]; !ok {
			out[platform] = "connected"
		}
	}
	return out
}

// ReloadBindings reconnects quick-bind channels from the on-disk binding store.
func (s *Service) ReloadBindings() error {
	bound, err := loadBindings(s.cfg)
	if err != nil {
		return err
	}
	wanted := map[string]config.ChatChannel{}
	for p, b := range bound {
		c := channelFor(p, b)
		wanted[c.Name] = c
	}
	s.mu.Lock()
	var stale []string
	for name := range s.connections {
		if strings.HasSuffix(name, "-quick") {
			if _, ok := wanted[name]; !ok {
				stale = append(stale, name)
			}
		}
	}
	s.mu.Unlock()
	for _, name := range stale {
		s.Disconnect(name)
	}
	for _, c := range wanted {
		s.Connect(c)
	}
	return nil
}
func owner(c config.ChatChannel, m Message) string {
	b, _ := json.Marshal([]string{c.Name, m.User, m.Chat})
	return string(b)
}
func event(c config.ChatChannel, m Message) string {
	b, _ := json.Marshal([]string{c.Name, m.Chat, m.ID})
	return string(b)
}
func (s *Service) command(c config.ChatChannel, m Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return "", errors.New("service stopped")
	}
	if isQuick(c.Platform) && (s.connections[c.Name] == nil || s.generations[c.Name] != c.Generation) {
		return "", errors.New("connection stopped")
	}
	if c.PairCode != "" && s.paired[c.Name] == "" && !m.Group && m.User != "" && equal(strings.TrimSpace(m.Text), "/ds pair "+c.PairCode) {
		if err := pairBinding(s.cfg, c.Name, m.User); err != nil {
			return "", err
		}
		s.paired[c.Name] = m.User
		return "控制账号已绑定。\n\n" + s.helpText(c), nil
	}

	if (!slices.Contains(c.AllowedUsers, m.User) && s.paired[c.Name] != m.User) || (m.Group && !slices.Contains(c.AllowedChats, m.Chat)) || (!m.Group && len(c.AllowedChats) > 0 && !slices.Contains(c.AllowedChats, m.Chat)) {
		return "", errors.New("sender or chat not allowed")
	}
	text := strings.TrimSpace(m.Text)
	if text == "" && len(m.Attachments) > 0 {
		text = "请处理这条消息中的图片、语音或文件。"
	}
	own := owner(c, m)
	if m.ID == "" || m.User == "" || m.Chat == "" {
		return "", errors.New("missing message identity")
	}
	now := time.Now()
	key := event(c, m)
	for k, t := range s.seen {
		if now.Sub(t) > 24*time.Hour {
			delete(s.seen, k)
		}
	}
	if _, ok := s.seen[key]; ok {
		return "", nil
	}
	for _, j := range s.jobs {
		if j.Event == key {
			return "", nil
		}
	}
	if len(s.seen) >= 10000 {
		return "", errors.New("too many messages")
	}
	if len(m.Attachments) == 0 && !strings.HasPrefix(text, "/") && !isRestartTalk(normalizeSessionTalk(text)) {
		if dir := s.confirmDirForOwnerLocked(own); dir != "" {
			if req, err := ReadConfirmRequest(dir); err == nil && req.Kind == "input" {
				if err := WriteInputReply(dir, text); err == nil {
					s.seen[key] = now
					return "已收到补充信息，继续原任务。", nil
				}
			}
		}
	}
	if decision, ok := ParseConfirmText(text); ok {
		if dir := s.confirmDirForOwnerLocked(own); dir != "" {
			req, readErr := ReadConfirmRequest(dir)
			if readErr == nil {
				if err := WriteConfirmReply(dir, decision); err == nil {
					s.rememberApprovalLocked(own, req.ScopeKey, decision)
					if m.ID != "" {
						s.seen[event(c, m)] = time.Now()
					}
					if decision == "deny" {
						return "已拒绝本次操作。", nil
					}
					if decision == "allow_all_session" {
						return "已允许本次会话所有高危操作；新建、切换会话或重启服务后失效。", nil
					}
					return "已收到确认，任务继续。", nil
				}
			}
		}
	}
	// Common IM commands run outside the agent queue, including while a turn is busy.
	text = normalizeChatCommand(text)
	if len(m.Attachments) > 0 {
		if text == "/ds" {
			text = "/ds run 请处理所附图片、语音或文件。"
		}
		// Sender and group allowlists have already been checked above.
		if m.Group && !strings.HasPrefix(text, "/") {
			text = "/ds run " + text
		}
	}
	plainChat := false
	if text != "/ds" && !strings.HasPrefix(text, "/ds ") {
		// Private chats may talk to the Agent naturally; groups keep the /ds prefix.
		if m.Group || text == "" || strings.HasPrefix(text, "/") {
			return "", nil
		}
		plainChat = true
	}
	response := ""
	talk := text
	if !plainChat {
		talk = strings.TrimSpace(strings.TrimPrefix(text, "/ds"))
	}
	if handled, out := s.handleSessionTalkLocked(own, talk); handled {
		response = out
	} else if plainChat {
		response = s.submitLocked(c, m, own, key, text, now)
		s.seen[key] = now
		return response, nil
	} else {
		parts := strings.SplitN(talk, " ", 2)
		cmd := parts[0]
		arg := ""
		if len(parts) > 1 {
			arg = strings.TrimSpace(parts[1])
		}
		switch cmd {
		case "", "help", "帮助", "info", "仪表盘", "about":
			response = s.helpText(c)
		case "delivery", "投递":
			response = s.deliveryStatusLocked(own)
		case "retry", "重发":
			response = s.retryDeliveryLocked(own, arg, m)
		case "steer", "补充":
			var target *Job
			for _, j := range s.jobs {
				if len(m.Attachments) == 0 && j.Owner == own && j.Status == "running" && j.SessionID == s.sessionIDLocked(own) {
					target = j
					break
				}
			}
			response = s.submitLocked(c, m, own, key, arg, now)
			if response == "" && target != nil {
				s.steerLocked(c.Name, own, target, key)
				response = "补充已排入，当前任务会在下一次模型调用前读取；若它已结束，将作为后续对话处理。"
			}
		case "stop", "停止":
			response = s.stopOwnerLocked(own, now)
		case "run", "执行":
			response = s.submitLocked(c, m, own, key, arg, now)
		case "status", "状态", "result", "结果", "cancel", "取消":
			page := 1
			if cmd == "result" || cmd == "结果" {
				fields := strings.Fields(arg)
				if len(fields) > 0 {
					arg = fields[0]
				}
				if len(fields) > 1 {
					var err error
					page, err = strconv.Atoi(fields[1])
					if err != nil || page < 1 || len(fields) > 2 {
						response = "用法：/ds result 任务ID [页码]"
						break
					}
				}
			}
			var j *Job
			if arg != "" {
				j = s.jobs[arg]
			} else if cmd == "status" || cmd == "状态" || cmd == "result" || cmd == "结果" {
				for _, candidate := range s.jobs {
					if candidate.Owner == own && (j == nil || candidate.Created.After(j.Created)) {
						j = candidate
					}
				}
			}
			if j == nil || j.Owner != own {
				response = "未找到当前会话中属于你的任务。"
				break
			}
			if cmd == "cancel" || cmd == "取消" {
				if j.Status == "queued" {
					if !s.cancelPendingLocked(j.ID, false) {
						response = "补充已被当前任务读取；如需停止，请发 /stop。"
						break
					}
					j.Status = "cancelled"
					j.Updated = now
					_ = s.saveLocked()
				} else if cancel := s.cancel[j.ID]; cancel != nil {
					cancel()
					j.Status = "cancelling"
					j.Updated = now
				}
			}
			response = "任务 " + j.ID + "：" + statusLabel(j.Status)
			if j.Progress != "" && jobLive(j) {
				response += "\n" + j.Progress
			}
			if cmd == "result" || cmd == "结果" {
				pages := textPages(j.Result, 1200)
				if page > len(pages) {
					response = "页码超出范围，共 " + strconv.Itoa(len(pages)) + " 页"
					break
				}
				response = formatResultPage(j.Status, j.ID, pages[page-1], page, len(pages))
			}
		default:
			// "/ds 查看系统状态" — treat unknown verbs as a natural-language task.
			prompt := strings.TrimSpace(strings.TrimPrefix(text, "/ds"))
			response = s.submitLocked(c, m, own, key, prompt, now)
		}
	}
	s.seen[key] = now
	return response, nil
}

func (s *Service) submitLocked(c config.ChatChannel, m Message, own, key, prompt string, now time.Time) string {
	if !s.cfg.AllowBatch && !c.AllowBatch {
		return "聊天执行未授权。请在连接窗口勾选执行权限，或设置 chat.allow_batch。"
	}
	if prompt == "" || len(prompt) > 16000 {
		return "任务描述不能为空且不能超过 16000 字节。"
	}
	if len(s.jobs) >= 1000 {
		var oldest *Job
		for _, j := range s.jobs {
			if jobLive(j) {
				continue
			}
			if oldest == nil || j.Created.Before(oldest.Created) {
				oldest = j
			}
		}
		if oldest == nil {
			return "任务队列已满，请稍后再试。"
		}
		delete(s.jobs, oldest.ID)
		delete(s.pending, oldest.ID)
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "无法分配任务 ID，请重试。"
	}
	id := hex.EncodeToString(idBytes)
	sessionID := s.sessionIDLocked(own)
	job := &Job{ID: id, Owner: own, Event: key, Prompt: prompt, SessionID: sessionID, Status: "queued", Created: now, Updated: now}
	s.jobs[id] = job
	s.pending[id] = pendingRun{channel: c, message: m, prompt: prompt, sessionID: sessionID}
	if err := s.saveLocked(); err != nil {
		delete(s.jobs, id)
		delete(s.pending, id)
		return "保存任务失败，请检查 chat.store 写入权限。"
	}
	if len(m.Attachments) > 0 {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.prefetchPendingMedia(id, c, m)
		}()
	}
	s.pumpQueueLocked()
	s.wg.Add(1)
	go s.ackIfSlow(s.ctx, id, c, m)
	return ""
}

func jobLive(j *Job) bool {
	return j != nil && (j.Status == "running" || j.Status == "cancelling" || j.Status == "queued")
}

func (s *Service) runningCountLocked() int {
	n := 0
	for _, j := range s.jobs {
		if j.Status == "running" || j.Status == "cancelling" {
			n++
		}
	}
	return n
}

func (s *Service) ownerBusyLocked(own string) bool {
	for _, j := range s.jobs {
		if j.Owner == own && (j.Status == "running" || j.Status == "cancelling") {
			return true
		}
	}
	return false
}

func (s *Service) pumpQueueLocked() {
	if s.ctx.Err() != nil {
		return
	}
	for s.runningCountLocked() < s.cfg.MaxConcurrent {
		var next *Job
		for _, j := range s.jobs {
			if j.Status != "queued" || s.ownerBusyLocked(j.Owner) {
				continue
			}
			if next == nil || j.Created.Before(next.Created) || (j.Created.Equal(next.Created) && j.ID < next.ID) {
				next = j
			}
		}
		if next == nil {
			return
		}
		p, ok := s.pending[next.ID]
		if !ok {
			next.Status = "failed"
			next.Result = "排队任务已丢失。"
			next.Updated = time.Now()
			continue
		}
		s.startLocked(next, p)
	}
}

func (s *Service) startLocked(j *Job, p pendingRun) {
	j.Status = "running"
	j.Updated = time.Now()
	delete(s.pending, j.ID)
	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(s.cfg.TaskTimeoutSec)*time.Second)
	s.cancel[j.ID] = cancel
	_ = s.saveLocked()
	s.wg.Add(1)
	go s.execute(ctx, cancel, j.ID, p.channel, p.message, p.prompt, p.sessionID)
}

func (s *Service) ackIfSlow(ctx context.Context, id string, c config.ChatChannel, m Message) {
	defer s.wg.Done()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.mu.Lock()
	j := s.jobs[id]
	active := jobLive(j)
	queued := j != nil && j.Status == "queued"
	s.mu.Unlock()
	if active {
		text := "还在处理，好了马上回你。可发 /status 查看进度，/stop 停止。"
		if queued {
			text = "消息已排队，轮到后会继续处理。可发 /stop 取消当前对话的执行和排队任务。"
		}
		s.enqueue(delivery{channel: c, message: m, text: text, jobID: id, ephemeral: true})
	}
}

func sessionGensPath(cfg config.ChatConfig) string {
	store := cfg.Store
	if store == "" {
		store = "reports/chat/jobs.json"
	}
	return filepath.Join(filepath.Dir(store), "sessions.json")
}

func loadSessionGens(cfg config.ChatConfig) map[string]int {
	out := map[string]int{}
	data, err := os.ReadFile(sessionGensPath(cfg))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	if out == nil {
		out = map[string]int{}
	}
	return out
}

func saveSessionGens(cfg config.ChatConfig, sessions map[string]int) error {
	path := sessionGensPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sessions-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Service) confirmDirForOwnerLocked(own string) string {
	for _, j := range s.jobs {
		if j.Owner == own && j.Status == "running" && (j.SessionID == "" || j.SessionID == s.sessionIDLocked(own)) {
			if dir := s.confirms[j.ID]; dir != "" {
				return dir
			}
		}
	}
	return ""
}

func (s *Service) watchConfirm(ctx context.Context, dir string, c config.ChatChannel, m Message) {
	var lastSeq int64
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			req, err := ReadConfirmRequest(dir)
			if err != nil || req.Seq == 0 || req.Seq == lastSeq {
				continue
			}
			lastSeq = req.Seq
			s.mu.Lock()
			j := s.jobs[filepath.Base(dir)]
			active := j != nil && j.Status == "running" && j.SessionID == s.sessionIDLocked(j.Owner)
			auto := req.Kind != "input" && active && s.hasApprovalLocked(j.Owner, req.ScopeKey)
			if auto {
				auto = WriteConfirmReply(dir, "allow_once") == nil
			}
			s.mu.Unlock()
			if auto || !active {
				continue
			}
			if req.Kind == "input" {
				if err := s.deliverOutboundFiles(ctx, c, m, filepath.Join(dir, "send")); err != nil {
					req.Summary += "\n提示：准备好的附件未能入队，请检查发送状态。"
				}
			}
			s.enqueue(delivery{channel: c, message: m, text: FormatConfirmPrompt(req), jobID: filepath.Base(dir), ephemeral: true, confirmSeq: req.Seq})
		}
	}
}

func (s *Service) execute(ctx context.Context, cancel context.CancelFunc, id string, c config.ChatChannel, m Message, prompt, sessionID string) {
	defer s.wg.Done()
	defer cancel()
	dir := filepath.Join(filepath.Dir(s.cfg.Store), "confirm", id)
	_ = os.MkdirAll(dir, 0700)
	s.mu.Lock()
	s.confirms[id] = dir
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.confirms, id)
		s.mu.Unlock()
		_ = os.RemoveAll(dir)
	}()
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); s.watchConfirm(ctx, dir, c, m) }()
	defer func() { cancel(); <-watchDone }()
	var progressMu sync.Mutex
	lastProgress := time.Now()
	progressCount := 0
	runCtx := withProgress(WithConfirmDir(ctx, dir), func(text string) {
		progressMu.Lock()
		defer progressMu.Unlock()
		s.mu.Lock()
		if j := s.jobs[id]; j != nil {
			j.Progress = text
		}
		s.mu.Unlock()
		interval := time.Duration(s.cfg.ProgressIntervalSec) * time.Second
		if interval <= 0 {
			interval = 30 * time.Second
		}
		maxMessages := s.cfg.ProgressMaxMessages
		if maxMessages < 0 {
			return
		}
		if maxMessages == 0 {
			maxMessages = 3
		}
		if c.Platform == "qqbot" {
			maxMessages = min(maxMessages, 1)
		}
		if progressCount >= maxMessages || time.Since(lastProgress) < interval || ctx.Err() != nil {
			return
		}
		lastProgress = time.Now()
		progressCount++
		s.enqueue(delivery{channel: c, message: m, text: text, jobID: id, ephemeral: true})
	})
	runCtx = context.WithValue(runCtx, noticeKey{}, func(text string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		j := s.jobs[id]
		if j == nil || j.Status != "running" || j.SessionID != s.sessionIDLocked(j.Owner) || ctx.Err() != nil {
			return
		}
		// A notice must survive normal task completion, unlike transient progress.
		_ = s.queueDelivery(delivery{channel: c, message: m, text: text, jobID: id})
	})
	prepared, images, err := s.prepareChannelMedia(runCtx, c, m, dir, prompt)
	result := ""
	if err == nil && len(images) > 0 {
		err = writeConfirmJSON(filepath.Join(dir, "images.json"), images)
	}
	if err == nil {
		result, err = s.run(runCtx, prepared, sessionID)
	}
	if sendErr := s.deliverOutboundFiles(runCtx, c, m, filepath.Join(dir, "send")); sendErr != nil {
		if strings.TrimSpace(result) == "" {
			result = sendErr.Error()
		} else {
			result = strings.TrimSpace(result) + "\n" + sendErr.Error()
		}
		if err == nil {
			err = sendErr
		}
	}
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		s.pumpQueueLocked()
		s.mu.Unlock()
		return
	}
	s.settleSteeringLocked(id, dir)
	j.Updated = time.Now()
	j.Status = "completed"
	if err != nil {
		j.Status = "failed"
		if strings.TrimSpace(result) == "" {
			detail := strings.TrimSpace(err.Error())
			if detail == "" || strings.HasPrefix(detail, "exit status") {
				result = "任务执行失败。可发「重启会话」或 /ds new 开新会话后再试。"
			} else {
				result = detail
			}
		}
	}
	if ctx.Err() != nil {
		j.Status = "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			j.Status = "timed_out"
		}
	}
	j.Result = clip(formatMobileText(extractConclusion(result)), 24000)
	if j.Result == "" {
		j.Result = clip(strings.TrimSpace(result), 24000)
	}
	response := formatTaskNotify(j.Status, j.ID, j.Result)
	// A reset cancels the previous generation. Keep its result available in
	// history, but do not inject its cancellation/result into the new chat.
	if ctx.Err() != nil && j.SessionID != s.sessionIDLocked(j.Owner) {
		j.Notification = nil
		if err := s.saveLocked(); err != nil {
			log.Printf("chat task=%s 保存取消状态失败", id)
		}
		delete(s.cancel, id)
		s.pumpQueueLocked()
		s.mu.Unlock()
		return
	}
	j.Notification = &OutboxItem{ID: "final-" + id, Channel: c, Message: m, Chunks: deliveryChunks(c.Platform, response), Status: "pending", Created: time.Now()}
	j.Notification.Channel.AppSecret = ""
	j.Notification.Channel.PairCode = ""
	j.Notification.Message.Attachments = nil
	if err := s.saveLocked(); err != nil {
		log.Printf("chat task=%s 保存结果失败", id)
	}
	delete(s.cancel, id)
	s.enqueue(delivery{channel: c, message: m, text: response, id: "final-" + id, created: j.Notification.Created})
	s.pumpQueueLocked()
	s.mu.Unlock()
}
func (s *Service) enqueue(d delivery) {
	if err := s.queueDelivery(d); err != nil {
		log.Printf("chat channel=%s durable reply failed: %v; use /result", d.channel.Name, err)
	}
	// Compatibility observation channel for embedded callers; delivery is driven by the durable outbox.
	select {
	case s.outbound <- d:
	default:
	}
}
func (s *Service) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.cfg.Store), 0700); err != nil {
		return err
	}
	jobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, *j)
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.cfg.Store), ".chat-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.cfg.Store)
}
func clip(s string, n int) string {
	if len(s) <= n {
		return strings.ToValidUTF8(s, "�")
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.ToValidUTF8(s[:n], "�") + "\n…（输出已截断）"
}

func textPages(s string, size int) []string {
	s = strings.ToValidUTF8(s, "�")
	pages := []string{}
	for len(s) > size {
		n := size
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		pages = append(pages, s[:n])
		s = s[n:]
	}
	pages = append(pages, s)
	return pages
}
