package harness

import (
	"ai-edr/internal/memory"
	"ai-edr/internal/skills"
)

// agentBuilder 内部构建器（对标 create_deep_agent 可扩展参数）
type agentBuilder struct {
	cfg        Config
	middleware []Middleware
}

// Option DeepAgent 配置选项
type Option func(*agentBuilder)

// WithMiddleware 追加或替换中间件（按 Name 去重，后者覆盖前者）
func WithMiddleware(mw Middleware) Option {
	return func(b *agentBuilder) {
		replaced := false
		for i, existing := range b.middleware {
			if existing.Name() == mw.Name() {
				b.middleware[i] = mw
				replaced = true
				break
			}
		}
		if !replaced {
			b.middleware = append(b.middleware, mw)
		}
	}
}

// WithSkillSources 自定义 Skill 来源目录
func WithSkillSources(sources []string) Option {
	return func(b *agentBuilder) {
		b.cfg.SkillSources = sources
	}
}

// WithWorkspaceDir 自定义 workspace 目录
func WithWorkspaceDir(dir string) Option {
	return func(b *agentBuilder) {
		b.cfg.WorkspaceDir = dir
	}
}

// WithMemoryScope 自定义 memory 作用域
func WithMemoryScope(scope string) Option {
	return func(b *agentBuilder) {
		b.cfg.MemoryScope = scope
	}
}

// WithSessionID 指定会话 ID（checkpoint 用）
func WithSessionID(id string) Option {
	return func(b *agentBuilder) {
		b.cfg.SessionID = id
	}
}

// WithNativeTools 启用/禁用原生 tool calling
func WithNativeTools(enabled bool) Option {
	return func(b *agentBuilder) {
		b.cfg.UseNativeTools = enabled
	}
}

// WithMCPServers 注册 MCP 服务器配置路径
func WithMCPServers(servers []string) Option {
	return func(b *agentBuilder) {
		b.cfg.MCPServers = servers
	}
}

// defaultMiddlewareStack 默认 middleware 栈
func defaultMiddlewareStack(catalog *skills.SkillCatalog, memStore *memory.Store) []Middleware {
	return []Middleware{
		NewMemoryMiddleware(memStore),
		NewTodoMiddleware(),
		NewSkillsMiddleware(catalog),
		NewToolsMiddleware(catalog),
		NewFilesystemMiddleware(memStore),
		NewSubAgentMiddleware(),
		NewContextMiddleware(),
	}
}

// SubAgentMiddlewareStack 子 Agent 用精简栈（无 sub-sub-agent）
func SubAgentMiddlewareStack(catalog *skills.SkillCatalog, memStore *memory.Store) []Middleware {
	return []Middleware{
		NewMemoryMiddleware(memStore),
		NewTodoMiddleware(),
		NewSkillsMiddleware(catalog),
		NewToolsMiddleware(catalog),
		NewFilesystemMiddleware(memStore),
		NewContextMiddleware(),
	}
}
