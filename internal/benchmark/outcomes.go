package benchmark

import (
	"sort"
	"time"
)

// SecurityTaskFixture describes outcome-based, reproducible security tasks.
// The target/output fixture is intentionally external so the same observation
// can be replayed against legacy and Runtime v3 with an identical model.
type SecurityTaskFixture struct {
	ID               string
	Category         string
	Goal             string
	Fixture          string
	ExpectedTools    []string
	RequiredEvidence []string
}

func RuntimeSecurityTasks() []SecurityTaskFixture {
	return []SecurityTaskFixture{
		{"RT-IR-01", "应急", "建立主机应急基线", "fixtures/ir/linux_baseline", []string{"host_incident_baseline"}, []string{"process", "listen", "connection", "login"}},
		{"RT-IR-02", "应急", "定位异常外联进程", "fixtures/ir/c2_process", []string{"proc_socket_map"}, []string{"pid", "remote_ip"}},
		{"RT-IR-03", "应急", "核查异常自启动", "fixtures/ir/persistence", []string{"service_unit_audit"}, []string{"path", "exec_start"}},
		{"RT-IR-04", "应急", "排查 WebShell", "fixtures/ir/webshell", []string{"webshell_hunt"}, []string{"path", "code_signal", "connection"}},
		{"RT-LOG-01", "日志", "识别 SSH 爆破源", "fixtures/log/auth_bruteforce", []string{"read_log"}, []string{"source_ip", "failure_count"}},
		{"RT-LOG-02", "日志", "关联成功登录与爆破", "fixtures/log/auth_success", []string{"login_audit"}, []string{"source_ip", "success_time"}},
		{"RT-LOG-03", "日志", "分析 gzip 轮转日志", "fixtures/log/nginx_gzip/access.log.1.gz", []string{"read_gzip"}, []string{"request_path", "status"}},
		{"RT-LOG-04", "日志", "定位数据库认证失败", "fixtures/log/db_auth", []string{"db_log_read"}, []string{"account", "source_ip"}},
		{"RT-FOR-01", "取证", "识别伪装二进制", "fixtures/forensic/magic", []string{"file_ident", "file_hash"}, []string{"sha256", "file_type"}},
		{"RT-FOR-02", "取证", "从 PCAP 提取恶意域名", "fixtures/forensic/dns.pcap", []string{"pcap_analyze"}, []string{"domain", "source_ip"}},
		{"RT-FOR-03", "取证", "从 PCAP 提取 TLS SNI", "fixtures/forensic/tls.pcap", []string{"pcap_analyze"}, []string{"sni", "flow"}},
		{"RT-FOR-04", "取证", "解析可疑 Office 文档", "fixtures/forensic/dropper.docx", []string{"document_parse"}, []string{"metadata", "indicator"}},
		{"RT-FLEET-01", "Fleet", "汇总生产主机健康异常", "fixtures/fleet/health", []string{"fleet_inventory", "fleet_exec"}, []string{"target", "failure", "anomaly"}},
		{"RT-FLEET-02", "Fleet", "只读批量核查监听端口", "fixtures/fleet/ports", []string{"fleet_exec"}, []string{"target", "port"}},
		{"RT-FLEET-03", "Fleet", "读取多目标证据文件", "fixtures/fleet/files", []string{"fleet_file"}, []string{"target", "path", "hash"}},
		{"RT-FLEET-04", "Fleet", "SSH 中断后恢复汇总", "fixtures/fleet/ssh_reset", []string{"fleet_exec"}, []string{"target", "retry", "final_status"}},
		{"RT-WEB-01", "Web", "提取网页表单与脚本", "fixtures/web/forms", []string{"web_snapshot"}, []string{"form", "script"}},
		{"RT-WEB-02", "Web", "跟进分页安全公告", "fixtures/web/advisory", []string{"browser_browse"}, []string{"url", "advisory_id"}},
		{"RT-DB-01", "数据库", "审计 Redis 危险配置", "fixtures/db/redis", []string{"db_config_audit"}, []string{"config_path", "risk_item"}},
		{"RT-DB-02", "数据库", "识别 MySQL 服务版本", "fixtures/db/mysql_handshake", []string{"mysql_probe"}, []string{"version", "capability"}},
		{"RT-DB-03", "数据库", "提取 SQLite schema", "fixtures/db/sample.sqlite", []string{"sqlite_inspect"}, []string{"table", "column"}},
		{"RT-CTF-01", "CTF/AWD", "扫描标准 flag", "fixtures/ctf/flags", []string{"flag_scan"}, []string{"flag", "path"}},
		{"RT-CTF-02", "CTF/AWD", "检查 AWD 服务可用性", "fixtures/ctf/services", []string{"awd_service_check"}, []string{"target", "status"}},
		{"RT-CTF-03", "CTF/AWD", "解析题目 PCAP 线索", "fixtures/ctf/challenge.pcap", []string{"pcap_analyze"}, []string{"flow", "indicator"}},
	}
}

type TaskObservation struct {
	TaskID                  string
	Success                 bool
	SelectedTools           []string
	EvidenceKeys            []string
	ValidToolCalls          int
	InvalidToolCalls        int
	UnsupportedHighRisk     int
	CorrectionRetries       int
	RecoveredFailure        bool
	FailureWasRecoverable   bool
	ModifyingToolDuplicates int
	Tokens                  int
	Latency                 time.Duration
}

type OutcomeScore struct {
	TaskSuccessRate          float64
	CorrectToolSelectionRate float64
	EvidenceCoverageRate     float64
	ValidToolCallRate        float64
	UnsupportedHighRiskRate  float64
	RecoverableFailureRate   float64
	ModifyingDuplicates      int
	AverageCorrectionRetries float64
	P95Tokens                int
	P95Latency               time.Duration
}

type AcceptanceDecision struct {
	Passed  bool
	Reasons []string
}

func EvaluateRuntimeV3Gate(legacy, v3 OutcomeScore) AcceptanceDecision {
	decision := AcceptanceDecision{Passed: true}
	require := func(ok bool, reason string) {
		if !ok {
			decision.Passed = false
			decision.Reasons = append(decision.Reasons, reason)
		}
	}
	require(v3.TaskSuccessRate >= 0.85 || v3.TaskSuccessRate-legacy.TaskSuccessRate >= 0.15, "任务成功率未达到 85% 或相对提升 15 个百分点")
	require(v3.ValidToolCallRate >= 0.99, "有效工具调用率低于 99%")
	require(v3.EvidenceCoverageRate >= 0.95, "关键结论证据覆盖率低于 95%")
	require(v3.UnsupportedHighRiskRate <= 0.02, "无依据高风险结论超过 2%")
	require(v3.RecoverableFailureRate >= 0.95, "可恢复故障成功率低于 95%")
	require(v3.ModifyingDuplicates == 0, "恢复后发生修改型工具重复执行")
	if legacy.P95Tokens > 0 {
		require(float64(v3.P95Tokens) <= float64(legacy.P95Tokens)*1.10, "p95 Token 超过 legacy 110%")
	}
	if legacy.P95Latency > 0 {
		require(v3.P95Latency <= time.Duration(float64(legacy.P95Latency)*1.15), "p95 延迟超过 legacy 115%")
	}
	return decision
}

// EvaluateControlledRuntimeV3Gate applies correctness and recovery thresholds
// to deterministic in-process fixtures. Their microsecond timing is dominated
// by scheduler noise and is not a meaningful proxy for provider p95 latency;
// real-model RuntimeProbeAB owns the token and latency guardrails.
func EvaluateControlledRuntimeV3Gate(legacy, v3 OutcomeScore) AcceptanceDecision {
	legacy.P95Tokens = 0
	legacy.P95Latency = 0
	v3.P95Tokens = 0
	v3.P95Latency = 0
	return EvaluateRuntimeV3Gate(legacy, v3)
}

func EvaluateWorkflowExperiment(internal, candidate OutcomeScore) AcceptanceDecision {
	decision := AcceptanceDecision{Passed: true}
	if candidate.TaskSuccessRate-internal.TaskSuccessRate < 0.10 {
		decision.Passed = false
		decision.Reasons = append(decision.Reasons, "成功率提升不足 10 个百分点")
	}
	if internal.P95Latency > 0 && candidate.P95Latency > time.Duration(float64(internal.P95Latency)*1.15) {
		decision.Passed = false
		decision.Reasons = append(decision.Reasons, "p95 延迟增加超过 15%")
	}
	return decision
}

func EvaluateOutcomes(tasks []SecurityTaskFixture, observations []TaskObservation) OutcomeScore {
	byID := make(map[string]TaskObservation, len(observations))
	for _, observation := range observations {
		byID[observation.TaskID] = observation
	}
	var score OutcomeScore
	var expectedTools, selectedTools, requiredEvidence, foundEvidence int
	var valid, invalid, recoverable, recovered, unsupported, retries int
	var tokens []int
	var latencies []time.Duration
	for _, task := range tasks {
		observation, ok := byID[task.ID]
		if !ok {
			continue
		}
		if observation.Success {
			score.TaskSuccessRate++
		}
		expectedTools += len(task.ExpectedTools)
		selectedTools += overlap(task.ExpectedTools, observation.SelectedTools)
		requiredEvidence += len(task.RequiredEvidence)
		foundEvidence += overlap(task.RequiredEvidence, observation.EvidenceKeys)
		valid += observation.ValidToolCalls
		invalid += observation.InvalidToolCalls
		unsupported += observation.UnsupportedHighRisk
		retries += observation.CorrectionRetries
		if observation.FailureWasRecoverable {
			recoverable++
			if observation.RecoveredFailure {
				recovered++
			}
		}
		score.ModifyingDuplicates += observation.ModifyingToolDuplicates
		tokens = append(tokens, observation.Tokens)
		latencies = append(latencies, observation.Latency)
	}
	count := len(observations)
	if count > 0 {
		score.TaskSuccessRate /= float64(count)
		score.UnsupportedHighRiskRate = float64(unsupported) / float64(count)
		score.AverageCorrectionRetries = float64(retries) / float64(count)
	}
	score.CorrectToolSelectionRate = ratio(selectedTools, expectedTools)
	score.EvidenceCoverageRate = ratio(foundEvidence, requiredEvidence)
	score.ValidToolCallRate = ratio(valid, valid+invalid)
	score.RecoverableFailureRate = ratio(recovered, recoverable)
	sort.Ints(tokens)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(tokens) > 0 {
		i := p95Index(len(tokens))
		score.P95Tokens = tokens[i]
		score.P95Latency = latencies[i]
	}
	return score
}

func overlap(expected, actual []string) int {
	set := make(map[string]bool, len(actual))
	for _, value := range actual {
		set[value] = true
	}
	count := 0
	for _, value := range expected {
		if set[value] {
			count++
		}
	}
	return count
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func p95Index(length int) int {
	index := (95*length + 99) / 100
	if index < 1 {
		index = 1
	}
	return index - 1
}
