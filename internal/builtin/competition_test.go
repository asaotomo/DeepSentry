package builtin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAWDServiceCheckConcurrentHealthAndEvidence(t *testing.T) {
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(35 * time.Millisecond)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else if r.URL.Path == "/auth" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = fmt.Fprint(w, "<title>AWD &amp; 服务</title>healthy")
	}))
	defer server.Close()
	output, err := AWDServiceCheckWithOptions(server.URL+"/ok,"+server.URL+"/fail,"+server.URL+"/auth", 2, 3, 0, "healthy")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"UP HTTP 200", "DOWN HTTP 503", "WARN HTTP 403", "title=\"AWD & 服务\"", "UP=1 WARN=1 DOWN=1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q: %s", want, output)
		}
	}
	if peak.Load() < 2 {
		t.Fatalf("probes were sequential: peak=%d", peak.Load())
	}
	output, err = AWDServiceCheckWithOptions(server.URL+"/ok", 2, 1, http.StatusCreated, "missing")
	if err != nil || !strings.Contains(output, "WARN HTTP 200") || !strings.Contains(output, "expected_text_missing=true") {
		t.Fatalf("expected health mismatch: output=%s err=%v", output, err)
	}
	output, err = AWDServiceCheckWithOptions(server.URL+"/redirect", 2, 1, http.StatusFound, "")
	if err != nil || !strings.Contains(output, "UP HTTP 302") {
		t.Fatalf("redirect status was lost: output=%s err=%v", output, err)
	}
	output, err = AWDServiceCheckWithOptions(server.URL+"/fail", 2, 1, http.StatusOK, "")
	if err != nil || !strings.Contains(output, "DOWN HTTP 503") {
		t.Fatalf("server failure was downgraded: output=%s err=%v", output, err)
	}
}

func TestCompetitionAnswerCheckFindsEvidenceAndRequiredSections(t *testing.T) {
	answer := `【任务状态】已完成
【结论】端口无物理故障。
【关键证据】
1. display interface brief -> GE1/0/1 up/up。
2. display interface GE1/0/1 -> input rate 10 bps, 0 errors。
3. ping 10.0.0.1 -> 0% loss, 1 ms。
【处置/答案】无需修改配置。
【复验】重复 ping 仍为 0% loss。
【AI 复核与纠错】已否定“端口 down”假设。
【风险与回滚】无变更，无需回滚。`
	out, err := CompetitionAnswerCheck("排查端口故障", answer)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"估算=100/100", "缺失=无", "AI纠错=true", "顺序正确=true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestCompetitionAnswerCheckRejectsEmptyDraft(t *testing.T) {
	if _, err := CompetitionAnswerCheck("task", " "); err == nil {
		t.Fatal("expected empty answer error")
	}
}
