package evaluation

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// ReviewAttempt records dispatch and terminal state without prompts or raw
// provider errors. A durable started state also identifies interrupted reviews.
type ReviewAttempt struct {
	Version                                                  int
	ID, TaskID, AttemptID, EvaluatorModel, EvaluatorProvider string
	ReviewerID                                               string `json:",omitempty"`
	RequestDigest                                            string `json:",omitempty"`
	// EstimatedCost is the configured admission estimate captured before
	// dispatch. It is not a provider-reported or reconciled charge.
	EstimatedCost float64 `json:",omitempty"`
	// Usage and Elapsed preserve a verified terminal provider measurement when
	// output validation fails. They are not audit evidence or a success signal.
	Usage                 *providers.Usage `json:",omitempty"`
	Elapsed               time.Duration    `json:",omitempty"`
	Status, Code, AuditID string
	StartedAt, FinishedAt time.Time
}

func (r ReviewAttempt) Validate() error {
	if r.Version != 1 || !auditLabel(r.ID) || !auditLabel(r.TaskID) || !auditLabel(r.AttemptID) || !auditLabel(r.EvaluatorModel) || !auditLabel(r.EvaluatorProvider) || r.StartedAt.IsZero() || !reviewCost(r.EstimatedCost) {
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
		if !r.FinishedAt.IsZero() || r.Code != "" || r.AuditID != "" || r.Usage != nil || r.Elapsed != 0 {
			return ErrAudit
		}
	case "completed":
		if r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || !auditLabel(r.AuditID) || r.Code != "" || r.Usage != nil || r.Elapsed != 0 {
			return ErrAudit
		}
	case "failed":
		if r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || r.AuditID != "" || (r.Code != "review_failed" && r.Code != "canceled" && r.Code != "persistence_failed") || r.Elapsed < 0 || r.Elapsed > MaxReviewDuration || r.Elapsed > r.FinishedAt.Sub(r.StartedAt) || (r.Usage != nil && (r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0)) || (r.Usage == nil && r.Elapsed != 0) || (r.Code == "canceled" && (r.Usage != nil || r.Elapsed != 0)) {
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
