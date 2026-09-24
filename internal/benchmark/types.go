package benchmark

import "time"

// Category 评测维度
type Category string

const (
	CatLLM        Category = "llm"
	CatLocalTool  Category = "local_tool"
	CatRemoteTool Category = "remote_tool"
	CatLinkage    Category = "linkage"
	CatFilesystem Category = "filesystem"
	CatAgent      Category = "agent"
	CatHarness    Category = "harness"
	CatResilience Category = "resilience"
	CatForensics  Category = "forensics"
	CatIncident   Category = "incident"
	CatSecurity   Category = "security"
)

// CategoryMeta 维度权重与说明
var CategoryMeta = map[Category]struct {
	Weight      float64
	DisplayName string
}{
	CatLLM:        {10, "LLM 接入与协议"},
	CatLocalTool:  {7, "控制端原生工具"},
	CatRemoteTool: {14, "目标机 BusyBox 工具"},
	CatLinkage:    {11, "本地-远程联动"},
	CatFilesystem: {7, "文件系统 (SFTP/本地)"},
	CatForensics:  {12, "取证分析工具"},
	CatIncident:   {10, "安全应急场景"},
	CatAgent:      {14, "Agent 编排 (Harness)"},
	CatHarness:    {5, "Middleware/Skills/Memory"},
	CatResilience: {5, "高可用与恢复"},
	CatSecurity:   {5, "安全边界与合规"},
}

// Scenario 单个测试场景
type Scenario struct {
	ID             string
	Category       Category
	Name           string
	Description    string
	RequiresRemote bool
	RequiresLLM    bool
	SkipLocalOnly  bool // 仅远程模式运行
	Run            func(ctx *Context) Result
}

// Result 场景执行结果
type Result struct {
	Passed   bool
	Partial  bool
	Score    float64 // 0-100
	Latency  time.Duration
	Message  string
	Evidence string
	Metrics  map[string]float64
}

// Context benchmark 运行时上下文
type Context struct {
	RemoteAvailable bool
	UseNativeTools  bool
}

// SuiteReport 完整报告
type SuiteReport struct {
	Timestamp    time.Time
	Provider     string
	Model        string
	RemoteMode   bool
	SSHHost      string
	Scenarios    []ScenarioReport
	CategoryAvg  map[Category]float64
	OverallScore float64
	Grade        string
	Duration     time.Duration
}

// ScenarioReport 单场景报告
type ScenarioReport struct {
	ID       string
	Category Category
	Name     string
	Passed   bool
	Partial  bool
	Score    float64
	Latency  time.Duration
	Message  string
	Evidence string
	Metrics  map[string]float64
}

func scorePass(latency time.Duration, threshold time.Duration) float64 {
	if latency <= threshold {
		return 100
	}
	if latency <= threshold*2 {
		return 85
	}
	return 70
}

func mkResult(passed, partial bool, score float64, lat time.Duration, msg, ev string) Result {
	return Result{Passed: passed, Partial: partial, Score: score, Latency: lat, Message: msg, Evidence: ev}
}
