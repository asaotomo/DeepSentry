package scheduler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	Path string
	mu   sync.Mutex
}

var storeFileMu sync.Mutex

func NewStore(path string) *Store {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultStorePath
	}
	return &Store{Path: path}
}

func (s *Store) Load() ([]Task, error) {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, lockErr := s.lockStore()
	if lockErr != nil {
		err := lockErr
		return nil, err
	}
	defer release()
	return s.loadUnlocked()
}

func (s *Store) Save(tasks []Task) error {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, lockErr := s.lockStore()
	if lockErr != nil {
		err := lockErr
		return err
	}
	defer release()
	return s.saveUnlocked(tasks)
}

func (s *Store) Add(task Task) error {
	_, created, err := s.AddUnique(task)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("等价定时任务已存在")
	}
	return nil
}

// AddUnique makes schedule creation idempotent. Re-submitting the same prompt
// and schedule returns the existing task instead of writing another job.
func (s *Store) AddUnique(task Task) (existing Task, created bool, err error) {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, lockErr := s.lockStore()
	if lockErr != nil {
		err := lockErr
		return Task{}, false, err
	}
	defer release()
	tasks, err := s.loadUnlocked()
	if err != nil {
		return Task{}, false, err
	}
	for _, existing := range tasks {
		if existing.ID == task.ID {
			if existing.ReplySession != task.ReplySession || existing.ReplyText != task.ReplyText {
				return Task{}, false, fmt.Errorf("任务 ID 已被不同收件会话或内容使用")
			}
			return existing, false, nil
		}
	}
	for _, existing := range tasks {
		if equivalentTask(existing, task) {
			return existing, false, nil
		}
	}
	tasks = append(tasks, task)
	if err := s.saveUnlocked(tasks); err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func equivalentTask(a, b Task) bool {
	normalize := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return normalize(a.Prompt) == normalize(b.Prompt) &&
		a.Kind == b.Kind && a.RunAt.Equal(b.RunAt) && a.Timezone == b.Timezone &&
		a.Repeat == b.Repeat && a.Weekday == b.Weekday && a.IntervalSec == b.IntervalSec &&
		normalize(a.Selector) == normalize(b.Selector) && normalize(a.Notify) == normalize(b.Notify) &&
		a.AllowBatch == b.AllowBatch && a.TimeoutSec == b.TimeoutSec &&
		a.ReplySession == b.ReplySession && a.ReplyText == b.ReplyText
}

func (s *Store) Remove(id string) (Task, bool, error) {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, lockErr := s.lockStore()
	if lockErr != nil {
		err := lockErr
		return Task{}, false, err
	}
	defer release()
	tasks, err := s.loadUnlocked()
	if err != nil {
		return Task{}, false, err
	}
	var removed Task
	kept := tasks[:0]
	found := false
	for _, task := range tasks {
		if task.ID == id {
			removed = task
			found = true
			continue
		}
		kept = append(kept, task)
	}
	if !found {
		return Task{}, false, nil
	}
	return removed, true, s.saveUnlocked(kept)
}

func (s *Store) Update(task Task) error {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, lockErr := s.lockStore()
	if lockErr != nil {
		err := lockErr
		return err
	}
	defer release()
	tasks, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == task.ID {
			tasks[i] = task
			return s.saveUnlocked(tasks)
		}
	}
	return fmt.Errorf("未找到任务: %s", task.ID)
}

func (s *Store) Due(now time.Time) ([]Task, error) {
	tasks, err := s.Load()
	if err != nil {
		return nil, err
	}
	var due []Task
	for _, task := range tasks {
		if task.Status != StatusEnabled {
			continue
		}
		if !task.RunAt.After(now) {
			due = append(due, task)
		}
	}
	sortTasks(due)
	return due, nil
}

func (s *Store) loadUnlocked() ([]Task, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("读取定时任务失败: %w", err)
	}
	sortTasks(tasks)
	return tasks, nil
}

func (s *Store) saveUnlocked(tasks []Task) error {
	sortTasks(tasks)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(s.Path), "tasks-*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer os.Remove(tmp)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(append(data, '\n')); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, s.Path)
}

func sortTasks(tasks []Task) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].RunAt.Equal(tasks[j].RunAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].RunAt.Before(tasks[j].RunAt)
	})
}

// CompleteRun preserves a pause/removal requested while the job was running.
func (s *Store) CompleteRun(task Task) error {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockStore()
	if err != nil {
		return err
	}
	defer release()
	tasks, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == task.ID {
			if tasks[i].Status != StatusRunning {
				task.Status = tasks[i].Status
			}
			tasks[i] = task
			return s.saveUnlocked(tasks)
		}
	}
	return nil // A removed task must not be resurrected.
}
func (s *Store) SetStatus(id, status string) error {
	if status == StatusEnabled {
		sum := sha256.Sum256([]byte(id))
		release, ok, err := acquireRunLock(fmt.Sprintf("%s.%x", s.Path, sum[:8]), time.Now())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("任务仍在运行，不能 resume 或重放")
		}
		defer release()
	}
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockStore()
	if err != nil {
		return err
	}
	defer release()
	tasks, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			if status == StatusEnabled && tasks[i].Repeat == RepeatOnce && (tasks[i].RunCount > 0 || tasks[i].LastRunAt != nil) {
				return fmt.Errorf("一次性任务已经开始过，resume 不会重放；请核对实际结果后新建任务")
			}
			tasks[i].Status = status
			tasks[i].UpdatedAt = time.Now()
			if status == StatusEnabled {
				tasks[i].FailureCount = 0
			}
			return s.saveUnlocked(tasks)
		}
	}
	return fmt.Errorf("未找到任务: %s", id)
}
func (s *Store) lockStore() (func(), error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, ok, err := acquireRunLock(s.Path+".store", time.Now())
		if err != nil {
			return nil, err
		}
		if ok {
			return release, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("任务存储被其他进程占用")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *Store) Claim(id string, now time.Time, manual bool) (Task, bool, error) {
	storeFileMu.Lock()
	defer storeFileMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lockStore()
	if err != nil {
		return Task{}, false, err
	}
	defer release()
	tasks, err := s.loadUnlocked()
	if err != nil {
		return Task{}, false, err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			task := tasks[i]
			if task.Status == StatusRunning {
				return task, false, fmt.Errorf("上次运行尚未记录完成；核验副作用后 pause/resume，不盲目重放")
			}
			if !manual && (task.Status != StatusEnabled || task.RunAt.After(now)) {
				return task, false, nil
			}
			task.Status = StatusRunning
			task.UpdatedAt = now
			// Persist the attempt before executing any side effects. A crash must
			// not make an interrupted one-shot look like it has never started.
			task.LastRunAt = &now
			task.RunCount++
			tasks[i] = task
			if err = s.saveUnlocked(tasks); err != nil {
				return Task{}, false, err
			}
			return task, true, nil
		}
	}
	return Task{}, false, fmt.Errorf("未找到任务: %s", id)
}
