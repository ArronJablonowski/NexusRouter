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
	return len(b) + 1024, nil
}
