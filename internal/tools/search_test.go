package tools

import "testing"

func TestSearchRelevanceRefreshesChangedToolMetadata(t *testing.T) {
	tool := &Tool{Name: "dynamic_example", Description: "alpha evidence", Enabled: true}
	if score := SearchRelevance(tool, "alpha"); score <= 0 {
		t.Fatalf("initial description was not indexed: %d", score)
	}
	tool.Description = "beta evidence"
	if score := SearchRelevance(tool, "alpha"); score != 0 {
		t.Fatalf("stale description remained indexed: %d", score)
	}
	if score := SearchRelevance(tool, "beta"); score <= 0 {
		t.Fatalf("updated description was not indexed: %d", score)
	}
}
