package runtimev3

// PatchDanglingToolCalls returns a copy of messages where every tool call has
// a corresponding result. This makes canceled and restored histories valid for
// strict provider APIs without pretending that the tool succeeded.
func PatchDanglingToolCalls(messages []Message) []Message {
	results := make(map[string]bool)
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.ToolResult != nil && block.ToolResult.CallID != "" {
				results[block.ToolResult.CallID] = true
			}
		}
	}
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, message)
		for _, block := range message.Blocks {
			if block.ToolCall == nil || block.ToolCall.ID == "" || results[block.ToolCall.ID] {
				continue
			}
			out = append(out, Message{
				ID:        NewID("msg"),
				Role:      "tool",
				CreatedAt: message.CreatedAt,
				Blocks: []ContentBlock{{Kind: ContentToolResult, ToolResult: &ToolResult{
					CallID:  block.ToolCall.ID,
					Name:    block.ToolCall.Name,
					IsError: true,
					Blocks:  []ContentBlock{{Kind: ContentText, Text: "Tool execution was interrupted before a result was recorded."}},
				}}},
			})
		}
	}
	return out
}
