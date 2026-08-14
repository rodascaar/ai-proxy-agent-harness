package engine

import "ai-proxy-agent-harness/internal/core/openai"

// mergeToolCallDelta acumula los deltas de tool_calls de un stream por
// índice: cada índice recibe un único ToolCall cuya id/type se setea la
// primera vez y cuyo nombre/argumentos se van concatenando.
func mergeToolCallDelta(acc map[int]*openai.ToolCall, deltas []openai.ToolCallDelta) {
	for _, delta := range deltas {
		index := 0
		if delta.Index != nil {
			index = *delta.Index
		}
		entry, ok := acc[index]
		if !ok {
			entry = &openai.ToolCall{Type: "function"}
			acc[index] = entry
		}
		if delta.ID != nil {
			entry.ID = *delta.ID
		}
		if delta.Type != nil {
			entry.Type = *delta.Type
		}
		if delta.Function != nil {
			if delta.Function.Name != nil {
				entry.Function.Name += *delta.Function.Name
			}
			if delta.Function.Arguments != nil {
				entry.Function.Arguments += *delta.Function.Arguments
			}
		}
	}
}
