package chat

import (
	"os"

	"ai-edr/internal/config"
)

func secret(c config.ChatChannel) string {
	if c.AppSecret != "" {
		return c.AppSecret
	}
	return os.Getenv(c.SecretEnv)
}

func ptr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
