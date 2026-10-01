package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

var errHarnessReviewHeadConflict = errors.New("harness review head conflict")

// ReconcileHarnessAudit consumes a durable, completed advisory review. It never
// invokes an evaluator, changes the task journal or supersedes an operator head.
func ReconcileHarnessAudit(ctx context.Context, path string, ledger *harness.EvidenceStore, task, operation string) (harness.Review, error) {
	if ctx == nil || ledger == nil || !auditOperationIdentity(task, operation) {
		return harness.Review{}, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return harness.Review{}, err
	}
	defer db.Close()
	return reconcileHarnessAuditStore(ctx, db, ledger, task, operation)
}

func reconcileHarnessAuditStore(ctx context.Context, db *telemetry.Store, ledger *harness.EvidenceStore, task, operation string) (harness.Review, error) {
	attempt, err := db.ReviewAttempt(ctx, operation)
	if err != nil || attempt.TaskID != task || attempt.Status != "completed" || attempt.SourceKind != "harness" {
		return harness.Review{}, ErrAdmission
	}
	audit, err := db.Audit(ctx, attempt.AuditID)
	if err != nil {
		return harness.Review{}, err
	}
	if _, err = evaluation.NewAuditStatus(attempt, &audit); err != nil {
		return harness.Review{}, err
	}
	outcome, err := runtime.RecordHarnessOutcome(ctx, db, ledger, task, time.Now().UTC())
	if err != nil {
		return harness.Review{}, err
	}
	digest, _ := outcome.Digest()
	if digest != audit.AttemptID {
		return harness.Review{}, ErrAdmission
	}
	id := sha256.Sum256([]byte("harness-audit-review-v1\x00" + operation))
	methodBody, _ := json.Marshal([]string{audit.EvaluatorProvider, audit.EvaluatorModel, audit.Audit.RubricVersion})
	method := sha256.Sum256(methodBody)
	review := harness.Review{Version: 1, ID: hex.EncodeToString(id[:]), ExecutionDigest: digest, Reviewer: audit.Audit.EvaluatorID, CreatedAt: audit.Time.UTC(), Verdict: "unverified"}
	if audit.Audit.Verdict != "abstain" && audit.Audit.Confidence > 0 {
		review.Method = "automated_ai"
		review.MethodVersion = hex.EncodeToString(method[:])
		review.Confidence = audit.Audit.Confidence
		review.Verdict = "failed"
		if audit.Audit.Verdict == "accept" {
			review.Verdict = "passed"
			review.Quality = 1
		}
	}
	if err = ledger.AppendReview(ctx, review, time.Now().UTC()); err != nil {
		if errors.Is(err, harness.ErrConflict) {
			err = errors.Join(errHarnessReviewHeadConflict, err)
		}
		return harness.Review{}, err
	}
	return review, nil
}

func (s *Service) reviewNativeCompleted(ctx context.Context, result Result) Result {
	request := evaluation.AuditRequest{Version: 1, IdempotencyKey: "native-auto-review-v1:" + result.TaskID, TaskID: result.TaskID, ReviewerModelID: s.settings.Evaluation.AutoReviewModel, MaxCost: s.settings.Evaluation.AutoReviewMaxCost}
	status, err := s.RunAudit(ctx, request, func(evaluation.AuditEvent) error { return nil })
	result.AuditID = status.AuditID
	result.HarnessAuditOperationID = status.ID
	result.AuditStatus = status.Status
	if status.AuditID != "" {
		result.AuditStatus = "recorded"
	}
	result.HarnessReviewStatus = "failed"
	if err != nil {
		if result.AuditStatus == "" {
			result.AuditStatus = "failed"
		}
		return result
	}
	if s.harnessEvidence == nil {
		result.HarnessReviewStatus = "not_configured"
		return result
	}
	review, err := ReconcileHarnessAudit(ctx, s.settings.Telemetry.Database, s.harnessEvidence, result.TaskID, status.ID)
	if err != nil {
		if errors.Is(err, harness.ErrConflict) {
			result.HarnessReviewStatus = "conflict"
		}
		return result
	}
	result.HarnessEvidenceStatus = "recorded"
	result.HarnessReview = &review
	result.HarnessReviewStatus = "recorded"
	return result
}

// Ledger persistence follows canonical completion and must never cause inference
// retry. A failed copy is repairable from the immutable task journal.
func (s *Service) recordNativeCompleted(ctx context.Context, result Result) Result {
	result.HarnessEvidenceStatus = "not_configured"
	if s.harnessEvidence == nil {
		return result
	}
	result.HarnessEvidenceStatus = "failed"
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(persist, s.settings.Telemetry.Database)
	if err != nil {
		return result
	}
	defer db.Close()
	outcome, err := runtime.RecordHarnessOutcome(persist, db, s.harnessEvidence, result.TaskID, time.Now().UTC())
	if err == nil && result.HarnessOutcome != nil && outcome == *result.HarnessOutcome {
		result.HarnessEvidenceStatus = "recorded"
	}
	return result
}
