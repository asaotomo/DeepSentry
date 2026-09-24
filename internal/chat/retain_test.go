package chat

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestPruneFinishedJobsKeepsLiveAndLatest(t *testing.T) {
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.Local)
	old := now.Add(-8 * 24 * time.Hour)
	recent := now.Add(-24 * time.Hour)
	jobs := map[string]*Job{
		"live":    {ID: "live", Owner: "a", Status: "running", Updated: old},
		"latest":  {ID: "latest", Owner: "a", Status: "completed", Updated: old.Add(time.Hour)},
		"older":   {ID: "older", Owner: "a", Status: "completed", Updated: old},
		"recent":  {ID: "recent", Owner: "b", Status: "interrupted", Updated: recent},
		"ancient": {ID: "ancient", Owner: "b", Status: "completed", Updated: old},
	}
	if n := pruneFinishedJobs(jobs, now); n != 2 {
		t.Fatalf("removed %d jobs: %#v", n, jobs)
	}
	for _, id := range []string{"live", "latest", "recent"} {
		if jobs[id] == nil {
			t.Fatalf("kept job %s missing", id)
		}
	}
	if jobs["older"] != nil || jobs["ancient"] != nil {
		t.Fatalf("stale jobs stayed: %#v", jobs)
	}
}

func TestStaleChatSessionsKeepCurrentAndRecent(t *testing.T) {
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.Local)
	current := "session_chat_abcdabcdabcdabcd_g3"
	entries := []sessionDirInfo{
		{ID: current, ModTime: now.Add(-30 * 24 * time.Hour)},
		{ID: "session_chat_abcdabcdabcdabcd_g1", ModTime: now.Add(-30 * 24 * time.Hour)},
		{ID: "session_chat_abcdabcdabcdabcd_g2", ModTime: now.Add(-2 * 24 * time.Hour)},
		{ID: "session_1790840438712534000", ModTime: now.Add(-30 * 24 * time.Hour)},
	}
	stale := staleChatSessionIDs(entries, map[string]bool{current: true}, now)
	if len(stale) != 1 || stale[0] != "session_chat_abcdabcdabcdabcd_g1" {
		t.Fatalf("stale=%v", stale)
	}
}

func TestRotateDaemonLogKeepsTail(t *testing.T) {
	path := t.TempDir() + "/daemon.log"
	var b strings.Builder
	for i := 0; i < 8000; i++ {
		b.WriteString("2026/09/10 10:51:21 DeepSentry chat listening on 127.0.0.1:1\n")
	}
	b.WriteString("TAIL-MARKER listening once\n")
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rotateDaemonLog(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > daemonLogKeepBytes+80 || !strings.Contains(string(data), "TAIL-MARKER") {
		t.Fatalf("rotated log len=%d tail=%q", len(data), string(data[max(0, len(data)-80):]))
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > daemonLogMaxBytes {
		t.Fatalf("size=%v err=%v", info, err)
	}
}
