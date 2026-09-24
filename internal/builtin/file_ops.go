package builtin

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ai-edr/internal/config"
)

type archiveBudget struct {
	entries, maxEntries      int
	total, maxFile, maxTotal int64
}

func newArchiveBudget() *archiveBudget {
	entries, fileBytes, totalBytes := config.GlobalConfig.EffectiveArchiveLimits()
	return &archiveBudget{maxEntries: entries, maxFile: fileBytes, maxTotal: totalBytes}
}

func (b *archiveBudget) allow(name string, declared int64) (int64, error) {
	b.entries++
	if b.entries > b.maxEntries {
		return 0, fmt.Errorf("归档条目超过上限 %d", b.maxEntries)
	}
	if declared < 0 {
		return 0, fmt.Errorf("归档条目 %s 尺寸非法", name)
	}
	if declared > b.maxFile {
		return 0, fmt.Errorf("归档条目 %s 超过单文件上限 %d 字节", name, b.maxFile)
	}
	remaining := b.maxTotal - b.total
	if declared > remaining {
		return 0, fmt.Errorf("归档解压总量超过上限 %d 字节", b.maxTotal)
	}
	if remaining > b.maxFile {
		remaining = b.maxFile
	}
	return remaining, nil
}

func (b *archiveBudget) addExtracted(name string, n, allowed int64) error {
	if n > allowed {
		return fmt.Errorf("归档条目 %s 实际解压大小超过安全上限", name)
	}
	b.total += n
	return nil
}

func safeArchiveName(name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(strings.TrimSpace(name)))
	if name == "." || name == "" || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("非法归档路径: %s", name)
	}
	return name, nil
}

func openArchiveRoot(dest string) (*os.Root, error) {
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return nil, err
	}
	return os.OpenRoot(dest)
}

func FileDownload(rt Runtime, remotePath, localPath string, chunkSize int) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if remotePath == "" || localPath == "" {
		return "", fmt.Errorf("remote_path 和 local_path 必填")
	}
	if chunkSize <= 0 {
		chunkSize = 4 << 20
	}
	out, err := rt.Exec.Run("download " + shellQuote(remotePath) + " " + shellQuote(localPath))
	logPath := writeToolExecLog("file_download", fmt.Sprintf("%s -> %s chunk=%d", remotePath, localPath, chunkSize), out, err)
	return formatTransferResult(rt, "下载", remotePath, localPath, chunkSize, logPath, out, err), err
}

func FileUpload(rt Runtime, localPath, remotePath string, chunkSize int) (string, error) {
	if rt.Exec == nil {
		return "", fmt.Errorf("执行器未初始化")
	}
	if localPath == "" || remotePath == "" {
		return "", fmt.Errorf("local_path 和 remote_path 必填")
	}
	if chunkSize <= 0 {
		chunkSize = 4 << 20
	}
	out, err := rt.Exec.Run("upload " + shellQuote(localPath) + " " + shellQuote(remotePath))
	logPath := writeToolExecLog("file_upload", fmt.Sprintf("%s -> %s chunk=%d", localPath, remotePath, chunkSize), out, err)
	return formatTransferResult(rt, "上传", localPath, remotePath, chunkSize, logPath, out, err), err
}

func ArchivePack(rt Runtime, format, source, dest string) (string, error) {
	format = normalizeArchiveFormat(format, dest)
	if source == "" || dest == "" {
		return "", fmt.Errorf("source 和 dest 必填")
	}
	var out string
	var err error
	if rt.Exec != nil && rt.Exec.IsRemote() {
		cmd, cerr := archivePackCommand(format, source, dest)
		if cerr != nil {
			return "", cerr
		}
		out, err = rt.Exec.Run(cmd)
	} else {
		err = packLocalArchive(format, source, dest)
		if err == nil {
			out = "本地打包完成"
		}
	}
	logPath := writeToolExecLog("archive_pack", fmt.Sprintf("format=%s source=%s dest=%s", format, source, dest), out, err)
	return archiveResult(rt, "打包", format, source, dest, logPath, out, err), err
}

func ArchiveExtract(rt Runtime, format, source, dest string) (string, error) {
	format = normalizeArchiveFormat(format, source)
	if source == "" || dest == "" {
		return "", fmt.Errorf("source 和 dest 必填")
	}
	var out string
	var err error
	if rt.Exec != nil && rt.Exec.IsRemote() {
		return "", fmt.Errorf("远程直接解压已禁用：无法跨所有目标工具链可靠阻止 Zip-Slip、符号链接逃逸和解压炸弹；请先 file_download 到控制端安全解压，检查后再 file_upload")
	} else {
		err = extractLocalArchive(format, source, dest)
		if err == nil {
			out = "本地解压完成"
		}
	}
	logPath := writeToolExecLog("archive_extract", fmt.Sprintf("format=%s source=%s dest=%s", format, source, dest), out, err)
	return archiveResult(rt, "解压", format, source, dest, logPath, out, err), err
}

func formatTransferResult(rt Runtime, op, src, dst string, chunk int, logPath, out string, err error) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 文件%s\n%s -> %s\nchunk_size=%d\n", rt.tag(), op, src, dst, chunk))
	if logPath != "" {
		b.WriteString("执行日志: " + logPath + "\n")
	}
	if err != nil {
		b.WriteString("状态: 失败: " + err.Error() + "\n")
	} else {
		b.WriteString("状态: 完成\n")
	}
	b.WriteString("\n输出:\n" + out)
	return b.String()
}

func normalizeArchiveFormat(format, path string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "" {
		return format
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".rar"):
		return "rar"
	case strings.HasSuffix(lower, ".7z"):
		return "7z"
	default:
		return "tar.gz"
	}
}

func archivePackCommand(format, source, dest string) (string, error) {
	switch format {
	case "tar.gz", "tgz":
		return fmt.Sprintf("tar -czf %s -C %s %s", shellQuote(dest), shellQuote(filepath.Dir(source)), shellQuote(filepath.Base(source))), nil
	case "tar":
		return fmt.Sprintf("tar -cf %s -C %s %s", shellQuote(dest), shellQuote(filepath.Dir(source)), shellQuote(filepath.Base(source))), nil
	case "zip":
		return fmt.Sprintf("cd %s && zip -r %s %s", shellQuote(filepath.Dir(source)), shellQuote(dest), shellQuote(filepath.Base(source))), nil
	case "7z":
		return fmt.Sprintf("7z a %s %s", shellQuote(dest), shellQuote(source)), nil
	case "rar":
		return fmt.Sprintf("rar a %s %s", shellQuote(dest), shellQuote(source)), nil
	default:
		return "", fmt.Errorf("不支持的打包格式: %s", format)
	}
}

func archiveExtractCommand(format, source, dest string) (string, error) {
	mkdir := "mkdir -p " + shellQuote(dest) + " && "
	switch format {
	case "tar.gz", "tgz":
		return mkdir + fmt.Sprintf("tar -xzf %s -C %s", shellQuote(source), shellQuote(dest)), nil
	case "tar":
		return mkdir + fmt.Sprintf("tar -xf %s -C %s", shellQuote(source), shellQuote(dest)), nil
	case "zip":
		return mkdir + fmt.Sprintf("unzip -o %s -d %s", shellQuote(source), shellQuote(dest)), nil
	case "7z":
		return mkdir + fmt.Sprintf("7z x -y %s -o%s", shellQuote(source), shellQuote(dest)), nil
	case "rar":
		return mkdir + fmt.Sprintf("unrar x -o+ %s %s", shellQuote(source), shellQuote(dest)), nil
	default:
		return "", fmt.Errorf("不支持的解压格式: %s", format)
	}
}

func packLocalArchive(format, source, dest string) error {
	switch format {
	case "tar.gz", "tgz":
		return packTarGz(source, dest)
	case "tar":
		return packTar(source, dest)
	case "zip":
		return packZip(source, dest)
	default:
		return fmt.Errorf("本地纯 Go 暂只支持 zip/tar/tar.gz；%s 需要目标系统安装 7z/rar", format)
	}
}

func extractLocalArchive(format, source, dest string) error {
	switch format {
	case "tar.gz", "tgz":
		return extractTar(source, dest, true)
	case "tar":
		return extractTar(source, dest, false)
	case "zip":
		return extractZip(source, dest)
	default:
		return fmt.Errorf("本地纯 Go 暂只支持 zip/tar/tar.gz；%s 需要目标系统安装 7z/unrar", format)
	}
}

func archiveResult(rt Runtime, op, format, source, dest, logPath, out string, err error) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 归档%s format=%s\n%s -> %s\n", rt.tag(), op, format, source, dest))
	if logPath != "" {
		b.WriteString("执行日志: " + logPath + "\n")
	}
	if err != nil {
		b.WriteString("状态: 失败: " + err.Error() + "\n")
	} else {
		b.WriteString("状态: 完成\n")
	}
	if strings.TrimSpace(out) != "" {
		b.WriteString("\n输出:\n" + out)
	}
	return b.String()
}

func packZip(source, dest string) error {
	out, err := openPrivateArchiveDest(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	walkErr := walkPackSource(source, func(name string, _ os.FileInfo, open func() (*os.File, error)) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		in, err := open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	zipCloseErr := zw.Close()
	outCloseErr := out.Close()
	finalErr := firstArchiveError(walkErr, zipCloseErr, outCloseErr)
	if finalErr != nil {
		_ = os.Remove(dest)
	}
	return finalErr
}

func packTar(source, dest string) error {
	out, err := openPrivateArchiveDest(dest)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(out)
	writeErr := writeTar(source, tw)
	tarCloseErr := tw.Close()
	outCloseErr := out.Close()
	finalErr := firstArchiveError(writeErr, tarCloseErr, outCloseErr)
	if finalErr != nil {
		_ = os.Remove(dest)
	}
	return finalErr
}

func packTarGz(source, dest string) error {
	out, err := openPrivateArchiveDest(dest)
	if err != nil {
		return err
	}
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)
	writeErr := writeTar(source, tw)
	tarCloseErr := tw.Close()
	gzipCloseErr := gw.Close()
	outCloseErr := out.Close()
	finalErr := firstArchiveError(writeErr, tarCloseErr, gzipCloseErr, outCloseErr)
	if finalErr != nil {
		_ = os.Remove(dest)
	}
	return finalErr
}

func openPrivateArchiveDest(dest string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(dest), 0o700); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := out.Chmod(0o600); err != nil {
		_ = out.Close()
		return nil, err
	}
	return out, nil
}

func firstArchiveError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func writeTar(source string, tw *tar.Writer) error {
	return walkPackSource(source, func(name string, info os.FileInfo, open func() (*os.File, error)) error {
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = name
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		in, err := open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// walkPackSource 用 os.Root 把打包读取限定在源目录内。遍历时拒绝链接和
// 特殊文件，避免取证包因符号链接或 Walk/Open 竞态意外携带目录外数据。
func walkPackSource(source string, visit func(name string, info os.FileInfo, open func() (*os.File, error)) error) error {
	source = filepath.Clean(source)
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("拒绝打包符号链接或特殊文件: %s", source)
		}
		root, err := os.OpenRoot(filepath.Dir(source))
		if err != nil {
			return err
		}
		defer root.Close()
		base := filepath.Base(source)
		return visit(filepath.ToSlash(base), info, func() (*os.File, error) { return root.Open(base) })
	}

	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	prefix := filepath.Base(source)
	return fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == "." || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝打包符号链接或特殊文件: %s", filepath.Join(source, filepath.FromSlash(rel)))
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("拒绝打包符号链接或特殊文件: %s", filepath.Join(source, filepath.FromSlash(rel)))
		}
		archiveName := filepath.ToSlash(filepath.Join(prefix, filepath.FromSlash(rel)))
		return visit(archiveName, entryInfo, func() (*os.File, error) { return root.Open(filepath.FromSlash(rel)) })
	})
}

func extractZip(source, dest string) error {
	r, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer r.Close()
	root, err := openArchiveRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	budget := newArchiveBudget()
	for _, f := range r.File {
		name, err := safeArchiveName(f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if _, err := budget.allow(name, 0); err != nil {
				return err
			}
			if err := root.MkdirAll(name, 0o750); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("拒绝解压非普通 zip 条目: %s", f.Name)
		}
		if f.UncompressedSize64 > uint64(^uint64(0)>>1) {
			return fmt.Errorf("拒绝解压尺寸超出 int64 范围的 zip 条目: %s", f.Name)
		}
		allowed, err := budget.allow(name, int64(f.UncompressedSize64))
		if err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			_ = in.Close()
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, allowed+1))
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr == nil {
			copyErr = closeInErr
		}
		if copyErr == nil {
			copyErr = closeOutErr
		}
		if copyErr == nil {
			copyErr = budget.addExtracted(name, n, allowed)
		}
		if copyErr != nil {
			_ = root.Remove(name)
			return copyErr
		}
	}
	return nil
}

func extractTar(source, dest string, gz bool) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	var r io.Reader = in
	if gz {
		gr, err := gzip.NewReader(in)
		if err != nil {
			return err
		}
		defer gr.Close()
		r = gr
	}
	tr := tar.NewReader(r)
	root, err := openArchiveRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	budget := newArchiveBudget()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := safeArchiveName(h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if _, err := budget.allow(name, 0); err != nil {
				return err
			}
			if err := root.MkdirAll(name, 0o750); err != nil {
				return err
			}
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("拒绝解压 tar 链接或特殊文件: %s", h.Name)
		}
		allowed, err := budget.allow(name, h.Size)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			return err
		}
		out, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(tr, allowed+1))
		closeErr := out.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil {
			copyErr = budget.addExtracted(name, n, allowed)
		}
		if copyErr != nil {
			_ = root.Remove(name)
			return copyErr
		}
	}
}
