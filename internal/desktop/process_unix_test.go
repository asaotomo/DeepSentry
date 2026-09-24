//go:build !windows

package desktop

import (
	"context"
	"testing"
	"time"
)

func TestCommandCancellationClosesInheritedPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := command(ctx, nil, "/bin/sh", "-c", "sleep 20 & wait")
	if err == nil {
		t.Fatal("cancelled command succeeded")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("descendant pipes blocked cancellation")
	}
}
