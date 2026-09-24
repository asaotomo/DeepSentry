package runtimev3

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

type ErrorKind string

const (
	ErrorUnknown       ErrorKind = "unknown"
	ErrorCanceled      ErrorKind = "canceled"
	ErrorTimeout       ErrorKind = "timeout"
	ErrorRateLimit     ErrorKind = "rate_limit"
	ErrorServer        ErrorKind = "server_error"
	ErrorInvalidOutput ErrorKind = "invalid_output"
	ErrorUnsupported   ErrorKind = "unsupported"
	ErrorConnection    ErrorKind = "connection"
)

type ClassifiedError struct {
	Kind       ErrorKind
	StatusCode int
	Err        error
}

func (e *ClassifiedError) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Kind)
	}
	return e.Err.Error()
}

func (e *ClassifiedError) Unwrap() error { return e.Err }

func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var classified *ClassifiedError
	if errors.As(err, &classified) && classified.Kind != "" {
		return classified.Kind
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ErrorTimeout
		}
		return ErrorConnection
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return ErrorConnection
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "429"), strings.Contains(s, "rate limit"), strings.Contains(s, "too many requests"):
		return ErrorRateLimit
	case strings.Contains(s, "500"), strings.Contains(s, "502"), strings.Contains(s, "503"), strings.Contains(s, "504"):
		return ErrorServer
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return ErrorTimeout
	case strings.Contains(s, "connection reset"), strings.Contains(s, "broken pipe"), strings.Contains(s, "unexpected eof"):
		return ErrorConnection
	case strings.Contains(s, "unsupported"), strings.Contains(s, "unknown parameter"), strings.Contains(s, "not support"):
		return ErrorUnsupported
	case strings.Contains(s, "parse error"), strings.Contains(s, "invalid json"), strings.Contains(s, "empty response"):
		return ErrorInvalidOutput
	default:
		return ErrorUnknown
	}
}

type Endpoint struct {
	ID         string
	Client     ModelClient
	MaxRetries int
}

type Router struct {
	Endpoints  []Endpoint
	FailoverOn map[ErrorKind]bool
	Backoff    func(attempt int) time.Duration
	Events     EventSink
	RunID      string
	TurnID     string
}

func (r *Router) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	if len(r.Endpoints) == 0 {
		return ModelResponse{}, errors.New("model router has no endpoints")
	}
	var lastErr error
	for endpointIndex, endpoint := range r.Endpoints {
		if endpoint.Client == nil {
			continue
		}
		if endpointIndex > 0 {
			r.emit(ctx, RunEvent{Kind: EventModelFailover, ModelID: endpoint.ID, ErrorClass: ClassifyError(lastErr), Message: safeError(lastErr)})
		}
		for attempt := 0; attempt <= maxInt(endpoint.MaxRetries, 0); attempt++ {
			if err := ctx.Err(); err != nil {
				return ModelResponse{}, err
			}
			if attempt > 0 {
				delay := r.retryDelay(attempt)
				r.emit(ctx, RunEvent{Kind: EventModelRetry, ModelID: endpoint.ID, ErrorClass: ClassifyError(lastErr), Message: fmt.Sprintf("attempt=%d backoff=%s", attempt, delay)})
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ModelResponse{}, ctx.Err()
				case <-timer.C:
				}
			}
			started := time.Now()
			r.emit(ctx, RunEvent{Kind: EventModelStart, ModelID: endpoint.ID})
			response, err := endpoint.Client.Generate(ctx, request)
			if err == nil {
				response.ModelID = endpoint.ID
				r.emit(ctx, RunEvent{Kind: EventModelEnd, ModelID: endpoint.ID, DurationMS: time.Since(started).Milliseconds(), Usage: response.Usage})
				return response, nil
			}
			lastErr = err
			kind := ClassifyError(err)
			r.emit(ctx, RunEvent{Kind: EventModelError, ModelID: endpoint.ID, DurationMS: time.Since(started).Milliseconds(), ErrorClass: kind, Message: safeError(err)})
			if kind == ErrorCanceled || !isRetryableKind(kind) {
				break
			}
		}
		if !r.shouldFailover(ClassifyError(lastErr)) {
			break
		}
	}
	return ModelResponse{}, fmt.Errorf("model router exhausted: %w", lastErr)
}

func (r *Router) shouldFailover(kind ErrorKind) bool {
	if len(r.FailoverOn) == 0 {
		return kind == ErrorRateLimit || kind == ErrorTimeout || kind == ErrorServer || kind == ErrorConnection || kind == ErrorInvalidOutput
	}
	return r.FailoverOn[kind]
}

func isRetryableKind(kind ErrorKind) bool {
	return kind == ErrorRateLimit || kind == ErrorTimeout || kind == ErrorServer || kind == ErrorConnection
}

func (r *Router) retryDelay(attempt int) time.Duration {
	if r.Backoff != nil {
		return r.Backoff(attempt)
	}
	base := time.Duration(attempt*attempt) * time.Second
	var random [2]byte
	if _, err := cryptorand.Read(random[:]); err == nil {
		return base + time.Duration(binary.BigEndian.Uint16(random[:])%501)*time.Millisecond
	}
	// Entropy failure must not prevent failover. Only jitter is omitted.
	return base
}

func (r *Router) emit(ctx context.Context, event RunEvent) {
	if r.Events == nil {
		return
	}
	event.RunID = r.RunID
	event.TurnID = r.TurnID
	event.Component = "model_router"
	_ = r.Events.Emit(ctx, event)
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
