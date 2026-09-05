package providers

import "encoding/json"

// EstimateContext counts serialized UTF-8 bytes of messages, tool definitions
// and output schemas, plus an output/framing reserve. This is deliberately
// conservative, not a model tokenizer or a guarantee about generated output.
func EstimateContext(r Request) (int, error) {
	b, err := json.Marshal(struct {
		Messages []Message
		Tools    []Tool
		Schema   json.RawMessage
	}{r.Messages, r.Tools, r.JSONSchema})
	if err != nil {
		return 0, err
	}
	overhead := 0
	for _, m := range r.Messages {
		if m.Role == "tool" && m.ToolFailed {
			overhead += len(toolFailurePrefix) + 1
		}
	}
	// Include the prefix's serialized newline escape, in addition to the
	// internal status field already present in the conservative byte estimate.
	return len(b) + overhead + 1024, nil
}
