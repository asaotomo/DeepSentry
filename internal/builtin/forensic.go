package builtin

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxForensicRead     = 5 << 20  // 5MB 压缩/原始读取上限
	maxDecompressed     = 20 << 20 // 20MB 解压输出上限
	maxStringsScan      = 2 << 20  // strings 扫描 2MB
	defaultStringsMin   = 4
	defaultLogLines     = 200
	defaultStringsLimit = 500
)

func validateReadPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("缺少参数 path")
	}
	lower := strings.ToLower(path)
	blocked := []string{"config.yaml", "deepsentry", "reports/"}
	for _, b := range blocked {
		if strings.Contains(lower, strings.ToLower(b)) {
			return fmt.Errorf("禁止读取受保护路径: %s", path)
		}
	}
	return nil
}

func readTargetLimited(path string, max int) ([]byte, error) {
	if err := validateReadPath(path); err != nil {
		return nil, err
	}
	if ex := currentRuntimeExec(); ex != nil && !ex.IsRemote() && max > 0 {
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Size() > int64(max) {
			return nil, fmt.Errorf("文件过大 (%d 字节，上限 %d)", st.Size(), max)
		}
	}
	data, err := readTarget(path)
	if err != nil {
		return nil, err
	}
	if max > 0 && len(data) > max {
		return nil, fmt.Errorf("文件过大 (%d 字节，上限 %d)", len(data), max)
	}
	return data, nil
}

// FileIdentify 魔数/文件类型识别（Go 原生，无需 file 命令）
func FileIdentify(rt Runtime, path string) (string, error) {
	data, err := readTargetLimited(path, 512*1024) // 读前 512KB 足够识别
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("空文件")
	}

	head := data[:min(len(data), 512)]
	types, hints := identifyMagic(head)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 文件类型识别 (魔数分析)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("路径: %s\n", path))
	b.WriteString(fmt.Sprintf("大小: %d 字节\n", len(data)))
	b.WriteString(fmt.Sprintf("魔数头: %s\n\n", hexHead(head, 16)))

	if len(types) == 0 {
		if looksText(data) {
			b.WriteString("类型: ASCII/UTF-8 文本\n")
			// 额外文本特征
			sample := string(head[:min(len(head), 200)])
			if strings.Contains(sample, "eval(") || strings.Contains(sample, "base64_decode") {
				b.WriteString("⚠️  可疑: 含 PHP 危险函数特征\n")
			}
		} else {
			b.WriteString("类型: 未知二进制 (data)\n")
		}
	} else {
		b.WriteString("识别类型:\n")
		for _, t := range types {
			b.WriteString("  - " + t + "\n")
		}
	}
	if len(hints) > 0 {
		b.WriteString("\n提示:\n")
		for _, h := range hints {
			b.WriteString("  · " + h + "\n")
		}
	}
	return b.String(), nil
}

// FileStrings 提取可打印字符串（Go 原生，无需 strings 命令）
func FileStrings(rt Runtime, path string, minLen, limit int, pattern string) (string, error) {
	data, err := readTargetLimited(path, maxStringsScan)
	if err != nil {
		return "", err
	}
	if minLen <= 0 {
		minLen = defaultStringsMin
	}
	if limit <= 0 {
		limit = defaultStringsLimit
	}
	if limit > 2000 {
		limit = 2000
	}

	found := extractStrings(data, minLen)
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("非法 pattern: %v", err)
		}
		var filtered []string
		for _, s := range found {
			if re.MatchString(s) {
				filtered = append(filtered, s)
			}
		}
		found = filtered
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 字符串提取 (Go 原生)\n", rt.tag()))
	b.WriteString(fmt.Sprintf("路径: %s  min_len=%d  匹配=%d 条", path, minLen, len(found)))
	if pattern != "" {
		b.WriteString(fmt.Sprintf("  pattern=%s", pattern))
	}
	b.WriteString("\n\n")

	shown := 0
	for _, s := range found {
		if shown >= limit {
			b.WriteString(fmt.Sprintf("\n...(仅显示前 %d 条)...", limit))
			break
		}
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		b.WriteString(s + "\n")
		shown++
	}
	if shown == 0 {
		b.WriteString("(无匹配字符串)\n")
	}
	return truncate(b.String(), 20000), nil
}

func extractStrings(data []byte, minLen int) []string {
	var results []string
	var cur strings.Builder

	flush := func() {
		if cur.Len() >= minLen {
			results = append(results, cur.String())
		}
		cur.Reset()
	}

	for i := 0; i < len(data); i++ {
		c := data[i]
		if c >= 32 && c <= 126 || c == '\t' {
			cur.WriteByte(c)
		} else {
			flush()
		}
	}
	flush()

	// UTF-8 片段补充
	results = append(results, extractUTF8Strings(data, minLen)...)
	return dedupeStrings(results)
}

func extractUTF8Strings(data []byte, minLen int) []string {
	var out []string
	i := 0
	for i < len(data) {
		r, size := utf8.DecodeRune(data[i:])
		if size == 0 {
			break
		}
		if r >= 0x4e00 && r <= 0x9fff { // CJK 常用区
			start := i
			for i < len(data) {
				r2, sz := utf8.DecodeRune(data[i:])
				if sz == 0 || (r2 < 0x4e00 || r2 > 0x9fff) && !(r2 >= 0x3000 && r2 <= 0x303f) {
					break
				}
				i += sz
			}
			if s := string(data[start:i]); utf8.RuneCountInString(s) >= minLen/2 {
				out = append(out, s)
			}
			continue
		}
		i += size
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ReadGzip 解压读取 gzip 日志（Go 原生，无需 zcat/gunzip）
func ReadGzip(rt Runtime, path string, lines int, pattern string) (string, error) {
	raw, err := readTargetLimited(path, maxForensicRead)
	if err != nil {
		return "", err
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return "", fmt.Errorf("非 gzip 文件，请用 read_log 自动识别或 read_file")
	}

	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("gzip 打开失败: %v", err)
	}
	defer gz.Close()

	decompressed, err := io.ReadAll(io.LimitReader(gz, maxDecompressed))
	if err != nil {
		return "", fmt.Errorf("gzip 解压失败: %v", err)
	}

	return formatLogOutput(rt, path, decompressed, lines, pattern, "gzip 解压")
}

// ReadLog 智能读日志（自动识别 plain/gzip）
func ReadLog(rt Runtime, path string, lines int, pattern string) (string, error) {
	raw, err := readTargetLimited(path, maxForensicRead)
	if err != nil {
		return "", err
	}
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", err
		}
		defer gz.Close()
		decompressed, err := io.ReadAll(io.LimitReader(gz, maxDecompressed))
		if err != nil {
			return "", err
		}
		return formatLogOutput(rt, path, decompressed, lines, pattern, "gzip 自动解压")
	}
	return formatLogOutput(rt, path, raw, lines, pattern, "纯文本")
}

func formatLogOutput(rt Runtime, path string, data []byte, lines int, pattern, mode string) (string, error) {
	if lines <= 0 {
		lines = defaultLogLines
	}
	if lines > 2000 {
		lines = 2000
	}

	text := string(data)
	allLines := strings.Split(text, "\n")

	var re *regexp.Regexp
	if pattern != "" {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("非法 pattern: %v", err)
		}
	}

	if re != nil {
		var matched []string
		for _, line := range allLines {
			if re.MatchString(line) {
				matched = append(matched, line)
			}
		}
		allLines = matched
	}

	start := 0
	if len(allLines) > lines {
		start = len(allLines) - lines
	}
	selected := allLines[start:]

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s 日志读取 (%s)\n", rt.tag(), mode))
	b.WriteString(fmt.Sprintf("路径: %s\n", path))
	b.WriteString(fmt.Sprintf("解压/原始: %d 字节  显示: 最后 %d 行", len(data), len(selected)))
	if pattern != "" {
		b.WriteString(fmt.Sprintf("  过滤: %s", pattern))
	}
	b.WriteString("\n\n")
	b.WriteString(strings.Join(selected, "\n"))
	if len(allLines) > lines {
		b.WriteString(fmt.Sprintf("\n\n...(共 %d 行，仅显示最后 %d 行)...", len(allLines), lines))
	}
	return truncate(b.String(), 30000), nil
}
