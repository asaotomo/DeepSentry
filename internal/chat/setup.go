package chat

import (
	"ai-edr/internal/config"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed setup.html
var setupHTML string

type pendingLogin struct {
	Platform, Device, QR, URL, Key, State, BaseURL string
	Expires, Next                                  time.Time
	Interval                                       time.Duration
	AllowBatch                                     bool
}
type setupState struct {
	mu      sync.Mutex
	cfg     config.ChatConfig
	client  *http.Client
	service *Service
	attach  bool
	pending map[string]*pendingLogin
	status  map[string]string
}
type setupInput struct {
	Platform   string `json:"platform"`
	AllowBatch bool   `json:"allow_batch"`
	State      string `json:"state"`
	BotID      string `json:"botid"`
	Secret     string `json:"secret"`
	VerifyCode string `json:"verify_code"`
}

func Setup(ctx context.Context, cfg config.ChatConfig, run Runner) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	prefix := "/" + hex.EncodeToString(token) + "/"
	client := *config.HTTPClient(18 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	state := &setupState{cfg: cfg, client: &client, pending: map[string]*pendingLogin{}, status: map[string]string{}}

	var runtimeDone chan error
	service, err := New(ctx, cfg, run)
	if err != nil {
		return err
	}
	state.service = service
	service.OnStatus = func(name, status string) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.status[strings.TrimSuffix(name, "-quick")] = status
	}
	runtimeDone = make(chan error, 1)
	go func() { runtimeDone <- service.Serve() }()
	select {
	case <-service.Ready:
	case err := <-runtimeDone:
		if !isStoreBusy(err) {
			return err
		}
		// Background daemon already owns the chat store; open UI in attach mode.
		state.attach = true
		state.service = nil
		if states, statusErr := daemonStatus(cfg); statusErr == nil {
			state.status = states
		}
		fmt.Println("检测到聊天服务已在运行；本次只打开连接窗口。关闭本窗口不会单独留下后台进程。")
	case <-ctx.Done():
		return ctx.Err()
	}

	defer func() {
		stop()
		if runtimeDone != nil && !state.attach {
			select {
			case <-runtimeDone:
			case <-time.After(6 * time.Second):
			}
		}
	}()

	address := "http://" + listener.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != listener.Addr().String() {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, prefix)
		if action == "" && r.Method == "GET" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, setupHTML)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Origin") != address || r.Header.Get("X-DeepSentry-Setup") != "1" {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		var input setupInput
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input) != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if action != "status" && !validPlatform(input.Platform) {
			http.Error(w, "invalid platform", http.StatusBadRequest)
			return
		}
		var err error
		switch action {
		case "begin":
			err = state.begin(r.Context(), input)
		case "poll":
			err = state.poll(r.Context(), input)
		case "wecom-complete":
			err = state.completeWecom(input)
		case "cancel":
			delete(state.pending, input.Platform)
		case "disconnect":
			delete(state.pending, input.Platform)
			if state.attach {
				err = changeBinding(cfg, input.Platform, nil)
				if err == nil {
					err = notifyDaemonReload(cfg)
				}
			} else if state.service != nil {
				state.service.Disconnect(input.Platform + "-quick")
				err = changeBinding(cfg, input.Platform, nil)
			}
			state.status[input.Platform] = "unbound"
		case "status":
			if state.attach {
				if states, statusErr := daemonStatus(cfg); statusErr == nil {
					for k, v := range states {
						state.status[k] = v
					}
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		response := state.publicState(input.Platform)
		if err != nil {
			response["error"] = err.Error()
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	link := address + prefix
	if state.attach {
		fmt.Printf("通讯工具连接窗口：%s\nCtrl+C 关闭窗口并返回终端；聊天服务仍由当前 DeepSentry 进程托管。\n", link)
	} else {
		fmt.Printf("通讯工具连接窗口：%s\nCtrl+C 关闭窗口并停止聊天服务。\n", link)
	}
	openBrowser(link)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || err == nil {
		return nil
	}
	return err
}

func (st *setupState) publicState(platform string) map[string]any {
	bindings, _ := loadBindings(st.cfg)
	states := map[string]string{}
	for _, p := range []string{"feishu", "qq", "wechat", "wecom"} {
		state := st.status[p]
		if _, exists := bindings[p]; !exists {
			state = "unbound"
		}
		if state == "" {
			state = "unbound"
		}
		if pending := st.pending[p]; pending != nil && time.Now().Before(pending.Expires) {
			state = "waiting_scan"
		}
		if b, ok := bindings[p]; ok && b.User == "" && state == "connected" {
			state = "pairing"
		}
		states[p] = state
	}
	result := map[string]any{"states": states, "status": states[platform], "platform": platform}
	if pending := st.pending[platform]; pending != nil {
		if time.Now().After(pending.Expires) {
			delete(st.pending, platform)
			result["status"] = "expired"
			states[platform] = "expired"
		} else {
			result["qr"] = pending.QR
			result["url"] = pending.URL
			result["state"] = pending.State
			result["expires_at"] = pending.Expires.Unix()
		}
	}
	if b, ok := bindings[platform]; ok && b.User == "" && b.PairCode != "" {
		result["pair_command"] = "/ds pair " + b.PairCode
	}
	return result
}
func (st *setupState) bind(p string, b binding) error {
	if err := changeBinding(st.cfg, p, &b); err != nil {
		return errors.New("保存绑定失败，请检查连接目录写入权限")
	}
	delete(st.pending, p)
	st.status[p] = "connecting"
	if st.attach {
		if err := notifyDaemonReload(st.cfg); err != nil {
			return errors.New("已保存绑定，但通知后台聊天服务失败: " + err.Error())
		}
		return nil
	}
	if st.service != nil {
		st.service.Connect(channelFor(p, b))
	}
	return nil
}
func openBrowser(link string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
