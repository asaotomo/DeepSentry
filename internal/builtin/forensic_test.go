package builtin

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestIdentifyMagicELF(t *testing.T) {
	head := []byte{0x7f, 'E', 'L', 'F', 1, 2, 3}
	types, _ := identifyMagic(head)
	if len(types) == 0 || types[0] != "ELF executable" {
		t.Fatalf("expected ELF, got %v", types)
	}
}

func TestIdentifyMagicGzip(t *testing.T) {
	head := []byte{0x1f, 0x8b, 0x08}
	types, hints := identifyMagic(head)
	found := false
	for _, ty := range types {
		if ty == "GZIP compressed" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected gzip")
	}
	if len(hints) == 0 {
		t.Fatal("expected hint")
	}
}

func TestIdentifyMagicPHP(t *testing.T) {
	head := []byte("<?php @eval($_POST['x']);")
	types, _ := identifyMagic(head)
	found := false
	for _, ty := range types {
		if ty == "PHP script" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected PHP, got %v", types)
	}
}

func TestExtractStrings(t *testing.T) {
	data := []byte("hello\x00world\x00" + strings.Repeat("A", 10) + "\x00short")
	found := extractStrings(data, 4)
	if len(found) < 2 {
		t.Fatalf("expected strings, got %v", found)
	}
}

func TestReadGzipRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte("line1\nline2\nerror: failed login\nline4\n"))
	w.Close()

	gz, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	gz.Close()

	out, err := formatLogOutput(Runtime{}, "/var/log/test.log.gz", decompressed, 10, "error", "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "failed login") {
		t.Fatalf("missing content: %s", out)
	}
	if strings.Contains(out, "line1") {
		t.Fatal("pattern filter should exclude line1")
	}
}

func TestReadLogPlain(t *testing.T) {
	content := "a\nb\nc\nd\ne"
	out, err := formatLogOutput(Runtime{}, "/tmp/app.log", []byte(content), 2, "", "plain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "d") || !strings.Contains(out, "e") {
		t.Fatalf("expected last 2 lines: %s", out)
	}
	if strings.Contains(out, "\na\n") {
		t.Fatal("should not include early lines")
	}
}

func TestValidateReadPath(t *testing.T) {
	if validateReadPath("config.yaml") == nil {
		t.Fatal("should block config")
	}
	if validateReadPath("/var/log/auth.log") != nil {
		t.Fatal("should allow log")
	}
}

func TestFormatLogOutputPattern(t *testing.T) {
	data := []byte("ok\nfail login\nok\n")
	out, err := formatLogOutput(Runtime{}, "/x", data, 100, "fail", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fail login") || strings.Contains(out, "\nok\n") {
		t.Fatalf("bad filter: %s", out)
	}
}

func TestLooksText(t *testing.T) {
	if !looksText([]byte("hello world\n")) {
		t.Fatal("should be text")
	}
	if looksText([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatal("should be binary")
	}
}
