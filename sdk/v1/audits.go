package v1

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

var ErrAuditOperation = app.ErrAuditOperation
var ErrAuditDelivery = app.ErrAuditDelivery

// AuditRequest starts one independently attributed advisory review. Its
// idempotency key scopes retries of the same request; it is not persisted raw.
type AuditRequest = evaluation.AuditRequest

// AuditStatus is a content-bounded projection of durable review state. It has
// no dedicated prompt, candidate-output, tool-payload, provider-error, endpoint,
// or credential fields. Model-authored findings are task-derived content and
// require the same authorization and handling as task inspection.
type AuditStatus = evaluation.AuditStatus
type AuditEvent = evaluation.AuditEvent
type AuditEventPage = evaluation.AuditEventPage

// RunAudit starts or replays an idempotent audit operation and emits committed
// lifecycle events in sequence. The callback is synchronous and must return
// only after consuming its detached event. A callback failure stops delivery;
// callers can resume with ReadAuditEvents without repeating model inference.
func (c *Client) RunAudit(ctx context.Context, request AuditRequest, emit func(AuditEvent) error) (AuditStatus, error) {
	if !c.valid(ctx) || request.Validate() != nil || emit == nil {
		return AuditStatus{Version: 1}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return AuditStatus{Version: 1}, err
	}
	status, err := c.service.RunAudit(ctx, request, func(event evaluation.AuditEvent) error {
		if event.Validate() != nil {
			return evaluation.ErrAudit
		}
		return emit(cloneAuditEvent(event))
	})
	if err != nil {
		if status.Validate() == nil {
			return cloneAuditStatus(status), err
		}
		return AuditStatus{Version: 1}, err
	}
	if status.Validate() != nil {
		return AuditStatus{Version: 1}, ErrAdmission
	}
	return cloneAuditStatus(status), nil
}

// InspectAudit reads one durable operation without creating storage,
// dispatching a provider, or changing its lifecycle.
func (c *Client) InspectAudit(ctx context.Context, task, id string) (AuditStatus, error) {
	if !c.valid(ctx) || !validAuditOperationIdentity(task, id) {
		return AuditStatus{Version: 1}, contextErrorOrAdmission(ctx)
	}
	if err := ctx.Err(); err != nil {
		return AuditStatus{Version: 1}, err
	}
	status, err := c.service.InspectAudit(ctx, task, id)
	if err != nil {
		return AuditStatus{Version: 1}, err
	}
	if status.Validate() != nil {
		return AuditStatus{Version: 1}, ErrAdmission
	}
	return cloneAuditStatus(status), nil
}

// CancelAudit durably requests cancellation. A terminal return describes the
// persisted lifecycle; it is not permission to repeat an uncertain operation.
func (c *Client) CancelAudit(ctx context.Context, task, id string) (AuditStatus, error) {
	if !c.valid(ctx) || !validAuditOperationIdentity(task, id) {
		return AuditStatus{Version: 1}, contextErrorOrAdmission(ctx)
	}
	if err := ctx.Err(); err != nil {
		return AuditStatus{Version: 1}, err
	}
	status, err := c.service.CancelAudit(ctx, task, id)
	if err != nil {
		return AuditStatus{Version: 1}, err
	}
	if status.Validate() != nil {
		return AuditStatus{Version: 1}, ErrAdmission
	}
	return cloneAuditStatus(status), nil
}

// ReadAuditEvents returns committed lifecycle events strictly after after.
// Persist NextSequence only after processing the whole detached page.
func (c *Client) ReadAuditEvents(ctx context.Context, task, id string, after int64) (AuditEventPage, error) {
	if !c.valid(ctx) || !validAuditOperationIdentity(task, id) || after < 0 {
		return AuditEventPage{Version: 1}, contextErrorOrAdmission(ctx)
	}
	if err := ctx.Err(); err != nil {
		return AuditEventPage{Version: 1}, err
	}
	page, err := c.service.ReadAuditEvents(ctx, task, id, after)
	if err != nil {
		return AuditEventPage{Version: 1}, err
	}
	if page.Validate() != nil {
		return AuditEventPage{Version: 1}, ErrAdmission
	}
	return cloneAuditEventPage(page), nil
}

func validAuditOperationIdentity(task, id string) bool {
	if !evaluation.ValidAuditOperationID(task) || !evaluation.ValidAuditOperationID(id) || len(id) != 64 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func contextErrorOrAdmission(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrAdmission
}

func cloneAuditStatus(in evaluation.AuditStatus) AuditStatus {
	out := in
	out.Findings = make([]evaluation.AuditFinding, len(in.Findings))
	for i, finding := range in.Findings {
		out.Findings[i] = finding
		out.Findings[i].EvidenceRefs = append([]string(nil), finding.EvidenceRefs...)
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

func cloneAuditEvent(in evaluation.AuditEvent) AuditEvent {
	out := in
	out.Status = cloneAuditStatus(in.Status)
	return out
}

func cloneAuditEventPage(in evaluation.AuditEventPage) AuditEventPage {
	out := in
	out.Events = make([]evaluation.AuditEvent, len(in.Events))
	for i, event := range in.Events {
		out.Events[i] = cloneAuditEvent(event)
	}
	return out
}
