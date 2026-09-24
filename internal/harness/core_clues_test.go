package harness

import (
	"ai-edr/internal/analyzer"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestCoreCluesExtractDeduplicateAndRejectSecrets(t *testing.T) {
	state := NewAgentState(t.TempDir())
	text := `关键结论：10.10.8.7 连接 https://c2.example/a，Webshell 位于 /var/www/html/a.php
CVE-2026-12345 hash=d41d8cd98f00b204e9800998ecf8427e
api_key=secret-value should-not-survive`
	state.ObserveCoreClues(text, "tool/scan")
	state.ObserveCoreClues(text, "tool/recheck")
	prompt := state.CoreCluesPrompt(8000)
	for _, want := range []string{"10.10.8.7", "https://c2.example/a", "/var/www/html/a.php", "CVE-2026-12345", "d41d8cd98f00b204e9800998ecf8427e"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("core clue prompt lost %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "secret-value") {
		t.Fatalf("sensitive value reached clue board:\n%s", prompt)
	}
	if strings.Count(prompt, "[IP] 10.10.8.7") != 1 {
		t.Fatalf("duplicate clue was not merged:\n%s", prompt)
	}
	if !strings.Contains(prompt, "tool/scan | tool/recheck") {
		t.Fatalf("duplicate clue lost multi-source provenance:\n%s", prompt)
	}
}

func TestCoreClueBoardConcurrentMergeIsBounded(t *testing.T) {
	state := NewAgentState(t.TempDir())
	var wg sync.WaitGroup
	for i := 1; i <= 80; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			state.ObserveCoreClues(fmt.Sprintf("关键结论：发现地址 10.0.0.%d", i), "parallel")
		}()
	}
	wg.Wait()
	if got := len(state.CoreCluesSnapshot()); got > maxSessionCoreClues {
		t.Fatalf("clue board grew beyond bound: %d", got)
	}
}

func TestSubAgentMissionBriefCarriesGoalTodoAndClues(t *testing.T) {
	state := NewAgentState(t.TempDir())
	state.Todos = []TodoItem{{ID: "2", Content: "关联网络证据", Status: "in_progress"}}
	state.ObserveCoreClues("关键结论：攻击源 192.0.2.8", "main")
	history := []analyzer.Message{{Role: "user", Content: "调查生产服务器入侵"}, {Role: "user", Content: "补充：只读，不修改文件"}}
	brief := subAgentMissionBrief(&StepContext{State: state, History: &history}, "只分析网络连接", "network-analyst")
	for _, want := range []string{"调查生产服务器入侵", "只读，不修改文件", "关联网络证据", "192.0.2.8", "只分析网络连接", "唯一分工"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("mission brief lost %q:\n%s", want, brief)
		}
	}
}
