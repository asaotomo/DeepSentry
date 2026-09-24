//go:build 386

package chat

import (
	"context"
	"log"

	"ai-edr/internal/config"
)

// Feishu long-connection SDK overflows int on 32-bit; keep the binary but refuse the channel.
func (s *Service) runFeishu(ctx context.Context, c config.ChatChannel, update func(string)) {
	update("connection_failed")
	log.Printf("chat channel=%s: 当前为 32 位架构，不支持飞书长连接；请使用 64 位构建，或改用其他平台/HTTP 回调通道", c.Name)
	<-ctx.Done()
}
