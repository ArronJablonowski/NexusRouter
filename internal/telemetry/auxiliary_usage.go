package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func configuredAuxiliaryEstimate(source, provider, model string, cost float64, effectiveAt time.Time) (*float64, *accounting.PricingProvenance) {
	digest := sha256.Sum256([]byte(source + "\x00" + provider + "\x00" + model + "\x00" + strconv.FormatFloat(cost, 'g', -1, 64)))
	value := cost
	return &value, &accounting.PricingProvenance{
		Version:     1,
		ID:          "configured-auxiliary-estimate-v1",
		Source:      source,
		Digest:      hex.EncodeToString(digest[:]),
		Currency:    "USD",
		Method:      accounting.FlatRate,
		Basis:       accounting.ConfiguredEstimate,
		FixedCost:   cost,
		EffectiveAt: effectiveAt.UTC(),
	}
}

func taskSession(ctx context.Context, tx *sql.Tx, task string) (string, error) {
	var session string
	err := tx.QueryRowContext(ctx, "SELECT session_id FROM task_heads WHERE task_id=?", task).Scan(&session)
	return session, err
}

// appendReviewUsage records one public orchestrator audit or legacy optional
// judge invocation. The attempt/audit terminal row must already be visible in
// tx so appendUsage can independently verify every attribution field.
func appendReviewUsage(ctx context.Context, tx *sql.Tx, attempt evaluation.ReviewAttempt) error {
	session, err := taskSession(ctx, tx, attempt.TaskID)
	if err != nil {
		return err
	}
	role := accounting.OptionalJudge
	if attempt.ReviewerID != "" {
		role = accounting.OrchestratorAudit
	}
	r := accounting.Record{
		Version:            1,
		ID:                 usageID(role, attempt.ID),
		TaskID:             attempt.TaskID,
		SessionID:          session,
		OperationID:        attempt.ID,
		CandidateAttemptID: attempt.AttemptID,
		RouteID:            attempt.ID,
		Provider:           attempt.EvaluatorProvider,
		Model:              attempt.EvaluatorModel,
		Role:               role,
		Disposition:        accounting.Failed,
		RetryClass:         accounting.NonRetryable,
		OccurredAt:         attempt.FinishedAt.UTC(),
	}
	r.NormalizedCost, r.Pricing = configuredAuxiliaryEstimate("review_attempt.estimated_cost", r.Provider, r.Model, attempt.EstimatedCost, attempt.StartedAt)
	if attempt.Status == "completed" {
		var body []byte
		if err := tx.QueryRowContext(ctx, "SELECT body FROM audit_records WHERE id=?", attempt.AuditID).Scan(&body); err != nil {
			return err
		}
		var audit evaluation.AuditRecord
		if json.Unmarshal(body, &audit) != nil || audit.Validate() != nil {
			return accounting.ErrUsage
		}
		r.EvidenceKind, r.EvidenceID, r.AuditID = accounting.AuditEvidence, audit.ID, audit.ID
		r.Usage = cloneUsage(audit.Usage)
		r.Disposition, r.RetryClass = accounting.Completed, accounting.NotApplicable
	} else {
		r.EvidenceKind, r.EvidenceID = accounting.ReviewEvidence, attempt.ID
		if attempt.Code == "canceled" {
			r.Disposition = accounting.Canceled
		} else {
			r.Usage = cloneUsage(attempt.Usage)
		}
	}
	return appendUsage(ctx, tx, r)
}

// appendSummaryUsage records the bounded summarizer call independently from
// routed task cost. A failed attempt may retain a verified normal-terminal
// measurement when only later host validation rejected the output; canceled,
// interrupted, provider-error, and abnormal-finish calls remain unknown.
func appendSummaryUsage(ctx context.Context, tx *sql.Tx, attempt sessions.SummaryAttempt) error {
	session, err := taskSession(ctx, tx, attempt.TaskID)
	if err != nil {
		return err
	}
	r := accounting.Record{
		Version:      1,
		ID:           usageID(accounting.Summarizer, attempt.ID),
		TaskID:       attempt.TaskID,
		SessionID:    session,
		OperationID:  attempt.ID,
		RouteID:      attempt.ID,
		EvidenceID:   attempt.ID,
		Provider:     attempt.Provider,
		Model:        attempt.Model,
		Role:         accounting.Summarizer,
		EvidenceKind: accounting.SummaryEvidence,
		Disposition:  accounting.Failed,
		RetryClass:   accounting.NonRetryable,
		OccurredAt:   attempt.FinishedAt.UTC(),
	}
	r.NormalizedCost, r.Pricing = configuredAuxiliaryEstimate("summary_attempt.estimated_cost", r.Provider, r.Model, attempt.EstimatedCost, attempt.StartedAt)
	if attempt.Status == "drafted" {
		r.Usage = cloneUsage(attempt.Draft.Usage)
		r.Disposition, r.RetryClass = accounting.Completed, accounting.NotApplicable
	} else if attempt.Code == "canceled" {
		r.Disposition = accounting.Canceled
	} else {
		r.Usage = cloneUsage(attempt.Usage)
	}
	return appendUsage(ctx, tx, r)
}

func cloneUsage(in *providers.Usage) *providers.Usage {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
