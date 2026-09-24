package harness

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// AgentState 持久化 Agent 运行时状态（对标 DeepAgentState）
type AgentState struct {
	WorkContext           map[string]string `json:"work_context,omitempty"`
	Todos                 []TodoItem
	LoadedSkills          map[string]string // skill name -> full content
	Memory                map[string]string // 会话内临时 KV（不落盘）
	CoreClues             []CoreClue        // 会话级核心线索，checkpoint 持久化并共享给子 Agent
	SelectedTools         map[string]bool   // 当前任务经 tool_search/实际调用验证过的工具
	TriedTools            map[string]bool   `json:"tried_tools,omitempty"` // 本会话已真正执行过的工具（用于 ZIP 原生优先等路由）
	PendingToolCalls      map[string]ToolCallRecord
	CompletedToolCalls    map[string]ToolCallRecord
	PendingAction         string `json:"pending_action,omitempty"`        // redacted main-agent action awaiting a durable result
	PendingActionHash     string `json:"pending_action_hash,omitempty"`   // executable fingerprint; never contains raw arguments
	RecoveryBlockedHash   string `json:"recovery_blocked_hash,omitempty"` // uncertain action must not be replayed before a read-only check
	Artifacts             []ArtifactRecord
	WorkspaceDir          string // 工具输出卸载目录
	SessionID             string // checkpoint/session id，用于隔离输出卸载文件
	LastActionFingerprint string
	LastActionRepeat      int
	LastOutputHash        string
	LastFailed            bool
	LastErrorClass        string
	StallCount            int
	FailStreak            int
	NoProgressStreak      int
	TimeoutStreak         int
	StepsSinceTodo        int
	mu                    sync.RWMutex
}

// NewAgentState 创建初始状态
func NewAgentState(workspaceDir string) *AgentState {
	return NewAgentStateWithSession(workspaceDir, "")
}

func NewAgentStateWithSession(workspaceDir, sessionID string) *AgentState {
	return &AgentState{
		Todos:              []TodoItem{},
		LoadedSkills:       make(map[string]string),
		Memory:             make(map[string]string),
		CoreClues:          []CoreClue{},
		SelectedTools:      make(map[string]bool),
		TriedTools:         make(map[string]bool),
		PendingToolCalls:   make(map[string]ToolCallRecord),
		CompletedToolCalls: make(map[string]ToolCallRecord),
		Artifacts:          []ArtifactRecord{},
		WorkspaceDir:       workspaceDir,
		SessionID:          sessionID,
	}
}

type ToolCallRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Risk      string    `json:"risk,omitempty"`
	ArgsHash  string    `json:"args_hash,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

type ArtifactRecord struct {
	Path       string    `json:"path"`
	SHA256     string    `json:"sha256"`
	Source     string    `json:"source"`
	Target     string    `json:"target,omitempty"`
	Size       int64     `json:"size"`
	Summary    string    `json:"summary"`
	RecordedAt time.Time `json:"recorded_at"`
}

func (s *AgentState) AddArtifact(record ArtifactRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Artifacts = append(s.Artifacts, record)
}

func (s *AgentState) MarkSelectedTool(name string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SelectedTools == nil {
		s.SelectedTools = make(map[string]bool)
	}
	s.SelectedTools[name] = true
}

func (s *AgentState) MarkTriedTool(name string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markTriedToolLocked(name)
}

func (s *AgentState) markTriedToolLocked(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if s.TriedTools == nil {
		s.TriedTools = make(map[string]bool)
	}
	s.TriedTools[name] = true
}

func (s *AgentState) HasTriedTool(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasTriedToolLocked(name)
}

func (s *AgentState) hasTriedToolLocked(name string) bool {
	if s.TriedTools[name] {
		return true
	}
	for _, record := range s.CompletedToolCalls {
		if strings.EqualFold(record.Name, name) {
			return true
		}
	}
	return false
}

func (s *AgentState) SelectedToolNames() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.SelectedTools))
	for name := range s.SelectedTools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *AgentState) ToolCallPending(id string) (ToolCallRecord, bool) {
	if s == nil || id == "" {
		return ToolCallRecord{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.PendingToolCalls[id]
	return record, ok
}

// PendingMutationByFingerprint detects the same non-idempotent operation even
// when a provider regenerates a different tool-call ID after resume. A pending
// modifying call is deliberately not replayed automatically because the
// process may have crashed after the external side effect but before the
// completion checkpoint became durable.
func (s *AgentState) PendingMutationByFingerprint(name, argsHash string) (ToolCallRecord, bool) {
	if s == nil || name == "" || argsHash == "" {
		return ToolCallRecord{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, record := range s.PendingToolCalls {
		if record.Name == name && record.ArgsHash == argsHash && record.Risk != "" && record.Risk != "low" {
			return record, true
		}
	}
	return ToolCallRecord{}, false
}

func (s *AgentState) BeginToolCall(record ToolCallRecord) bool {
	if s == nil || record.ID == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, done := s.CompletedToolCalls[record.ID]; done {
		return false
	}
	if s.PendingToolCalls == nil {
		s.PendingToolCalls = make(map[string]ToolCallRecord)
	}
	record.StartedAt = time.Now().UTC()
	s.PendingToolCalls[record.ID] = record
	return true
}

func (s *AgentState) CompleteToolCall(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.PendingToolCalls[id]
	record.ID = id
	record.EndedAt = time.Now().UTC()
	delete(s.PendingToolCalls, id)
	if s.CompletedToolCalls == nil {
		s.CompletedToolCalls = make(map[string]ToolCallRecord)
	}
	s.CompletedToolCalls[id] = record
}

// FailToolCall releases a failed low-risk/idempotent call so the model may
// retry it with the same or a regenerated ID. Non-low-risk calls remain
// pending because a transport error cannot prove that an external mutation
// did not happen.
func (s *AgentState) FailToolCall(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.PendingToolCalls[id]
	if !ok {
		return
	}
	if record.Risk == "" || record.Risk == "low" {
		delete(s.PendingToolCalls, id)
	}
}

func (s *AgentState) ToolCallCompleted(id string) bool {
	if s == nil || id == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.CompletedToolCalls[id]
	return ok
}

// SetMemory 写入记忆
func (s *AgentState) SetMemory(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Memory[key] = value
}

// GetMemory 读取记忆
func (s *AgentState) GetMemory(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.Memory[key]
	return v, ok
}

// ResetLoopTurn gives a new user turn a fresh recovery budget, preserving evidence.
func (s *AgentState) ResetLoopTurn() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastActionFingerprint = ""
	s.LastActionRepeat = 0
	s.LastOutputHash = ""
	s.LastFailed = false
	s.LastErrorClass = ""
	s.StallCount = 0
	s.FailStreak = 0
	s.NoProgressStreak = 0
	s.TimeoutStreak = 0
	s.StepsSinceTodo = 0
}
