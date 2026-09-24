package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestREADMEListsEveryRegisteredTool(t *testing.T) {
	path := filepath.Join("..", "..", "README.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	readme := string(data)
	start := strings.Index(readme, "## 内置工具清单")
	if start < 0 {
		t.Fatal("README is missing the built-in tool section")
	}
	section := readme[start:]
	if end := strings.Index(section[len("## 内置工具清单"):], "\n## "); end >= 0 {
		section = section[:len("## 内置工具清单")+end]
	}
	for name := range Registry {
		if !strings.Contains(section, "`"+name+"`") {
			t.Errorf("README built-in tool list is missing %q", name)
		}
	}
}
