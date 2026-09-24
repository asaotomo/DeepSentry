package builtin

import (
	"bytes"
	"fmt"
	"strings"
)

type magicRule struct {
	Name  string
	Match func([]byte) bool
	Hint  string
}

var magicRules = []magicRule{
	{
		Name: "ELF executable",
		Match: func(b []byte) bool {
			return len(b) >= 4 && b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F'
		},
		Hint: "Linux/Unix 可执行文件或共享库",
	},
	{
		Name:  "PE executable (Windows)",
		Match: func(b []byte) bool { return len(b) >= 2 && b[0] == 'M' && b[1] == 'Z' },
		Hint:  "Windows 可执行文件/DLL",
	},
	{
		Name:  "GZIP compressed",
		Match: func(b []byte) bool { return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b },
		Hint:  "gzip 压缩文件，可用 read_gzip 解压查看",
	},
	{
		Name: "ZIP archive",
		Match: func(b []byte) bool {
			return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' && (b[2] == 3 || b[2] == 5 || b[2] == 7) && (b[3] == 4 || b[3] == 6 || b[3] == 8)
		},
		Hint: "ZIP/JAR/APK 等压缩包",
	},
	{
		Name: "PNG image",
		Match: func(b []byte) bool {
			return len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G'
		},
	},
	{
		Name:  "JPEG image",
		Match: func(b []byte) bool { return len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff },
	},
	{
		Name: "GIF image",
		Match: func(b []byte) bool {
			return len(b) >= 6 && (bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")))
		},
	},
	{
		Name:  "PDF document",
		Match: func(b []byte) bool { return bytes.HasPrefix(b, []byte("%PDF-")) },
		Hint:  "PDF 文档，可用 document_parse 提取正文/元信息",
	},
	{
		Name: "OLE Compound document",
		Match: func(b []byte) bool {
			return len(b) >= 8 && b[0] == 0xd0 && b[1] == 0xcf && b[2] == 0x11 && b[3] == 0xe0 &&
				b[4] == 0xa1 && b[5] == 0xb1 && b[6] == 0x1a && b[7] == 0xe1
		},
		Hint: "老式 Office .doc/.xls 复合文档，可用 document_parse 做字符串级解析",
	},
	{
		Name:  "Shell script",
		Match: func(b []byte) bool { return bytes.HasPrefix(b, []byte("#!")) },
		Hint:  "脚本文件，检查 shebang 指向",
	},
	{
		Name: "PHP script",
		Match: func(b []byte) bool {
			return bytes.HasPrefix(b, []byte("<?php")) || bytes.Contains(b[:min(len(b), 256)], []byte("<?php"))
		},
		Hint: "PHP 源码/潜在 Webshell",
	},
	{
		Name: "HTML/XML markup",
		Match: func(b []byte) bool {
			s := strings.ToLower(string(b[:min(len(b), 512)]))
			return strings.HasPrefix(s, "<!doctype") || strings.HasPrefix(s, "<html") ||
				strings.HasPrefix(s, "<?xml") || strings.HasPrefix(s, "<svg")
		},
	},
	{
		Name: "JSON text",
		Match: func(b []byte) bool {
			trim := bytes.TrimSpace(b)
			return len(trim) > 0 && (trim[0] == '{' || trim[0] == '[')
		},
	},
	{
		Name: "Java class",
		Match: func(b []byte) bool {
			return len(b) >= 4 && b[0] == 0xca && b[1] == 0xfe && b[2] == 0xba && b[3] == 0xbe
		},
	},
	{
		Name:  "SQLite database",
		Match: func(b []byte) bool { return bytes.HasPrefix(b, []byte("SQLite format 3")) },
	},
	{
		Name: "7-Zip archive",
		Match: func(b []byte) bool {
			return len(b) >= 6 && b[0] == '7' && b[1] == 'z' && b[2] == 0xbc && b[3] == 0xaf && b[4] == 0x27 && b[5] == 0x1c
		},
	},
	{
		Name:  "BZIP2 compressed",
		Match: func(b []byte) bool { return len(b) >= 3 && b[0] == 'B' && b[1] == 'Z' && b[2] == 'h' },
	},
	{
		Name: "XZ compressed",
		Match: func(b []byte) bool {
			return len(b) >= 6 && b[0] == 0xfd && b[1] == '7' && b[2] == 'z' && b[3] == 'X' && b[4] == 'Z' && b[5] == 0x00
		},
	},
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func identifyMagic(head []byte) (types []string, hints []string) {
	for _, rule := range magicRules {
		if rule.Match(head) {
			types = append(types, rule.Name)
			if rule.Hint != "" {
				hints = append(hints, rule.Hint)
			}
		}
	}
	return types, hints
}

func looksText(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	printable := 0
	for _, c := range data[:min(len(data), 512)] {
		if c == 9 || c == 10 || c == 13 || (c >= 32 && c <= 126) {
			printable++
		}
	}
	return float64(printable)/float64(min(len(data), 512)) > 0.85
}

func hexHead(data []byte, n int) string {
	if len(data) > n {
		data = data[:n]
	}
	var parts []string
	for _, b := range data {
		parts = append(parts, fmt.Sprintf("%02x", b))
	}
	return strings.Join(parts, " ")
}
