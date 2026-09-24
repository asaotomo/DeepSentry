package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ScopeGlobal = "global"
	ScopeLocal  = "local"

	BuiltinAgentsMDPath = "builtin://AGENTS.md"
)

const builtinAgentsMD = `# DeepSentry Built-in AGENTS.md

This default memory is embedded in the DeepSentry binary. It gives the agent
stable operating preferences even when users copy only one executable file.

## Operating Preferences

- Prefer Shell-first troubleshooting: inspect with native commands, then use built-in tools when structured parsing or control-plane probing is useful.
- Keep actions auditable: summarize command purpose, important output, and final evidence.
- Avoid storing secrets in memory. Never save API keys, passwords, tokens, private keys, or webhook signing secrets.
- For non-interactive/WebShell mode, continue with conservative defaults and clearly report skipped optional inputs.
- Memory should be helpful, not noisy: preserve durable user preferences, repeated collaboration habits, target/project facts, and reusable investigation lessons.
- Warm memory is allowed when it improves future collaboration, such as the user's preferred language, report style, pacing, or recurring product expectations. Keep it short, respectful, and practical.
- Do not preserve transient emotions, private life details, raw conversation logs, or one-off wording unless the user explicitly asks.

## Memory Writing Policy

- Structured memory is the default place for specific lessons and facts. Use it for reusable commands, target paths, investigation conclusions, and user preferences.
- AGENTS.md is for durable operating rules and long-lived context that should shape every future session. It does not require manual editing only; the agent may maintain it when the user explicitly asks to remember something permanently, or when a stable preference emerges across multiple turns.
- Before writing AGENTS.md automatically, compress the point into a concise rule under a suitable section such as User Preferences, Target Environment, Collaboration Notes, or Historical Conclusions.

## Safety Defaults

- Read-only inspection is preferred unless the user explicitly asks for modification.
- High-risk destructive actions require approval unless the process is running in batch/webshell mode.
- Reports should be concise, evidence-oriented, and written in Chinese by default when the user writes Chinese.
`

// Entry 单条持久化记忆
type Entry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Scope     string    `json:"scope"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source"` // user | agent
}

// Store 跨会话记忆存储（对标 deepagents MemoryMiddleware + store backend）
type Store struct {
	mu       sync.RWMutex
	filePath string
	scope    string // 当前会话作用域
	entries  map[string]*Entry
	agentsMD map[string]string // source path -> content
	dirty    bool
}

// PersistedData 磁盘序列化格式
type PersistedData struct {
	Entries []Entry `json:"entries"`
}

// NewStore 创建并加载记忆存储
func NewStore(scope string) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	memDir := filepath.Join(home, ".deepsentry", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 memory 目录失败: %w", err)
	}
	if err := os.Chmod(memDir, 0o700); err != nil {
		return nil, fmt.Errorf("收紧 memory 目录权限失败: %w", err)
	}

	s := &Store{
		filePath: filepath.Join(memDir, "store.json"),
		scope:    scope,
		entries:  make(map[string]*Entry),
		agentsMD: make(map[string]string),
	}

	if err := s.load(); err != nil {
		return nil, err
	}
	s.loadAgentsMD()
	return s, nil
}

// ScopeForTarget 根据连接目标生成作用域
func ScopeForTarget(isRemote bool, sshHost string) string {
	if isRemote && sshHost != "" {
		host := strings.ReplaceAll(sshHost, ":", "_")
		return "ssh:" + host
	}
	return ScopeLocal
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var persisted PersistedData
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("解析 memory 文件失败: %w", err)
	}

	for i := range persisted.Entries {
		e := persisted.Entries[i]
		s.entries[entryID(e.Scope, e.Key)] = &e
	}
	return nil
}

func (s *Store) loadAgentsMD() {
	if strings.TrimSpace(builtinAgentsMD) != "" {
		s.agentsMD[BuiltinAgentsMDPath] = strings.TrimSpace(builtinAgentsMD)
	}
	sources := DefaultAgentsMDSources()
	for _, src := range sources {
		path := expandPath(src)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := stripHTMLComments(string(data))
		if strings.TrimSpace(content) != "" {
			s.agentsMD[path] = content
		}
	}
}

// Save 持久化到磁盘
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if !s.dirty {
		return nil
	}

	var entries []Entry
	for _, e := range s.entries {
		entries = append(entries, *e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Scope == entries[j].Scope {
			return entries[i].Key < entries[j].Key
		}
		return entries[i].Scope < entries[j].Scope
	})

	data, err := json.MarshalIndent(PersistedData{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(s.filePath), "memory-*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer os.Remove(tmp)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := replaceMemoryFile(tmp, s.filePath); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func replaceMemoryFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

// Set 写入记忆（当前作用域）
func (s *Store) Set(key, value, source string) error {
	return s.SetScoped(s.scope, key, value, source)
}

// SetScoped writes through the same locked store while selecting a different
// target scope, so parallel workers cannot overwrite each other's snapshots.
func (s *Store) SetScoped(scope, key, value, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return fmt.Errorf("memory scope 不能为空")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("memory key 不能为空")
	}
	if err := validateMemoryContent(key, value); err != nil {
		return err
	}

	id := entryID(scope, key)
	s.entries[id] = &Entry{
		Key:       key,
		Value:     value,
		Scope:     scope,
		UpdatedAt: time.Now(),
		Source:    source,
	}
	s.dirty = true
	return s.saveLocked()
}

// SetGlobal 写入全局记忆
func (s *Store) SetGlobal(key, value, source string) error {
	return s.SetScoped(ScopeGlobal, key, value, source)
}

// Delete 删除当前作用域的记忆
func (s *Store) Delete(key string) error {
	return s.DeleteScoped(s.scope, key)
}

// DeleteScoped removes a key from exactly one target scope.
func (s *Store) DeleteScoped(scope, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return fmt.Errorf("memory scope 不能为空")
	}
	id := entryID(scope, key)
	if _, ok := s.entries[id]; !ok {
		return fmt.Errorf("未找到记忆: %s", key)
	}
	delete(s.entries, id)
	s.dirty = true
	return s.saveLocked()
}

// DeleteGlobal 删除全局记忆
func (s *Store) DeleteGlobal(key string) error {
	return s.DeleteScoped(ScopeGlobal, key)
}

// Clear 删除结构化记忆。scope 支持 all/global/target(local/current)。
func (s *Store) Clear(scope string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "all"
	}
	removed := 0
	for id, e := range s.entries {
		remove := false
		switch scope {
		case "all", "*":
			remove = true
		case ScopeGlobal:
			remove = e.Scope == ScopeGlobal
		case "target", "current", "local":
			remove = e.Scope == s.scope
		default:
			return 0, fmt.Errorf("未知 memory 清理范围: %s", scope)
		}
		if remove {
			delete(s.entries, id)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	s.dirty = true
	return removed, s.saveLocked()
}

// ActiveEntries 返回当前会话可见的记忆（global + 当前 scope）
func (s *Store) ActiveEntries() []Entry {
	return s.ActiveEntriesForScope(s.scope)
}

// ActiveEntriesForScope returns global memory plus one target's memory.
func (s *Store) ActiveEntriesForScope(scope string) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeEntriesLocked(scope)
}

func (s *Store) activeEntriesLocked(scope string) []Entry {
	selected := make(map[string]Entry)
	for _, e := range s.entries {
		if e.Scope == ScopeGlobal {
			selected[e.Key] = *e
		}
	}
	// Target-specific memory always overrides a global value with the same key.
	for _, e := range s.entries {
		if e.Scope == scope {
			selected[e.Key] = *e
		}
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Entry, 0, len(keys))
	for _, key := range keys {
		result = append(result, selected[key])
	}
	return result
}

// FormatPrompt 生成注入 system prompt 的记忆片段
func (s *Store) FormatPrompt() string {
	return s.FormatPromptBudget(24000)
}

// FormatPromptBudget 按固定预算注入长期记忆，避免 AGENTS.md/KV 无限挤占任务上下文。
func (s *Store) FormatPromptBudget(maxChars int) string {
	return s.FormatPromptBudgetForScope(s.scope, maxChars)
}

// FormatPromptBudgetForScope injects only global and selected-target memory.
func (s *Store) FormatPromptBudgetForScope(scope string, maxChars int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if maxChars < 4000 {
		maxChars = 4000
	}
	policy := `
【记忆管理 — 跨会话持久化】
使用以下 action 保存/删除记忆（自动写入 ~/.deepsentry/memory/）:
- remember: {"action":"remember","memory_key":"键名","memory_value":"内容","memory_scope":"target|global"}
- forget: {"action":"forget","memory_key":"键名","memory_scope":"target|global"}

何时保存: 用户偏好、常用路径、目标环境特征、排查结论、SSH 主机别名等。
禁止保存: API Key、密码、Token 等凭证。
有温度的记忆点: 可保存稳定的沟通偏好、协作节奏、报告风格和反复出现的产品体验要求；保持简短、尊重、可执行。
AGENTS.md 不要求用户手动维护。AGENTS.md 用途: 长期稳定规则、跨会话协作偏好、项目/目标长期上下文；不必完全手写，用户明确要求永久记住或多轮对话形成稳定偏好时，可通过 write_file/edit_file 维护 ~/.deepsentry/AGENTS.md。
`
	contentBudget := maxChars - len(policy)
	var b strings.Builder

	if len(s.agentsMD) > 0 {
		b.WriteString("\n【Agent 持久记忆 (AGENTS.md)】\n")
		b.WriteString("以下是从 AGENTS.md 加载的项目/用户上下文，跨会话有效。\n\n")
		paths := make([]string, 0, len(s.agentsMD))
		for path := range s.agentsMD {
			paths = append(paths, path)
		}
		sort.Slice(paths, func(i, j int) bool {
			if paths[i] == BuiltinAgentsMDPath {
				return true
			}
			if paths[j] == BuiltinAgentsMDPath {
				return false
			}
			return paths[i] < paths[j]
		})
		for _, path := range paths {
			content := s.agentsMD[path]
			block := fmt.Sprintf("--- %s ---\n%s\n\n", path, truncateMemoryPrompt(content, 6000))
			if b.Len()+len(block) > contentBudget {
				b.WriteString("--- 其余 AGENTS.md 已因上下文预算省略 ---\n")
				break
			}
			b.WriteString(block)
		}
	}

	entries := s.activeEntriesLocked(scope)
	if len(entries) > 0 {
		sort.SliceStable(entries, func(i, j int) bool {
			iTarget := entries[i].Scope == scope
			jTarget := entries[j].Scope == scope
			if iTarget != jTarget {
				return iTarget
			}
			if !entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
				return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
			}
			return entries[i].Key < entries[j].Key
		})
		b.WriteString("\n【结构化记忆 (跨会话 KV)】\n")
		b.WriteString("以下是从历史会话中保存的关键信息。\n\n")
		for _, e := range entries {
			scopeTag := e.Scope
			if e.Scope == scope {
				scopeTag = "当前目标"
			}
			line := fmt.Sprintf("- [%s] **%s**: %s\n", scopeTag, e.Key, truncateMemoryPrompt(e.Value, 1200))
			if b.Len()+len(line) > contentBudget {
				b.WriteString("- ...(其余结构化记忆已因上下文预算省略)\n")
				break
			}
			b.WriteString(line)
		}
	}

	if b.Len() == 0 {
		return ""
	}

	b.WriteString(policy)
	return b.String()
}

func truncateMemoryPrompt(s string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(s))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return strings.TrimSpace(s)
	}
	return string(runes[:maxRunes]) + "\n...(该记忆源已按预算截断)..."
}

// Count 返回当前可见记忆条数
func (s *Store) Count() int {
	return len(s.ActiveEntries())
}

// HasContent 是否有任何可注入的记忆内容
func (s *Store) HasContent() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.activeEntriesLocked(s.scope)) > 0 || len(s.agentsMD) > 0
}

// AgentsMDCount 已加载的 AGENTS.md 文件数
func (s *Store) AgentsMDCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.agentsMD)
}

func entryID(scope, key string) string {
	return scope + "::" + key
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// DefaultAgentsMDSources AGENTS.md 来源（对标 deepagents memory sources）
func DefaultAgentsMDSources() []string {
	return []string{
		"~/.deepsentry/AGENTS.md",
		".deepsentry/AGENTS.md",
	}
}

// ClearExternalAgentsMD 删除外部 AGENTS.md 记忆文件；内置默认 AGENTS.md 保留。
func (s *Store) ClearExternalAgentsMD() (int, error) {
	removed := 0
	var errs []string
	for _, src := range DefaultAgentsMDSources() {
		path := expandPath(src)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, err.Error())
			continue
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		removed++
	}
	s.ReloadAgentsMD()
	if len(errs) > 0 {
		return removed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return removed, nil
}

func stripHTMLComments(s string) string {
	for {
		start := strings.Index(s, "<!--")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "-->")
		if end == -1 {
			break
		}
		s = s[:start] + s[start+end+3:]
	}
	return strings.TrimSpace(s)
}

func validateMemoryContent(key, value string) error {
	lower := strings.ToLower(key + " " + value)
	sensitive := []string{"api_key", "apikey", "password", "passwd", "secret", "token", "bearer", "private_key", "ssh_password"}
	for _, s := range sensitive {
		if strings.Contains(lower, s) {
			return fmt.Errorf("禁止存储敏感凭证 (%s)", s)
		}
	}
	if len(value) > 4096 {
		return fmt.Errorf("记忆内容过长 (最大 4096 字符)")
	}
	return nil
}

// IsAgentsMDPath 是否为 AGENTS.md 路径
func IsAgentsMDPath(path string) bool {
	path = expandPath(path)
	lower := strings.ToLower(filepath.Base(path))
	if lower != "agents.md" {
		return false
	}
	home, _ := os.UserHomeDir()
	allowed := []string{
		filepath.Join(home, ".deepsentry", "AGENTS.md"),
		filepath.Join(".deepsentry", "AGENTS.md"),
	}
	for _, a := range allowed {
		if path == expandPath(a) || strings.HasSuffix(path, string(os.PathSeparator)+".deepsentry"+string(os.PathSeparator)+"AGENTS.md") {
			return true
		}
	}
	return strings.Contains(path, ".deepsentry") && lower == "agents.md"
}

// UpdateAgentsMD 热更新 AGENTS.md 内容（write_file/edit_file 写回后调用）
func (s *Store) UpdateAgentsMD(path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = expandPath(path)
	content = stripHTMLComments(content)
	if strings.TrimSpace(content) == "" {
		delete(s.agentsMD, path)
		return
	}
	s.agentsMD[path] = content
}

// ReloadAgentsMD 从磁盘重新加载 AGENTS.md
func (s *Store) ReloadAgentsMD() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentsMD = make(map[string]string)
	s.loadAgentsMD()
}

// EnsureDefaultAgentsMD 保留兼容入口。
//
// 默认 AGENTS.md 已内置进二进制，不再首次运行时强制写入 ~/.deepsentry/AGENTS.md。
// 用户需要自定义长期偏好时，可以手动创建 ~/.deepsentry/AGENTS.md 或项目 .deepsentry/AGENTS.md。
func EnsureDefaultAgentsMD() error {
	return nil
}
