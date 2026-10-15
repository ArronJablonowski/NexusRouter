package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"time"
)

// Approval requests borrow the dispatcher's already validated store. Fresh
// standalone controls still cannot create/migrate storage. Identity, schema and
// journal checks are retained on every borrow; stale handles never reopen.
func (s *Service) openApprovalStore(ctx context.Context) (*telemetry.Store, func(), error) {
	s.taskStoreMu.Lock()
	db := s.taskStore
	s.taskStoreMu.Unlock()
	if db != nil {
		if err := db.ValidateIdentity(ctx, s.settings.Telemetry.Database); err != nil {
			return nil, nil, err
		}
		return db, func() {}, nil
	}
	db, err := telemetry.OpenApprovalControl(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = db.Close() }, nil
}
func (s *Service) InspectApproval(ctx context.Context, task, id string) (approvals.Record, error) {
	if ctx == nil || !sessions.ValidEventPageID(task) || !sessions.ValidEventPageID(id) {
		return approvals.Record{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, release, err := s.openTaskReadStore(ctx)
	if err != nil {
		return approvals.Record{}, ErrInspection
	}
	defer release()
	record, err := db.ReadApproval(ctx, id)
	if err != nil || record.Validate() != nil || record.Request.TaskID != task || record.Request.ID != id {
		return approvals.Record{}, ErrInspection
	}
	return record, nil
}
func (s *Service) ListApprovals(ctx context.Context, options approvals.ListOptions) (approvals.Page, error) {
	if ctx == nil || options.Validate() != nil {
		return approvals.Page{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, release, err := s.openTaskReadStore(ctx)
	if err != nil {
		return approvals.Page{}, ErrInspection
	}
	defer release()
	page, err := db.ListApprovals(ctx, options)
	if err != nil || page.Validate() != nil || page.Query != options {
		return approvals.Page{}, ErrInspection
	}
	return page, nil
}
func (s *Service) approvalTaskSnapshot(ctx context.Context, task string) (sessions.Snapshot, error) {
	if ctx == nil || !sessions.ValidEventPageID(task) {
		return sessions.Snapshot{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, release, err := s.openTaskReadStore(ctx)
	if err != nil {
		return sessions.Snapshot{}, ErrInspection
	}
	defer release()
	snapshot, err := db.TaskSnapshot(ctx, task)
	if err != nil {
		return sessions.Snapshot{}, ErrInspection
	}
	return snapshot, nil
}

// ApprovalExecutionStatus observes durable execution evidence without granting
// authority or replaying a tool. It shares the daemon's validated store.
func (s *Service) ApprovalExecutionStatus(ctx context.Context, task, id string) (approvals.ExecutionStatus, error) {
	if ctx == nil || !sessions.ValidEventPageID(task) || !sessions.ValidEventPageID(id) {
		return approvals.ExecutionStatus{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, release, err := s.openTaskReadStore(ctx)
	if err != nil {
		return approvals.ExecutionStatus{}, ErrInspection
	}
	defer release()
	status, err := db.ApprovalExecutionStatus(ctx, task, id, time.Now().UTC())
	if err != nil || status.Validate() != nil || status.Approval.Request.TaskID != task || status.Approval.Request.ID != id {
		return approvals.ExecutionStatus{}, ErrInspection
	}
	return status, nil
}
