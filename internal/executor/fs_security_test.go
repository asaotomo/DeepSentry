package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneralLocalFileReadIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxReadSize+1024), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFileWithExecutor(&LocalExecutor{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != maxReadSize {
		t.Fatalf("read %d bytes, want bounded %d", len(data), maxReadSize)
	}
}

func TestLocalExecutorDirectReadIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large-direct.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxReadSize+1024), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := (&LocalExecutor{}).ReadTargetFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != maxReadSize {
		t.Fatalf("direct read %d bytes, want bounded %d", len(data), maxReadSize)
	}
}

func TestWriteLocalFileUsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "evidence.txt")
	if err := WriteLocalFile(path, []byte("sensitive evidence")); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}
}

func TestLocalWritesPreserveExistingFilePermissions(t *testing.T) {
	for _, write := range []struct {
		name string
		fn   func(string, []byte) error
	}{
		{name: "WriteLocalFile", fn: WriteLocalFile},
		{name: "WriteFileWithExecutor", fn: func(path string, data []byte) error {
			return WriteFileWithExecutor(&LocalExecutor{}, path, data)
		}},
	} {
		t.Run(write.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing.txt")
			if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := write.fn(path, []byte("private")); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o644 {
				t.Fatalf("existing file mode = %o, want preserved 644", got)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "private" {
				t.Fatalf("data=%q err=%v", data, err)
			}
		})
	}
}

func TestLocalWriteDoesNotChangeExistingParentMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "served-content")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileWithExecutor(&LocalExecutor{}, filepath.Join(dir, "index.html"), []byte("ok")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing parent mode = %o, want preserved 755", got)
	}
}
