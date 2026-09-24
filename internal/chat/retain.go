package chat

import (
	"bytes"
	"io"
	"log"
	"os"
	"regexp"
	"time"

	"ai-edr/internal/harness"
)

const (
	chatHistoryKeep    = 7 * 24 * time.Hour
	chatPruneInterval  = 6 * time.Hour
	daemonLogMaxBytes  = 256 << 10
	daemonLogKeepBytes = 64 << 10
)

var chatSessionDirRE = regexp.MustCompile(`^session_chat_[0-9a-f]+_g\d+$`)

func jobStamp(j *Job) time.Time {
	if j == nil {
		return time.Time{}
	}
	if !j.Updated.IsZero() {
		return j.Updated
	}
	return j.Created
}

// pruneFinishedJobs drops finished tasks that have not been touched for
// chatHistoryKeep. The newest finished task for each owner stays, so the last
// result can still be fetched. Running, queued and cancelling tasks stay.
func pruneFinishedJobs(jobs map[string]*Job, now time.Time) int {
	latest := map[string]string{}
	latestAt := map[string]time.Time{}
	for _, j := range jobs {
		if j == nil || jobLive(j) {
			continue
		}
		at := jobStamp(j)
		if prev, ok := latestAt[j.Owner]; !ok || at.After(prev) {
			latest[j.Owner] = j.ID
			latestAt[j.Owner] = at
		}
	}
	n := 0
	for id, j := range jobs {
		if j == nil || jobLive(j) || latest[j.Owner] == id {
			continue
		}
		if now.Sub(jobStamp(j)) < chatHistoryKeep {
			continue
		}
		delete(jobs, id)
		n++
	}
	return n
}

type sessionDirInfo struct {
	ID      string
	ModTime time.Time
}

func staleChatSessionIDs(entries []sessionDirInfo, current map[string]bool, now time.Time) []string {
	var stale []string
	for _, entry := range entries {
		if !chatSessionDirRE.MatchString(entry.ID) || current[entry.ID] {
			continue
		}
		if !entry.ModTime.IsZero() && now.Sub(entry.ModTime) < chatHistoryKeep {
			continue
		}
		stale = append(stale, entry.ID)
	}
	return stale
}

func pruneChatSessions(current map[string]bool, now time.Time) (int, error) {
	root, err := harness.SessionRoot()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	infos := make([]sessionDirInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		infos = append(infos, sessionDirInfo{ID: entry.Name(), ModTime: info.ModTime()})
	}
	n := 0
	for _, id := range staleChatSessionIDs(infos, current, now) {
		if err := harness.RemoveSession(id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Service) currentSessionIDsLocked() map[string]bool {
	current := make(map[string]bool, len(s.sessions))
	for own, gen := range s.sessions {
		current[sessionIDFor(own, gen)] = true
	}
	return current
}

func (s *Service) pruneHistory(now time.Time) {
	s.mu.Lock()
	removedJobs := pruneFinishedJobs(s.jobs, now)
	var saveErr error
	if removedJobs > 0 {
		saveErr = s.saveLocked()
	}
	current := s.currentSessionIDsLocked()
	s.mu.Unlock()
	if saveErr != nil {
		log.Printf("chat history prune save failed: %v", saveErr)
	}
	var removedSessions int
	var err error
	if s.pruneSessions != nil {
		removedSessions, err = s.pruneSessions(current, now)
	}
	if err != nil && !os.IsNotExist(err) {
		log.Printf("chat session prune failed: %v", err)
	}
	if removedJobs > 0 || removedSessions > 0 {
		log.Printf("chat history closed %d old tasks and %d unused sessions", removedJobs, removedSessions)
	}
}

func (s *Service) pruneHistoryLoop() {
	tick := time.NewTicker(chatPruneInterval)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-tick.C:
			s.pruneHistory(now)
		}
	}
}

func rotateDaemonLog(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() <= daemonLogMaxBytes {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	keep := int64(daemonLogKeepBytes)
	if info.Size() < keep {
		keep = info.Size()
	}
	if _, err = f.Seek(info.Size()-keep, 0); err != nil {
		return err
	}
	buf := make([]byte, keep)
	if _, err = io.ReadFull(f, buf); err != nil {
		return err
	}
	if i := bytes.IndexByte(buf, '\n'); i >= 0 && i < len(buf)-1 {
		buf = buf[i+1:]
	}
	buf = bytes.TrimPrefix(buf, []byte{'\n'})
	if !bytes.HasSuffix(buf, []byte{'\n'}) {
		buf = append(buf, '\n')
	}
	tmp := path + ".rotate"
	if err = os.WriteFile(tmp, buf, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
