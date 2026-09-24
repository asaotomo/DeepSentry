package chat

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type platformError struct {
	Message              string
	Retryable, Uncertain bool
	RetryAfter           time.Duration
}

func (e *platformError) Error() string { return e.Message }
func responseError(r *http.Response) error {
	d := time.Duration(0)
	if n, err := strconv.Atoi(r.Header.Get("Retry-After")); err == nil && n > 0 {
		d = time.Duration(min(n, 3600)) * time.Second
	} else if t, err := http.ParseTime(r.Header.Get("Retry-After")); err == nil {
		d = min(time.Until(t), time.Hour)
	}
	return &platformError{Message: fmt.Sprintf("平台返回 HTTP %d", r.StatusCode), Retryable: r.StatusCode == 429 || r.StatusCode >= 500, RetryAfter: d}
}

func preflightError(err error) error {
	var pe *platformError
	if errors.As(err, &pe) && pe.Uncertain {
		return &platformError{Message: "获取平台令牌时网络中断，将重试", Retryable: true}
	}
	return err
}
