package logger

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/security"
)

// EvidenceRecord is the redacted, append-only audit entry for one observable
// conversation turn or execution. Hashes cover the redacted record: secrets
// removed by policy cannot be reconstructed from this archive.
type EvidenceRecord struct {
	Sequence     int                        `json:"sequence"`
	ID           string                     `json:"id"`
	Timestamp    time.Time                  `json:"timestamp"`
	Kind         string                     `json:"kind"`
	Step         int                        `json:"step,omitempty"`
	Status       string                     `json:"status,omitempty"`
	Action       string                     `json:"action,omitempty"`
	Target       string                     `json:"target,omitempty"`
	Risk         string                     `json:"risk,omitempty"`
	Approval     string                     `json:"approval,omitempty"`
	Rationale    string                     `json:"rationale,omitempty"`
	Input        string                     `json:"input,omitempty"`
	Output       string                     `json:"output,omitempty"`
	Error        string                     `json:"error,omitempty"`
	DurationMS   int64                      `json:"duration_ms,omitempty"`
	Attachments  []analyzer.ImageAttachment `json:"attachments,omitempty"`
	ToolResults  []EvidenceToolResult       `json:"tool_results,omitempty"`
	OutputSHA256 string                     `json:"output_sha256,omitempty"`
	PrevSHA256   string                     `json:"prev_sha256,omitempty"`
	RecordSHA256 string                     `json:"record_sha256,omitempty"`
}

type EvidenceToolResult struct {
	ID          string                     `json:"id,omitempty"`
	Name        string                     `json:"name"`
	Output      string                     `json:"output,omitempty"`
	Error       string                     `json:"error,omitempty"`
	Attachments []analyzer.ImageAttachment `json:"attachments,omitempty"`
}

func (r *Reporter) EvidencePath() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evidencePath
}

// RecordUserHistory archives every real user turn before context compaction.
// Message IDs make repeated identical replies distinct and prevent duplicate
// entries when the same Reporter serves several turns of one TUI session.
func (r *Reporter) RecordUserHistory(history *[]analyzer.Message) error {
	if r == nil || history == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range *history {
		message := &(*history)[i]
		if !analyzer.IsRealUserTurn(*message) {
			continue
		}
		if message.ID == "" {
			message.ID = fmt.Sprintf("user_%d_%d", time.Now().UnixNano(), i)
		}
		if r.seenUserIDs[message.ID] {
			continue
		}
		record := EvidenceRecord{
			Kind: "user_message", Status: "received", Action: message.ID,
			Input: message.Content, Attachments: message.Attachments,
		}
		ref, err := r.appendEvidenceLocked(record)
		if err != nil {
			return err
		}
		r.seenUserIDs[message.ID] = true
		preview := safeUTF8Prefix(security.RedactSensitiveText(message.Content), 300)
		if _, err := fmt.Fprintf(r.file, "### [%s] 用户原话 · %s\n\n%s\n\n完整原话在证据 `%s`。报告这里只保留可读摘录。\n\n", time.Now().Format("15:04:05"), ref, preview, ref); err != nil {
			return err
		}
		if err := r.file.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// RecordEvidence writes one durable journal entry and a short report index.
// The complete redacted output stays in JSONL; Markdown remains readable.
func (r *Reporter) RecordEvidence(record EvidenceRecord) (string, error) {
	if r == nil {
		return "", nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, err := r.appendEvidenceLocked(record)
	if err != nil {
		return "", err
	}
	if r.file == nil {
		return ref, nil
	}
	if record.Kind == "action" || record.Kind == "assistant_question" {
		status := record.Status
		if status == "" {
			status = "observed"
		}
		title := record.Action
		if title == "" {
			title = record.Kind
		}
		var b strings.Builder
		fmt.Fprintf(&b, "### [%s] 步骤 %d · %s · %s\n\n", time.Now().Format("15:04:05"), record.Step, title, status)
		fmt.Fprintf(&b, "- 证据：`%s`（`%s`）\n", ref, filepath.Base(r.evidencePath))
		if record.Target != "" {
			fmt.Fprintf(&b, "- 目标：%s\n", security.RedactSensitiveText(record.Target))
		}
		if record.Risk != "" || record.Approval != "" {
			fmt.Fprintf(&b, "- 风险/授权：%s / %s\n", riskLabel(record.Risk), approvalLabel(record.Approval))
		}
		if record.Rationale != "" {
			fmt.Fprintf(&b, "- 判断说明：%s\n", safeUTF8Prefix(security.RedactSensitiveText(record.Rationale), 240))
		}
		if record.DurationMS > 0 {
			fmt.Fprintf(&b, "- 耗时：%d ms\n", record.DurationMS)
		}
		if record.Input != "" {
			input := safeUTF8Prefix(security.RedactSensitiveText(record.Input), 360)
			if len(input) < len(security.RedactSensitiveText(record.Input)) {
				input += "…"
			}
			fmt.Fprintf(&b, "- 请求摘要：`%s`\n", strings.ReplaceAll(input, "`", "'"))
		}
		if record.Error != "" {
			fmt.Fprintf(&b, "- 错误：%s\n", safeUTF8Prefix(security.RedactSensitiveText(record.Error), 400))
		}
		if record.Output != "" {
			preview := safeUTF8Prefix(security.RedactSensitiveText(record.Output), 900)
			if len(preview) < len(security.RedactSensitiveText(record.Output)) {
				preview += "\n…（报告摘要；完整脱敏输出见证据档案）"
			}
			fence := markdownFence(preview)
			fmt.Fprintf(&b, "\n%stext\n%s\n%s\n", fence, preview, fence)
		}
		b.WriteString("\n")
		if _, err := io.WriteString(r.file, b.String()); err != nil {
			return ref, err
		}
		if err := r.file.Sync(); err != nil {
			return ref, err
		}
	}
	return ref, nil
}

func (r *Reporter) LogFinal(content string) error {
	if r == nil {
		return nil
	}
	ref, err := r.RecordEvidence(EvidenceRecord{Kind: "final_report", Status: "reported", Output: content})
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return fmt.Errorf("报告文件不可写")
	}
	entry := fmt.Sprintf("## 最终结论\n\n以下文字是模型基于已归档步骤给出的结论。命令、文件和数值以证据编号为准；写明证据不足的内容不能当成已核实事实。\n\n### [%s] 结论 · %s\n\n%s\n\n", time.Now().Format("15:04:05"), ref, security.RedactSensitiveText(content))
	if _, err := io.WriteString(r.file, entry); err != nil {
		return err
	}
	return r.file.Sync()
}

func (r *Reporter) appendEvidenceLocked(record EvidenceRecord) (string, error) {
	if r.evidence == nil {
		return "", fmt.Errorf("证据档案不可写")
	}
	record.Sequence = r.sequence + 1
	record.ID = fmt.Sprintf("E-%06d", record.Sequence)
	record.Timestamp = time.Now().UTC()
	record.PrevSHA256 = r.previousHash
	record.Rationale = security.RedactSensitiveText(record.Rationale)
	record.Input = security.RedactSensitiveText(record.Input)
	record.Output = security.RedactSensitiveText(record.Output)
	record.Error = security.RedactSensitiveText(record.Error)
	record.Target = security.RedactSensitiveText(record.Target)
	for i := range record.ToolResults {
		record.ToolResults[i].Output = security.RedactSensitiveText(record.ToolResults[i].Output)
		record.ToolResults[i].Error = security.RedactSensitiveText(record.ToolResults[i].Error)
	}
	if record.Output != "" {
		sum := sha256.Sum256([]byte(record.Output))
		record.OutputSHA256 = fmt.Sprintf("%x", sum[:])
	}
	canonical, err := security.RedactJSON(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	record.RecordSHA256 = fmt.Sprintf("%x", sum[:])
	raw, err := security.RedactJSON(record)
	if err != nil {
		return "", err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", err
	}
	compact.WriteByte('\n')
	if _, err := io.WriteString(r.evidence, compact.String()); err != nil {
		return "", err
	}
	if err := r.evidence.Sync(); err != nil {
		return "", err
	}
	r.noteEvidenceLocked(record)
	r.sequence = record.Sequence
	r.previousHash = record.RecordSHA256
	return record.ID, nil
}

// RecordSessionOutcome archives runs that stop without a model conclusion,
// so a cancelled, failed, or paused session cannot be read as a finished audit.
func (r *Reporter) RecordSessionOutcome(status, reason, detail string) error {
	if r == nil || r.HasFinal() {
		return nil
	}
	_, err := r.RecordEvidence(EvidenceRecord{
		Kind: "session_outcome", Status: status, Action: reason, Output: detail,
	})
	if err != nil || r.file == nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	body := sessionOutcomeText(status, reason, detail)
	_, err = fmt.Fprintf(r.file, "## 会话结束\n\n%s\n\n", body)
	if err != nil {
		return err
	}
	return r.file.Sync()
}

func (r *Reporter) HasFinal() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sawFinal
}

func (r *Reporter) noteEvidenceLocked(record EvidenceRecord) {
	switch record.Kind {
	case "user_message":
		r.userTurns++
	case "final_report":
		r.sawFinal = true
	case "action", "assistant_question":
		r.actions++
		switch strings.ToLower(record.Risk) {
		case "high":
			r.highRisk++
		case "medium":
			r.mediumRisk++
		case "low":
			r.lowRisk++
		}
		switch record.Status {
		case "denied", "blocked":
			if record.Status == "denied" {
				r.denied++
			} else {
				r.blocked++
			}
		case "error", "empty_result":
			r.failures++
		}
	}
}

func sessionOutcomeText(status, reason, detail string) string {
	label := map[string]string{
		"cancelled":      "会话已取消，不能把中断前的中间结果当成最终结论。",
		"failed":         "会话因错误结束，失败步骤的证据仍然有效，但整体结论未完成。",
		"awaiting_input": "会话停在等待补充。恢复前不要把当前内容当成已完成报告。",
		"max_steps":      "达到本轮步数上限后暂停。已归档步骤有效，结论尚未收束。",
	}[status]
	if label == "" {
		label = "会话未以最终结论结束。"
	}
	text := label
	if reason != "" {
		text += " 原因：`" + reason + "`。"
	}
	if strings.TrimSpace(detail) != "" {
		text += " " + safeUTF8Prefix(security.RedactSensitiveText(detail), 400)
	}
	return text
}

func riskLabel(risk string) string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "high":
		return "高"
	case "medium":
		return "中"
	case "low":
		return "低"
	case "":
		return "未分级"
	default:
		return risk
	}
}

func approvalLabel(approval string) string {
	switch approval {
	case "confirmed":
		return "人工确认后执行"
	case "auto":
		return "低风险自动执行"
	case "batch":
		return "无人值守自动执行"
	case "user_denied":
		return "用户拒绝"
	case "loop_guard":
		return "循环守卫拦截"
	case "recovery_guard":
		return "恢复保护拦截"
	case "subagent_policy":
		return "子 Agent 策略限制"
	case "not_applicable":
		return "无需执行授权"
	case "":
		return "未记录"
	default:
		return approval
	}
}

// VerifyEvidenceJournal checks sequence, per-output digest and the SHA256
// chain. Compare its final hash with the report footer to detect tail removal.
func VerifyEvidenceJournal(path string) (int, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	count := 0
	previous := ""
	for scanner.Scan() {
		var record EvidenceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return count, previous, err
		}
		count++
		if record.Sequence != count || record.ID != fmt.Sprintf("E-%06d", count) || record.PrevSHA256 != previous {
			return count, previous, fmt.Errorf("证据 %d 序号或链前值不匹配", count)
		}
		if record.Output != "" {
			sum := sha256.Sum256([]byte(record.Output))
			if record.OutputSHA256 != fmt.Sprintf("%x", sum[:]) {
				return count, previous, fmt.Errorf("证据 %d 输出摘要不匹配", count)
			}
		}
		claimed := record.RecordSHA256
		record.RecordSHA256 = ""
		canonical, err := security.RedactJSON(record)
		if err != nil {
			return count, previous, err
		}
		sum := sha256.Sum256(canonical)
		if claimed != fmt.Sprintf("%x", sum[:]) {
			return count, previous, fmt.Errorf("证据 %d SHA256 不匹配", count)
		}
		previous = claimed
	}
	if err := scanner.Err(); err != nil {
		return count, previous, err
	}
	return count, previous, nil
}
