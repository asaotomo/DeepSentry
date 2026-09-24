package scheduler

import (
	"os"
	"testing"
	"time"
)

func TestAcquireRunLockIsExclusiveAndRecoversStaleLock(t *testing.T) {
	store := t.TempDir() + "/tasks.json"
	now := time.Now()
	release, acquired, err := acquireRunLock(store, now)
	if err != nil || !acquired {
		t.Fatalf("first lock: acquired=%v err=%v", acquired, err)
	}
	defer release()

	_, acquired, err = acquireRunLock(store, now)
	if err != nil || acquired {
		t.Fatalf("second process must not acquire live lock: acquired=%v err=%v", acquired, err)
	}
	release()

	lockPath := store + ".run.lock"
	if err := os.WriteFile(lockPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-staleRunLockAfter - time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	releaseStale, acquired, err := acquireRunLock(store, now)
	if err != nil || !acquired {
		t.Fatalf("stale lock should be recovered: acquired=%v err=%v", acquired, err)
	}
	releaseStale()
}
