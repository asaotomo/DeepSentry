package security

import (
	"ai-edr/internal/config"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	credentialKVPattern   = regexp.MustCompile(`(?i)(["']?(?:password|passwd|pwd|token|secret|api[_-]?key|authorization|access[_-]?key|private[_-]?key)["']?\s*[:=]\s*)(["']?)([^"'\s,}\]]+)(["']?)`)
	credentialFlagPattern = regexp.MustCompile(`(?i)((?:--?(?:password|passwd|pass|token|secret|api-key)|sshpass\s+-p)\s+)(["']?)([^"'\s]+)(["']?)`)
	bearerPattern         = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	credentialURLPattern  = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:/\s]+:)([^@\s/]+)(@)`)
	privateKeyPattern     = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	sensitiveJSONKey      = regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|api[_-]?key|authorization|access[_-]?key|private[_-]?key)`)
)

// RedactSensitiveText is the final egress guard for model history, reports,
// checkpoints and UI details. Tool-specific redaction is still useful, but no
// tool is allowed to rely on remembering it.
func RedactSensitiveText(text string) string {
	if text == "" {
		return text
	}
	return redactSensitiveTextWithSecrets(text, configuredSecrets())
}

func redactSensitiveTextWithSecrets(text string, secrets []string) string {
	if text == "" {
		return text
	}
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "***")
	}
	// Most checkpoint/tool-output strings contain no credential syntax. Avoid
	// scanning every such string with five regular expressions.
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "pass") && !strings.Contains(lower, "pwd") &&
		!strings.Contains(lower, "token") && !strings.Contains(lower, "secret") &&
		!strings.Contains(lower, "key") && !strings.Contains(lower, "author") &&
		!strings.Contains(lower, "bearer") && !strings.Contains(lower, "://") {
		return text
	}
	text = privateKeyPattern.ReplaceAllString(text, "[PRIVATE KEY REDACTED]")
	text = bearerPattern.ReplaceAllString(text, `${1}***`)
	text = credentialKVPattern.ReplaceAllString(text, `${1}${2}***${4}`)
	text = credentialFlagPattern.ReplaceAllString(text, `${1}${2}***${4}`)
	text = credentialURLPattern.ReplaceAllString(text, `${1}***${3}`)
	return text
}

// RedactJSON redacts decoded values before serializing them again. Applying
// text regexes to an already serialized JSON document can consume quote or
// backslash delimiters and produce an invalid checkpoint.
func RedactJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode JSON for redaction: %w", err)
	}
	redacted := redactJSONValue(decoded, "", configuredSecrets())
	out, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode redacted JSON: %w", err)
	}
	return out, nil
}

func redactJSONValue(value any, key string, secrets []string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, child := range typed {
			out[childKey] = redactJSONValue(child, childKey, secrets)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactJSONValue(child, key, secrets)
		}
		return out
	case string:
		if sensitiveJSONKey.MatchString(key) && typed != "" {
			return "***"
		}
		return redactSensitiveTextWithSecrets(typed, secrets)
	default:
		return value
	}
}

func configuredSecrets() []string {
	cfg := config.GlobalConfig
	values := []string{
		cfg.ApiKey,
		cfg.SSHPassword,
		cfg.SSHEnablePassword,
		cfg.TelnetPassword,
		cfg.TelnetEnablePassword,
		cfg.FTPPassword,
		cfg.BenchmarkToken,
		cfg.DingTalkWebhook,
		cfg.DingTalkSecret,
		cfg.FeishuWebhook,
		cfg.FeishuSecret,
		cfg.EmailGatewayToken,
	}
	for _, target := range cfg.Targets {
		values = append(values, target.Password, target.EnablePassword)
	}
	for _, server := range cfg.MCPServerConfigs {
		for key, value := range server.Env {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
				values = append(values, value)
			}
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) < 4 || value == "none" || value == "***" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	// Replace longer secrets first when values share a prefix.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}
