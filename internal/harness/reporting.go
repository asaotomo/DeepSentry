package harness

import (
	"fmt"
	"strings"
	"time"

	"ai-edr/internal/logger"
)

func recordActionEvidence(reporter *logger.Reporter, action AgentAction, result *ActionResult, runErr error, step int, status, approval, source string, duration time.Duration) error {
	if reporter == nil {
		return nil
	}
	redacted := RedactedAction(action)
	rationale := strings.TrimSpace(redacted.Thought)
	redacted.Thought = "" // reasoning is archived separately from the executed request
	redacted.ReasoningContent = ""
	input := actionToJSON(redacted)
	name := string(action.Type)
	if action.Type == ActionTool && action.ToolName != "" {
		name += "/" + action.ToolName
	}
	if source != "" {
		name = source + "/" + name
	}
	target := strings.TrimSpace(action.TargetName)
	if action.TargetHost != "" {
		if target != "" {
			target += " "
		}
		target += action.TargetHost
	}
	if target == "" {
		target = strings.TrimSpace(action.TargetSelector)
	}
	if target == "" && action.ToolArgs != nil {
		target = strings.TrimSpace(action.ToolArgs["selector"])
	}
	record := logger.EvidenceRecord{
		Kind: "action", Step: step, Status: status, Action: name,
		Target: target, Risk: action.RiskLevel, Approval: approval,
		Rationale: rationale, Input: input, DurationMS: duration.Milliseconds(),
	}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if result != nil {
		record.Output = result.Output
		record.Attachments = result.Attachments
		for _, item := range result.ToolResults {
			record.ToolResults = append(record.ToolResults, logger.EvidenceToolResult{
				ID: item.ID, Name: item.Name, Output: item.Output,
				Error: item.Error, Attachments: item.Attachments,
			})
		}
	}
	_, err := reporter.RecordEvidence(record)
	if err != nil {
		return fmt.Errorf("保存步骤 %d 证据失败: %w", step, err)
	}
	return nil
}
