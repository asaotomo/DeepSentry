package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/collector"
	"ai-edr/internal/security"
	"encoding/json"
	"fmt"
	"strings"
)

type commandRiskReview struct {
	Risk       string  `json:"risk"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

func reviewCommandRiskWithAI(sysCtx collector.SystemContext, command, ruleReason string) (risk, reason string, ok bool) {
	command = strings.TrimSpace(command)
	ruleReason = strings.TrimSpace(ruleReason)
	if command == "" {
		return "", "", false
	}
	// Risk review is an external model call. Preserve the command's structure
	// for classification, but never send credentials to that secondary call.
	command = security.RedactSensitiveText(command)

	prompt := fmt.Sprintf(`请复核下面这条命令是否真的需要高风险确认。

判定标准：
- low：只读取、枚举、查看、统计信息，不写文件、不改配置、不启停服务、不删除、不提权、不持久化、不执行下载脚本。
- high：会删除/覆盖/写入文件，修改权限/账号/服务/注册表/计划任务，启停系统或服务，执行远程脚本，扫描攻击，建立反连/隧道，或有明显副作用。
- 如果命令链里只有 echo/hostname/whoami/cd/dir/ls/cat/grep/findstr/tasklist/netstat/ipconfig/ver 等观测动作，应为 low。
- 2>&1、1>&2 只是合并标准输出/错误，不是写文件。
- 只有能明确确认整条命令链无副作用时才返回 low；不确定时返回 high。

只输出 JSON，不要解释：
{"risk":"low|high","reason":"一句话原因","confidence":0.95}

系统: %s/%s user=%s host=%s
规则命中原因: %s
命令:
%s`, sysCtx.OS, sysCtx.Arch, sysCtx.Username, sysCtx.Hostname, ruleReason, command)

	result, err := analyzer.CallLLMWithRetry([]analyzer.Message{
		{Role: "system", Content: "你是命令安全复核器，只做风险分类，必须输出严格 JSON。"},
		{Role: "user", Content: prompt},
	}, false, nil)
	if err != nil {
		return "", "", false
	}

	review, ok := parseCommandRiskReview(result.Content)
	if !ok {
		return "", "", false
	}
	risk = strings.ToLower(strings.TrimSpace(review.Risk))
	reason = strings.TrimSpace(review.Reason)
	if reason == "" {
		reason = "AI 风险复核"
	}
	if review.Confidence <= 0 {
		review.Confidence = 0.5
	}
	if risk != "low" && risk != "high" {
		return "", "", false
	}
	if risk == "low" && review.Confidence < 0.6 {
		return "", "", false
	}
	return risk, reason, true
}

func parseCommandRiskReview(raw string) (commandRiskReview, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return commandRiskReview{}, false
	}
	jsonPart := extractFirstJSONObject(raw)
	if jsonPart == "" {
		return commandRiskReview{}, false
	}
	var review commandRiskReview
	if err := json.Unmarshal([]byte(jsonPart), &review); err != nil {
		return commandRiskReview{}, false
	}
	return review, true
}

func extractFirstJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}
