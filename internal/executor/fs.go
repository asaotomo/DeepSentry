package executor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxReadSize = 2 << 20 // 2MB

// DirEntry 目录项（统一本地/远程）
type DirEntry struct {
	Name    string
	Size    int64
	IsDir   bool
	Mode    os.FileMode
	ModTime time.Time
}

// WriteTargetFile 写入目标文件（本地、远程 SFTP 或 FTP STOR）
func WriteTargetFile(path string, content []byte) error {
	if Current == nil {
		return fmt.Errorf("执行器未初始化")
	}
	return WriteFileWithExecutor(Current, path, content)
}

func WriteFileWithExecutor(ex Executor, path string, content []byte) error {
	if ex == nil {
		return fmt.Errorf("执行器未初始化")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("path 不能为空")
	}

	if se, ok := ex.(*SSHExecutor); ok && ex.IsRemote() {
		return se.writeTargetFile(path, content)
	}
	if fe, ok := ex.(*FTPExecutor); ok && ex.IsRemote() {
		return fe.writeTargetFile(path, content)
	}
	if _, ok := ex.(*LocalExecutor); !ok && ex.IsRemote() {
		return fmt.Errorf("%s 模式暂不支持写入目标文件", CurrentModeOf(ex))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeLocalFileSecureDefault(path, content)
}

func writeLocalFileSecureDefault(path string, content []byte) error {
	// New Agent-created files are private, while an existing file keeps its
	// operator-selected mode. Silently chmodding an existing web/config file can
	// break a service that intentionally reads it through another account.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *SSHExecutor) writeTargetFile(path string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sftpClient != nil {
		dir := filepath.Dir(path)
		_ = s.sftpClient.MkdirAll(dir)
		f, err := s.sftpClient.Create(path)
		if err == nil {
			defer f.Close()
			_, werr := f.Write(content)
			return werr
		}
	}
	// fallback: base64 heredoc via shell (minimal systems)
	tmp := fmt.Sprintf("/tmp/deepsentry_write_%d", time.Now().UnixNano())
	quoted := shellQuotePath(path)
	// use printf for small files only
	if len(content) > 65536 {
		return fmt.Errorf("SFTP 不可用且文件过大，无法写入 %s", path)
	}
	escaped := strings.ReplaceAll(string(content), `'`, `'\''`)
	cmd := fmt.Sprintf("printf '%%s' '%s' > %s", escaped, shellQuotePath(tmp))
	if _, err := s.runLocked(cmd); err != nil {
		return err
	}
	_, err := s.runLocked(fmt.Sprintf("mv %s %s", shellQuotePath(tmp), quoted))
	return err
}

// ReadTargetEntries 列出目标目录
func ReadTargetEntries(path string) ([]DirEntry, error) {
	if Current == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	return ReadEntriesWithExecutor(Current, path)
}

func ReadEntriesWithExecutor(ex Executor, path string) ([]DirEntry, error) {
	if ex == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	if path == "" {
		path = "."
	}

	switch e := ex.(type) {
	case *LocalExecutor:
		return readLocalEntries(path)
	case *SSHExecutor:
		return e.readTargetEntries(path)
	case *TelnetExecutor, *FTPExecutor:
		names, err := ex.ListTargetDir(path)
		if err != nil {
			return nil, err
		}
		out := make([]DirEntry, 0, len(names))
		for _, name := range names {
			out = append(out, DirEntry{Name: name})
		}
		return out, nil
	default:
		return readLocalEntries(path)
	}
}

func readLocalEntries(path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]DirEntry, 0, len(entries))
	for _, ent := range entries {
		info, err := ent.Info()
		if err != nil {
			result = append(result, DirEntry{Name: ent.Name(), IsDir: ent.IsDir()})
			continue
		}
		result = append(result, DirEntry{
			Name:    ent.Name(),
			Size:    info.Size(),
			IsDir:   ent.IsDir(),
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
		})
	}
	return result, nil
}

func (s *SSHExecutor) readTargetEntries(path string) ([]DirEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sftpClient != nil {
		entries, err := s.sftpClient.ReadDir(path)
		if err == nil {
			return sftpEntriesToDir(entries), nil
		}
	}
	out, err := s.runLocked(fmt.Sprintf("ls -la %s 2>/dev/null", shellQuotePath(path)))
	if err != nil {
		return nil, err
	}
	return parseLsOutput(out), nil
}

func sftpEntriesToDir(entries []os.FileInfo) []DirEntry {
	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, DirEntry{
			Name:    e.Name(),
			Size:    e.Size(),
			IsDir:   e.IsDir(),
			Mode:    e.Mode(),
			ModTime: e.ModTime(),
		})
	}
	return result
}

func parseLsOutput(out string) []DirEntry {
	var result []DirEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		name := strings.Join(fields[8:], " ")
		if name == "." || name == ".." {
			continue
		}
		isDir := strings.HasPrefix(fields[0], "d")
		var size int64
		fmt.Sscanf(fields[4], "%d", &size)
		result = append(result, DirEntry{Name: name, Size: size, IsDir: isDir})
	}
	return result
}

// GrepTargetFile 在目标文件中搜索（Go 原生，不依赖 grep 命令）
func GrepTargetFile(path, pattern string, maxLines int) (string, error) {
	if Current == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	return GrepFileWithExecutor(Current, path, pattern, maxLines)
}

func GrepFileWithExecutor(ex Executor, path, pattern string, maxLines int) (string, error) {
	if ex == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if maxLines <= 0 {
		maxLines = 100
	}
	data, err := ex.ReadTargetFile(path)
	if err != nil {
		return "", err
	}
	var matches []string
	for i, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, pattern) {
			matches = append(matches, fmt.Sprintf("%d:%s", i+1, line))
			if len(matches) >= maxLines {
				break
			}
		}
	}
	if len(matches) == 0 {
		return "(无匹配)", nil
	}
	return strings.Join(matches, "\n"), nil
}

func ReadFileWithExecutor(ex Executor, path string) ([]byte, error) {
	if ex == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	if _, ok := ex.(*LocalExecutor); ok {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxReadSize))
	}
	return ex.ReadTargetFile(path)
}

func CurrentModeOf(ex Executor) string {
	if ex == nil {
		return "unknown"
	}
	if m, ok := ex.(ModeReporter); ok {
		return m.Mode()
	}
	if ex.IsRemote() {
		return "remote"
	}
	return "local"
}

// GlobTarget 在目标文件系统上 glob 匹配（** 支持有限：仅单段 * 与 **）
func GlobTarget(root, pattern string, maxResults int) ([]string, error) {
	if Current == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	return GlobTargetWithExecutor(Current, root, pattern, maxResults)
}

func GlobTargetWithExecutor(ex Executor, root, pattern string, maxResults int) ([]string, error) {
	if ex == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	if maxResults <= 0 {
		maxResults = 200
	}
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("pattern 不能为空")
	}

	var matches []string
	if ex.IsRemote() {
		matches = globRemote(ex, root, pattern, maxResults)
	} else {
		matches = globLocal(root, pattern, maxResults)
	}
	return matches, nil
}

func globLocal(root, pattern string, max int) []string {
	var matches []string
	fullPattern := filepath.Join(root, pattern)
	base := filepath.Dir(fullPattern)
	globPat := filepath.Base(fullPattern)

	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(matches) >= max {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		if rel == "." {
			return nil
		}
		matched, _ := filepath.Match(globPat, rel)
		if !matched {
			matched, _ = filepath.Match(globPat, filepath.Base(path))
		}
		if matched {
			matches = append(matches, path)
		}
		return nil
	})
	return matches
}

func globRemote(ex Executor, root, pattern string, max int) []string {
	// 无通配符时仅扫描当前目录，避免 /proc 等深层目录递归爆炸
	if !strings.ContainsAny(pattern, "*?[") {
		var matches []string
		entries, err := ReadEntriesWithExecutor(ex, root)
		if err != nil {
			return matches
		}
		for _, e := range entries {
			if e.Name == pattern || strings.Contains(e.Name, strings.Trim(pattern, "*")) {
				matches = append(matches, filepath.Join(root, e.Name))
			}
			if len(matches) >= max {
				break
			}
		}
		return matches
	}
	var matches []string
	globRemoteWalk(ex, root, pattern, &matches, max, 0, 4)
	return matches
}

func globRemoteWalk(ex Executor, dir, pattern string, matches *[]string, max, depth, maxDepth int) {
	if len(*matches) >= max || depth > maxDepth {
		return
	}
	entries, err := ReadEntriesWithExecutor(ex, dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if len(*matches) >= max {
			return
		}
		full := filepath.Join(dir, e.Name)
		base := filepath.Base(full)
		matched, _ := filepath.Match(pattern, base)
		if matched {
			*matches = append(*matches, full)
		}
		if e.IsDir && depth < maxDepth {
			globRemoteWalk(ex, full, pattern, matches, max, depth+1, maxDepth)
		}
	}
}

// ReadTargetFileLimited 带大小限制的读取
func ReadTargetFileLimited(path string, limit int64) ([]byte, error) {
	if Current == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	if limit <= 0 {
		limit = maxReadSize
	}
	data, err := Current.ReadTargetFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return data[:limit], nil
	}
	return data, nil
}

// ReadLocalFile 读取控制端本地文件（workspace / AGENTS.md）
func ReadLocalFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxReadSize))
}

// WriteLocalFile 写入控制端本地文件
func WriteLocalFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeLocalFileSecureDefault(path, content)
}
