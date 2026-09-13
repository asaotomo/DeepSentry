package harness

import (
	"ai-edr/internal/executor"
	"ai-edr/internal/memory"
	"ai-edr/internal/ui"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func truncateContent(content string, total int) string {
	if len(content) <= maxFileReadDisplay {
		if total > len(content) {
			return content + fmt.Sprintf("\n...(内容已截断，共 %d 字节)...", total)
		}
		return content
	}
	return safeUTF8BytePrefix(content, maxFileReadDisplay) + fmt.Sprintf("\n...(内容已截断，共 %d 字节)...", total)
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
	content := string(data)
	if oldStr == "" {
		return "", fmt.Errorf("old_string 不能为空")
	}
	if !strings.Contains(content, oldStr) {
		return "", fmt.Errorf("未找到 old_string，文件未修改")
	}
	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		updated = strings.Replace(content, oldStr, newStr, 1)
	}
	if err := writeTargetOrLocalWithExecutor(path, []byte(updated), ex); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s已编辑 %s (%d -> %d 字节)", ui.Prefix("✅", "[OK]"), path, len(content), len(updated)), nil
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
