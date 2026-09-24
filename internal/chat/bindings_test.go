package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/config"
)

func TestConfiguredChatEmptyIsFalse(t *testing.T) {
	cfg := config.ChatConfig{BindingFile: filepath.Join(t.TempDir(), "missing.json")}
	ok, names := ConfiguredChat(cfg)
	if ok || len(names) != 0 {
		t.Fatalf("empty chat should not autostart: ok=%v names=%v", ok, names)
	}
}

func TestConfiguredChatUsesSavedBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	data, _ := json.Marshal(map[string]binding{
		"wechat": {Platform: "wechat", AppID: "app", Secret: "sec", User: "u1"},
		"feishu": {Platform: "feishu", AppID: "app2", Secret: "sec2", User: "u2"},
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ok, names := ConfiguredChat(config.ChatConfig{BindingFile: path})
	if !ok {
		t.Fatal("saved bindings should autostart")
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "微信") || !strings.Contains(joined, "飞书") {
		t.Fatalf("names=%v", names)
	}
}

func TestConfiguredChatUsesYAMLChannelsWithoutBindings(t *testing.T) {
	ok, names := ConfiguredChat(config.ChatConfig{
		BindingFile: filepath.Join(t.TempDir(), "missing.json"),
		Channels:    []config.ChatChannel{{Name: "ops", Platform: "wecom"}},
	})
	if !ok || len(names) != 1 || names[0] != "企业微信" {
		t.Fatalf("yaml channel should autostart: ok=%v names=%v", ok, names)
	}
}

func TestStartupSummaryListsEnabledChannelsAndLiveDaemon(t *testing.T) {
	dir := t.TempDir()
	cfg := config.ChatConfig{
		Store:       filepath.Join(dir, "jobs.json"),
		BindingFile: filepath.Join(dir, "missing.json"),
		Channels: []config.ChatChannel{
			{Name: "ops", Platform: "wecom_ws"},
			{Name: "qq-bot", Platform: "qqbot"},
		},
	}
	if got := StartupSummary(cfg); got != "已启用：QQ、企业微信" {
		t.Fatalf("configured channels: %q", got)
	}
	if got := StartupSummary(config.ChatConfig{BindingFile: filepath.Join(dir, "missing.json")}); got != "未开启" {
		t.Fatalf("empty chat: %q", got)
	}

	path := filepath.Join(dir, "connections.json")
	data, err := json.Marshal(map[string]binding{
		"wechat": {Platform: "wechat", AppID: "app", Secret: "sec", User: "u1"},
		"manual-ding": {
			Platform: "dingtalk",
			Secret:   "sec",
			Manual:   &config.ChatChannel{Name: "manual-ding-quick", Platform: "dingtalk", AllowedUsers: []string{"owner"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.BindingFile = path
	got := StartupSummary(cfg)
	for _, name := range []string{"QQ", "企业微信", "微信", "钉钉"} {
		if !strings.Contains(got, name) {
			t.Fatalf("missing %s: %q", name, got)
		}
	}

	meta, err := json.Marshal(daemonMeta{PID: os.Getpid(), Addr: "127.0.0.1:9", Token: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	got = StartupSummary(cfg)
	if !strings.Contains(got, "后台运行中") {
		t.Fatalf("live daemon not noted: %q", got)
	}
}

func TestConfiguredChatIgnoresBrokenBindingFileWhenNoChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, names := ConfiguredChat(config.ChatConfig{BindingFile: path})
	if ok || len(names) != 0 {
		t.Fatalf("broken binding file should not autostart: ok=%v names=%v", ok, names)
	}
}
