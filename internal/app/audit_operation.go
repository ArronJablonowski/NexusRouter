package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

var (
	ErrAuditOperation = errors.New("audit unavailable")
	ErrAuditDelivery  = errors.New("audit event delivery failed")
	errAuditReplay    = errors.New("audit operation already admitted")
)

type auditOperation struct {
	id, requestDigest string
	onAdmitted        func(evaluation.ReviewAttempt) error
}

// RunAudit admits at most one provider dispatch for a caller key. Exact
// retries replay the two lifecycle events derived from durable state. A
// process that finds an earlier started operation does not guess whether its
// provider call happened and never dispatches it again.
func (s *Service) RunAudit(ctx context.Context, request evaluation.AuditRequest, emit func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
	zero := evaluation.AuditStatus{}
	if s == nil || ctx == nil || ctx.Err() != nil || request.Validate() != nil || len(request.IdempotencyKey) < 16 || emit == nil {
		return zero, ErrAdmission
	}
	if request.MaxCost == 0 { // Canonicalize negative zero.
		request.MaxCost = 0
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !auditOperationRequestClean(request, secrets) {
		return zero, ErrAdmission
	}
	id := auditOperationID(request.IdempotencyKey)
	digest, err := s.auditRequestDigest(request)
	if err != nil {
		return zero, ErrAdmission
	}
	if status, found, err := s.lookupAuditStatus(ctx, request.TaskID, id, request.ReviewerModelID, digest); err != nil {
		return zero, err
	} else if found {
		return emitAuditReplay(status, emit)
	}

	op := &auditOperation{id: id, requestDigest: digest}
	op.onAdmitted = func(attempt evaluation.ReviewAttempt) error {
		status, err := publicAuditStatus(attempt, nil, secrets)
		if err != nil {
			return err
		}
		return emit(evaluation.AuditEvent{Version: 1, AuditID: id, Sequence: 1, Status: status})
	}
	_, runErr := s.auditTask(ctx, request.TaskID, request.ReviewerModelID, request.MaxCost, op)
	if errors.Is(runErr, errAuditReplay) {
		status, found, err := s.lookupAuditStatus(context.WithoutCancel(ctx), request.TaskID, id, request.ReviewerModelID, digest)
		if err != nil || !found {
			return zero, auditOperationError(ctx, err)
		}
		return emitAuditReplay(status, emit)
	}
	status, found, inspectErr := s.lookupAuditStatus(context.WithoutCancel(ctx), request.TaskID, id, request.ReviewerModelID, digest)
	if inspectErr != nil || !found {
		return zero, auditOperationError(ctx, errors.Join(runErr, inspectErr))
	}
	// Admission delivery failed before dispatch. Its durable cancellation is
	// the second and terminal event, so a retry can replay both safely.
	if errors.Is(runErr, ErrAuditDelivery) {
		return status, ErrAuditDelivery
	}
	if status.Status == "pending" {
		// A started row without a terminal update is deliberately indeterminate.
		// It is inspectable but is never silently retried.
		return status, auditOperationError(ctx, runErr)
	}
	event := evaluation.AuditEvent{Version: 1, AuditID: id, Sequence: 2, Status: status}
	if event.Validate() != nil || emit(event) != nil {
		return status, ErrAuditDelivery
	}
	if status.Status == "canceled" && ctx.Err() == nil {
		// A concurrent CancelAudit owns the terminal CAS. The provider-side
		// cancellation and cleanup conflict are implementation details; the
		// durable canceled projection is the successful operation outcome.
		return status, nil
	}
	if runErr != nil {
		return status, auditOperationError(ctx, runErr)
	}
	return status, nil
}

// InspectAudit reads an operation by its opaque returned ID. It never creates
// or migrates a database and re-applies the current secret policy.
func (s *Service) InspectAudit(ctx context.Context, task, operation string) (evaluation.AuditStatus, error) {
	zero := evaluation.AuditStatus{}
	if s == nil || ctx == nil || ctx.Err() != nil || !auditOperationIdentity(task, operation) {
		return zero, ErrAdmission
	}
	status, found, err := s.lookupAuditStatus(ctx, task, operation, "", "")
	if err != nil {
		return zero, err
	}
	if !found {
		return zero, sql.ErrNoRows
	}
	return status, nil
}

// CancelAudit durably resolves a pending operation as canceled. A terminal
// operation wins a race and is returned unchanged.
func (s *Service) CancelAudit(ctx context.Context, task, operation string) (evaluation.AuditStatus, error) {
	zero := evaluation.AuditStatus{}
	if s == nil || ctx == nil || ctx.Err() != nil || !auditOperationIdentity(task, operation) {
		return zero, ErrAdmission
	}
	before, err := s.InspectAudit(ctx, task, operation)
	if err != nil || before.Status != "pending" {
		return before, err
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return zero, auditOperationError(ctx, err)
	}
	defer db.Close()
	attempt, err := db.CancelReview(ctx, task, operation, time.Now().UTC())
	if err != nil {
		return zero, auditOperationError(ctx, err)
	}
	status, err := publicAuditStatus(attempt, auditForAttempt(ctx, db, attempt), memorySecrets(s.settings, s.secret))
	if err != nil {
		return zero, auditOperationError(ctx, err)
	}
	return status, nil
}

// ReadAuditEvents replays the bounded lifecycle stream without a separate
// mutable event log: sequence one is durable admission and sequence two is the
// terminal attempt plus its atomically linked audit record, when present.
func (s *Service) ReadAuditEvents(ctx context.Context, task, operation string, after int64) (evaluation.AuditEventPage, error) {
	zero := evaluation.AuditEventPage{}
	if s == nil || ctx == nil || ctx.Err() != nil || !auditOperationIdentity(task, operation) || after < 0 {
		return zero, ErrAdmission
	}
	status, err := s.InspectAudit(ctx, task, operation)
	if err != nil {
		return zero, err
	}
	head := int64(1)
	if status.Status != "pending" {
		head = 2
	}
	if after > head {
		return zero, ErrAdmission
	}
	events := auditEvents(status)
	page := evaluation.AuditEventPage{Version: 1, AuditID: operation, FromSequence: after, NextSequence: head, HeadSequence: head, Events: append([]evaluation.AuditEvent(nil), events[after:head]...)}
	if page.Validate() != nil {
		return zero, ErrAuditOperation
	}
	return page, nil
}

func (s *Service) auditRequestDigest(request evaluation.AuditRequest) (string, error) {
	request.IdempotencyKey = ""
	body, err := json.Marshal(struct {
		Version  int                     `json:"version"`
		Request  evaluation.AuditRequest `json:"request"`
		Settings any                     `json:"settings"`
	}{1, request, s.settings})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func auditOperationID(key string) string {
	sum := sha256.Sum256(append([]byte("audit-operation-v1\x00"), []byte(key)...))
	return hex.EncodeToString(sum[:])
}

func auditOperationIdentity(task, operation string) bool {
	if !evaluation.ValidAuditOperationID(task) || len(operation) != sha256.Size*2 || strings.ToLower(operation) != operation {
		return false
	}
	_, err := hex.DecodeString(operation)
	return err == nil
}

func auditOperationRequestClean(request evaluation.AuditRequest, secrets []string) bool {
	return redact(request.IdempotencyKey, secrets) == request.IdempotencyKey && redact(request.TaskID, secrets) == request.TaskID && redact(request.ReviewerModelID, secrets) == request.ReviewerModelID
}

func (s *Service) lookupAuditStatus(ctx context.Context, task, operation, reviewer, digest string) (evaluation.AuditStatus, bool, error) {
	zero := evaluation.AuditStatus{}
	if ctx == nil || ctx.Err() != nil {
		return zero, false, auditOperationError(ctx, context.Canceled)
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(readCtx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, sql.ErrNoRows) {
			return zero, false, nil
		}
		return zero, false, auditOperationError(readCtx, err)
	}
	defer db.Close()
	attempt, err := db.ReviewAttempt(readCtx, operation)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, auditOperationError(readCtx, err)
	}
	if attempt.TaskID != task {
		// Do not reveal that an opaque operation belongs to another task.
		return zero, false, sql.ErrNoRows
	}
	if attempt.ReviewerID == "" {
		return zero, false, ErrAuditOperation
	}
	if (reviewer != "" && attempt.ReviewerID != reviewer) || (digest != "" && attempt.RequestDigest != digest) {
		return zero, false, telemetry.ErrConflict
	}
	status, err := publicAuditStatus(attempt, auditForAttempt(readCtx, db, attempt), memorySecrets(s.settings, s.secret))
	if err != nil {
		return zero, false, auditOperationError(readCtx, err)
	}
	return status, true, nil
}

func auditForAttempt(ctx context.Context, db *telemetry.Store, attempt evaluation.ReviewAttempt) *evaluation.AuditRecord {
	if attempt.Status != "completed" {
		return nil
	}
	record, err := db.Audit(ctx, attempt.AuditID)
	if err != nil {
		return nil
	}
	return &record
}

func publicAuditStatus(attempt evaluation.ReviewAttempt, record *evaluation.AuditRecord, secrets []string) (evaluation.AuditStatus, error) {
	status, err := evaluation.NewAuditStatus(attempt, record)
	if err != nil {
		return evaluation.AuditStatus{}, err
	}
	identities := []string{status.ID, status.TaskID, status.SourceAttemptID, status.ErrorCode, status.ReviewerID, status.EvaluatorModel, status.EvaluatorProvider, status.RubricVersion, status.Domain, status.AuditID}
	identities = append(identities, status.EvidenceRefs...)
	for _, finding := range status.Findings {
		identities = append(identities, finding.EvidenceRefs...)
	}
	for _, value := range identities {
		if redact(value, secrets) != value {
			return evaluation.AuditStatus{}, evaluation.ErrAudit
		}
	}
	findings := status.Findings
	status.Findings = make([]evaluation.AuditFinding, len(findings))
	copy(status.Findings, findings)
	for i := range status.Findings {
		status.Findings[i].EvidenceRefs = append([]string(nil), status.Findings[i].EvidenceRefs...)
		status.Findings[i].Summary = redact(status.Findings[i].Summary, secrets)
	}
	if status.Validate() != nil {
		return evaluation.AuditStatus{}, evaluation.ErrAudit
	}
	return status, nil
}

func auditEvents(terminal evaluation.AuditStatus) []evaluation.AuditEvent {
	pending := cloneAuditStatus(terminal)
	pending.Status, pending.TerminalDisposition, pending.ErrorCode, pending.RubricVersion, pending.Domain, pending.AuditID = "pending", "", "", "", "", ""
	pending.Findings, pending.EvidenceRefs, pending.Usage, pending.ElapsedMillis, pending.FinishedAt = []evaluation.AuditFinding{}, []string{}, nil, 0, nil
	out := []evaluation.AuditEvent{{Version: 1, AuditID: terminal.ID, Sequence: 1, Status: pending}}
	if terminal.Status != "pending" {
		out = append(out, evaluation.AuditEvent{Version: 1, AuditID: terminal.ID, Sequence: 2, Status: cloneAuditStatus(terminal)})
	}
	return out
}

func cloneAuditStatus(in evaluation.AuditStatus) evaluation.AuditStatus {
	out := in
	out.Findings = make([]evaluation.AuditFinding, len(in.Findings))
	for i := range in.Findings {
		out.Findings[i] = in.Findings[i]
		out.Findings[i].EvidenceRefs = append([]string(nil), in.Findings[i].EvidenceRefs...)
	}
	out.EvidenceRefs = make([]string, len(in.EvidenceRefs))
	copy(out.EvidenceRefs, in.EvidenceRefs)
	out.EvidencePrecedence = append([]evaluation.Source(nil), in.EvidencePrecedence...)
	if in.Usage != nil {
		usage := *in.Usage
		out.Usage = &usage
	}
	if in.StartedAt != nil {
		started := *in.StartedAt
		out.StartedAt = &started
	}
	if in.FinishedAt != nil {
		finished := *in.FinishedAt
		out.FinishedAt = &finished
	}
	return out
}

func emitAuditReplay(status evaluation.AuditStatus, emit func(evaluation.AuditEvent) error) (evaluation.AuditStatus, error) {
	for _, event := range auditEvents(status) {
		if event.Validate() != nil || emit(event) != nil {
			return status, ErrAuditDelivery
		}
	}
	return status, nil
}

func auditOperationError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, sql.ErrNoRows, telemetry.ErrConflict, ErrAdmission, ErrAuditDelivery} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrAuditOperation
}

// monitorAuditCancellation owns and joins its polling goroutine. Any loss of
// durable observation cancels inference fail-closed; it never survives the
// caller's audit invocation.
func monitorAuditCancellation(parent context.Context, path, task, operation string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		cancel()
		close(done)
		return ctx, func() { cancel() }
	}
	attempt, err := db.ReviewAttempt(ctx, operation)
	if err != nil || attempt.TaskID != task || attempt.Status != "started" {
		db.Close()
		cancel()
		close(done)
		return ctx, func() { cancel() }
	}
	go func() {
		defer close(done)
		defer db.Close()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			attempt, err := db.ReviewAttempt(ctx, operation)
			if err != nil || attempt.TaskID != task || (attempt.Status == "failed" && attempt.Code == "canceled") {
				cancel()
				return
			}
			if attempt.Status != "started" {
				return
			}
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return ctx, func() {
		once.Do(func() { close(stop) })
		<-done
		cancel()
	}
}
