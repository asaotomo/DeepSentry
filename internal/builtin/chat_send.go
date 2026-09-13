package builtin

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const chatSendDirEnv = "DEEPSENTRY_CHAT_SEND_DIR"
const maxChatSendBytes = 20 << 20

// ChatSendFile copies a local controller file into the chat outbound directory.
func ChatSendFile(path, name string) (string, error) {
	dir := strings.TrimSpace(os.Getenv(chatSendDirEnv))
	if dir == "" {
		return "", fmt.Errorf("当前不是聊天机器人任务，无法把文件发到聊天；请在机器人对话里提出发送文件")
	}
	abs, err := filepath.Abs(expandChatLocalPath(path))
	if err != nil {
		return "", fmt.Errorf("文件路径无效")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("找不到本机文件 %s", abs)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("请指定具体文件，而不是目录：%s", abs)
	}
	if info.Size() <= 0 || info.Size() > maxChatSendBytes {
		return "", fmt.Errorf("发送文件大小须在 1 字节到 20 MiB 之间")
	}
	src, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("无法读取本机文件 %s", abs)
	}
	defer src.Close()
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(abs)
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(name)
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".sent") {
		name = "file-" + name
	}
	if name == "." || name == ".." || name == "" {
		name = "file.bin"
	}
	// #nosec G703 -- dir is the task-owned outbound directory supplied by ProcessRunner, not the tool filename.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, name)
	if rel, err := filepath.Rel(dir, dest); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("文件名无效")
	}
	tmp, err := os.CreateTemp(dir, ".send-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	written, copyErr := io.Copy(tmp, io.LimitReader(src, maxChatSendBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || written == 0 || written > maxChatSendBytes {
		// #nosec G703 -- tmpName is returned by os.CreateTemp in the task-owned directory.
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("复制待发送文件失败")
	}
	// Hard-link publication is atomic and never overwrites another queued file.
	original := name
	for n := 1; ; n++ {
		err = os.Link(tmpName, dest)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			// #nosec G703 -- tmpName is returned by os.CreateTemp in the task-owned directory.
			_ = os.Remove(tmpName)
			return "", err
		}
		ext := filepath.Ext(original)
		name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(original, ext), n, ext)
		dest = filepath.Join(dir, name)
	}
	// #nosec G703 -- tmpName is returned by os.CreateTemp in the task-owned directory.
	_ = os.Remove(tmpName)
	return fmt.Sprintf("已排队发送到当前聊天：%s（%d 字节）。任务结束后会上传给用户。", name, written), nil
}

func expandChatLocalPath(path string) string {
	path = strings.TrimSpace(path)
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	if path == "桌面" {
		return filepath.Join(home, "Desktop")
	}
	if strings.HasPrefix(path, "桌面/") || strings.HasPrefix(path, `桌面\`) {
		return filepath.Join(home, "Desktop", path[len("桌面/"):])
	}
	return path
}
