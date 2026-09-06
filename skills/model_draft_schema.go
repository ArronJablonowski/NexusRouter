package skills

import "encoding/json"

// modelDraftOutputSchema owns fresh bytes for each request. Provider schemas
// constrain generation, never establish validation, provenance, or acceptance.
// The existing parser separately enforces UTF-8, duplicate/unknown fields,
// meaningful content, identifier rules, and the total 64 KiB stream bound.
// Empty strings/arrays deliberately permit the complete abstention envelope;
// parseModelDraft still rejects it as an unqualified proposal.
func modelDraftOutputSchema() json.RawMessage {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	list := func(identifiers bool) map[string]any {
		item := text()
		if identifiers {
			item["pattern"] = "^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$"
		}
		out := map[string]any{"type": "array", "items": item}
		return out
	}
	object := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"version", "description", "tags", "steps", "required_tools", "configuration", "risks", "validation_cases"},
		"properties": map[string]any{
			"version":     map[string]any{"type": "integer", "enum": []int{1}},
			"description": text(), "tags": list(true), "steps": list(false),
			"required_tools": list(true), "configuration": text(), "risks": list(false), "validation_cases": list(false),
		},
	}
	body, _ := json.Marshal(object)
	return body
}
