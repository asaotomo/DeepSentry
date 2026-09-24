package analyzer

import (
	"ai-edr/internal/config"
	"testing"
)

func BenchmarkNativeToolDefinitionsForRequest(b *testing.B) {
	cfg := config.Config{AgentRuntime: "v3", Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: "https://example.com/v1", ModelName: "large", ContextWindowTokens: 1_048_576}
	messages := []Message{
		{Role: "system", Content: "【本任务已验证工具】后续轮次优先保留这些工具的完整 schema: pcap_analyze, webshell_hunt"},
		{Role: "user", Content: "继续分析服务器日志并检查异常网络连接"},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if defs := nativeToolDefinitionsForRequest(cfg, messages); len(defs) == 0 {
			b.Fatal("empty tool definitions")
		}
	}
}
