package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreSetAndLoad(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	store, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Set("web_root", "/var/www/html", "agent"); err != nil {
		t.Fatal(err)
	}

	store2, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}

	entries := store2.ActiveEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Key != "web_root" || entries[0].Value != "/var/www/html" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}

	// 验证文件落盘
	path := filepath.Join(tmp, ".deepsentry", "memory", "store.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file not created: %v", err)
	}
}

func TestStoreSupportsConcurrentSubAgentMemoryUpdates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Set(fmt.Sprintf("finding_%02d", i), fmt.Sprintf("value-%02d", i), "agent"); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := store.Count(); got != workers {
		t.Fatalf("concurrent updates retained %d/%d entries", got, workers)
	}
}

func TestStoreScopedSubAgentMemoryIsolationAndPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, scope := range []string{"ssh:host-a", "ssh:host-b"} {
		scope := scope
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.SetScoped(scope, "os", scope, "agent"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := store.ActiveEntries(); len(got) != 0 {
		t.Fatalf("parent scope received child target memory: %#v", got)
	}
	for _, scope := range []string{"ssh:host-a", "ssh:host-b"} {
		entries := store.ActiveEntriesForScope(scope)
		if len(entries) != 1 || entries[0].Value != scope {
			t.Fatalf("scope %s entries=%#v", scope, entries)
		}
	}
	reloaded, err := NewStore("ssh:host-a")
	if err != nil {
		t.Fatal(err)
	}
	if entries := reloaded.ActiveEntries(); len(entries) != 1 || entries[0].Value != "ssh:host-a" {
		t.Fatalf("scoped memory was not persisted: entries=%#v", entries)
	}
}

func TestBuiltinAgentsMDLoadedWithoutExternalFile(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	store, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	if store.AgentsMDCount() != 1 {
		t.Fatalf("expected builtin AGENTS.md only, got %d", store.AgentsMDCount())
	}
	prompt := store.FormatPrompt()
	if !strings.Contains(prompt, BuiltinAgentsMDPath) {
		t.Fatalf("prompt should include builtin AGENTS.md source, got:\n%s", prompt)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".deepsentry", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("builtin AGENTS.md should not create external file, stat err=%v", err)
	}
}

func TestMemoryPromptExplainsAutoAgentsMDPolicy(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	store, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	prompt := store.FormatPrompt()
	for _, want := range []string{"AGENTS.md 不要求用户手动维护", "有温度的记忆点", "稳定偏好"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt should explain auto memory policy %q, got:\n%s", want, prompt)
		}
	}
}

func TestMemoryPromptHasBoundedContextBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.agentsMD["/huge/AGENTS.md"] = strings.Repeat("长期规则和项目背景。", 10000)
	store.mu.Unlock()
	for i := 0; i < 20; i++ {
		if err := store.Set(fmt.Sprintf("note_%02d", i), strings.Repeat("高信号记忆", 150), "agent"); err != nil {
			t.Fatal(err)
		}
	}
	prompt := store.FormatPromptBudget(8000)
	if len(prompt) > 8200 {
		t.Fatalf("memory prompt exceeded bounded budget: %d", len(prompt))
	}
	if !strings.Contains(prompt, "记忆管理") || !strings.Contains(prompt, "因上下文预算省略") || !strings.Contains(prompt, BuiltinAgentsMDPath) {
		t.Fatalf("bounded prompt lost policy or omission marker:\n%s", prompt)
	}
}

func TestStoreScopeIsolation(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	store, _ := NewStore("ssh:192.168.1.1_22")
	_ = store.Set("hostname", "prod-web", "agent")
	_ = store.SetGlobal("report_lang", "zh-CN", "agent")

	local, _ := NewStore(ScopeLocal)
	localEntries := local.ActiveEntries()
	if len(localEntries) != 1 || localEntries[0].Key != "report_lang" {
		t.Fatalf("local should only see global entry, got %+v", localEntries)
	}

	ssh, _ := NewStore("ssh:192.168.1.1_22")
	sshEntries := ssh.ActiveEntries()
	if len(sshEntries) != 2 {
		t.Fatalf("ssh scope should see 2 entries, got %d", len(sshEntries))
	}
}

func TestTargetMemoryDeterministicallyOverridesGlobalKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore("ssh:demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetGlobal("report_lang", "en-US", "agent"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("report_lang", "zh-CN", "agent"); err != nil {
		t.Fatal(err)
	}
	entries := store.ActiveEntries()
	if len(entries) != 1 || entries[0].Value != "zh-CN" || entries[0].Scope != "ssh:demo" {
		t.Fatalf("target override failed: %#v", entries)
	}
}

func TestRejectSensitiveMemory(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	store, _ := NewStore(ScopeLocal)
	// Assemble the marker at runtime so repository secret scanners do not
	// mistake this deliberately fake rejection fixture for a committed key.
	err := store.Set("creds", "api_"+"key=sk-secret123", "agent")
	if err == nil {
		t.Fatal("expected error for sensitive content")
	}
}

func TestDeleteMemory(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	store, _ := NewStore(ScopeLocal)
	_ = store.Set("temp", "value", "agent")
	if err := store.Delete("temp"); err != nil {
		t.Fatal(err)
	}
	if len(store.ActiveEntries()) != 0 {
		t.Fatal("entry should be deleted")
	}
}

func TestClearMemoryScopes(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	store, _ := NewStore("ssh:demo")
	_ = store.Set("target_note", "value", "agent")
	_ = store.SetGlobal("global_note", "value", "agent")

	n, err := store.Clear("target")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("target clear removed %d entries, want 1", n)
	}
	entries := store.ActiveEntries()
	if len(entries) != 1 || entries[0].Key != "global_note" {
		t.Fatalf("target clear should keep global entry, got %+v", entries)
	}

	n, err = store.Clear("all")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(store.ActiveEntries()) != 0 {
		t.Fatalf("all clear failed: n=%d entries=%+v", n, store.ActiveEntries())
	}
}

func TestClearExternalAgentsMD(t *testing.T) {
	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", oldHome)

	userAgents := filepath.Join(tmp, ".deepsentry", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(userAgents), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userAgents, []byte("# Custom\n- remember me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	if store.AgentsMDCount() != 2 {
		t.Fatalf("expected builtin + custom AGENTS.md, got %d", store.AgentsMDCount())
	}
	n, err := store.ClearExternalAgentsMD()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d AGENTS.md files, want 1", n)
	}
	if _, err := os.Stat(userAgents); !os.IsNotExist(err) {
		t.Fatalf("custom AGENTS.md should be deleted, stat err=%v", err)
	}
	if store.AgentsMDCount() != 1 {
		t.Fatalf("builtin AGENTS.md should remain, got %d sources", store.AgentsMDCount())
	}
}
