package executor

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ai-edr/internal/config"
)

func TestMatchTargets(t *testing.T) {
	targets := []config.TargetConfig{
		{Name: "web-01", Protocol: "ssh", Host: "10.0.0.1:22", Tags: []string{"prod", "web"}},
		{Name: "legacy-01", Protocol: "telnet", Host: "10.0.0.2:23", Tags: []string{"legacy"}},
		{Name: "ftp-01", Protocol: "ftp", Host: "10.0.0.3:21", Tags: []string{"evidence"}},
	}
	if got := MatchTargets(targets, "prod"); len(got) != 1 || got[0].Name != "web-01" {
		t.Fatalf("tag selector failed: %#v", got)
	}
	if got := MatchTargets(targets, "telnet"); len(got) != 1 || got[0].Name != "legacy-01" {
		t.Fatalf("protocol selector failed: %#v", got)
	}
	if got := MatchTargets(targets, "all"); len(got) != 3 {
		t.Fatalf("all selector failed: %#v", got)
	}
	if got := MatchTargets(targets, "prod,ssh"); len(got) != 1 || got[0].Name != "web-01" {
		t.Fatalf("selector intersection failed: %#v", got)
	}
	if got := MatchTargets(targets, "tag:prod|protocol:ftp"); len(got) != 2 {
		t.Fatalf("selector union failed: %#v", got)
	}
	if got := MatchTargets(targets, "host:10.0.0.1"); len(got) != 1 || got[0].Name != "web-01" {
		t.Fatalf("host without port failed: %#v", got)
	}
	if got := MatchTargets(targets, "prod,telnet"); len(got) != 0 {
		t.Fatalf("overbroad selector unexpectedly matched: %#v", got)
	}
}

func TestFormatFleetResults(t *testing.T) {
	results := []FleetResult{
		{Target: config.TargetConfig{Name: "a", Protocol: "ssh", Host: "h"}, Success: true, Output: "ok", Duration: time.Millisecond},
		{Target: config.TargetConfig{Name: "b", Protocol: "ftp", Host: "f"}, Success: false, Error: "no shell", Duration: time.Millisecond},
	}
	out := FormatFleetResults(results)
	if !strings.Contains(out, "1/2 成功") || !strings.Contains(out, "[ERR] b") {
		t.Fatalf("unexpected format: %s", out)
	}
}

func TestFleetTruncationPreservesUTF8(t *testing.T) {
	value := strings.Repeat("界", 668)
	got := truncateFleetOutput(value, 2000)
	if !utf8.ValidString(got) || !strings.Contains(got, "已截断") {
		t.Fatalf("invalid truncated output: %q", got)
	}
}

func TestRunFleetRejectsFTPShellExec(t *testing.T) {
	results := RunFleet([]config.TargetConfig{
		{Name: "ftp-01", Protocol: "ftp", Host: "127.0.0.1:21"},
	}, "all", "whoami", 1)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %#v", results)
	}
	if results[0].Success || !strings.Contains(results[0].Error, "FTP 目标不支持 shell 命令") {
		t.Fatalf("expected ftp shell rejection, got %#v", results[0])
	}
}

func TestRunFleetKeepsInventoryOrder(t *testing.T) {
	results := RunFleet([]config.TargetConfig{
		{Name: "same", Protocol: "ftp", Host: "first:21"},
		{Name: "same", Protocol: "ftp", Host: "second:21"},
	}, "all", "whoami", 2)
	if len(results) != 2 || results[0].Target.Host != "first:21" || results[1].Target.Host != "second:21" {
		t.Fatalf("results lost inventory order: %#v", results)
	}
}

func TestRunFleetStopCancelsQueuedTargetsWithoutConnecting(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	targets := []config.TargetConfig{
		{Name: "a", Protocol: "ssh", Host: "192.0.2.1:22"},
		{Name: "b", Protocol: "telnet", Host: "192.0.2.2:23"},
	}
	results := RunFleetWithProgressAndStop(targets, "all", "sleep 60", 1, nil, stop)
	if len(results) != len(targets) {
		t.Fatalf("expected canceled result per target, got %#v", results)
	}
	for _, result := range results {
		if result.Success || !strings.Contains(result.Error, "取消") {
			t.Fatalf("target was not canceled: %#v", result)
		}
	}
}
