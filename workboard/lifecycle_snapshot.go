package workboard

import (
	"context"
	"time"
)

const MaxSnapshotCheckpoints = 100

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
	Candidate        *CandidateRecord
	Evidence         []EvidenceRecord
	Acceptance       *AcceptanceRecord
	StartedAt        time.Time
	EndedAt          *time.Time
}

// CardLifecycleSnapshot is the bounded current-attempt view attached to a card
// page. Historical attempt/checkpoint pagination is intentionally a separate
// future read surface; this projection never loads more than the most recent
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
