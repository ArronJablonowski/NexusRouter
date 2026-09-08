package evaluation

import (
	"encoding/hex"
	"strings"
	"time"
)

// ReviewAttempt records dispatch and terminal state without prompts or raw
// provider errors. A durable started state also identifies interrupted reviews.
type ReviewAttempt struct {
	Version                                                  int
	ID, TaskID, AttemptID, EvaluatorModel, EvaluatorProvider string
	ReviewerID                                               string `json:",omitempty"`
	RequestDigest                                            string `json:",omitempty"`
	Status, Code, AuditID                                    string
	StartedAt, FinishedAt                                    time.Time
}

func (r ReviewAttempt) Validate() error {
	if r.Version != 1 || !auditLabel(r.ID) || !auditLabel(r.TaskID) || !auditLabel(r.AttemptID) || !auditLabel(r.EvaluatorModel) || !auditLabel(r.EvaluatorProvider) || r.StartedAt.IsZero() {
		return ErrAudit
	}
	// Both fields are absent on rows written before public audit operations.
	// New public operations bind the caller-visible reviewer and canonical
	// request together so an idempotency replay cannot change either identity.
	if (r.ReviewerID == "") != (r.RequestDigest == "") {
		return ErrAudit
	}
	if r.ReviewerID != "" && (!auditLabel(r.ReviewerID) || !auditDigest(r.RequestDigest)) {
		return ErrAudit
	}
	switch r.Status {
	case "started":
		if !r.FinishedAt.IsZero() || r.Code != "" || r.AuditID != "" {
			return ErrAudit
		}
	case "completed":
		if r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || !auditLabel(r.AuditID) || r.Code != "" {
			return ErrAudit
		}
	case "failed":
		if r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || r.AuditID != "" || (r.Code != "review_failed" && r.Code != "canceled" && r.Code != "persistence_failed") {
			return ErrAudit
		}
	default:
		return ErrAudit
	}
	return nil
}

func auditDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}
