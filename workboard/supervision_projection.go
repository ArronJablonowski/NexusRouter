package workboard

import (
	"context"
	"time"
)

const MaxSupervisionPageItems = 100

type SupervisionState string

const (
	SupervisionReady    SupervisionState = "ready"
	SupervisionRunning  SupervisionState = "running"
	SupervisionStalled  SupervisionState = "stalled"
	SupervisionOrphaned SupervisionState = "orphaned"
)

type SupervisionReason string

const (
	SupervisionDependenciesSatisfied SupervisionReason = "dependencies_satisfied"
	SupervisionLeaseHealthy          SupervisionReason = "lease_healthy"
	SupervisionHeartbeatStale        SupervisionReason = "heartbeat_stale"
	SupervisionLeaseExpired          SupervisionReason = "lease_expired"
	SupervisionTaskCompleted         SupervisionReason = "task_completed_with_claim"
	SupervisionTaskFailed            SupervisionReason = "task_failed_with_claim"
	SupervisionTaskCanceled          SupervisionReason = "task_canceled_with_claim"
)

// SupervisionActions identifies requests that are safe to make from the
// observed state. RecoveryCheck means only that proof-gated recovery may be
// inspected; it is never a claim that the old process stopped or work may run.
type SupervisionActions struct {
	Claim         bool `json:"claim"`
	PauseRequest  bool `json:"pause_request"`
	ResumeRequest bool `json:"resume_request"`
	CancelRequest bool `json:"cancel_request"`
	RecoveryCheck bool `json:"recovery_check"`
}

type SupervisionItem struct {
	Version       int                `json:"version"`
	BoardID       string             `json:"board_id"`
	CardID        string             `json:"card_id"`
	CardRevision  int64              `json:"card_revision"`
	State         SupervisionState   `json:"state"`
	Reason        SupervisionReason  `json:"reason"`
	AttemptID     string             `json:"attempt_id,omitempty"`
	ClaimID       string             `json:"claim_id,omitempty"`
	ClaimRevision int64              `json:"claim_revision,omitempty"`
	PausePhase    PausePhase         `json:"pause_phase,omitempty"`
	AssigneeID    string             `json:"assignee_id,omitempty"`
	WorkerID      string             `json:"worker_id,omitempty"`
	TaskID        string             `json:"task_id,omitempty"`
	LastHeartbeat time.Time          `json:"last_heartbeat,omitempty"`
	ExpiresAt     time.Time          `json:"expires_at,omitempty"`
	Actions       SupervisionActions `json:"actions"`
}

func (i SupervisionItem) Validate() error {
	if i.Version != SchemaVersion || !validLifecycleIDs(i.BoardID, i.CardID) || i.CardRevision < 1 {
		return fail(CodeInvalid, "supervision_item")
	}
	if i.State == SupervisionReady {
		if i.Reason != SupervisionDependenciesSatisfied || i.AttemptID != "" || i.ClaimID != "" || i.ClaimRevision != 0 ||
			i.WorkerID != "" || i.TaskID != "" || i.PausePhase != PauseNone || !i.LastHeartbeat.IsZero() || !i.ExpiresAt.IsZero() ||
			!optionalID(i.AssigneeID) || i.Actions != (SupervisionActions{Claim: true}) {
			return fail(CodeInvalid, "supervision_item")
		}
		return nil
	}
	if i.State != SupervisionRunning && i.State != SupervisionStalled && i.State != SupervisionOrphaned ||
		!validLifecycleIDs(i.AttemptID, i.ClaimID, i.WorkerID) || !optionalID(i.AssigneeID) || !optionalID(i.TaskID) || i.ClaimRevision < 1 || !validTime(i.LastHeartbeat) || !validTime(i.ExpiresAt) ||
		!i.LastHeartbeat.Before(i.ExpiresAt) || !validPausePhase(i.PausePhase) || i.Actions.Claim ||
		(i.AssigneeID != "" && i.AssigneeID != i.WorkerID) {
		return fail(CodeInvalid, "supervision_item")
	}
	switch i.State {
	case SupervisionRunning:
		canPause := i.PausePhase == PauseNone
		canResume := i.PausePhase == PauseAcknowledged
		if i.Reason != SupervisionLeaseHealthy || i.Actions.PauseRequest != canPause || i.Actions.ResumeRequest != canResume || i.Actions.RecoveryCheck {
			return fail(CodeInvalid, "supervision_item")
		}
	case SupervisionStalled:
		if i.Reason != SupervisionHeartbeatStale && i.Reason != SupervisionLeaseExpired || i.Actions.PauseRequest || i.Actions.ResumeRequest || !i.Actions.RecoveryCheck {
			return fail(CodeInvalid, "supervision_item")
		}
	case SupervisionOrphaned:
		if i.Reason != SupervisionTaskCompleted && i.Reason != SupervisionTaskFailed && i.Reason != SupervisionTaskCanceled ||
			i.TaskID == "" || i.Actions.PauseRequest || i.Actions.ResumeRequest || !i.Actions.RecoveryCheck {
			return fail(CodeInvalid, "supervision_item")
		}
	}
	return nil
}

type SupervisionOptions struct {
	After string
	Limit int
}

func (o SupervisionOptions) Validate() error {
	if !validCursor(o.After) || o.Limit < 1 || o.Limit > MaxSupervisionPageItems {
		return fail(CodeInvalid, "supervision_options")
	}
	return nil
}

type SupervisionQuery struct {
	BoardID     string
	After       string
	Limit       int
	ObservedAt  time.Time
	StaleBefore time.Time
}

func (q SupervisionQuery) Validate() error {
	if !validID(q.BoardID) || !validCursor(q.After) || q.Limit < 1 || q.Limit > MaxSupervisionPageItems ||
		!validTime(q.ObservedAt) || !validTime(q.StaleBefore) || q.StaleBefore.After(q.ObservedAt) {
		return fail(CodeInvalid, "supervision_query")
	}
	return nil
}

type SupervisionPage struct {
	Version       int               `json:"version"`
	BoardID       string            `json:"board_id"`
	BoardRevision int64             `json:"board_revision"`
	ObservedAt    time.Time         `json:"observed_at"`
	Items         []SupervisionItem `json:"items"`
	NextCursor    string            `json:"next_cursor,omitempty"`
	HasMore       bool              `json:"has_more"`
}

func (p SupervisionPage) Validate() error {
	if p.Version != SchemaVersion || !validID(p.BoardID) || p.BoardRevision < 1 || !validTime(p.ObservedAt) ||
		len(p.Items) > MaxSupervisionPageItems || p.HasMore != (p.NextCursor != "") || !validCursor(p.NextCursor) || p.HasMore && len(p.Items) == 0 {
		return fail(CodeInvalid, "supervision_page")
	}
	previous := ""
	for _, item := range p.Items {
		if item.Validate() != nil || item.BoardID != p.BoardID || previous != "" && item.CardID <= previous {
			return fail(CodeInvalid, "supervision_page")
		}
		previous = item.CardID
	}
	return nil
}

type SupervisionRepository interface {
	ReadSupervisionPage(context.Context, SupervisionQuery) (SupervisionPage, error)
}

type SupervisionService struct {
	repository SupervisionRepository
	authority  AuthoritySource
	now        func() time.Time
	staleAfter time.Duration
}

func NewSupervisionService(repository SupervisionRepository, authority AuthoritySource, now func() time.Time, staleAfter time.Duration) (*SupervisionService, error) {
	if repository == nil || authority == nil || now == nil || staleAfter < MinLeaseTTL || staleAfter > MaxLeaseTTL {
		return nil, fail(CodeInvalid, "service")
	}
	return &SupervisionService{repository: repository, authority: authority, now: now, staleAfter: staleAfter}, nil
}

func (s *SupervisionService) Read(ctx context.Context, boardID string, options SupervisionOptions) (SupervisionPage, error) {
	if !validID(boardID) || options.Validate() != nil {
		return SupervisionPage{}, fail(CodeInvalid, "supervision")
	}
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil || authority.Validate() != nil {
		return SupervisionPage{}, fail(CodeInvalid, "authority")
	}
	now := s.now().UTC()
	return s.repository.ReadSupervisionPage(ctx, SupervisionQuery{BoardID: boardID, After: options.After, Limit: options.Limit,
		ObservedAt: now, StaleBefore: now.Add(-s.staleAfter)})
}
