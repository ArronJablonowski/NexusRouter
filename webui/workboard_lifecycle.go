package webui

import "time"

const MaxSnapshotCheckpoints = 100

type WorkCheckpoint struct {
	Version          int       `json:"version"`
	ID               string    `json:"id"`
	BoardID          string    `json:"board_id"`
	CardID           string    `json:"card_id"`
	AttemptID        string    `json:"attempt_id"`
	ClaimID          string    `json:"claim_id"`
	Revision         int64     `json:"revision"`
	ClaimRevision    int64     `json:"claim_revision"`
	CriteriaRevision int64     `json:"criteria_revision"`
	CriteriaDigest   string    `json:"criteria_digest"`
	PolicyDigest     string    `json:"policy_digest"`
	Evidence         string    `json:"evidence"`
	EvidenceDigest   string    `json:"evidence_digest"`
	ActorID          string    `json:"actor_id"`
	ActorType        string    `json:"actor_type"`
	CreatedAt        time.Time `json:"created_at"`
}

func (c WorkCheckpoint) Validate() error {
	if c.Version != ContractVersion || !validID(c.ID) || !validID(c.BoardID) || !validID(c.CardID) || !validID(c.AttemptID) ||
		!validID(c.ClaimID) || c.Revision < 1 || c.ClaimRevision < 1 || c.CriteriaRevision < 1 ||
		!validWorkboardDigest(c.CriteriaDigest) || !validWorkboardDigest(c.PolicyDigest) ||
		requireText(c.Evidence, MaxEvidenceBytes) != nil || !validWorkboardDigest(c.EvidenceDigest) ||
		!validID(c.ActorID) || c.ActorType != "worker" || !validWorkboardTime(c.CreatedAt) {
		return ErrContract
	}
	return encodedWithin(c, MaxTransactionBytes)
}

// CardLifecycle is the bounded latest-attempt projection for one card in a
// board page. CheckpointCount makes truncation explicit; no missing historical
// record is presented as absent.
type CardLifecycle struct {
	Version            int                       `json:"version"`
	CardID             string                    `json:"card_id"`
	Attempt            Attempt                   `json:"attempt"`
	Checkpoints        []WorkCheckpoint          `json:"checkpoints"`
	CheckpointCount    int                       `json:"checkpoint_count"`
	CheckpointsHasMore bool                      `json:"checkpoints_has_more"`
	Acceptance         *AcceptanceDecisionRecord `json:"acceptance,omitempty"`
}

func (l CardLifecycle) Validate() error {
	if l.Version != ContractVersion || !validID(l.CardID) || l.Attempt.Validate() != nil || l.Attempt.CardID != l.CardID ||
		l.Checkpoints == nil || len(l.Checkpoints) > MaxSnapshotCheckpoints || l.CheckpointCount < len(l.Checkpoints) ||
		l.CheckpointsHasMore != (l.CheckpointCount > len(l.Checkpoints)) || (l.Acceptance != nil) != (l.Attempt.State == "accepted" || l.Attempt.State == "rejected") {
		return ErrContract
	}
	for index, checkpoint := range l.Checkpoints {
		if checkpoint.Validate() != nil || checkpoint.BoardID != l.Attempt.BoardID || checkpoint.CardID != l.CardID || checkpoint.AttemptID != l.Attempt.ID ||
			(index > 0 && checkpoint.Revision <= l.Checkpoints[index-1].Revision) {
			return ErrContract
		}
	}
	if l.Acceptance != nil && l.Acceptance.ValidateAgainst(l.Attempt) != nil {
		return ErrContract
	}
	return encodedWithin(l, MaxTransactionBytes)
}
