package builtin

import (
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	cryptozip "github.com/yeka/zip"
)

// Python string.printable used by ZipCracker's short-plaintext CRC32 recovery.
const zipCRCPrintable = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~ \t\n\r\x0b\x0c"
const zipCRCAlnum = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

type zipCRCHit struct {
	Name    string
	Size    int
	CRC32   uint32
	Content string
}

func recoverZIPCRC32(source string, args map[string]string) (string, bool, error) {
	file, err := os.Open(source)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	reader, err := cryptozip.NewReader(file, info.Size())
	if err != nil {
		return "", false, fmt.Errorf("解析 ZIP 失败: %w", err)
	}
	hits := make([]zipCRCHit, 0)
	short := 0
	files := 0
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		files++
		size64 := entry.UncompressedSize64
		if size64 < 1 || size64 > 6 {
			continue
		}
		size := int(size64)
		short++
		charset := zipCRCPrintable
		if size >= 5 {
			charset = zipCRCAlnum
		}
		content, ok := crc32Enumerate(entry.CRC32, size, []byte(charset))
		if !ok {
			continue
		}
		hits = append(hits, zipCRCHit{Name: entry.Name, Size: size, CRC32: entry.CRC32, Content: content})
	}
	if short == 0 {
		return "", false, fmt.Errorf("没有 1～6 字节的短明文条目，无法做 CRC32 原像枚举")
	}
	if len(hits) == 0 {
		return "", false, fmt.Errorf("短明文 CRC32 枚举未命中（已检查 %d 个 1～6 字节条目）", short)
	}
	var b strings.Builder
	b.WriteString("短明文 CRC32 枚举恢复成功（恢复的是文件内容，不是 ZIP 口令）\n")
	for _, hit := range hits {
		fmt.Fprintf(&b, "- %s · %d bytes · CRC32=%08x · 内容: %q\n", hit.Name, hit.Size, hit.CRC32, hit.Content)
	}
	all := files > 0 && len(hits) == files
	if all {
		b.WriteString("全部条目均已通过 CRC32 恢复，无需再爆破 ZIP 口令。\n")
	}
	if zipBoolDefault(args["extract"], false) {
		dest, destNote, err := zipAllocateExtractDest(source, args["dest"])
		if err != nil {
			return "", all, fmt.Errorf("%s\n写入恢复内容失败: %w", strings.TrimSpace(b.String()), err)
		}
		if destNote != "" {
			b.WriteString(destNote + "\n")
		}
		if err := writeZIPCRCHits(dest, hits); err != nil {
			return "", all, fmt.Errorf("%s\n写入恢复内容失败: %w", strings.TrimSpace(b.String()), err)
		}
		fmt.Fprintf(&b, "已写入目录: %s\n", dest)
	}
	return b.String(), all, nil
}

func writeZIPCRCHits(dest string, hits []zipCRCHit) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("dest 已存在，拒绝覆盖: %s", dest)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	for _, hit := range hits {
		name, err := safeArchiveName(hit.Name)
		if err != nil {
			return err
		}
		path := filepath.Join(dest, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(hit.Content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func crc32Enumerate(target uint32, size int, charset []byte) (string, bool) {
	if size < 1 || size > 6 || len(charset) == 0 {
		return "", false
	}
	if size == 1 {
		buf := []byte{0}
		for _, ch := range charset {
			buf[0] = ch
			if crc32.ChecksumIEEE(buf) == target {
				return string(buf), true
			}
		}
		return "", false
	}
	workers := len(charset)
	if workers > 16 {
		workers = 16
	}
	var (
		found   atomic.Bool
		hit     string
		mu      sync.Mutex
		wg      sync.WaitGroup
		counter atomic.Uint32
	)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, size)
			for {
				if found.Load() {
					return
				}
				index := int(counter.Add(1) - 1)
				if index >= len(charset) {
					return
				}
				buf[0] = charset[index]
				if crc32Walk(target, charset, buf, 1, &found) {
					mu.Lock()
					hit = string(buf)
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return hit, found.Load()
}

func crc32Walk(target uint32, charset, buf []byte, depth int, found *atomic.Bool) bool {
	if found.Load() {
		return false
	}
	if depth == len(buf) {
		if crc32.ChecksumIEEE(buf) == target {
			found.Store(true)
			return true
		}
		return false
	}
	for _, ch := range charset {
		if found.Load() {
			return false
		}
		buf[depth] = ch
		if crc32Walk(target, charset, buf, depth+1, found) {
			return true
		}
	}
	return false
}

func zipBoolDefault(raw string, fallback bool) bool {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	return zipBool(raw)
}
