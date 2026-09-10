package workboard

import (
	"context"
	"time"
)

const (
	MaxSnapshotCheckpoints = 100
	MaxLifecyclePageItems  = 25
	MaxAttemptLinks        = 128
)

type ClaimSnapshot struct {
	ID            string
	BoardID       string
	CardID        string
	AttemptID     string
	Revision      int64
	State         string
	OwnerID       string
	OwnerType     string
	TaskID        string
	ExpiresAt     time.Time
	LastHeartbeat time.Time
	ReleasedAt    *time.Time
}

type AttemptSnapshot struct {
	ID               string
	BoardID          string
	CardID           string
	Ordinal          int
	Revision         int64
	State            string
	WorkerID         string
	CriteriaRevision int64
	CriteriaDigest   string
	PolicyDigest     string
	Budget           WorkBudget
	Criteria         []AcceptanceCriterion
	TaskIDs          []string
	SessionIDs       []string
	Claim            *ClaimSnapshot
	Reassignment     *ReassignmentRecord
	Candidate        *CandidateRecord
	Evidence         []EvidenceRecord
	Acceptance       *AcceptanceRecord
	StartedAt        time.Time
	EndedAt          *time.Time
}

func (a AttemptSnapshot) Validate() error {
	if !validLifecycleIDs(a.ID, a.BoardID, a.CardID, a.WorkerID) || a.Ordinal < 1 || a.Ordinal > MaxAttemptsPerCard || a.Revision < 1 ||
		a.CriteriaRevision < 1 || !digest(a.CriteriaDigest) || !digest(a.PolicyDigest) || a.Budget.Validate() != nil ||
		validateAcceptanceCriteria(a.Criteria, a.CriteriaRevision) != nil || !validLifecycleLinks(a.TaskIDs) || !validLifecycleLinks(a.SessionIDs) ||
		!validAttemptState(a.State) || !validTime(a.StartedAt) || (a.EndedAt == nil) != (a.State == "running") ||
		a.EndedAt != nil && (!validTime(*a.EndedAt) || a.EndedAt.Before(a.StartedAt)) || len(a.Evidence) > MaxEvaluationEvidence {
		return fail(CodeInvalid, "attempt_snapshot")
	}
	if a.Claim == nil || !validLifecycleIDs(a.Claim.ID, a.Claim.BoardID, a.Claim.CardID, a.Claim.AttemptID, a.Claim.OwnerID) ||
		a.Claim.BoardID != a.BoardID || a.Claim.CardID != a.CardID || a.Claim.AttemptID != a.ID || a.Claim.OwnerID != a.WorkerID || a.Claim.OwnerType != "worker" ||
		ValidateLease(Lease{BoardID: a.Claim.BoardID, CardID: a.Claim.CardID, AttemptID: a.Claim.AttemptID, ClaimID: a.Claim.ID,
			Revision: a.Claim.Revision, State: LeaseState(a.Claim.State), OwnerID: a.Claim.OwnerID, ExpiresAt: a.Claim.ExpiresAt,
			LastHeartbeat: a.Claim.LastHeartbeat, ReleasedAt: a.Claim.ReleasedAt}) != nil {
		return fail(CodeInvalid, "attempt_snapshot")
	}
	if a.Reassignment != nil && (a.Reassignment.Validate() != nil || a.Ordinal < 2 ||
		a.Reassignment.BoardID != a.BoardID || a.Reassignment.CardID != a.CardID ||
		a.Reassignment.SuccessorAttemptID != a.ID || a.Reassignment.SuccessorClaimID != a.Claim.ID ||
		!a.Reassignment.CreatedAt.Equal(a.StartedAt)) {
		return fail(CodeInvalid, "attempt_snapshot")
	}
	for index, evidence := range a.Evidence {
		if evidence.Validate() != nil || evidence.BoardID != a.BoardID || evidence.CardID != a.CardID || evidence.AttemptID != a.ID ||
			index > 0 && evidence.Revision <= a.Evidence[index-1].Revision {
			return fail(CodeInvalid, "attempt_snapshot")
		}
	}
	if a.Candidate != nil && (a.Candidate.Validate() != nil || a.Candidate.BoardID != a.BoardID || a.Candidate.CardID != a.CardID || a.Candidate.AttemptID != a.ID) {
		return fail(CodeInvalid, "attempt_snapshot")
	}
	if a.Acceptance != nil && (a.Acceptance.Validate() != nil || a.Acceptance.BoardID != a.BoardID || a.Acceptance.CardID != a.CardID || a.Acceptance.AttemptID != a.ID) {
		return fail(CodeInvalid, "attempt_snapshot")
	}
	if (a.Candidate != nil) != (a.State == "review" || a.State == "accepted" || a.State == "rejected") ||
		(a.Acceptance != nil) != (a.State == "accepted" || a.State == "rejected") || a.State == "running" && a.Claim.State == string(LeaseReleased) ||
		a.State != "running" && a.Claim.State != string(LeaseReleased) {
		return fail(CodeInvalid, "attempt_snapshot")
	}
	return nil
}

func validLifecycleLinks(values []string) bool {
	if values == nil || len(values) > MaxAttemptLinks {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !validID(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

// CardLifecycleSnapshot is the bounded current-attempt view attached to a card
// page. Historical records are available through the explicitly paged read
// surface below; this projection never loads more than the most recent
// MaxSnapshotCheckpoints records.
type CardLifecycleSnapshot struct {
	CardID             string
	Attempt            *AttemptSnapshot
	Checkpoints        []CheckpointRecord
	CheckpointCount    int
	CheckpointsHasMore bool
}

type LifecycleSnapshotRepository interface {
	ReadCardLifecycleSnapshots(ctx context.Context, boardID string, cardIDs []string) (map[string]CardLifecycleSnapshot, error)
}

type AttemptHistoryOptions struct {
	After string
	Limit int
}

func (o AttemptHistoryOptions) Validate() error {
	if !validCursor(o.After) || o.Limit < 1 || o.Limit > MaxLifecyclePageItems {
		return fail(CodeInvalid, "attempt_history")
	}
	return nil
}

// AttemptHistoryRecord is deliberately compact. Full criteria, evidence,
// candidate and claim state are returned only by the bounded detail read.
type AttemptHistoryRecord struct {
	Version          int                 `json:"version"`
	ID               string              `json:"id"`
	BoardID          string              `json:"board_id"`
	CardID           string              `json:"card_id"`
	Ordinal          int                 `json:"ordinal"`
	Revision         int64               `json:"revision"`
	State            string              `json:"state"`
	WorkerID         string              `json:"worker_id"`
	CriteriaRevision int64               `json:"criteria_revision"`
	CheckpointCount  int                 `json:"checkpoint_count"`
	CandidateID      string              `json:"candidate_id,omitempty"`
	AcceptanceID     string              `json:"acceptance_id,omitempty"`
	Reassignment     *ReassignmentRecord `json:"reassignment,omitempty"`
	StartedAt        time.Time           `json:"started_at"`
	EndedAt          *time.Time          `json:"ended_at,omitempty"`
}

func (r AttemptHistoryRecord) Validate() error {
	if r.Version != SchemaVersion || !validLifecycleIDs(r.ID, r.BoardID, r.CardID, r.WorkerID) || r.Ordinal < 1 || r.Ordinal > MaxAttemptsPerCard ||
		r.Revision < 1 || r.CriteriaRevision < 1 || r.CheckpointCount < 0 || r.CheckpointCount > MaxCheckpointsPerAttempt ||
		!optionalID(r.CandidateID) || !optionalID(r.AcceptanceID) || !validAttemptState(r.State) || !validTime(r.StartedAt) ||
		(r.EndedAt == nil) != (r.State == "running") || r.EndedAt != nil && (!validTime(*r.EndedAt) || r.EndedAt.Before(r.StartedAt)) {
		return fail(CodeInvalid, "attempt_history")
	}
	if (r.CandidateID != "") != (r.State == "review" || r.State == "accepted" || r.State == "rejected") ||
		(r.AcceptanceID != "") != (r.State == "accepted" || r.State == "rejected") {
		return fail(CodeInvalid, "attempt_history")
	}
	if r.Reassignment != nil && (r.Reassignment.Validate() != nil || r.Ordinal < 2 ||
		r.Reassignment.BoardID != r.BoardID || r.Reassignment.CardID != r.CardID || r.Reassignment.SuccessorAttemptID != r.ID ||
		!r.Reassignment.CreatedAt.Equal(r.StartedAt)) {
		return fail(CodeInvalid, "attempt_history")
	}
	return nil
}

type AttemptHistoryPage struct {
	Version          int                    `json:"version"`
	BoardID          string                 `json:"board_id"`
	CardID           string                 `json:"card_id"`
	HighWaterOrdinal int                    `json:"high_water_ordinal"`
	Items            []AttemptHistoryRecord `json:"items"`
	NextCursor       string                 `json:"next_cursor,omitempty"`
	HasMore          bool                   `json:"has_more"`
}

func (p AttemptHistoryPage) Validate() error {
	if p.Version != SchemaVersion || !validLifecycleIDs(p.BoardID, p.CardID) || p.HighWaterOrdinal < 0 || p.HighWaterOrdinal > MaxAttemptsPerCard ||
		len(p.Items) > MaxLifecyclePageItems || p.HasMore != (p.NextCursor != "") || !validCursor(p.NextCursor) || p.HasMore && len(p.Items) == 0 ||
		p.HighWaterOrdinal == 0 != (len(p.Items) == 0) {
		return fail(CodeInvalid, "attempt_history_page")
	}
	previous := p.HighWaterOrdinal + 1
	for _, item := range p.Items {
		if item.Validate() != nil || item.BoardID != p.BoardID || item.CardID != p.CardID || item.Ordinal >= previous || item.Ordinal > p.HighWaterOrdinal {
			return fail(CodeInvalid, "attempt_history_page")
		}
		previous = item.Ordinal
	}
	if !p.HasMore && len(p.Items) > 0 && previous != 1 {
		return fail(CodeInvalid, "attempt_history_page")
	}
	return nil
}

type AttemptDetailOptions struct {
	After string
	Limit int
}

func (o AttemptDetailOptions) Validate() error {
	if !validCursor(o.After) || o.Limit < 1 || o.Limit > MaxLifecyclePageItems {
		return fail(CodeInvalid, "attempt_detail")
	}
	return nil
}

// AttemptDetailPage returns one canonical attempt plus a frozen high-water
// page of immutable checkpoints, newest first.
type AttemptDetailPage struct {
	Version                     int                `json:"version"`
	Attempt                     AttemptSnapshot    `json:"attempt"`
	CheckpointHighWaterRevision int64              `json:"checkpoint_high_water_revision"`
	Checkpoints                 []CheckpointRecord `json:"checkpoints"`
	NextCursor                  string             `json:"next_cursor,omitempty"`
	HasMore                     bool               `json:"has_more"`
}

func (p AttemptDetailPage) Validate() error {
	if p.Version != SchemaVersion || p.Attempt.Validate() != nil || p.CheckpointHighWaterRevision < 0 || p.CheckpointHighWaterRevision > MaxCheckpointsPerAttempt ||
		len(p.Checkpoints) > MaxLifecyclePageItems || p.HasMore != (p.NextCursor != "") || !validCursor(p.NextCursor) || p.HasMore && len(p.Checkpoints) == 0 ||
		p.CheckpointHighWaterRevision == 0 != (len(p.Checkpoints) == 0) {
		return fail(CodeInvalid, "attempt_detail_page")
	}
	previous := p.CheckpointHighWaterRevision + 1
	for _, checkpoint := range p.Checkpoints {
		if checkpoint.Validate() != nil || checkpoint.BoardID != p.Attempt.BoardID || checkpoint.CardID != p.Attempt.CardID || checkpoint.AttemptID != p.Attempt.ID ||
			checkpoint.Revision >= previous || checkpoint.Revision > p.CheckpointHighWaterRevision {
			return fail(CodeInvalid, "attempt_detail_page")
		}
		previous = checkpoint.Revision
	}
	if !p.HasMore && len(p.Checkpoints) > 0 && previous != 1 {
		return fail(CodeInvalid, "attempt_detail_page")
	}
	return nil
}

type LifecycleHistoryRepository interface {
	ListAttemptHistory(context.Context, string, string, AttemptHistoryOptions) (AttemptHistoryPage, error)
	ReadAttemptDetail(context.Context, string, string, string, AttemptDetailOptions) (AttemptDetailPage, error)
}

func validAttemptState(state string) bool {
	return state == "running" || state == "review" || state == "accepted" || state == "rejected" || state == "failed" || state == "canceled"
}
