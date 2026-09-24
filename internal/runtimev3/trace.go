package runtimev3

import (
	"ai-edr/internal/security"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type JSONLTraceSink struct {
	file *os.File
	mu   sync.Mutex
}

func NewJSONLTraceSink(path string) (*JSONLTraceSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLTraceSink{file: f}, nil
}

func (s *JSONLTraceSink) Emit(_ context.Context, event RunEvent) error {
	if s == nil || s.file == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	raw, err := security.RedactJSON(event)
	if err != nil {
		return err
	}
	// RedactJSON is intentionally human-readable for checkpoint files. JSONL
	// requires exactly one JSON value per physical line, so compact only after
	// structural redaction has completed.
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return err
	}
	compact.WriteByte('\n')
	_, err = s.file.Write(compact.Bytes())
	return err
}

// Flush makes all append-only events durable. Runtime checkpoints call this
// before persisting an event cursor to preserve interrupt/resume durability.
func (s *JSONLTraceSink) Flush(_ context.Context) error {
	if s == nil || s.file == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Sync()
}

func (s *JSONLTraceSink) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.file.Sync()
	if closeErr := s.file.Close(); err == nil {
		err = closeErr
	}
	s.file = nil
	return err
}
