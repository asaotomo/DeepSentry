// Package taskwait waits without consuming model turns. File completion needs
// an expected size or checksum, not merely a file that stopped growing.
package taskwait

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type Options struct {
	Mode, Path, SHA256        string
	ExpectedSize              int64
	Timeout, Interval, Stable time.Duration
}
type Result struct {
	Ready          bool    `json:"ready"`
	Reason         string  `json:"reason"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	Checks         int     `json:"checks"`
	Size           int64   `json:"size,omitempty"`
}

func Validate(o Options) error {
	if o.Timeout <= 0 || o.Timeout > 24*time.Hour {
		return fmt.Errorf("timeout must be 1 second..24 hours")
	}
	if o.Mode == "delay" {
		return nil
	}
	if o.Mode != "file_exists" && o.Mode != "file_ready" {
		return fmt.Errorf("mode must be delay/file_exists/file_ready")
	}
	if o.Path == "" {
		return fmt.Errorf("path required")
	}
	if o.Mode == "file_ready" && o.ExpectedSize <= 0 && o.SHA256 == "" {
		return fmt.Errorf("file_ready requires expected_size or sha256; size stability alone cannot prove download completion")
	}
	if o.SHA256 != "" {
		b, e := hex.DecodeString(o.SHA256)
		if e != nil || len(b) != 32 {
			return fmt.Errorf("invalid sha256")
		}
	}
	if o.Interval < 100*time.Millisecond || o.Interval > time.Minute || o.Stable < 0 || o.Stable > time.Hour {
		return fmt.Errorf("invalid polling/stability interval")
	}
	return nil
}
func Wait(ctx context.Context, o Options, progress func(Result)) Result {
	start := time.Now()
	r := Result{}
	done := func(reason string, ready bool) Result {
		r.Reason = reason
		r.Ready = ready
		r.ElapsedSeconds = time.Since(start).Seconds()
		return r
	}
	if e := Validate(o); e != nil {
		return done(e.Error(), false)
	}
	if o.Mode == "delay" {
		t := time.NewTimer(o.Timeout)
		defer t.Stop()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return done("cancelled", false)
			case <-t.C:
				return done("delay_elapsed", true)
			case <-ticker.C:
				if progress != nil {
					progress(Result{Reason: "waiting", ElapsedSeconds: time.Since(start).Seconds(), Checks: r.Checks})
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	deadline := time.NewTimer(o.Timeout)
	defer deadline.Stop()
	tick := time.NewTicker(o.Interval)
	defer tick.Stop()
	var previous os.FileInfo
	var stableSince time.Time
	for {
		if err := ctx.Err(); err != nil {
			return done(waitStopReason(err), false)
		}
		r.Checks++
		info, err := os.Stat(o.Path)
		if err != nil && !os.IsNotExist(err) {
			return done(err.Error(), false)
		}
		if err == nil && info.Mode().IsRegular() {
			if o.Mode == "file_exists" {
				return done("file_exists_only", true)
			}
			blocked := false
			for _, suffix := range []string{".crdownload", ".part", ".download", ".tmp"} {
				if strings.HasSuffix(strings.ToLower(o.Path), suffix) {
					blocked = true
				}
				if _, e := os.Stat(o.Path + suffix); e == nil {
					blocked = true
				}
			}
			if blocked || previous == nil || !os.SameFile(previous, info) || previous.Size() != info.Size() || !previous.ModTime().Equal(info.ModTime()) {
				stableSince = time.Now()
			}
			previous = info
			r.Size = info.Size()
			if !blocked && time.Since(stableSince) >= o.Stable && (o.ExpectedSize <= 0 || info.Size() == o.ExpectedSize) {
				if o.SHA256 == "" {
					return done("expected_size_and_stability_verified", true)
				}
				hash, e := fileHash(ctx, o.Path)
				if e != nil {
					if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
						return done(waitStopReason(e), false)
					}
					return done(e.Error(), false)
				}
				after, e := os.Stat(o.Path)
				if e == nil && os.SameFile(info, after) && info.Size() == after.Size() && info.ModTime().Equal(after.ModTime()) {
					if strings.EqualFold(hash, o.SHA256) {
						return done("sha256_verified", true)
					}
					return done("checksum_mismatch", false)
				}
				stableSince = time.Now()
			}
		} else {
			previous = nil
			stableSince = time.Time{}
		}
		r.ElapsedSeconds = time.Since(start).Seconds()
		r.Reason = "waiting"
		if progress != nil {
			progress(r)
		}
		select {
		case <-ctx.Done():
			return done(waitStopReason(ctx.Err()), false)
		case <-deadline.C:
			return done("timeout", false)
		case <-tick.C:
		}
	}
}
func fileHash(ctx context.Context, path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		n, e := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func waitStopReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "cancelled"
}
