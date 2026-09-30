package app

import (
	"encoding/json"
	"strconv"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// auditExecutionEvidence projects durable execution facts, not model-generated
// claims. The caller must establish replay consistency and tool pairing first.
// Effect describes side effects, not tool success; code and accepted retain their
// separate meanings. Neither a completed tool nor an advisory audit proves that
// a compiler or test suite was run.
func auditExecutionEvidence(events []runtime.Event, secrets []string) ([]evaluation.ReviewEvidence, error) {
	const maxRecords, maxBytes = 250, 64 << 10
	out := make([]evaluation.ReviewEvidence, 0)
	seen := make(map[int64]bool)
	var task, session string
	total := 0
	for i, e := range events {
		if e.Sequence < 1 || seen[e.Sequence] || e.TaskID == "" || e.SessionID == "" {
			return nil, ErrAdmission
		}
		seen[e.Sequence] = true
		if i == 0 {
			task, session = e.TaskID, e.SessionID
		} else if e.TaskID != task || e.SessionID != session {
			return nil, ErrAdmission
		}
		if e.Kind != runtime.ToolCompleted && e.Kind != runtime.EvaluationRecorded {
			continue
		}
		if len(out) == maxRecords || e.Validate() != nil || e.TurnID == "" || e.AttemptID == "" {
			return nil, ErrAdmission
		}
		p := struct {
			Version      int                  `json:"version"`
			Sequence     int64                `json:"sequence"`
			Kind         runtime.Kind         `json:"kind"`
			TurnID       string               `json:"turn_id"`
			AttemptID    string               `json:"attempt_id"`
			ToolCallID   string               `json:"tool_call_id,omitempty"`
			ToolName     string               `json:"tool_name,omitempty"`
			ToolBehavior runtime.ToolBehavior `json:"tool_behavior,omitempty"`
			Effect       string               `json:"effect,omitempty"`
			Code         string               `json:"code,omitempty"`
			Accepted     *bool                `json:"accepted,omitempty"`
			Validation   string               `json:"validation,omitempty"`
		}{Version: 1, Sequence: e.Sequence, Kind: e.Kind, TurnID: e.TurnID, AttemptID: e.AttemptID, Code: e.Data.Code}
		if e.Kind == runtime.ToolCompleted {
			p.ToolCallID, p.ToolName, p.Effect = e.Data.ToolCallID, e.Data.ToolName, string(e.Data.Effect)
			p.ToolBehavior = e.Data.ToolBehavior
		} else {
			p.Accepted, p.Validation = e.Data.Accepted, e.Data.Validation
		}
		// Bound original metadata before redaction/encoding. Raw tool output and
		// arguments are deliberately never copied into this projection.
		fields := []*string{&p.TurnID, &p.AttemptID, &p.ToolCallID, &p.ToolName, &p.Effect, &p.Code, &p.Validation}
		original := 0
		for _, field := range fields {
			if len(*field) > maxBytes-original {
				return nil, ErrAdmission
			}
			original += len(*field)
			*field = redact(*field, secrets)
		}
		body, err := json.Marshal(p)
		if err != nil || len(body) > maxBytes-total {
			return nil, ErrAdmission
		}
		content := redact(string(body), secrets)
		if !json.Valid([]byte(content)) || len(content) > maxBytes-total {
			return nil, ErrAdmission
		}
		total += len(content)
		out = append(out, evaluation.ReviewEvidence{ID: "execution_" + strconv.FormatInt(e.Sequence, 10), Content: content})
	}
	return out, nil
}
