package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type delegateAuditRunner func(context.Context, string, string) (*runtime.DelegationAudit, error)

type delegateAuditProjection struct {
	Status     string   `json:"status"`
	Verdict    string   `json:"verdict,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Citations  []string `json:"cited_evidence"`
}

func projectDelegateAudit(a *runtime.DelegationAudit) *delegateAuditProjection {
	if a == nil {
		return nil
	}
	out := &delegateAuditProjection{Status: a.Status, Verdict: a.Verdict, Citations: append([]string(nil), a.Citations...)}
	if a.Confidence != nil {
		confidence := *a.Confidence
		out.Confidence = &confidence
	}
	return out
}

func delegationAuditKey(workID string) string {
	return "delegation-audit-v1:" + workID
}

func (s *Service) bindDelegationAudit() delegateAuditRunner {
	return func(ctx context.Context, workID, executionID string) (*runtime.DelegationAudit, error) {
		operationID := auditOperationID(delegationAuditKey(workID))
		unavailable := func(status string) (*runtime.DelegationAudit, error) {
			return &runtime.DelegationAudit{Version: 1, OperationID: operationID, ReviewerID: s.settings.Evaluation.AutoReviewModel, Status: status, Citations: []string{}}, nil
		}
		request := evaluation.AuditRequest{
			Version:         1,
			IdempotencyKey:  delegationAuditKey(workID),
			TaskID:          executionID,
			ReviewerModelID: s.settings.Evaluation.AutoReviewModel,
			MaxCost:         s.settings.Evaluation.AutoReviewMaxCost,
		}
		status, err := s.RunAudit(ctx, request, func(evaluation.AuditEvent) error { return nil })
		if ctx.Err() != nil || status.Status == "canceled" || status.Status == "pending" {
			return nil, errors.Join(ErrAdmission, ctx.Err())
		}
		if status.ID == "" {
			// A policy, credential, context, resource or cost rejection happens
			// before dispatch and intentionally has no review-attempt record.
			return unavailable("not_run")
		}
		if status.ID != operationID {
			return nil, ErrAdmission
		}
		if status.Status == "failed" {
			return unavailable("failed")
		}
		if err != nil || (status.Status != "completed" && status.Status != "rejected" && status.Status != "abstained") {
			return nil, ErrAdmission
		}

		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		db, openErr := telemetry.OpenReadOnly(inspect, s.settings.Telemetry.Database)
		if openErr != nil {
			return nil, ErrAdmission
		}
		defer db.Close()
		attempt, readErr := db.ReviewAttempt(inspect, operationID)
		if readErr != nil || attempt.Status != "completed" {
			return nil, ErrAdmission
		}
		record, readErr := db.Audit(inspect, attempt.AuditID)
		if readErr != nil {
			return nil, ErrAdmission
		}
		projected, projectErr := evaluation.NewAuditStatus(attempt, &record)
		if projectErr != nil || projected.ID != status.ID || projected.Status != status.Status || projected.AuditID != status.AuditID {
			return nil, ErrAdmission
		}
		seen := map[string]bool{}
		citations := make([]string, 0, len(record.EvidenceRefs))
		for _, finding := range record.Audit.Findings {
			for _, ref := range finding.EvidenceRefs {
				if !seen[ref] {
					seen[ref] = true
					citations = append(citations, ref)
				}
			}
		}
		confidence := record.Audit.Confidence
		out := &runtime.DelegationAudit{Version: 1, OperationID: operationID, ReviewerID: request.ReviewerModelID, AuditID: record.ID, Status: status.Status, Verdict: record.Audit.Verdict, Confidence: &confidence, Citations: citations}
		intent := &runtime.DelegationAuditIntent{Version: 1, OperationID: operationID, ReviewerID: request.ReviewerModelID}
		if out.Validate(intent) != nil || !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
			return nil, ErrAdmission
		}
		return out, nil
	}
}
