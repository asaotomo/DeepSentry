package tui

import (
	"ai-edr/internal/analyzer"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	tea "github.com/charmbracelet/bubbletea"
)

type imageAttachResultMsg struct {
	attachment analyzer.ImageAttachment
	source     string
	err        error
}

type clipboardPasteResultMsg struct {
	attachment analyzer.ImageAttachment
	text       string
	err        error
}

func attachImageCmd(path, sessionID string) tea.Cmd {
	return func() tea.Msg {
		path = strings.Trim(strings.TrimSpace(path), "\"'")
		source := "文件"
		clipboardPath := false
		if path == "" {
			source = "剪贴板"
			var err error
			path, err = saveClipboardImage(sessionID)
			if err != nil {
				return imageAttachResultMsg{source: source, err: err}
			}
			clipboardPath = true
		}
		attachment, err := analyzer.PrepareImageAttachment(path)
		if err != nil && clipboardPath {
			_ = os.Remove(path)
		}
		return imageAttachResultMsg{attachment: attachment, source: source, err: err}
	}
}

// pasteClipboardCmd gives Ctrl+V native editor semantics with image priority:
// a clipboard image becomes an attachment chip; otherwise clipboard text is
// inserted at the current cursor. This avoids forcing users through /image and
// also avoids breaking ordinary text paste when no image flavor is present.
func pasteClipboardCmd(sessionID string) tea.Cmd {
	return func() tea.Msg {
		return resolveClipboardPaste(
			sessionID,
			saveClipboardImage,
			readClipboardText,
			analyzer.PrepareImageAttachment,
		)
	}
}

// pasteClipboardImageOnlyCmd is used for macOS Command+V. The terminal still
// pastes text itself; we only steal the chord when the clipboard is an image,
// otherwise Command+V would duplicate ordinary text.
func pasteClipboardImageOnlyCmd(sessionID string) tea.Cmd {
	return func() tea.Msg {
		path, err := saveClipboardImage(sessionID)
		if err != nil {
			return macosCmdVIgnoredMsg{}
		}
		attachment, err := analyzer.PrepareImageAttachment(path)
		if err != nil {
			_ = os.Remove(path)
			return macosCmdVIgnoredMsg{}
		}
		return clipboardPasteResultMsg{attachment: attachment}
	}
}

func looksLikeLocalImagePath(text string) (string, bool) {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, `"'`)
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, "\n\r\t") {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(text), "file://") {
		text = strings.TrimPrefix(text, "file://")
		text = strings.TrimPrefix(text, "localhost")
		if decoded := unescapeFileURLPath(text); decoded != "" {
			text = decoded
		}
	}
	if !filepath.IsAbs(text) {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(text)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
	default:
		return "", false
	}
	stat, err := os.Stat(text)
	if err != nil || stat.IsDir() {
		return "", false
	}
	return text, true
}

func unescapeFileURLPath(path string) string {
	return strings.NewReplacer("%20", " ", "%2F", "/", "%2f", "/").Replace(path)
}

func resolveClipboardPaste(
	sessionID string,
	saveImage func(string) (string, error),
	readText func() (string, error),
	prepare func(string) (analyzer.ImageAttachment, error),
) clipboardPasteResultMsg {
	path, imageErr := saveImage(sessionID)
	if imageErr == nil {
		attachment, err := prepare(path)
		if err != nil {
			_ = os.Remove(path)
		}
		return clipboardPasteResultMsg{attachment: attachment, err: err}
	}
	text, textErr := readText()
	if textErr == nil && text != "" {
		return clipboardPasteResultMsg{text: text}
	}
	// A picture clipboard has no CF_UNICODETEXT. Windows then reports error 0,
	// whose text is "The operation completed successfully." That is not a
	// second failure and must not hide the image error.
	if textErr != nil && !clipboardTextAbsent(textErr) {
		return clipboardPasteResultMsg{err: fmt.Errorf("剪贴板无可用图片（%v），读取文本也失败: %w", imageErr, textErr)}
	}
	return clipboardPasteResultMsg{err: imageErr}
}

func clipboardTextAbsent(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "operation completed successfully") || strings.Contains(msg, "操作成功完成")
}

func saveClipboardImage(sessionID string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取工作目录失败: %w", err)
	}
	dir := filepath.Join(cwd, "reports", "attachments", safePathSegment(sessionID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建图片附件目录失败: %w", err)
	}
	path := filepath.Join(dir, "clipboard_"+time.Now().Format("20060102_150405.000000000")+".png")
	if err := writeClipboardPNG(path); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("设置剪贴板图片权限失败: %w", err)
	}
	return path, nil
}

func safePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "draft"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return "draft"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func writeClipboardPNG(path string) error {
	switch runtime.GOOS {
	case "darwin":
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return err
		}
		const pngScript = `on run argv
set outPath to item 1 of argv
set outFile to missing value
try
  set pngData to the clipboard as «class PNGf»
  set outFile to open for access POSIX file outPath with write permission
  set eof outFile to 0
  write pngData to outFile
  close access outFile
on error errMsg
  try
    if outFile is not missing value then close access outFile
  end try
  error errMsg
end try
end run`
		pngOutput, pngErr := exec.Command("/usr/bin/osascript", "-e", pngScript, path).CombinedOutput()
		if pngErr == nil {
			if stat, err := os.Stat(path); err == nil && stat.Size() > 0 {
				return nil
			}
		}
		// Some macOS applications expose an NSImage only as TIFF clipboard
		// flavor. Convert it to the same private PNG attachment so Ctrl+V works
		// for screenshots copied from Preview, browsers and other native apps.
		tiffPath := path + ".tiff"
		defer os.Remove(tiffPath)
		if err := os.WriteFile(tiffPath, nil, 0o600); err != nil {
			return err
		}
		const tiffScript = `on run argv
set outPath to item 1 of argv
set outFile to missing value
try
  set imageData to the clipboard as «class TIFF»
  set outFile to open for access POSIX file outPath with write permission
  set eof outFile to 0
  write imageData to outFile
  close access outFile
on error errMsg
  try
    if outFile is not missing value then close access outFile
  end try
  error errMsg
end try
end run`
		tiffOutput, tiffErr := exec.Command("/usr/bin/osascript", "-e", tiffScript, tiffPath).CombinedOutput()
		if tiffErr != nil {
			return fmt.Errorf("剪贴板中没有可读取的图片: PNG=%s; TIFF=%s", strings.TrimSpace(string(pngOutput)), strings.TrimSpace(string(tiffOutput)))
		}
		sipsOutput, err := exec.Command("/usr/bin/sips", "-s", "format", "png", tiffPath, "--out", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("转换剪贴板 TIFF 失败: %s", strings.TrimSpace(string(sipsOutput)))
		}
		if stat, err := os.Stat(path); err != nil || stat.Size() == 0 {
			return errors.New("剪贴板图片转换后为空")
		}
		return nil
	case "linux":
		candidates := [][]string{
			{"wl-paste", "--no-newline", "--type", "image/png"},
			{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
		}
		var failures []string
		for _, args := range candidates {
			if _, err := exec.LookPath(args[0]); err != nil {
				continue
			}
			if err := commandOutputToFile(path, args[0], args[1:]...); err == nil {
				return nil
			} else {
				failures = append(failures, err.Error())
			}
		}
		if len(failures) == 0 {
			return errors.New("读取图片剪贴板需要 wl-paste 或 xclip；也可使用 /image <路径>")
		}
		return fmt.Errorf("剪贴板中没有可读取的 PNG 图片: %s", strings.Join(failures, "; "))
	case "windows":
		// Windows PowerShell appends every argument after -Command onto the
		// script text. Passing the path that way makes Save() see a bare
		// E:\... token and abort with UnexpectedToken before it reads the
		// clipboard. The path has to live inside an encoded script.
		exe, args := windowsClipboardPowerShell(path)
		output, err := exec.Command(exe, args...).CombinedOutput()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
				return errors.New("剪贴板中没有可读取的 PNG 图片")
			}
			detail := strings.TrimSpace(string(output))
			if detail == "" {
				detail = err.Error()
			}
			return fmt.Errorf("剪贴板中没有可读取的 PNG 图片: %s", detail)
		}
		stat, statErr := os.Stat(path)
		if statErr != nil || stat.Size() == 0 {
			return errors.New("剪贴板图片保存后为空")
		}
		return nil
	default:
		return fmt.Errorf("%s 暂不支持直接读取图片剪贴板；请使用 /image <路径>", runtime.GOOS)
	}
}

func windowsClipboardPowerShell(path string) (string, []string) {
	quoted := strings.ReplaceAll(path, "'", "''")
	// Clipboard.GetImage only works on an STA thread. Windows PowerShell is MTA
	// unless -STA is set, and then a real screenshot comes back as null.
	// Some apps also put a PNG byte blob on the clipboard instead of a bitmap.
	script := "$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; $data=[System.Windows.Forms.Clipboard]::GetDataObject(); if($null -ne $data){ foreach($fmt in @('PNG','image/png')){ if($data.GetDataPresent($fmt)){ $raw=$data.GetData($fmt); if($raw -is [byte[]]){ [IO.File]::WriteAllBytes('" + quoted + "',$raw); exit 0 }; if($raw -is [IO.MemoryStream]){ [IO.File]::WriteAllBytes('" + quoted + "',$raw.ToArray()); exit 0 } } } }; $img=[System.Windows.Forms.Clipboard]::GetImage(); if($null -eq $img){ exit 3 }; $img.Save('" + quoted + "',[System.Drawing.Imaging.ImageFormat]::Png)"
	encoded := utf16.Encode([]rune(script))
	raw := make([]byte, len(encoded)*2)
	for i, unit := range encoded {
		binary.LittleEndian.PutUint16(raw[i*2:], unit)
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	exe := filepath.Join(root, `System32\WindowsPowerShell\v1.0\powershell.exe`)
	return exe, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw)}
}

func commandOutputToFile(path, command string, args ...string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(command, args...)
	cmd.Stdout = file
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := file.Close()
	if runErr != nil {
		return fmt.Errorf("%s: %v %s", command, runErr, strings.TrimSpace(stderr.String()))
	}
	if closeErr != nil {
		return closeErr
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Size() == 0 {
		return fmt.Errorf("%s 未返回图片数据", command)
	}
	return nil
}
