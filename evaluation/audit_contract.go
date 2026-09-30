package evaluation

import (
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const MaxAuditEventPageBytes = 1 << 20

// AuditRequest is the public, provider-neutral request contract. The caller's
// idempotency key is never used as the durable operation ID or persisted raw.
type AuditRequest struct {
	Version         int     `json:"version"`
	IdempotencyKey  string  `json:"idempotency_key"`
	TaskID          string  `json:"task_id"`
	ReviewerModelID string  `json:"reviewer_model_id"`
	MaxCost         float64 `json:"max_cost"`
}

func (r AuditRequest) Validate() error {
	if r.Version != 1 || !auditIdempotencyKey(r.IdempotencyKey) || !ValidAuditOperationID(r.TaskID) || !ValidAuditOperationID(r.ReviewerModelID) || !reviewCost(r.MaxCost) {
		return ErrAudit
	}
	return nil
}

// ValidAuditOperationID accepts only opaque path-safe identifiers. Model names
// containing provider syntax such as slash or colon must be configured behind
// a stable reviewer model ID before crossing the public API boundary.
func ValidAuditOperationID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i, b := range []byte(id) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || (i > 0 && (b == '_' || b == '.' || b == '-')) {
			continue
		}
		return false
	}
	return true
}

func auditIdempotencyKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for _, b := range []byte(key) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

// AuditStatus is a public-safe projection: it contains bounded, model-authored
// findings and opaque evidence identifiers, but no dedicated prompt, candidate
// output, tool payload, or raw-error fields. Findings are task-derived content
// and must receive the same authorization and handling as task inspection.
type AuditStatus struct {
	Version             int    `json:"version"`
	ID                  string `json:"id"`
	TaskID              string `json:"task_id"`
	SourceAttemptID     string `json:"source_attempt_id"`
	Status              string `json:"status"`
	TerminalDisposition string `json:"terminal_disposition,omitempty"`
	ErrorCode           string `json:"error_code,omitempty"`
	ReviewerID          string `json:"reviewer_id"`
	EvaluatorModel      string `json:"evaluator_model"`
	EvaluatorProvider   string `json:"evaluator_provider"`
	RubricVersion       string `json:"rubric_version,omitempty"`
	Domain              string `json:"domain,omitempty"`
	AuditID             string `json:"audit_id,omitempty"`
	// Confidence is deliberately absent: it is a model self-report, not
	// acceptance evidence. Findings retain their citable provenance.
	Findings           []AuditFinding   `json:"findings"`
	EvidenceRefs       []string         `json:"evidence_refs"`
	EvidencePrecedence []Source         `json:"evidence_precedence"`
	Usage              *providers.Usage `json:"usage,omitempty"`
	ElapsedMillis      int64            `json:"elapsed_millis"`
	StartedAt          *time.Time       `json:"started_at"`
	FinishedAt         *time.Time       `json:"finished_at,omitempty"`
}

var fixedAuditEvidencePrecedence = []Source{Deterministic, ToolResult, UserFeedback, LLMJudge}

// AuditEvidencePrecedence returns a copy so callers cannot mutate the public
// contract's fixed evidence ladder.
func AuditEvidencePrecedence() []Source {
	return append([]Source(nil), fixedAuditEvidencePrecedence...)
}

func validAuditPublicStatus(s string) bool {
	switch s {
	case "pending", "completed", "rejected", "abstained", "canceled", "failed":
		return true
	}
	return false
}

func (s AuditStatus) Validate() error {
	if s.Version != 1 || !ValidAuditOperationID(s.ID) || !ValidAuditOperationID(s.TaskID) || !ValidAuditOperationID(s.SourceAttemptID) || !validAuditPublicStatus(s.Status) || !ValidAuditOperationID(s.ReviewerID) || !auditLabel(s.EvaluatorModel) || !auditLabel(s.EvaluatorProvider) || s.Findings == nil || s.EvidenceRefs == nil || len(s.Findings) > 64 || len(s.EvidenceRefs) > 256 || len(s.EvidencePrecedence) != len(fixedAuditEvidencePrecedence) || s.StartedAt == nil || s.StartedAt.IsZero() || s.StartedAt.Location() != time.UTC || s.ElapsedMillis < 0 || s.ElapsedMillis > MaxReviewDuration.Milliseconds() {
		return ErrAudit
	}
	for i, source := range fixedAuditEvidencePrecedence {
		if s.EvidencePrecedence[i] != source {
			return ErrAudit
		}
	}
	if s.Usage != nil && (s.Usage.InputTokens < 0 || s.Usage.OutputTokens < 0) {
		return ErrAudit
	}
	if s.Status == "pending" {
		if s.TerminalDisposition != "" || s.ErrorCode != "" || s.AuditID != "" || s.FinishedAt != nil || s.RubricVersion != "" || s.Domain != "" || len(s.Findings) != 0 || len(s.EvidenceRefs) != 0 || s.Usage != nil || s.ElapsedMillis != 0 {
			return ErrAudit
		}
		return nil
	}
	if s.TerminalDisposition != s.Status || s.FinishedAt == nil || s.FinishedAt.IsZero() || s.FinishedAt.Location() != time.UTC || s.FinishedAt.Before(*s.StartedAt) || s.ElapsedMillis > s.FinishedAt.Sub(*s.StartedAt).Milliseconds() {
		return ErrAudit
	}
	if s.Status == "failed" || s.Status == "canceled" {
		if (s.Status == "canceled" && s.ErrorCode != "canceled") || (s.Status == "failed" && s.ErrorCode != "review_failed" && s.ErrorCode != "persistence_failed") || s.AuditID != "" || s.RubricVersion != "" || s.Domain != "" || len(s.Findings) != 0 || len(s.EvidenceRefs) != 0 || s.Usage != nil || s.ElapsedMillis != 0 {
			return ErrAudit
		}
		return nil
	}
	if s.ErrorCode != "" || !ValidAuditOperationID(s.AuditID) || !auditLabel(s.RubricVersion) || !auditLabel(s.Domain) {
		return ErrAudit
	}
	seen := make(map[string]bool, len(s.EvidenceRefs))
	for _, ref := range s.EvidenceRefs {
		if !auditLabel(ref) || seen[ref] {
			return ErrAudit
		}
		seen[ref] = true
	}
	auditVerdict := map[string]string{"completed": "accept", "rejected": "reject", "abstained": "abstain"}[s.Status]
	a := Audit{Version: 1, EvaluatorID: s.ReviewerID, RubricVersion: s.RubricVersion, Domain: s.Domain, Verdict: auditVerdict, Findings: s.Findings}
	if s.Status != "abstained" {
		a.Confidence = 1 // Validate structure/reference provenance; confidence is not projected today.
	}
	body, err := json.Marshal(a)
	if err != nil {
		return ErrAudit
	}
	_, err = ParseAudit(body, AuditContext{EvaluatorID: s.ReviewerID, RubricVersion: s.RubricVersion, Domain: s.Domain, AllowedEvidenceRefs: s.EvidenceRefs})
	return err
}

// NewAuditStatus maps the durable lifecycle exactly. A completed attempt must
// carry its matching advisory record; failed attempts must not.
func NewAuditStatus(r ReviewAttempt, a *AuditRecord) (AuditStatus, error) {
	if r.Validate() != nil || r.ReviewerID == "" {
		return AuditStatus{}, ErrAudit
	}
	started := r.StartedAt.UTC()
	out := AuditStatus{Version: 1, ID: r.ID, TaskID: r.TaskID, SourceAttemptID: r.AttemptID, ReviewerID: r.ReviewerID, EvaluatorModel: r.EvaluatorModel, EvaluatorProvider: r.EvaluatorProvider, Findings: []AuditFinding{}, EvidenceRefs: []string{}, EvidencePrecedence: AuditEvidencePrecedence(), StartedAt: &started}
	switch r.Status {
	case "started":
		if a != nil {
			return AuditStatus{}, ErrAudit
		}
		out.Status = "pending"
	case "failed":
		if a != nil {
			return AuditStatus{}, ErrAudit
		}
		out.Status, out.TerminalDisposition, out.ErrorCode = "failed", "failed", r.Code
		if r.Code == "canceled" {
			out.Status, out.TerminalDisposition = "canceled", "canceled"
		}
		finished := r.FinishedAt.UTC()
		out.FinishedAt = &finished
	case "completed":
		if a == nil || a.Validate() != nil || r.AuditID != a.ID || r.TaskID != a.TaskID || r.AttemptID != a.AttemptID || r.EvaluatorModel != a.EvaluatorModel || r.EvaluatorProvider != a.EvaluatorProvider || r.ReviewerID != a.Audit.EvaluatorID || a.Time.Before(r.StartedAt) || a.Time.After(r.FinishedAt) || a.Elapsed > a.Time.Sub(r.StartedAt) {
			return AuditStatus{}, ErrAudit
		}
		out.Status = map[string]string{"accept": "completed", "reject": "rejected", "abstain": "abstained"}[a.Audit.Verdict]
		out.TerminalDisposition = out.Status
		out.AuditID, out.RubricVersion, out.Domain = a.ID, a.Audit.RubricVersion, a.Audit.Domain
		out.Findings = make([]AuditFinding, len(a.Audit.Findings))
		for i, finding := range a.Audit.Findings {
			out.Findings[i] = AuditFinding{Summary: finding.Summary, EvidenceRefs: append([]string{}, finding.EvidenceRefs...)}
		}
		out.EvidenceRefs = append([]string{}, a.EvidenceRefs...)
		if a.Usage != nil {
			u := *a.Usage
			out.Usage = &u
		}
		out.ElapsedMillis = a.Elapsed.Milliseconds()
		finished := r.FinishedAt.UTC()
		out.FinishedAt = &finished
	default:
		return AuditStatus{}, ErrAudit
	}
	if out.Validate() != nil {
		return AuditStatus{}, ErrAudit
	}
	return out, nil
}

type AuditEvent struct {
	Version  int         `json:"version"`
	AuditID  string      `json:"audit_id"`
	Sequence int64       `json:"sequence"`
	Status   AuditStatus `json:"status"`
}

func (e AuditEvent) Validate() error {
	if e.Version != 1 || e.Sequence < 1 || e.Sequence > 2 || e.AuditID != e.Status.ID || e.Status.Validate() != nil || (e.Sequence == 1) != (e.Status.Status == "pending") || (e.Sequence == 2 && e.Status.Status == "pending") {
		return ErrAudit
	}
	return nil
}

type AuditEventPage struct {
	Version      int          `json:"version"`
	AuditID      string       `json:"audit_id"`
	FromSequence int64        `json:"from_sequence"`
	NextSequence int64        `json:"next_sequence"`
	HeadSequence int64        `json:"head_sequence"`
	HasMore      bool         `json:"has_more"`
	Events       []AuditEvent `json:"events"`
}

func (p AuditEventPage) Validate() error {
	if p.Version != 1 || !ValidAuditOperationID(p.AuditID) || p.HeadSequence < 1 || p.HeadSequence > 2 || p.FromSequence < 0 || p.FromSequence > p.HeadSequence || p.NextSequence < p.FromSequence || p.NextSequence > p.HeadSequence || p.HasMore != (p.NextSequence < p.HeadSequence) || len(p.Events) > 2 || p.NextSequence-p.FromSequence != int64(len(p.Events)) || (len(p.Events) == 0 && p.FromSequence != p.HeadSequence) {
		return ErrAudit
	}
	total := 0
	for i, event := range p.Events {
		if event.Validate() != nil || event.AuditID != p.AuditID || event.Sequence != p.FromSequence+int64(i)+1 {
			return ErrAudit
		}
		body, err := json.Marshal(event)
		if err != nil || len(body) > MaxAuditEventPageBytes-total {
			return ErrAudit
		}
		total += len(body)
	}
	return nil
}
