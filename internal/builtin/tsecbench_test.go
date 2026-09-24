package builtin

import (
	"ai-edr/internal/config"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTSecBenchListUsesConfiguredTokenHeader(t *testing.T) {
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi/v1/challenges" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("BENCHMARK_TOKEN"); got != "test-token" {
			t.Fatalf("BENCHMARK_TOKEN header = %q, want test-token", got)
		}
		_ = json.NewEncoder(w).Encode([]tsecChallenge{{
			UniqueCode:       "c-06",
			Description:      "graph test",
			Difficulty:       "easy",
			TotalScore:       100,
			FlagCount:        1,
			CorrectFlagCount: 0,
			ContainerStatus:  "stopped",
		}})
	}))
	defer srv.Close()

	config.GlobalConfig.BenchmarkBaseURL = srv.URL
	config.GlobalConfig.BenchmarkToken = "test-token"

	out, err := TSecBench(NewRuntime("", false), map[string]string{"action": "list", "unique_code": "c-06"})
	if err != nil {
		t.Fatalf("TsecBench list: %v", err)
	}
	if !strings.Contains(out, "c-06") || !strings.Contains(out, "total=1") {
		t.Fatalf("unexpected output: %s", out)
	}
	if strings.Contains(out, "test-token") {
		t.Fatalf("output leaked token: %s", out)
	}
}

func TestTSecBenchStartAndSubmitShowsFlag(t *testing.T) {
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("BENCHMARK_TOKEN"); got != "test-token" {
			t.Fatalf("BENCHMARK_TOKEN header = %q, want test-token", got)
		}
		switch r.URL.Path {
		case "/openapi/v1/challenges/start":
			if r.URL.Query().Get("unique_code") != "c-06" {
				t.Fatalf("unexpected unique_code: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"unique_code":"c-06","container_addr":["10.0.0.1:8080"]}`))
		case "/openapi/v1/challenges/submit":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode submit payload: %v", err)
			}
			if payload["unique_code"] != "c-06" || payload["flag"] != "flag{secret}" {
				t.Fatalf("unexpected submit payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"correct":true,"awarded":100,"cumulative_score":100,"correct_flag_count":1,"total_flag_count":1,"matched_flag_index":0}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	config.GlobalConfig.BenchmarkBaseURL = srv.URL
	config.GlobalConfig.BenchmarkToken = "test-token"

	startOut, err := TSecBench(NewRuntime("", false), map[string]string{"action": "start", "unique_code": "c-06"})
	if err != nil {
		t.Fatalf("TsecBench start: %v", err)
	}
	if !strings.Contains(startOut, "10.0.0.1:8080") {
		t.Fatalf("unexpected start output: %s", startOut)
	}

	submitOut, err := TSecBench(NewRuntime("", false), map[string]string{"action": "submit", "unique_code": "c-06", "flag": "flag{secret}"})
	if err != nil {
		t.Fatalf("TsecBench submit: %v", err)
	}
	if !strings.Contains(submitOut, "correct=true") {
		t.Fatalf("unexpected submit output: %s", submitOut)
	}
	if !strings.Contains(submitOut, "flag{secret}") {
		t.Fatalf("submit output should include flag for benchmark review: %s", submitOut)
	}
}

func TestTSecBenchAcceptsChallengeIDAlias(t *testing.T) {
	code, err := requireTsecCode(map[string]string{"challenge_id": "a-05"})
	if err != nil {
		t.Fatalf("requireTsecCode challenge_id: %v", err)
	}
	if code != "a-05" {
		t.Fatalf("code=%q want a-05", code)
	}
}
