package runtimev3

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// ContentKind describes an ordered model-native content block. Keeping tool
// calls and results as first-class blocks avoids flattening provider protocols
// into synthetic user messages.
type ContentKind string

const (
	ContentText        ContentKind = "text"
	ContentReasoning   ContentKind = "reasoning"
	ContentToolCall    ContentKind = "tool_call"
	ContentToolResult  ContentKind = "tool_result"
	ContentArtifactRef ContentKind = "artifact_ref"
)

type ContentBlock struct {
	Kind       ContentKind     `json:"kind"`
	Text       string          `json:"text,omitempty"`
	ToolCall   *ToolCall       `json:"tool_call,omitempty"`
	ToolResult *ToolResult     `json:"tool_result,omitempty"`
	Artifact   *ArtifactRef    `json:"artifact,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type Message struct {
	ID        string         `json:"id"`
	Role      string         `json:"role"`
	Blocks    []ContentBlock `json:"blocks"`
	CreatedAt time.Time      `json:"created_at"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	CallID  string         `json:"call_id"`
	Name    string         `json:"name"`
	Blocks  []ContentBlock `json:"blocks,omitempty"`
	IsError bool           `json:"is_error,omitempty"`
}

type ArtifactRef struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256,omitempty"`
	Source     string `json:"source,omitempty"`
	Target     string `json:"target,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Summary    string `json:"summary,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	Redacted   bool   `json:"redacted,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
}

type ModelRequest struct {
	Messages []Message       `json:"messages"`
	Tools    []ToolInfo      `json:"tools,omitempty"`
	Options  json.RawMessage `json:"options,omitempty"`
}

type ModelResponse struct {
	Message Message    `json:"message"`
	Usage   TokenUsage `json:"usage,omitempty"`
	ModelID string     `json:"model_id,omitempty"`
}

type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type StreamEvent struct {
	Kind    ContentKind  `json:"kind"`
	Block   ContentBlock `json:"block,omitempty"`
	Usage   TokenUsage   `json:"usage,omitempty"`
	Err     error        `json:"-"`
	Partial bool         `json:"partial,omitempty"`
}

type StreamReader interface {
	Recv() (StreamEvent, error)
	Close() error
}

type ModelClient interface {
	Generate(context.Context, ModelRequest) (ModelResponse, error)
	Stream(context.Context, ModelRequest) (StreamReader, error)
}

type ToolInfo struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	InputSchema    json.RawMessage `json:"input_schema"`
	RiskLevel      string          `json:"risk_level,omitempty"`
	Perspective    string          `json:"perspective,omitempty"`
	Idempotent     bool            `json:"idempotent"`
	ConcurrencyKey string          `json:"concurrency_key,omitempty"`
	Timeout        time.Duration   `json:"timeout,omitempty"`
}

type Tool interface {
	Info(context.Context) (ToolInfo, error)
	Invoke(context.Context, json.RawMessage) (ToolResult, error)
}

type StreamingTool interface {
	Tool
	Stream(context.Context, json.RawMessage) (StreamReader, error)
}

type EventKind string

const (
	EventRunStart       EventKind = "run.start"
	EventRunEnd         EventKind = "run.end"
	EventModelStart     EventKind = "model.start"
	EventModelDelta     EventKind = "model.delta"
	EventModelEnd       EventKind = "model.end"
	EventModelError     EventKind = "model.error"
	EventModelRetry     EventKind = "model.retry"
	EventModelFailover  EventKind = "model.failover"
	EventToolStart      EventKind = "tool.start"
	EventToolDelta      EventKind = "tool.delta"
	EventToolEnd        EventKind = "tool.end"
	EventToolError      EventKind = "tool.error"
	EventInterrupt      EventKind = "run.interrupt"
	EventCheckpoint     EventKind = "checkpoint.save"
	EventContextCompact EventKind = "context.compact"
)

// RunEvent is the stable, redacted event envelope shared by traces, reports,
// UI adapters and tests. Payload must never contain raw credentials.
type RunEvent struct {
	Sequence     int64           `json:"sequence"`
	RunID        string          `json:"run_id"`
	TurnID       string          `json:"turn_id,omitempty"`
	StepID       string          `json:"step_id,omitempty"`
	ParentRunID  string          `json:"parent_run_id,omitempty"`
	Kind         EventKind       `json:"kind"`
	Component    string          `json:"component,omitempty"`
	ModelID      string          `json:"model_id,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Timestamp    time.Time       `json:"timestamp"`
	DurationMS   int64           `json:"duration_ms,omitempty"`
	Usage        TokenUsage      `json:"usage,omitempty"`
	ErrorClass   ErrorKind       `json:"error_class,omitempty"`
	Message      string          `json:"message,omitempty"`
	Artifact     *ArtifactRef    `json:"artifact,omitempty"`
	SafeMetadata json.RawMessage `json:"metadata,omitempty"`
}

type EventSink interface {
	Emit(context.Context, RunEvent) error
}

// DurableEventSink is implemented by append-only event stores that can make
// all events emitted so far durable before a checkpoint references their
// cursor. This ordering prevents recovery from observing a checkpoint whose
// preceding tool/model events were still buffered in memory.
type DurableEventSink interface {
	EventSink
	Flush(context.Context) error
}

type EventSinkFunc func(context.Context, RunEvent) error

func (f EventSinkFunc) Emit(ctx context.Context, event RunEvent) error { return f(ctx, event) }

type MultiSink []EventSink

func (m MultiSink) Emit(ctx context.Context, event RunEvent) error {
	var first error
	for _, sink := range m {
		if sink == nil {
			continue
		}
		if err := sink.Emit(ctx, event); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m MultiSink) Flush(ctx context.Context) error {
	var first error
	for _, sink := range m {
		if durable, ok := sink.(interface{ Flush(context.Context) error }); ok {
			if err := durable.Flush(ctx); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// SequenceSink assigns monotonically increasing event cursors per run.
type SequenceSink struct {
	Next int64
	Sink EventSink
	mu   sync.Mutex
}

func (s *SequenceSink) Emit(ctx context.Context, event RunEvent) error {
	if s == nil || s.Sink == nil {
		return nil
	}
	s.mu.Lock()
	s.Next++
	event.Sequence = s.Next
	s.mu.Unlock()
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	return s.Sink.Emit(ctx, event)
}

// Cursor returns the last allocated event sequence without racing Emit.
func (s *SequenceSink) Cursor() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Next
}

// AdvanceTo restores a persisted session cursor. It never moves backwards,
// allowing a resumed process to append events without reusing sequence IDs.
func (s *SequenceSink) AdvanceTo(cursor int64) {
	if s == nil || cursor <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor > s.Next {
		s.Next = cursor
	}
}

func (s *SequenceSink) Flush(ctx context.Context) error {
	if s == nil || s.Sink == nil {
		return nil
	}
	if durable, ok := s.Sink.(interface{ Flush(context.Context) error }); ok {
		return durable.Flush(ctx)
	}
	return nil
}

func NewID(prefix string) string {
	var raw [12]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err == nil {
		return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(raw[:]))
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
