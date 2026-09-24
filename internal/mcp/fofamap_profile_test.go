package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFofaMapV201ProfileCoversAuthoritativeServerContract(t *testing.T) {
	want := []string{
		"fofa_account", "fofa_agent_run", "fofa_export", "fofa_fields", "fofa_host_profile",
		"fofa_icon_search", "fofa_job_status", "fofa_rules", "fofa_search", "fofa_search_next",
		"fofa_stats", "fofa_syntax", "fofa_validate_query", "nuclei_execute", "nuclei_plan",
	}
	got := FofaMapKnownToolNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("FofaMap v2.0.1 profile drift\ngot=%v\nwant=%v", got, want)
	}
}

func TestHasFofaMapTools(t *testing.T) {
	empty := &Registry{tools: map[string]*ExternalTool{}, handlers: map[string]ToolHandler{}, aliases: map[string]string{}, ambiguous: map[string]bool{}}
	if empty.HasFofaMapTools() {
		t.Fatal("empty registry should not report FofaMap")
	}
	ready := &Registry{tools: map[string]*ExternalTool{
		"fofamap__fofa_search": {Name: "fofamap__fofa_search", OriginalName: "fofa_search", Server: "fofamap"},
	}, handlers: map[string]ToolHandler{}, aliases: map[string]string{}, ambiguous: map[string]bool{}}
	if !ready.HasFofaMapTools() {
		t.Fatal("FofaMap search tool should be detected")
	}
}

func TestFofaMapDetectionSupportsCustomAliasAndRejectsGenericNuclei(t *testing.T) {
	tools := []ExternalTool{
		{Name: "custom__fofa_account", OriginalName: "fofa_account", Server: "asset-mapper"},
		{Name: "custom__fofa_fields", OriginalName: "fofa_fields", Server: "asset-mapper"},
		{Name: "custom__fofa_search", OriginalName: "fofa_search", Server: "asset-mapper"},
		{Name: "custom__nuclei_execute", OriginalName: "nuclei_execute", Server: "asset-mapper"},
	}
	if !IsFofaMapTool(&tools[0], tools) || !IsFofaMapTool(&tools[3], tools) {
		t.Fatal("custom-named server with FofaMap signature was not recognized")
	}
	generic := ExternalTool{Name: "scanner__nuclei_execute", OriginalName: "nuclei_execute", Server: "generic-scanner"}
	if IsFofaMapTool(&generic, []ExternalTool{generic}) {
		t.Fatal("generic Nuclei server was misidentified as FofaMap")
	}
}

func TestFofaMapRiskAndTimeouts(t *testing.T) {
	tests := []struct {
		name string
		risk string
	}{
		{"fofa_search", MCPRiskLow},
		{"fofa_export", MCPRiskMedium},
		{"nuclei_plan", MCPRiskMedium},
		{"nuclei_execute", MCPRiskHigh},
	}
	for _, test := range tests {
		tool := &ExternalTool{OriginalName: test.name, Server: "fofamap"}
		got, _, ok := FofaMapToolRisk(tool, nil)
		if !ok || got != test.risk {
			t.Fatalf("FofaMapToolRisk(%s)=(%q,%t), want %q,true", test.name, got, ok, test.risk)
		}
	}
	if got := FofaMapToolTimeout("fofa_agent_run", time.Minute, false); got != 5*time.Minute {
		t.Fatalf("fofa_agent_run timeout=%s", got)
	}
	if got := FofaMapToolTimeout("nuclei_execute", time.Minute, false); got != 10*time.Minute {
		t.Fatalf("nuclei_execute timeout=%s", got)
	}
	if got := FofaMapToolTimeout("nuclei_execute", 45*time.Second, true); got != 45*time.Second {
		t.Fatalf("explicit timeout lost: %s", got)
	}
}

func TestFofaMapCallDefaultsWrapNestedRequestAndAliasCursor(t *testing.T) {
	export := applyFofaMapCallDefaults("fofa_export", map[string]string{
		"query": `domain="example.com"`, "fields": "ip,port,host", "format": "csv", "size": "5",
	})
	if export["query"] != "" || export["fields"] != "" {
		t.Fatalf("flat export args should be consumed into request: %#v", export)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(export["request"]), &payload); err != nil {
		t.Fatalf("request JSON: %v", err)
	}
	search, _ := payload["search"].(map[string]any)
	if search["query"] != `domain="example.com"` || payload["format"] != "csv" {
		t.Fatalf("wrapped export request=%#v", payload)
	}
	if search["size"] != float64(5) || search["max_records"] != float64(5) || search["max_pages"] != float64(1) {
		t.Fatalf("export size should cap max_records/max_pages: %#v", search)
	}
	fields, _ := search["fields"].([]any)
	if len(fields) != 3 {
		t.Fatalf("comma fields should become array: %#v", search["fields"])
	}

	next := applyFofaMapCallDefaults("fofa_search_next", map[string]string{
		"query": `app="nginx"`, "next_cursor": "opaque-token", "fields": `["ip","host"]`,
	})
	if next["cursor"] != "opaque-token" || next["next_cursor"] != "" {
		t.Fatalf("next_cursor alias failed: %#v", next)
	}

	plan := applyFofaMapCallDefaults("nuclei_plan", map[string]string{
		"targets": "https://example.com,https://app.example.com", "all_templates": "false",
	})
	if err := json.Unmarshal([]byte(plan["request"]), &payload); err != nil {
		t.Fatalf("plan request JSON: %v", err)
	}
	targets, _ := payload["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("plan targets should wrap as request.targets: %#v", payload)
	}
}

func TestValidateAndCoerceMCPArgsResolvesDefsAndAnyOfArray(t *testing.T) {
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"request"},
		"properties": map[string]interface{}{
			"request": map[string]interface{}{"$ref": "#/$defs/ExportRequest"},
			"fields": map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					map[string]interface{}{"type": "null"},
				},
			},
		},
		"$defs": map[string]interface{}{
			"ExportRequest": map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"search"},
				"properties": map[string]interface{}{
					"search": map[string]interface{}{"type": "object"},
					"format": map[string]interface{}{"type": "string"},
				},
			},
		},
	}
	got, err := validateAndCoerceMCPArgs(schema, map[string]string{
		"request": `{"search":{"query":"app=\"nginx\""},"format":"jsonl"}`,
		"fields":  "ip,host",
	})
	if err != nil {
		t.Fatalf("ref/anyOf args rejected: %v", err)
	}
	request, ok := got["request"].(map[string]interface{})
	if !ok || request["format"] != "jsonl" {
		t.Fatalf("request $ref should coerce to object: %#v", got["request"])
	}
	fields, ok := got["fields"].([]interface{})
	if !ok || len(fields) != 2 {
		t.Fatalf("comma fields via anyOf array failed: %#v", got["fields"])
	}
}

func TestFofaMapPromptExplainsSafeWorkflow(t *testing.T) {
	r := &Registry{tools: map[string]*ExternalTool{}, handlers: map[string]ToolHandler{}, aliases: map[string]string{}, ambiguous: map[string]bool{}}
	for _, name := range FofaMapKnownToolNames() {
		r.tools["fofamap__"+name] = &ExternalTool{Name: "fofamap__" + name, OriginalName: name, Server: "fofamap", Description: name}
	}
	prompt := r.FormatPrompt()
	for _, want := range []string{"FofaMap MCP v2.0.1", "fofa_validate_query", "next_cursor", "nuclei_plan", "一次性令牌", "load_skill", "fofa_recon.py"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("FofaMap prompt missing %q:\n%s", want, prompt)
		}
	}
}
