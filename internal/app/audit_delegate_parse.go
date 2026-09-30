package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type auditDelegateSuccess struct {
	WorkID      string                   `json:"work_task_id"`
	ExecutionID string                   `json:"execution_task_id"`
	Output      string                   `json:"untrusted_output"`
	Audit       *delegateAuditProjection `json:"audit,omitempty"`
}

// Only runtime-produced canonical envelopes are traversal authority. Generic
// rejections contain no child references. Batch item positions are preserved
// even when generic siblings are omitted from the metadata evidence.
func auditDelegationReferences(e runtime.Event) ([]auditDelegation, error) {
	if e.Kind != runtime.ToolCompleted || (e.Data.ToolName != "delegate" && e.Data.ToolName != "delegate_batch") {
		return nil, nil
	}
	if len(e.Data.Text) >= 1<<20 || !utf8.ValidString(e.Data.Text) || e.Data.Effect != runtime.NoEffect {
		return nil, ErrAdmission
	}
	body := bytes.TrimSpace([]byte(e.Data.Text))
	if bytes.Equal(body, []byte(`{"error":"delegate_unavailable_or_rejected"}`)) {
		return nil, nil
	}
	var items []json.RawMessage
	if e.Data.ToolName == "delegate_batch" {
		var batch struct {
			Results []json.RawMessage `json:"results"`
		}
		if json.Unmarshal(body, &batch) != nil || len(batch.Results) < 2 || len(batch.Results) > 4 {
			return nil, ErrAdmission
		}
		canonical, err := json.Marshal(batch)
		if err != nil || !bytes.Equal(body, canonical) {
			return nil, ErrAdmission
		}
		items = batch.Results
	} else {
		items = []json.RawMessage{body}
	}
	var out []auditDelegation
	for index, item := range items {
		ref, err := parseAuditDelegationItem(e, item)
		if err != nil {
			return nil, err
		}
		if ref == nil {
			continue
		}
		if e.Data.ToolName == "delegate_batch" {
			i := index
			ref.batchIndex = &i
		}
		out = append(out, *ref)
	}
	return out, nil
}

func parseAuditDelegationItem(e runtime.Event, body []byte) (*auditDelegation, error) {
	if bytes.Equal(body, []byte(`{"error":"delegate_unavailable_or_rejected"}`)) {
		return nil, nil
	}
	if len(body) > 512<<10 {
		return nil, ErrAdmission
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, ErrAdmission
	}
	rich := false
	for name := range fields {
		for _, reserved := range []string{"error", "version", "reason", "evidence"} {
			if strings.EqualFold(name, reserved) {
				rich = true
			}
		}
	}
	e.Data = runtime.Data{ToolCallID: e.Data.ToolCallID, ToolName: e.Data.ToolName, ToolBehavior: e.Data.ToolBehavior, Effect: e.Data.Effect}
	ref := &auditDelegation{parent: e}
	var canonical []byte
	var err error
	if rich {
		r := &ref.report
		if len(body) > 2048 || json.Unmarshal(body, r) != nil || r.Version != 1 || r.Error != "delegate_unavailable_or_rejected" || !sessions.ValidEventPageID(r.WorkID) || (r.ExecutionID != "" && !sessions.ValidEventPageID(r.ExecutionID)) || len(r.Evidence) < 1 || len(r.Evidence) > 2 {
			return nil, ErrAdmission
		}
		canonical, err = json.Marshal(r)
	} else {
		var success auditDelegateSuccess
		if json.Unmarshal(body, &success) != nil || !sessions.ValidEventPageID(success.WorkID) || !sessions.ValidEventPageID(success.ExecutionID) || success.WorkID == success.ExecutionID || len(success.Output) > 64<<10 || strings.TrimSpace(success.Output) == "" || !validDelegateAuditProjection(success.Audit) {
			return nil, ErrAdmission
		}
		canonical, err = json.Marshal(success)
		ref.success = &success
		ref.report.WorkID, ref.report.ExecutionID = success.WorkID, success.ExecutionID
	}
	// Includes exact field names/order, values, duplicates and unknown members.
	// RawMessage batch entries are checked independently after the outer shape.
	if err != nil || !bytes.Equal(body, canonical) {
		return nil, ErrAdmission
	}
	return ref, nil
}

func auditSuccessfulDelegation(work, execution auditChildHistory, output string, audit *delegateAuditProjection) bool {
	if len(work.events) < 4 || len(execution.events) < 3 {
		return false
	}
	worker := ""
	accepted, completed := false, false
	var durableAudit *delegateAuditProjection
	for _, e := range work.events {
		switch e.Kind {
		case runtime.WorkerStarted:
			if worker != "" || e.WorkerID == "" {
				return false
			}
			worker = e.WorkerID
		case runtime.EvaluationRecorded:
			if worker == "" || accepted || completed || e.WorkerID != worker || e.TurnID != "" || e.AttemptID != "" || e.Data.Code != "worker_validator" || e.Data.Accepted == nil || !*e.Data.Accepted {
				return false
			}
			accepted = true
		case runtime.WorkerCompleted:
			intent := work.events[0].Data.DelegationAuditIntent
			if !accepted || completed || e.WorkerID != worker || e.Data.Text != output || (intent == nil) != (e.Data.DelegationAudit == nil) || (e.Data.DelegationAudit != nil && e.Data.DelegationAudit.Validate(intent) != nil) {
				return false
			}
			durableAudit = projectDelegateAudit(e.Data.DelegationAudit)
			completed = true
		}
	}
	terminal := work.events[len(work.events)-1]
	if !accepted || !completed || terminal.Kind != runtime.TaskCompleted || terminal.WorkerID != worker || terminal.TurnID != "" || terminal.AttemptID != "" || execution.events[len(execution.events)-1].Kind != runtime.TaskCompleted {
		return false
	}
	// Bind the published work result to the execution's last completed answer,
	// not an earlier turn or a sibling's model output. Text is only compared.
	var final runtime.Event
	for _, e := range execution.events {
		if e.Kind == runtime.TurnStarted {
			final = runtime.Event{}
		}
		if e.Kind == runtime.TurnCompleted {
			final = e
		}
	}
	wantAudit, gotAudit := []byte("null"), []byte("null")
	if audit != nil {
		wantAudit, _ = json.Marshal(audit)
	}
	if durableAudit != nil {
		gotAudit, _ = json.Marshal(durableAudit)
	}
	return bytes.Equal(wantAudit, gotAudit) && final.Kind == runtime.TurnCompleted && len(final.Data.ToolCalls) == 0 && final.Data.Text == output
}

func validDelegateAuditProjection(a *delegateAuditProjection) bool {
	if a == nil {
		return true
	}
	op := "operation"
	auditID := ""
	if a.Status == "completed" || a.Status == "rejected" || a.Status == "abstained" {
		auditID = "audit"
	}
	full := runtime.DelegationAudit{Version: 1, OperationID: op, ReviewerID: "reviewer", AuditID: auditID, Status: a.Status, Verdict: a.Verdict, Confidence: a.Confidence, Citations: a.Citations}
	return full.Validate(&runtime.DelegationAuditIntent{Version: 1, OperationID: op, ReviewerID: "reviewer"}) == nil
}
