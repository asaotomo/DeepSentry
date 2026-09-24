package taskwait

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadWaitsForVerifiedFinalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.zip")
	os.WriteFile(path+".part", []byte("partial"), 0600)
	payload := []byte("download complete")
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	go func() {
		time.Sleep(120 * time.Millisecond)
		os.WriteFile(path+".part", payload, 0600)
		os.Rename(path+".part", path)
	}()
	r := Wait(context.Background(), Options{Mode: "file_ready", Path: path, SHA256: hash, Timeout: time.Second, Interval: 100 * time.Millisecond, Stable: 100 * time.Millisecond}, nil)
	if !r.Ready || r.Reason != "sha256_verified" || r.Checks < 2 {
		t.Fatalf("%+v", r)
	}
}
func TestTimeoutCancellationAndPartialFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	os.WriteFile(path, []byte("abc"), 0600)
	os.WriteFile(path+".crdownload", nil, 0600)
	o := Options{Mode: "file_ready", Path: path, ExpectedSize: 3, Timeout: 220 * time.Millisecond, Interval: 100 * time.Millisecond}
	if r := Wait(context.Background(), o, nil); r.Ready || r.Reason != "timeout" {
		t.Fatalf("partial download accepted or mislabeled: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := Wait(ctx, Options{Mode: "delay", Timeout: time.Hour}, nil); r.Ready || r.Reason != "cancelled" {
		t.Fatalf("cancel failed: %+v", r)
	}
	o.ExpectedSize = 0
	if Validate(o) == nil {
		t.Fatal("stable file treated as complete without evidence")
	}
}
