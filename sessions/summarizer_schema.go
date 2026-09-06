package sessions

import "encoding/json"

// summaryOutputSchema constrains generation, not acceptance. Empty arrays allow
// an honest abstention; the bounded host parser rejects an empty or invalid
// summary and derives provenance independently of generated fields.
func summaryOutputSchema() json.RawMessage {
	fields := []string{"decisions", "requirements", "pending_work", "failures", "artifacts", "activity"}
	properties := make(map[string]any, len(fields))
	for _, field := range fields {
		properties[field] = map[string]any{"type": "array", "maxItems": 128, "items": map[string]any{"type": "string"}}
	}
	body, _ := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"version", "summary"},
		"properties": map[string]any{
			"version": map[string]any{"type": "integer", "enum": []int{1}},
			"summary": map[string]any{"type": "object", "additionalProperties": false, "required": fields, "properties": properties},
		},
	})
	return body
}
