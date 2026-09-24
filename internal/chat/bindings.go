package chat

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type binding struct {
	Manual     *config.ChatChannel `json:"manual,omitempty"`
	Platform   string              `json:"platform"`
	AppID      string              `json:"app_id"`
	Secret     string              `json:"secret"`
	User       string              `json:"user"`
	APIURL     string              `json:"api_url,omitempty"`
	PairCode   string              `json:"pair_code,omitempty"`
	AllowBatch bool                `json:"allow_batch"`
}

var bindingMu sync.Mutex

func bindingPath(cfg config.ChatConfig) string {
	if cfg.BindingFile != "" {
		return cfg.BindingFile
	}
	return "reports/chat/connections.json"
}

// ConfiguredChat reports whether this process should auto-start chat: a saved
// QR binding or at least one YAML channel. An empty or missing binding file
// is not configured. An unreadable binding file is ignored if YAML channels exist.
func ConfiguredChat(cfg config.ChatConfig) (bool, []string) {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, c := range cfg.Channels {
		if strings.TrimSpace(c.Name) == "" && strings.TrimSpace(c.Platform) == "" {
			continue
		}
		if label := configuredChatLabel(c.Platform, c.Name); label != "" {
			add(label)
		}
	}
	bindings, err := loadBindings(cfg)
	if err == nil {
		for p, v := range bindings {
			platform, name := p, p
			if v.Manual != nil {
				platform, name = v.Manual.Platform, v.Manual.Name
			} else if v.Platform != "" {
				platform = v.Platform
			}
			add(configuredChatLabel(platform, name))
		}
	}
	return len(names) > 0, names
}

// StartupSummary lists configured chat channels for the TUI welcome panel.
// A live daemon PID is noted without waiting on the control HTTP endpoint.
func StartupSummary(cfg config.ChatConfig) string {
	ok, names := ConfiguredChat(cfg)
	if !ok {
		return "未开启"
	}
	sort.Strings(names)
	summary := "已启用：" + strings.Join(names, "、")
	if meta, err := readDaemonMeta(cfg); err == nil && processAlive(meta.PID) {
		summary += " · 后台运行中"
	}
	return summary
}

func configuredChatLabel(platform, name string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu", "feishu_ws":
		return "飞书"
	case "qq", "qqbot":
		return "QQ"
	case "wechat", "weixin":
		return "微信"
	case "wecom", "wecom_ws":
		return "企业微信"
	case "dingtalk":
		return "钉钉"
	case "onebot":
		return "OneBot"
	}
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return strings.TrimSpace(platform)
}

func loadBindings(cfg config.ChatConfig) (map[string]binding, error) {
	data, err := os.ReadFile(bindingPath(cfg))
	if os.IsNotExist(err) {
		return map[string]binding{}, nil
	}
	if err != nil {
		return nil, err
	}
	var b map[string]binding
	if err = json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	if b == nil {
		b = map[string]binding{}
	}
	for p, v := range b {
		if v.Manual != nil {
			c := v.Manual
			supported := c.Platform == "feishu_ws" || c.Platform == "qqbot" || c.Platform == "weixin" || c.Platform == "wecom_ws" || c.Platform == "dingtalk" || c.Platform == "onebot" || c.Platform == "wechat"
			if !strings.HasPrefix(p, "manual-") || c.Name != p+"-quick" || !supported || len(c.AllowedUsers) == 0 {
				return nil, errors.New("手动绑定文件无效")
			}
			continue
		}
		if !validPlatform(p) || v.AppID == "" || v.Secret == "" || (v.User == "" && v.PairCode == "") {
			return nil, errors.New("绑定文件无效，请检查 connections.json")
		}
	}
	return b, nil
}
func validPlatform(p string) bool { return p == "feishu" || p == "qq" || p == "wechat" || p == "wecom" }
func channelFor(p string, b binding) config.ChatChannel {
	if b.Manual != nil {
		c := *b.Manual
		c.AppSecret = b.Secret
		c.AllowBatch = b.AllowBatch
		return c
	}
	platform := map[string]string{"feishu": "feishu_ws", "qq": "qqbot", "wechat": "weixin", "wecom": "wecom_ws"}[p]
	users := []string{}
	if b.User != "" {
		users = append(users, b.User)
	}
	return config.ChatChannel{Name: p + "-quick", Platform: platform, AppID: b.AppID, AppSecret: b.Secret, APIURL: b.APIURL, AllowedUsers: users, AllowBatch: b.AllowBatch, PairCode: b.PairCode}
}
func withBinding(cfg config.ChatConfig) (config.ChatConfig, error) {
	b, err := loadBindings(cfg)
	if err != nil {
		return cfg, err
	}
	cfg.Channels = append([]config.ChatChannel(nil), cfg.Channels...)
	for p, v := range b {
		cfg.Channels = append(cfg.Channels, channelFor(p, v))
	}
	return cfg, nil
}
func changeBinding(cfg config.ChatConfig, p string, b *binding) error {
	bindingMu.Lock()
	defer bindingMu.Unlock()
	all, err := loadBindings(cfg)
	if err != nil {
		return err
	}
	if b == nil {
		delete(all, p)
	} else {
		all[p] = *b
	}
	return writeBindings(cfg, all)
}
func writeBindings(cfg config.ChatConfig, all map[string]binding) error {
	p := bindingPath(cfg)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".binding-*")
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
	return os.Rename(f.Name(), p)
}
func pairBinding(cfg config.ChatConfig, name, user string) error {
	bindingMu.Lock()
	defer bindingMu.Unlock()
	all, err := loadBindings(cfg)
	if err != nil {
		return err
	}
	p := strings.TrimSuffix(name, "-quick")
	b, ok := all[p]
	if !ok || b.User != "" {
		return errors.New("pairing no longer available")
	}
	b.User = user
	b.PairCode = ""
	all[p] = b
	return writeBindings(cfg, all)
}
func isQuick(p string) bool {
	return p == "feishu_ws" || p == "qqbot" || p == "weixin" || p == "wecom_ws"
}
func (s *Service) Connect(c config.ChatChannel) {
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	if cancel := s.connections[c.Name]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.connections[c.Name] = cancel
	s.generations[c.Name]++
	generation := s.generations[c.Name]
	c.Generation = generation
	s.channels[c.Name] = c
	delete(s.paired, c.Name)
	delete(s.senders, c.Name)
	s.wg.Add(1)
	s.mu.Unlock()
	update := func(status string) {
		s.mu.Lock()
		current := s.generations[c.Name] == generation
		if current {
			s.liveStatus[c.Name] = status
		}
		s.mu.Unlock()
		if current && s.OnStatus != nil {
			s.OnStatus(c.Name, status)
		}
	}
	go func() {
		defer s.wg.Done()
		update("connecting")
		switch c.Platform {
		case "feishu_ws":
			s.runFeishu(ctx, c, update)
		case "qqbot":
			s.runQQ(ctx, c, update)
		case "weixin":
			s.runWeixin(ctx, c, update)
		case "wecom_ws":
			s.runWecom(ctx, c, update)
		default:
			update("waiting_callback")
		}
	}()
}
func (s *Service) Disconnect(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generations[name]++
	delete(s.channels, name)
	if cancel := s.connections[name]; cancel != nil {
		cancel()
		delete(s.connections, name)
	}
	delete(s.senders, name)
	for grantOwner := range s.approvals {
		var parts []string
		_ = json.Unmarshal([]byte(grantOwner), &parts)
		if len(parts) > 0 && parts[0] == name {
			delete(s.approvals, grantOwner)
		}
	}
	for id, j := range s.jobs {
		var own []string
		_ = json.Unmarshal([]byte(j.Owner), &own)
		if len(own) > 0 && own[0] == name {
			if cancel := s.cancel[id]; cancel != nil {
				cancel()
			}
			if j.Status == "queued" {
				s.cancelPendingLocked(id, true)
				j.Status = "cancelled"
				j.Updated = time.Now()
				j.Result = "通道已断开。"
			}
		}
	}
	_ = s.saveLocked()
}
