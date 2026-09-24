package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTopLevelSlashCommandsAreDocumented(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, relativePath := range []string{"README.md", filepath.Join("docs", "操作手册.md")} {
		path := filepath.Join(root, relativePath)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		document := string(data)
		for _, command := range slashCommands {
			if !strings.Contains(document, "/"+command.Name) {
				t.Errorf("%s does not document /%s", relativePath, command.Name)
			}
		}
	}
}
