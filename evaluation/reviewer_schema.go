package evaluation

import "encoding/json"

// Provider schemas constrain generation, not acceptance. ParseAudit additionally
// checks byte bounds, nonblank summaries, unique references and verdict-dependent
// findings; these checks are not delegated to a provider's schema implementation.
func reviewOutputSchema(trusted AuditContext) json.RawMessage {
	label := func(value string) any { return map[string]any{"type": "string", "enum": []string{value}} }
	object := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"version", "evaluator_id", "rubric_version", "domain", "verdict", "confidence", "findings"},
		"properties": map[string]any{
			"version":        map[string]any{"type": "integer", "enum": []int{1}},
			"evaluator_id":   label(trusted.EvaluatorID),
			"rubric_version": label(trusted.RubricVersion),
			"domain":         label(trusted.Domain),
			"verdict":        map[string]any{"type": "string", "enum": []string{"accept", "reject", "abstain"}},
			"confidence":     map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"findings": map[string]any{
				"type": "array", "maxItems": 64,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"summary", "evidence_refs"},
					"properties": map[string]any{
						"summary": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
						"evidence_refs": map[string]any{"type": "array", "minItems": 1, "maxItems": 64,
							"items": map[string]any{"type": "string", "enum": trusted.AllowedEvidenceRefs}},
					},
				},
			},
		},
	}
	// Only host-validated strings and fixed JSON-compatible values enter this
	// object. Marshal owns all schema bytes independently of caller slices.
	body, _ := json.Marshal(object)
	return body
}
