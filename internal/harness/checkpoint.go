package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/security"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// CheckpointData 会话 checkpoint 快照
const currentCheckpointSchemaVersion = 4

type CheckpointData struct {
	SchemaVersion   int                 `json:"schema_version"`
	RuntimeVersion  string              `json:"runtime_version,omitempty"`
	RunID           string              `json:"run_id,omitempty"`
	TurnID          string              `json:"turn_id,omitempty"`
	EventCursor     int64               `json:"event_cursor,omitempty"`
	SessionID       string              `json:"session_id"`
	StepNum         int                 `json:"step_num"`
	UserGoal        string              `json:"user_goal,omitempty"`
	State           *AgentState         `json:"state"`
	History         []analyzer.Message  `json:"history"`
	SubAgentKeys    *[]string           `json:"sub_agent_keys,omitempty"` // present, even empty, scopes children to this parent task
	SubAgent        *SubAgentCheckpoint `json:"sub_agent,omitempty"`
	SavedAt         time.Time           `json:"saved_at"`
	IntegritySHA256 string              `json:"integrity_sha256,omitempty"`
}

// CheckpointStore checkpoint 持久化
type CheckpointStore struct {
	dir       string
	sessionID string
}

var sessionIDPattern = regexp.MustCompile(`^session_[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func validateSessionID(sessionID string) error {
	if !sessionIDPattern.MatchString(sessionID) {
		return fmt.Errorf("非法 session_id: %q", sessionID)
	}
	return nil
}

// NewCheckpointStore 创建 checkpoint 存储
func NewCheckpointStore(sessionID string) (*CheckpointStore, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".deepsentry", "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &CheckpointStore{dir: dir, sessionID: sessionID}, nil
}

// SessionDir 返回会话目录
func (c *CheckpointStore) SessionDir() string {
	return c.dir
}

// Save 保存 checkpoint
func (c *CheckpointStore) Save(data CheckpointData) error {
	if data.SchemaVersion == 0 {
		data.SchemaVersion = currentCheckpointSchemaVersion
	}
	if data.RuntimeVersion == "" {
		data.RuntimeVersion = config.GlobalConfig.EffectiveAgentRuntime()
	}
	data.SessionID = c.sessionID
	data.SavedAt = time.Now().UTC()
	data.IntegritySHA256 = ""
	raw, err := security.RedactJSON(data)
	if err != nil {
		return err
	}
	// v4 covers the complete redacted JSON object, including fields unknown to
	// older binaries. v1-v3 keep their original typed checksum for compatibility.
	sum := sha256.Sum256(raw)
	raw, err = checkpointAddIntegrity(raw, fmt.Sprintf("%x", sum[:]))
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(c.dir, "checkpoint-*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer os.Remove(tmp)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(raw); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return rotateCheckpointFile(tmp, filepath.Join(c.dir, "checkpoint.json"), filepath.Join(c.dir, "checkpoint.prev.json"))
}

func checkpointAddIntegrity(redacted []byte, digest string) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(redacted, &object); err != nil {
		return nil, err
	}
	encodedDigest, err := json.Marshal(digest)
	if err != nil {
		return nil, err
	}
	object["integrity_sha256"] = encodedDigest
	return json.MarshalIndent(object, "", "  ")
}

func rotateCheckpointFile(src, dst, previous string) error {
	if _, err := os.Stat(dst); err == nil {
		if runtime.GOOS == "windows" {
			_ = os.Remove(previous)
		}
		if err := os.Rename(dst, previous); err != nil {
			return err
		}
	}
	if err := replaceCheckpointFile(src, dst); err != nil {
		_ = os.Rename(previous, dst)
		return err
	}
	return nil
}

func replaceCheckpointFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	// Windows cannot atomically replace an existing destination with Rename.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

// Load 加载 checkpoint
func LoadCheckpoint(sessionID string) (*CheckpointData, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".deepsentry", "sessions", sessionID, "checkpoint.json")
	return loadCheckpointFile(path)
}

func loadCheckpointFile(path string) (*CheckpointData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			previous := filepath.Join(filepath.Dir(path), "checkpoint.prev.json")
			if priorRaw, priorErr := os.ReadFile(previous); priorErr == nil {
				if prior, priorErr := decodeCheckpoint(priorRaw); priorErr == nil {
					initializeCheckpointState(prior)
					return prior, nil
				}
			}
		}
		return nil, fmt.Errorf("无法加载 checkpoint %s: %w", path, err)
	}
	data, err := decodeCheckpoint(raw)
	if err != nil {
		previous := filepath.Join(filepath.Dir(path), "checkpoint.prev.json")
		if priorRaw, priorErr := os.ReadFile(previous); priorErr == nil {
			if prior, priorErr := decodeCheckpoint(priorRaw); priorErr == nil {
				initializeCheckpointState(prior)
				return prior, nil
			}
		}
		return nil, err
	}
	initializeCheckpointState(data)
	return data, nil
}

func decodeCheckpoint(raw []byte) (*CheckpointData, error) {
	var data CheckpointData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("checkpoint JSON 损坏: %w", err)
	}
	if data.IntegritySHA256 != "" {
		want := data.IntegritySHA256
		data.IntegritySHA256 = ""
		var canonical []byte
		var err error
		if data.SchemaVersion >= 4 {
			canonical, err = checkpointIntegrityPayload(raw)
		} else {
			canonical, err = security.RedactJSON(data)
		}
		if err != nil {
			return nil, err
		}
		got := sha256.Sum256(canonical)
		if fmt.Sprintf("%x", got[:]) != want {
			return nil, fmt.Errorf("checkpoint 完整性校验失败")
		}
		data.IntegritySHA256 = want
	}
	if data.SchemaVersion == 0 {
		data.SchemaVersion = 1
	}
	return &data, nil
}

func checkpointIntegrityPayload(raw []byte) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	delete(object, "integrity_sha256")
	return json.MarshalIndent(object, "", "  ")
}

func initializeCheckpointState(data *CheckpointData) {
	legacyBoundary := data.SchemaVersion < 3
	if strings.TrimSpace(data.RuntimeVersion) == "" {
		data.RuntimeVersion = "legacy"
	}
	if data.State == nil {
		data.State = NewAgentState("")
	}
	if data.State.LoadedSkills == nil {
		data.State.LoadedSkills = make(map[string]string)
	}
	if data.State.Memory == nil {
		data.State.Memory = make(map[string]string)
	}
	if data.State.CoreClues == nil {
		data.State.CoreClues = []CoreClue{}
	}
	if data.State.SelectedTools == nil {
		data.State.SelectedTools = make(map[string]bool)
	}
	if data.State.TriedTools == nil {
		data.State.TriedTools = make(map[string]bool)
	}
	if data.State.PendingToolCalls == nil {
		data.State.PendingToolCalls = make(map[string]ToolCallRecord)
	}
	if data.State.CompletedToolCalls == nil {
		data.State.CompletedToolCalls = make(map[string]ToolCallRecord)
	}
	if data.State.Artifacts == nil {
		data.State.Artifacts = []ArtifactRecord{}
	}
	if legacyBoundary {
		// Schema v1/v2 had no durable per-tool safe point. Resume only at the
		// saved turn boundary instead of trusting fields an old writer could not
		// have committed atomically.
		data.RunID = ""
		data.TurnID = ""
		data.EventCursor = 0
		data.State.PendingToolCalls = make(map[string]ToolCallRecord)
		data.State.CompletedToolCalls = make(map[string]ToolCallRecord)
	}
}

// SessionRoot is the directory that holds one subdirectory per checkpoint.
func SessionRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".deepsentry", "sessions"), nil
}

// RemoveSession deletes one checkpoint directory. The id must already be a
// validated session id so a bad value cannot escape the sessions directory.
func RemoveSession(sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	root, err := SessionRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, sessionID)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
		return fmt.Errorf("拒绝删除会话目录: %s", sessionID)
	}
	return os.RemoveAll(dir)
}

// ListSessions 列出可恢复的会话 ID
func ListSessions() ([]string, error) {
	root, err := SessionRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && validateSessionID(e.Name()) == nil {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "checkpoint.json")); err == nil {
				ids = append(ids, e.Name())
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

// SessionSummary 会话摘要（供 TUI 选择器）
type SessionSummary struct {
	ID      string
	StepNum int
	SavedAt time.Time
	Goal    string
}

// ListSessionSummaries 列出可恢复会话及元数据
func ListSessionSummaries() ([]SessionSummary, error) {
	ids, err := ListSessions()
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(ids))
	for _, id := range ids {
		cp, err := LoadCheckpoint(id)
		if err != nil {
			continue
		}
		out = append(out, SessionSummary{
			ID: id, StepNum: cp.StepNum, SavedAt: cp.SavedAt, Goal: cp.UserGoal,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SavedAt.Equal(out[j].SavedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].SavedAt.After(out[j].SavedAt)
	})
	return out, nil
}

// NewSessionID 生成新会话 ID
func NewSessionID() string {
	return fmt.Sprintf("session_%d", time.Now().UnixNano())
}
