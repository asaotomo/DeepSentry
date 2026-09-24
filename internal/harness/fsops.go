package harness

import (
	"ai-edr/internal/executor"
	"ai-edr/internal/memory"
	"ai-edr/internal/ui"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxFileReadDisplay = 8000

func fsPerspectiveForExecutor(local bool, ex executor.Executor) string {
	if local {
		return "controller"
	}
	if ex != nil && ex.IsRemote() {
		return "target"
	}
	return "local"
}

func isControllerLocalPath(path string) bool {
	path = expandUserPath(path)
	if memory.IsAgentsMDPath(path) {
		return true
	}
	home, _ := os.UserHomeDir()
	workspace := filepath.Join(home, ".deepsentry", "workspace")
	abs, err := filepath.Abs(path)
	if err != nil {
		return strings.Contains(path, ".deepsentry/workspace") || strings.Contains(path, ".deepsentry\\workspace")
	}
	wsAbs, _ := filepath.Abs(workspace)
	return strings.HasPrefix(abs, wsAbs+string(os.PathSeparator)) || abs == wsAbs
}

func expandUserPath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func readTargetOrLocalWithExecutor(path string, ex executor.Executor) ([]byte, error) {
	path = expandUserPath(path)
	if isControllerLocalPath(path) {
		if !memory.IsAgentsMDPath(path) {
			return readWorkspaceFile(path)
		}
		return executor.ReadLocalFile(path)
	}
	if isReadableReportArtifact(path) {
		return readLocalReportArtifact(path)
	}
	if ex == nil {
		return nil, fmt.Errorf("执行器未初始化")
	}
	return executor.ReadFileWithExecutor(ex, path)
}

func readLocalReportArtifact(path string) ([]byte, error) {
	if !isReadableReportArtifact(path) {
		return nil, fmt.Errorf("禁止读取受保护路径")
	}
	abs, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("报告文件不可读取")
	}
	return io.ReadAll(io.LimitReader(f, 2<<20))
}

func writeTargetOrLocalWithExecutor(path string, content []byte, ex executor.Executor) error {
	path = expandUserPath(path)
	if isControllerLocalPath(path) {
		if !memory.IsAgentsMDPath(path) {
			return writeWorkspaceFile(path, content)
		}
		return executor.WriteLocalFile(path, content)
	}
	return executor.WriteFileWithExecutor(ex, path, content)
}

func workspaceRootAndRelative(path string) (*os.Root, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	workspace := filepath.Join(home, ".deepsentry", "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, "", err
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, "", err
	}
	pathAbs, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(workspaceAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, "", fmt.Errorf("控制端路径必须位于 %s", workspaceAbs)
	}
	root, err := os.OpenRoot(workspaceAbs)
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

func readWorkspaceFile(path string) ([]byte, error) {
	root, rel, err := workspaceRootAndRelative(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("workspace 安全读取失败: %w", err)
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 2<<20))
}

func writeWorkspaceFile(path string, content []byte) error {
	root, rel, err := workspaceRootAndRelative(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("workspace 安全创建目录失败: %w", err)
		}
	}
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("workspace 安全写入失败: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readWorkspaceDir(path string) ([]os.DirEntry, error) {
	root, rel, err := workspaceRootAndRelative(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("workspace 安全列目录失败: %w", err)
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func globWorkspace(path, pattern string, maxResults int) ([]string, error) {
	if maxResults <= 0 {
		maxResults = 200
	}
	root, baseRel, err := workspaceRootAndRelative(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	basePath, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return nil, err
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("glob_pattern 不能为空")
	}
	var matches []string
	var walk func(string, string, int)
	walk = func(dirRel, displayRel string, depth int) {
		if len(matches) >= maxResults || depth > 8 {
			return
		}
		f, openErr := root.Open(dirRel)
		if openErr != nil {
			return
		}
		entries, readErr := f.ReadDir(-1)
		_ = f.Close()
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			if len(matches) >= maxResults {
				return
			}
			rel := filepath.Join(displayRel, entry.Name())
			matched, _ := filepath.Match(pattern, rel)
			if !matched {
				matched, _ = filepath.Match(pattern, entry.Name())
			}
			if !matched && strings.HasPrefix(pattern, "**"+string(os.PathSeparator)) {
				matched, _ = filepath.Match(strings.TrimPrefix(pattern, "**"+string(os.PathSeparator)), rel)
			}
			if matched {
				matches = append(matches, filepath.Join(basePath, rel))
			}
			// DirEntry.IsDir is false for symlinks, so traversal cannot cross a
			// link even before os.Root applies its confinement.
			if entry.IsDir() {
				walk(filepath.Join(dirRel, entry.Name()), rel, depth+1)
			}
		}
	}
	walk(baseRel, "", 0)
	return matches, nil
}

func formatFSResult(perspective, body string) string {
	tag := perspective
	switch perspective {
	case "target":
		tag = "目标机"
	case "controller":
		tag = "控制端"
	case "local":
		tag = "本地"
	}
	return fmt.Sprintf("[视角: %s]\n%s", tag, body)
}

func formatDirListing(path string, entries []executor.DirEntry) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("total %d\n", len(entries)))
	for _, e := range entries {
		prefix := "-"
		if e.IsDir {
			prefix = "d"
		}
		b.WriteString(fmt.Sprintf("%s %8d %s\n", prefix, e.Size, e.Name))
	}
	return b.String()
}

func editFileContentWithExecutor(path, oldStr, newStr string, replaceAll bool, ex executor.Executor) (string, error) {
	data, err := readTargetOrLocalWithExecutor(path, ex)
	if err != nil {
		return "", err
	}
	updated, summary, err := applyCodeEdit(string(data), oldStr, newStr, replaceAll)
	if err != nil {
		return "", err
	}
	if err := writeTargetOrLocalWithExecutor(path, []byte(updated), ex); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s已编辑 %s\n%s", ui.Prefix("✅", "[OK]"), path, summary), nil
}

// applyCodeEdit requires a unique match unless replaceAll is set. Matching
// ignores CRLF versus LF differences and writes back the file's original
// newline style, so a Windows checkout does not make every edit miss.
func applyCodeEdit(content, oldStr, newStr string, replaceAll bool) (string, string, error) {
	if oldStr == "" {
		return "", "", fmt.Errorf("old_string 不能为空")
	}
	file, old, newStrNorm, newline := normalizeEditNewlines(content, oldStr, newStr)
	if old == newStrNorm {
		return "", "", fmt.Errorf("old_string 与 new_string 相同，文件未修改")
	}
	count := strings.Count(file, old)
	if count == 0 {
		return "", "", fmt.Errorf("%s", editMissHint(file, old))
	}
	if count > 1 && !replaceAll {
		return "", "", fmt.Errorf("%s", ambiguousEditHint(file, old, count))
	}
	updated := file
	if replaceAll {
		updated = strings.ReplaceAll(file, old, newStrNorm)
	} else {
		updated = strings.Replace(file, old, newStrNorm, 1)
	}
	if newline == "\r\n" {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	return updated, editSummary(file, strings.ReplaceAll(updated, "\r\n", "\n"), count), nil
}

func normalizeEditNewlines(content, oldStr, newStr string) (file, old, newNorm, newline string) {
	newline = "\n"
	if strings.Contains(content, "\r\n") && !strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
		newline = "\r\n"
	}
	return strings.ReplaceAll(content, "\r\n", "\n"), strings.ReplaceAll(oldStr, "\r\n", "\n"), strings.ReplaceAll(newStr, "\r\n", "\n"), newline
}

func editMissHint(file, old string) string {
	msg := "未找到 old_string，文件未修改。请按 grep 给出的行号用 read_file offset 读取原文，再复制足够上下文重试；不要改用整文件 write_file 覆盖。"
	needle := ""
	for _, line := range strings.Split(old, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > len(needle) {
			needle = line
		}
	}
	if len([]rune(needle)) > 80 {
		needle = string([]rune(needle)[:80])
	}
	if needle == "" {
		return msg
	}
	var hits []string
	for i, line := range strings.Split(file, "\n") {
		if strings.Contains(line, needle) {
			hits = append(hits, fmt.Sprintf("%d|%s", i+1, trimEditLine(line)))
			if len(hits) == 3 {
				break
			}
		}
	}
	if len(hits) == 0 {
		return msg
	}
	return msg + "\n可能相关的行:\n" + strings.Join(hits, "\n")
}

func ambiguousEditHint(file, old string, count int) string {
	var hits []string
	rest := file
	lineBase := 1
	for len(hits) < 5 {
		idx := strings.Index(rest, old)
		if idx < 0 {
			break
		}
		line := lineBase + strings.Count(rest[:idx], "\n")
		hits = append(hits, strconv.Itoa(line))
		advance := idx + len(old)
		lineBase += strings.Count(rest[:advance], "\n")
		rest = rest[advance:]
	}
	return fmt.Sprintf("old_string 匹配了 %d 处（行 %s），文件未修改。请加入前后独有上下文，或在确认每一处都应替换时设置 replace_all=true。", count, strings.Join(hits, ", "))
}

func editSummary(before, after string, replacements int) string {
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")
	start := 0
	for start < len(b) && start < len(a) && b[start] == a[start] {
		start++
	}
	return fmt.Sprintf("替换 %d 处，变更从第 %d 行开始（原 %d 行，现 %d 行）。用编译、测试或 grep 验证，不要为了确认再整文件重读。", replacements, start+1, len(b), len(a))
}

func trimEditLine(line string) string {
	runes := []rune(line)
	if len(runes) <= 160 {
		return line
	}
	return string(runes[:160]) + "..."
}

// formatFileForModel returns the whole small file unchanged. Larger files and
// explicit windows are numbered so the next read_file can continue at offset.
func formatFileForModel(content string, offset, limit int) string {
	lines := strings.Split(content, "\n")
	total := len(lines)
	if offset <= 0 {
		offset = 1
	}
	if offset > total {
		return fmt.Sprintf("offset %d 超出文件，共 %d 行", offset, total)
	}
	if limit <= 0 && offset == 1 && len(content) <= maxFileReadDisplay {
		return content
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 400 {
		limit = 400
	}
	start := offset - 1
	end := start
	used := 0
	for end < total && end-start < limit && used < maxFileReadDisplay {
		used += len(lines[end]) + 1
		end++
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%d|%s\n", i+1, lines[i])
	}
	if end < total {
		fmt.Fprintf(&b, "...(已显示第 %d-%d 行，共 %d 行；继续 read_file 时设置 offset=%d)...", start+1, end, total, end+1)
	} else {
		fmt.Fprintf(&b, "...(第 %d-%d 行，文件结束，共 %d 行)...", start+1, end, total)
	}
	return b.String()
}

func grepLocalTree(root, pattern string, maxLines int) (string, error) {
	if maxLines <= 0 {
		maxLines = 80
	}
	if maxLines > 200 {
		maxLines = 200
	}
	rootDir, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer rootDir.Close()
	var matches []string
	files := 0
	truncated := false
	err = fs.WalkDir(rootDir.FS(), ".", func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (name == ".git" || name == "node_modules" || name == "vendor" || name == "dist") {
				return filepath.SkipDir
			}
			if strings.Count(path, "/") > 6 {
				return filepath.SkipDir
			}
			return nil
		}
		if files >= 300 || len(matches) >= maxLines {
			truncated = true
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() {
			return nil
		}
		files++
		info, err := d.Info()
		if err != nil || info.Size() > 512*1024 || info.Size() == 0 {
			return nil
		}
		f, err := rootDir.Open(path)
		if err != nil {
			return nil
		}
		opened, err := f.Stat()
		if err != nil || !opened.Mode().IsRegular() {
			f.Close()
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(f, 512*1024))
		f.Close()
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		rel := filepath.FromSlash(path)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, pattern) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, trimEditLine(line)))
				if len(matches) >= maxLines {
					truncated = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return "", err
	}
	if len(matches) == 0 {
		return "(无匹配)", nil
	}
	out := strings.Join(matches, "\n")
	if truncated {
		out += "\n...(结果已截断，请缩小目录或使用更具体的 pattern)..."
	}
	return out, nil
}

func maybeReloadAgentsMD(store *memory.Store, path string, content []byte) {
	if store == nil {
		return
	}
	path = expandUserPath(path)
	if memory.IsAgentsMDPath(path) {
		store.UpdateAgentsMD(path, string(content))
	}
}
