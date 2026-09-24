package harness

import "testing"

func TestParseCommandRiskReview(t *testing.T) {
	raw := "```json\n{\"risk\":\"low\",\"reason\":\"只读枚举命令\",\"confidence\":0.91}\n```"
	review, ok := parseCommandRiskReview(raw)
	if !ok {
		t.Fatal("expected risk review JSON to parse")
	}
	if review.Risk != "low" || review.Reason != "只读枚举命令" || review.Confidence < 0.9 {
		t.Fatalf("unexpected review: %+v", review)
	}
}

func TestExtractFirstJSONObjectKeepsNestedStrings(t *testing.T) {
	raw := `prefix {"risk":"high","reason":"包含 { 字符但仍是字符串","confidence":0.7} suffix`
	got := extractFirstJSONObject(raw)
	if got == "" {
		t.Fatal("expected JSON object")
	}
	if got != `{"risk":"high","reason":"包含 { 字符但仍是字符串","confidence":0.7}` {
		t.Fatalf("unexpected JSON: %q", got)
	}
}
