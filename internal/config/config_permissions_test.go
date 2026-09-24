package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestWriteConfigAsPrivateTightensExistingFileMode(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old: value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	viper.Set("api_key", "secret")
	if err := WriteConfigAsPrivate(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode=%o want 600", got)
	}
}
