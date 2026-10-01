package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/harness"
)

// RemoteEvaluator is trusted embedding-host configuration, never peer/model input.
// The host must bind privacy, costs, resources and credentials before supplying
// an Evaluator. Local asserts that its implementation cannot send data to cloud.
// Like evaluation.InvokeEvaluator, cancellation of extensions is cooperative.
type RemoteEvaluator struct {
	Evaluator evaluation.Evaluator
	Local     bool
	Timeout   time.Duration
}

type RemoteEvaluationStatus struct {
	Version       int            `json:"version"`
	ReceiptSHA256 string         `json:"receipt_sha256"`
	Status        string         `json:"status"` // waiting, task_failed/canceled, not_started, started, failed, completed
	ReviewApplied bool           `json:"review_applied"`
	Review        *OutcomeReview `json:"review,omitempty"`
}

type remoteEvaluationAdmission struct {
	Version         int                            `json:"version"`
	ReceiptSHA256   string                         `json:"receipt_sha256"`
	ExecutionSHA256 string                         `json:"execution_sha256"`
	InputSHA256     string                         `json:"input_sha256"`
	Descriptor      evaluation.EvaluatorDescriptor `json:"descriptor"`
	Local           bool                           `json:"local"`
	Timeout         time.Duration                  `json:"timeout"`
	StartedAt       time.Time                      `json:"started_at"`
}

type remoteEvaluationResult struct {
	Version         int               `json:"version"`
	AdmissionSHA256 string            `json:"admission_sha256"`
	Status          string            `json:"status"`
	FinishedAt      time.Time         `json:"finished_at"`
	Audit           *evaluation.Audit `json:"audit,omitempty"`
}

// EvaluateRecordedOutcome authenticates a completed remote outcome, durably
// admits one evaluation per receipt, then reconciles an advisory ledger review.
// A lost evaluator response is never replayed automatically. Completed persisted
// results may be reconciled repeatedly without invoking the evaluator again.
// Existing operator heads are never superseded; revisions use the explicit
// ReviewRecordedOutcome API. This host API does not schedule background work.
func (c *Client) EvaluateRecordedOutcome(ctx context.Context, routes *RouteStore, root, key string, task Task, policy RemoteEvaluator) (RemoteEvaluationStatus, error) {
	var status RemoteEvaluationStatus
	if ctx == nil || ctx.Err() != nil || policy.Timeout <= 0 || policy.Timeout > evaluation.MaxReviewDuration || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return status, ErrInvalid
	}
	if task.Private && !policy.Local {
		return status, ErrDenied
	}
	descriptor, err := evaluation.DescribeEvaluator(policy.Evaluator)
	if err != nil {
		return status, ErrInvalid
	}
	verified, err := c.RecordedOutcome(ctx, routes, key, task)
	if err != nil {
		return status, err
	}
	receipt := verified.Receipt()
	receiptHash, _ := receipt.Digest()
	executionHash, _ := receipt.Execution.Digest()
	identity, _ := json.Marshal(receipt)
	input := evaluation.EvaluatorRequest{Version: 1, Domain: task.Domain, Requirements: task.Prompt, Candidate: verified.Output(), Evidence: []evaluation.EvaluatorEvidence{{ID: "remote_execution", Content: string(identity)}}}
	if input.Validate() != nil {
		return status, ErrInvalid
	}
	admission := remoteEvaluationAdmission{Version: 1, ReceiptSHA256: receiptHash, ExecutionSHA256: executionHash, InputSHA256: hash(input), Descriptor: descriptor, Local: policy.Local, Timeout: policy.Timeout}
	status = RemoteEvaluationStatus{Version: 1, ReceiptSHA256: receiptHash, Status: "not_started"}
	// Record canonical execution only, never quality, before preparing its evaluator.
	ledger, err := verified.recordLedger(ctx, root, time.Now().UTC())
	if err != nil {
		return status, err
	}
	snapshot, err := ledger.Snapshot(ctx, time.Now().UTC())
	closeErr := ledger.Close()
	if err != nil {
		return status, err
	}
	if closeErr != nil {
		return status, closeErr
	}
	head, found, err := snapshot.ReviewState(executionHash)
	if err != nil || !found {
		return status, ErrConflict
	}
	scope := hash(struct{ Destination, Caller string }{receipt.Route.Destination, receipt.Route.CallerFingerprint})
	attempts, err := OpenRouteStore(filepath.Join(root, scope, "evaluations"))
	if err != nil {
		return status, err
	}
	directory := filepath.Join(attempts.directory, receiptHash)
	// Do not create a competing automatic review after any operator head exists.
	if _, err = os.Lstat(directory); errors.Is(err, os.ErrNotExist) && head.Head != nil {
		return status, ErrConflict
	}
	created := false
	if err = os.Mkdir(directory, 0700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return status, err
	}
	status.Status = "started"
	if err = attempts.syncDirectory(); err != nil {
		return status, err
	}
	admissionPath := filepath.Join(directory, "admission.json")
	if created {
		admission.StartedAt = time.Now().UTC()
		body, _ := json.Marshal(admission)
		if err = publishPrivateDocument(admissionPath, body, 4096); err != nil {
			return status, err
		}
	} else {
		body, e := readPrivateDocument(admissionPath, 4096, false)
		if e != nil {
			return status, ErrUnavailable
		}
		var saved remoteEvaluationAdmission
		if json.Unmarshal(body, &saved) != nil || saved.StartedAt.IsZero() || saved.StartedAt.After(time.Now().UTC()) {
			return status, ErrConflict
		}
		admission.StartedAt = saved.StartedAt
		expected, _ := json.Marshal(admission)
		if string(expected) != string(body) {
			return status, ErrConflict
		}
	}
	resultPath := filepath.Join(directory, "result.json")
	if created {
		// A descriptor can drift after admission. Never call a different evaluator.
		current, e := evaluation.DescribeEvaluator(policy.Evaluator)
		var result evaluation.EvaluatorResult
		if e == nil && current == descriptor {
			result, e = evaluation.InvokeEvaluator(ctx, policy.Evaluator, input, policy.Timeout)
		} else {
			e = ErrConflict
		}
		terminal := remoteEvaluationResult{Version: 1, AdmissionSHA256: hash(admission), Status: "failed", FinishedAt: time.Now().UTC()}
		if e == nil && result.Descriptor == descriptor {
			terminal.Status = "completed"
			terminal.Audit = &result.Audit
		}
		body, e := json.Marshal(terminal)
		if e != nil {
			return status, ErrInvalid
		}
		// Persist terminal evidence even if the request was canceled during review.
		if e = publishPrivateDocument(resultPath, body, 128<<10); e != nil {
			return status, e
		}
	}
	body, err := readPrivateDocument(resultPath, 128<<10, false)
	if errors.Is(err, os.ErrNotExist) {
		return status, ErrUnavailable
	}
	if err != nil {
		return status, err
	}
	var terminal remoteEvaluationResult
	if json.Unmarshal(body, &terminal) != nil || terminal.Version != 1 || terminal.AdmissionSHA256 != hash(admission) || terminal.FinishedAt.Before(admission.StartedAt) || terminal.FinishedAt.After(time.Now().UTC()) {
		return status, ErrConflict
	}
	canonical, _ := json.Marshal(terminal)
	if string(canonical) != string(body) {
		return status, ErrConflict
	}
	if terminal.Status == "failed" && terminal.Audit == nil {
		status.Status = "failed"
		return status, ErrUnavailable
	}
	if terminal.Status != "completed" || terminal.Audit == nil {
		return status, ErrConflict
	}
	auditBody, _ := json.Marshal(terminal.Audit)
	audit, err := evaluation.ParseAudit(auditBody, evaluation.AuditContext{EvaluatorID: descriptor.ID, RubricVersion: descriptor.RubricVersion, Domain: task.Domain, AllowedEvidenceRefs: []string{"requirements", "candidate", "remote_execution"}})
	if err != nil {
		return status, ErrConflict
	}
	review := harness.Review{Version: 1, ID: hash(struct{ Kind, Receipt string }{"remote-evaluation-v1", receiptHash}), ExecutionDigest: executionHash, Reviewer: descriptor.ID, CreatedAt: terminal.FinishedAt, Verdict: "unverified"}
	if audit.Verdict != "abstain" && audit.Confidence > 0 {
		review.Method = "automated_ai"
		review.MethodVersion = hash(descriptor)
		review.Confidence = audit.Confidence
		review.Verdict = "failed"
		if audit.Verdict == "accept" {
			review.Verdict = "passed"
			review.Quality = 1
		}
	}
	bound := OutcomeReview{Version: 1, ReceiptSHA256: receiptHash, Review: review}
	status.Status = "completed"
	status.Review = &bound
	if err = c.ReviewRecordedOutcome(ctx, routes, root, key, task, bound); err != nil {
		return status, err
	}
	status.ReviewApplied = true
	return status, nil
}

// EvaluateAutomaticOutcome retains the original saved model/harness/destination;
// it never performs discovery or changes an existing routing choice.
func (c *Client) EvaluateAutomaticOutcome(ctx context.Context, routes *RouteStore, root, key string, request AutomaticRequest, policy RemoteEvaluator) (RemoteEvaluationStatus, error) {
	task, _, err := routes.ResolveAutomatic(key, request)
	if err != nil {
		return RemoteEvaluationStatus{}, err
	}
	return c.EvaluateRecordedOutcome(ctx, routes, root, key, task, policy)
}
