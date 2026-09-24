package analyzer

import (
	"testing"

	"ai-edr/internal/mcp"
)

func TestFofaMapIntentRoutesPreflightSearchAndExport(t *testing.T) {
	tools := make([]mcp.ExternalTool, 0, len(mcp.FofaMapKnownToolNames()))
	for _, name := range mcp.FofaMapKnownToolNames() {
		tools = append(tools, mcp.ExternalTool{Name: "assets__" + name, OriginalName: name, Server: "fofamap"})
	}
	selected := selectMCPToolsForContext(tools, 12, "用 FOFA 规则查询 VPN 公网资产并导出 CSV", nil)
	names := map[string]bool{}
	for _, tool := range selected {
		names[tool.OriginalName] = true
	}
	for _, want := range []string{"fofa_account", "fofa_fields", "fofa_validate_query", "fofa_rules", "fofa_search", "fofa_export"} {
		if !names[want] {
			t.Fatalf("FofaMap workflow missing %s, selected=%v", want, names)
		}
	}
	if names["nuclei_execute"] {
		t.Fatal("passive FOFA export task unexpectedly preferred active nuclei_execute")
	}
}

func TestFofaMapExplicitScanRoutesPlanAndExecute(t *testing.T) {
	tools := []mcp.ExternalTool{
		{Name: "fofamap__fofa_account", OriginalName: "fofa_account", Server: "fofamap"},
		{Name: "fofamap__fofa_fields", OriginalName: "fofa_fields", Server: "fofamap"},
		{Name: "fofamap__fofa_validate_query", OriginalName: "fofa_validate_query", Server: "fofamap"},
		{Name: "fofamap__nuclei_plan", OriginalName: "nuclei_plan", Server: "fofamap"},
		{Name: "fofamap__nuclei_execute", OriginalName: "nuclei_execute", Server: "fofamap"},
	}
	selected := selectMCPToolsForContext(tools, 9, "对已授权目标执行 nuclei 主动扫描", nil)
	names := map[string]bool{}
	for _, tool := range selected {
		names[tool.OriginalName] = true
	}
	if !names["nuclei_plan"] || !names["nuclei_execute"] {
		t.Fatalf("explicit scan did not route plan+execute: %v", names)
	}
}
