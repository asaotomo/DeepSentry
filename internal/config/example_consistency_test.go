package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigExampleMentionsEveryTopLevelOption(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	typeOfConfig := reflect.TypeOf(Config{})
	for index := 0; index < typeOfConfig.NumField(); index++ {
		key := typeOfConfig.Field(index).Tag.Get("mapstructure")
		if key == "" || key == "-" {
			continue
		}
		// Options may be active or deliberately commented examples (models,
		// targets, Skills and MCP). Requiring a YAML-shaped key keeps the
		// public template complete without forcing every optional block on.
		pattern := regexp.MustCompile(`(?m)^\s*(?:#\s*)?` + regexp.QuoteMeta(key) + `\s*:`)
		if !pattern.Match(data) {
			t.Errorf("config.example.yaml does not mention top-level option %q", key)
		}
	}
}

func TestConfigExampleIsSafeToCopy(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var example map[string]any
	if err := yaml.Unmarshal(data, &example); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if example["target_protocol"] != "local" {
		t.Fatalf("release template must default to local mode, got %q", example["target_protocol"])
	}
	for _, key := range []string{"ssh_host", "telnet_host", "ftp_host"} {
		if example[key] != "" {
			t.Fatalf("release template must not set %s by default", key)
		}
	}
	if _, configured := example["targets"]; configured {
		t.Fatal("release template must not enable Fleet targets by default")
	}
	if example["api_key"] != "YOUR_API_KEY" {
		t.Fatalf("release template must contain an obvious API key placeholder")
	}
}
