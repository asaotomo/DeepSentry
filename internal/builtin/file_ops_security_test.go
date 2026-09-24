package builtin

import (
	"archive/tar"
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/config"
)

func TestExtractZipRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "traversal.zip")
	writeSecurityTestZip(t, archive, map[string]string{"../escape.txt": "owned"})
	dest := filepath.Join(t.TempDir(), "dest")
	if err := extractZip(archive, dest); err == nil || !strings.Contains(err.Error(), "非法归档路径") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped destination: %v", err)
	}
}

func TestExtractZipRejectsPreexistingSymlinkEscape(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "symlink.zip")
	writeSecurityTestZip(t, archive, map[string]string{"link/escape.txt": "owned"})
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := extractZip(archive, dest); err == nil {
		t.Fatal("expected os.Root to reject a symlink escaping the destination")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped through symlink: %v", err)
	}
}

func TestExtractTarRejectsLinks(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "links.tar")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	if err := tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTar(archive, filepath.Join(t.TempDir(), "dest"), false); err == nil || !strings.Contains(err.Error(), "链接或特殊文件") {
		t.Fatalf("expected tar link rejection, got %v", err)
	}
}

func TestExtractZipEnforcesFileLimitAndPrivateMode(t *testing.T) {
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()
	archive := filepath.Join(t.TempDir(), "files.zip")
	writeSecurityTestZip(t, archive, map[string]string{"small.txt": "ok"})
	dest := filepath.Join(t.TempDir(), "dest")
	config.GlobalConfig.ArchiveMaxEntries = 10
	config.GlobalConfig.ArchiveMaxFileBytes = 16
	config.GlobalConfig.ArchiveMaxTotalBytes = 32
	if err := extractZip(archive, dest); err != nil {
		t.Fatalf("extract small zip: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "small.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("extracted mode=%o want 600", got)
	}

	writeSecurityTestZip(t, archive, map[string]string{"large.txt": strings.Repeat("x", 17)})
	if err := extractZip(archive, filepath.Join(t.TempDir(), "limited")); err == nil || !strings.Contains(err.Error(), "单文件上限") {
		t.Fatalf("expected file limit rejection, got %v", err)
	}
}

func TestPackArchiveUsesPrivateModeAndRejectsSymlinks(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "evidence")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "auth.log"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(base, "private", "evidence.zip")
	if err := packZip(source, dest); err != nil {
		t.Fatalf("pack zip: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("archive mode=%o want 600", got)
	}

	link := filepath.Join(source, "outside-link")
	if err := os.Symlink(filepath.Join(base, "outside"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := packTar(source, filepath.Join(base, "private", "evidence.tar")); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func writeSecurityTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
