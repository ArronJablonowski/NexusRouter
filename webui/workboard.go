package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"
)

// Workboard hard limits bound graph checks, atomic mutations, and public
// projections independently from any stricter configured policy.
const (
	MaxBoards                = 100
	MaxCardsPerBoard         = 10_000
	MaxBoardPageItems        = 100
	MaxCardPageItems         = 100
	MaxReverseFanout         = 64
	MaxGraphVisits           = 10_000
	MaxDependencyDepth       = 64
	MaxTransactionEvents     = 128
	MaxTransactionBytes      = 1 << 20
	MaxAttemptsPerCard       = 32
	MaxLinkedTasksPerAttempt = 128
	MaxEvidenceRecords       = 100
	MaxCandidateArtifacts    = 32
	MaxRankBytes             = 128
	MaxCriterionTextBytes    = 4 << 10
	MaxCriterionAggregate    = 64 << 10
	MaxActorBytes            = 128
	MaxReferenceBytes        = 128
	MaxClaimLease            = 10 * time.Minute
	MaxWorkDurationMillis    = int64(30 * 24 * time.Hour / time.Millisecond)
	MaxWorkTokens            = int64(1_000_000_000)
	MaxWorkCostMicros        = int64(1_000_000_000_000)
)

type WorkBudget struct {
	AttemptLimit int   `json:"attempt_limit"`
	TimeLimitMS  int64 `json:"time_limit_ms"`
	TokenLimit   int64 `json:"token_limit"`
	CostMicros   int64 `json:"cost_micros"`
}

func (b WorkBudget) Validate() error {
	if b.AttemptLimit < 1 || b.AttemptLimit > MaxAttemptsPerCard ||
		b.TimeLimitMS < 0 || b.TimeLimitMS > MaxWorkDurationMillis ||
		b.TokenLimit < 0 || b.TokenLimit > MaxWorkTokens ||
		b.CostMicros < 0 || b.CostMicros > MaxWorkCostMicros {
		return ErrContract
	}
	return nil
}

type Board struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	Revision       int64     `json:"revision"`
	LayoutRevision int64     `json:"layout_revision"`
	EventSequence  int64     `json:"event_sequence"`
	State          string    `json:"state"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	CardCount      int       `json:"card_count"`
	ActiveClaims   int       `json:"active_claims"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Column is one of the seven durable, canonical workboard lanes. Its identity
// is the card state; title and rank are server-owned presentation metadata.
type Column struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	BoardID string `json:"board_id"`
	State   string `json:"state"`
	Title   string `json:"title"`
	Rank    string `json:"rank"`
}

func (c Column) Validate() error {
	if c.Version != ContractVersion || !validID(c.BoardID) || !validBoardState(c.State) ||
		c.ID != c.State || requireText(c.Title, MaxTitleBytes) != nil ||
		!boundedPrintable(c.Rank, 1, MaxRankBytes) {
		return ErrContract
	}
	return encodedWithin(c, 4<<10)
}

func (b Board) Validate() error {
	if b.Version != ContractVersion || !validID(b.ID) || b.Revision < 1 ||
		b.LayoutRevision < 1 || b.EventSequence < 1 ||
		(b.State != "active" && b.State != "archived") ||
		requireText(b.Title, MaxTitleBytes) != nil ||
		!boundedText(b.Description, MaxDescriptionBytes, true) ||
		b.CardCount < 0 || b.CardCount > MaxCardsPerBoard ||
		b.ActiveClaims < 0 || b.ActiveClaims > b.CardCount ||
		b.State == "archived" && b.ActiveClaims != 0 ||
		!validWorkboardTime(b.CreatedAt) || !validWorkboardTime(b.UpdatedAt) ||
		b.UpdatedAt.Before(b.CreatedAt) {
		return ErrContract
	}
	return encodedWithin(b, MaxTransactionBytes)
}

type AcceptanceCriterion struct {
	Version        int    `json:"version"`
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	RequiredSource string `json:"required_source"`
	ValidatorID    string `json:"validator_id"`
	Description    string `json:"description"`
	Required       bool   `json:"required"`
}

func (c AcceptanceCriterion) Validate() error {
	if c.Version != ContractVersion || !validID(c.ID) || !validID(c.ValidatorID) ||
		requireText(c.Description, MaxCriterionTextBytes) != nil {
		return ErrContract
	}
	switch c.Kind {
	case "objective":
		if c.RequiredSource != "deterministic" {
			return ErrContract
		}
	case "subjective":
		if c.RequiredSource != "user_feedback" {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return nil
}

type Card struct {
	Version               int                   `json:"version"`
	ID                    string                `json:"id"`
	BoardID               string                `json:"board_id"`
	Revision              int64                 `json:"revision"`
	CriteriaRevision      int64                 `json:"criteria_revision"`
	State                 string                `json:"state"`
	Rank                  string                `json:"rank"`
	Title                 string                `json:"title"`
	Description           string                `json:"description"`
	Priority              string                `json:"priority"`
	Labels                []string              `json:"labels"`
	ParentID              string                `json:"parent_id,omitempty"`
	Dependencies          []string              `json:"dependencies"`
	RemainingDependencies int                   `json:"remaining_dependencies"`
	AssigneeID            string                `json:"assignee_id,omitempty"`
	AttemptCount          int                   `json:"attempt_count"`
	CurrentAttemptID      string                `json:"current_attempt_id,omitempty"`
	CurrentClaimID        string                `json:"current_claim_id,omitempty"`
	AcceptanceID          string                `json:"acceptance_id,omitempty"`
	BlockReason           string                `json:"block_reason,omitempty"`
	CancelRequested       bool                  `json:"cancel_requested"`
	PauseRequested        bool                  `json:"pause_requested"`
	Budget                WorkBudget            `json:"budget"`
	Criteria              []AcceptanceCriterion `json:"criteria"`
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
}

func (c Card) Validate() error {
	if c.Version != ContractVersion || !validID(c.ID) || !validID(c.BoardID) ||
		c.Revision < 1 || c.CriteriaRevision < 1 || !validBoardState(c.State) ||
		!boundedPrintable(c.Rank, 1, MaxRankBytes) || requireText(c.Title, MaxTitleBytes) != nil ||
		!boundedText(c.Description, MaxDescriptionBytes, true) || !validPriority(c.Priority) ||
		!validLabels(c.Labels) || !optionalID(c.ParentID) || !validIDs(c.Dependencies, MaxDependencies) ||
		c.ParentID == c.ID || containsID(c.Dependencies, c.ID) ||
		c.RemainingDependencies < 0 || c.RemainingDependencies > len(c.Dependencies) ||
		!optionalID(c.AssigneeID) || c.AttemptCount < 0 || c.AttemptCount > MaxAttemptsPerCard ||
		!optionalID(c.CurrentAttemptID) || !optionalID(c.CurrentClaimID) || !optionalID(c.AcceptanceID) ||
		!optionalID(c.BlockReason) || c.Budget.Validate() != nil ||
		validateCriteria(c.Criteria, c.CriteriaRevision) != nil ||
		!validWorkboardTime(c.CreatedAt) || !validWorkboardTime(c.UpdatedAt) || c.UpdatedAt.Before(c.CreatedAt) {
		return ErrContract
	}
	if c.RemainingDependencies > 0 && c.State == "ready" {
		return ErrContract
	}
	switch c.State {
	case "in_progress":
		if c.CurrentAttemptID == "" || c.CurrentClaimID == "" || c.AcceptanceID != "" {
			return ErrContract
		}
	case "blocked":
		if c.CurrentAttemptID == "" || c.CurrentClaimID == "" || c.AcceptanceID != "" || c.BlockReason == "" {
			return ErrContract
		}
	case "review":
		if c.CurrentAttemptID == "" || c.CurrentClaimID != "" || c.AcceptanceID != "" {
			return ErrContract
		}
	case "done":
		if c.CurrentAttemptID == "" || c.CurrentClaimID != "" || c.AcceptanceID == "" || c.RemainingDependencies != 0 {
			return ErrContract
		}
	default:
		if c.CurrentClaimID != "" || c.AcceptanceID != "" {
			return ErrContract
		}
	}
	if c.State != "blocked" && c.BlockReason != "" ||
		(c.CancelRequested || c.PauseRequested) && c.State != "in_progress" && c.State != "blocked" {
		return ErrContract
	}
	return encodedWithin(c, MaxTransactionBytes)
}

type Claim struct {
	Version       int        `json:"version"`
	ID            string     `json:"id"`
	BoardID       string     `json:"board_id"`
	CardID        string     `json:"card_id"`
	AttemptID     string     `json:"attempt_id"`
	Revision      int64      `json:"revision"`
	State         string     `json:"state"`
	OwnerID       string     `json:"owner_id"`
	OwnerType     string     `json:"owner_type"`
	TaskID        string     `json:"task_id,omitempty"`
	ExpiresAt     time.Time  `json:"expires_at"`
	LastHeartbeat time.Time  `json:"last_heartbeat"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
}

func (c Claim) Validate() error {
	if c.Version != ContractVersion || !validID(c.ID) || !validID(c.BoardID) ||
		!validID(c.CardID) || !validID(c.AttemptID) || c.Revision < 1 ||
		!validID(c.OwnerID) || c.OwnerType != "worker" || !optionalID(c.TaskID) ||
		!validWorkboardTime(c.ExpiresAt) || !validWorkboardTime(c.LastHeartbeat) ||
		c.ExpiresAt.Before(c.LastHeartbeat) {
		return ErrContract
	}
	switch c.State {
	case "active", "attention":
		if c.ReleasedAt != nil || !c.ExpiresAt.After(c.LastHeartbeat) || c.ExpiresAt.Sub(c.LastHeartbeat) > MaxClaimLease {
			return ErrContract
		}
	case "released":
		if c.ReleasedAt == nil || !validWorkboardTime(*c.ReleasedAt) || c.ReleasedAt.Before(c.LastHeartbeat) {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(c, 16<<10)
}

type EvidenceRecord struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	Revision        int64     `json:"revision"`
	BoardID         string    `json:"board_id"`
	CardID          string    `json:"card_id"`
	AttemptID       string    `json:"attempt_id"`
	CandidateID     string    `json:"candidate_id"`
	CriterionID     string    `json:"criterion_id"`
	Source          string    `json:"source"`
	Outcome         string    `json:"outcome"`
	ActorID         string    `json:"actor_id"`
	ActorType       string    `json:"actor_type"`
	Reference       string    `json:"reference"`
	CandidateDigest string    `json:"candidate_digest"`
	CriteriaDigest  string    `json:"criteria_digest"`
	PolicyDigest    string    `json:"policy_digest"`
	CreatedAt       time.Time `json:"created_at"`
}

func (e EvidenceRecord) Validate() error {
	if e.Version != ContractVersion || !validID(e.ID) || e.Revision < 1 || !validID(e.BoardID) ||
		!validID(e.CardID) || !validID(e.AttemptID) || !validID(e.CandidateID) ||
		!validID(e.CriterionID) || !validID(e.ActorID) || !validActorType(e.ActorType) ||
		!boundedPrintable(e.Reference, 1, MaxReferenceBytes) || !validWorkboardDigest(e.CandidateDigest) ||
		!validWorkboardDigest(e.CriteriaDigest) || !validWorkboardDigest(e.PolicyDigest) ||
		!validWorkboardTime(e.CreatedAt) {
		return ErrContract
	}
	switch e.Outcome {
	case "passed", "failed":
	case "abstained":
		if e.Source == "user_feedback" {
			return ErrContract
		}
	default:
		return ErrContract
	}
	switch e.Source {
	case "deterministic":
		if e.ActorType != "validator" {
			return ErrContract
		}
	case "user_feedback":
		if e.ActorType != "operator" {
			return ErrContract
		}
	case "model_audit":
		if e.ActorType != "model" {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(e, MaxEvidenceBytes)
}

type Candidate struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	BoardID        string    `json:"board_id"`
	CardID         string    `json:"card_id"`
	AttemptID      string    `json:"attempt_id"`
	Revision       int64     `json:"revision"`
	Digest         string    `json:"digest"`
	CriteriaDigest string    `json:"criteria_digest"`
	PolicyDigest   string    `json:"policy_digest"`
	EvidenceDigest string    `json:"evidence_digest"`
	EvidenceCount  int       `json:"evidence_count"`
	Summary        string    `json:"summary"`
	ArtifactRefs   []string  `json:"artifact_refs"`
	SubmittedBy    string    `json:"submitted_by"`
	CreatedAt      time.Time `json:"created_at"`
}

func (c Candidate) Validate() error {
	if c.Version != ContractVersion || !validID(c.ID) || !validID(c.BoardID) ||
		!validID(c.CardID) || !validID(c.AttemptID) || c.Revision < 1 ||
		!validWorkboardDigest(c.Digest) || !validWorkboardDigest(c.CriteriaDigest) ||
		!validWorkboardDigest(c.PolicyDigest) || !validWorkboardDigest(c.EvidenceDigest) ||
		c.EvidenceCount < 0 || c.EvidenceCount > MaxEvidenceRecords ||
		requireText(c.Summary, MaxDescriptionBytes) != nil ||
		!validIDs(c.ArtifactRefs, MaxCandidateArtifacts) || !validID(c.SubmittedBy) ||
		!validWorkboardTime(c.CreatedAt) {
		return ErrContract
	}
	return encodedWithin(c, MaxTransactionBytes)
}

type Attempt struct {
	Version                  int                   `json:"version"`
	ID                       string                `json:"id"`
	BoardID                  string                `json:"board_id"`
	CardID                   string                `json:"card_id"`
	Ordinal                  int                   `json:"ordinal"`
	Revision                 int64                 `json:"revision"`
	State                    string                `json:"state"`
	WorkerID                 string                `json:"worker_id"`
	CriteriaRevision         int64                 `json:"criteria_revision"`
	CriteriaDigest           string                `json:"criteria_digest"`
	PolicyDigest             string                `json:"policy_digest"`
	Budget                   WorkBudget            `json:"budget"`
	Criteria                 []AcceptanceCriterion `json:"criteria"`
	TaskIDs                  []string              `json:"task_ids"`
	SessionIDs               []string              `json:"session_ids"`
	Claim                    *Claim                `json:"claim,omitempty"`
	Candidate                *Candidate            `json:"candidate,omitempty"`
	Evidence                 []EvidenceRecord      `json:"evidence"`
	AcceptanceID             string                `json:"acceptance_id,omitempty"`
	DecisionBy               string                `json:"decision_by,omitempty"`
	DecisionByType           string                `json:"decision_by_type,omitempty"`
	DecisionAuthorityID      string                `json:"decision_authority_id,omitempty"`
	AcceptanceEvidenceDigest string                `json:"acceptance_evidence_digest,omitempty"`
	StartedAt                time.Time             `json:"started_at"`
	EndedAt                  *time.Time            `json:"ended_at,omitempty"`
}

func (a Attempt) Validate() error {
	if a.Version != ContractVersion || !validID(a.ID) || !validID(a.BoardID) ||
		!validID(a.CardID) || a.Ordinal < 1 || a.Ordinal > MaxAttemptsPerCard ||
		a.Revision < 1 || !validID(a.WorkerID) || a.CriteriaRevision < 1 ||
		!validWorkboardDigest(a.CriteriaDigest) || !validWorkboardDigest(a.PolicyDigest) ||
		a.Budget.Validate() != nil || validateCriteria(a.Criteria, a.CriteriaRevision) != nil ||
		AcceptanceCriteriaDigest(a.Criteria) != a.CriteriaDigest ||
		!validIDs(a.TaskIDs, MaxLinkedTasksPerAttempt) || !validIDs(a.SessionIDs, MaxLinkedTasksPerAttempt) ||
		len(a.Evidence) > MaxEvidenceRecords || !optionalID(a.AcceptanceID) ||
		!optionalID(a.DecisionBy) || !optionalID(a.DecisionAuthorityID) ||
		(a.DecisionByType != "" && a.DecisionByType != "operator" && a.DecisionByType != "validator") ||
		(a.AcceptanceEvidenceDigest != "" && !validWorkboardDigest(a.AcceptanceEvidenceDigest)) ||
		!validWorkboardTime(a.StartedAt) {
		return ErrContract
	}
	if (a.EndedAt == nil) != (a.State == "running") {
		return ErrContract
	}
	if a.EndedAt != nil && (!validWorkboardTime(*a.EndedAt) || a.EndedAt.Before(a.StartedAt)) {
		return ErrContract
	}
	seen := map[string]bool{}
	criteria := map[string]AcceptanceCriterion{}
	for _, criterion := range a.Criteria {
		criteria[criterion.ID] = criterion
	}
	for index, evidence := range a.Evidence {
		if evidence.Validate() != nil || seen[evidence.ID] || evidence.BoardID != a.BoardID ||
			evidence.CardID != a.CardID || evidence.AttemptID != a.ID ||
			evidence.CriteriaDigest != a.CriteriaDigest || evidence.PolicyDigest != a.PolicyDigest ||
			criteria[evidence.CriterionID].ID == "" || index > 0 && (evidence.Revision <= a.Evidence[index-1].Revision || evidence.CreatedAt.Before(a.Evidence[index-1].CreatedAt)) {
			return ErrContract
		}
		seen[evidence.ID] = true
	}
	if a.Claim != nil && (a.Claim.Validate() != nil || a.Claim.BoardID != a.BoardID ||
		a.Claim.CardID != a.CardID || a.Claim.AttemptID != a.ID || a.Claim.OwnerID != a.WorkerID || a.Claim.OwnerType != "worker") {
		return ErrContract
	}
	if a.Candidate != nil {
		if a.Candidate.Validate() != nil || a.Candidate.BoardID != a.BoardID ||
			a.Candidate.CardID != a.CardID || a.Candidate.AttemptID != a.ID ||
			a.Candidate.SubmittedBy != a.WorkerID ||
			a.Candidate.CriteriaDigest != a.CriteriaDigest || a.Candidate.PolicyDigest != a.PolicyDigest ||
			a.Candidate.EvidenceCount > len(a.Evidence) ||
			a.Candidate.EvidenceDigest != EvidenceDigest(a.Evidence[:a.Candidate.EvidenceCount]) {
			return ErrContract
		}
		for _, evidence := range a.Evidence {
			if evidence.CandidateID != a.Candidate.ID || evidence.CandidateDigest != a.Candidate.Digest {
				return ErrContract
			}
		}
	}
	switch a.State {
	case "running":
		if a.Claim == nil || a.Claim.State == "released" || a.Candidate != nil || a.AcceptanceID != "" || a.DecisionBy != "" || a.DecisionByType != "" || a.DecisionAuthorityID != "" || a.AcceptanceEvidenceDigest != "" {
			return ErrContract
		}
	case "review":
		if a.Candidate == nil || a.Claim == nil || a.Claim.State != "released" || a.AcceptanceID != "" || a.DecisionBy != "" || a.DecisionByType != "" || a.DecisionAuthorityID != "" || a.AcceptanceEvidenceDigest != "" {
			return ErrContract
		}
	case "accepted":
		if a.Candidate == nil || a.Claim == nil || a.Claim.State != "released" || a.AcceptanceID == "" || a.DecisionBy == "" || a.DecisionBy == a.WorkerID ||
			a.DecisionBy == a.Claim.OwnerID || (a.DecisionByType != "operator" && a.DecisionByType != "validator") || a.DecisionAuthorityID == "" ||
			a.AcceptanceEvidenceDigest != EvidenceDigest(a.Evidence) || !acceptanceSatisfied(a.Criteria, a.Evidence) {
			return ErrContract
		}
	case "rejected":
		if a.Candidate == nil || a.Claim == nil || a.Claim.State != "released" || a.AcceptanceID == "" || a.DecisionBy == "" ||
			a.DecisionBy == a.WorkerID || a.DecisionBy == a.Claim.OwnerID || (a.DecisionByType != "operator" && a.DecisionByType != "validator") || a.DecisionAuthorityID == "" ||
			a.AcceptanceEvidenceDigest != EvidenceDigest(a.Evidence) {
			return ErrContract
		}
	case "failed", "canceled":
		if a.Claim != nil && a.Claim.State != "released" || a.AcceptanceID != "" || a.DecisionBy != "" || a.DecisionByType != "" || a.DecisionAuthorityID != "" || a.AcceptanceEvidenceDigest != "" {
			return ErrContract
		}
	default:
		return ErrContract
	}
	return encodedWithin(a, MaxTransactionBytes)
}

type BoardSnapshot struct {
	Version       int      `json:"version"`
	Board         Board    `json:"board"`
	Columns       []Column `json:"columns"`
	Cards         []Card   `json:"cards"`
	NextCursor    string   `json:"next_cursor,omitempty"`
	HasMore       bool     `json:"has_more"`
	GraphRevision int64    `json:"graph_revision"`
	GraphDigest   string   `json:"graph_digest"`
}

func (s BoardSnapshot) Validate() error {
	if s.Version != ContractVersion || s.Board.Validate() != nil || len(s.Cards) > MaxCardPageItems ||
		s.HasMore != (s.NextCursor != "") || s.GraphRevision < 1 || !validWorkboardDigest(s.GraphDigest) ||
		(s.NextCursor != "" && !boundedPrintable(s.NextCursor, 1, MaxCursorBytes)) ||
		validateColumns(s.Board.ID, s.Columns) != nil {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, card := range s.Cards {
		if card.Validate() != nil || card.BoardID != s.Board.ID || seen[card.ID] {
			return ErrContract
		}
		seen[card.ID] = true
	}
	if len(s.Cards) == 0 && s.HasMore {
		return ErrContract
	}
	if validateSnapshotGraph(s.Cards) != nil {
		return ErrContract
	}
	return encodedWithin(s, MaxTransactionBytes)
}

type Page struct {
	Version    int     `json:"version"`
	Items      []Board `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
}

func (p Page) Validate() error {
	if p.Version != ContractVersion || len(p.Items) > MaxBoardPageItems ||
		p.HasMore != (p.NextCursor != "") ||
		(p.NextCursor != "" && !boundedPrintable(p.NextCursor, 1, MaxCursorBytes)) ||
		len(p.Items) == 0 && p.HasMore {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, board := range p.Items {
		if board.Validate() != nil || seen[board.ID] {
			return ErrContract
		}
		seen[board.ID] = true
	}
	return encodedWithin(p, MaxTransactionBytes)
}

type OperationReceipt struct {
	Version          int       `json:"version"`
	BoardID          string    `json:"board_id"`
	OperationID      string    `json:"operation_id"`
	RequestDigest    string    `json:"request_digest"`
	ResponseDigest   string    `json:"response_digest"`
	FirstSequence    int64     `json:"first_sequence"`
	LastSequence     int64     `json:"last_sequence"`
	EventCount       int       `json:"event_count"`
	TransactionBytes int       `json:"transaction_bytes"`
	BoardRevision    int64     `json:"board_revision"`
	CardID           string    `json:"card_id,omitempty"`
	CardRevision     *int64    `json:"card_revision,omitempty"`
	ClaimRevision    *int64    `json:"claim_revision,omitempty"`
	Outcome          string    `json:"outcome"`
	CreatedAt        time.Time `json:"created_at"`
}

type AcceptanceDecisionRecord struct {
	Version              int       `json:"version"`
	ID                   string    `json:"id"`
	BoardID              string    `json:"board_id"`
	CardID               string    `json:"card_id"`
	AttemptID            string    `json:"attempt_id"`
	CandidateID          string    `json:"candidate_id"`
	CandidateDigest      string    `json:"candidate_digest"`
	CriteriaRevision     int64     `json:"criteria_revision"`
	CriteriaDigest       string    `json:"criteria_digest"`
	EvidenceHeadRevision int64     `json:"evidence_head_revision"`
	EvidenceSetDigest    string    `json:"evidence_set_digest"`
	PolicyDigest         string    `json:"policy_digest"`
	Decision             string    `json:"decision"`
	DecidedBy            string    `json:"decided_by"`
	DecidedByType        string    `json:"decided_by_type"`
	DecisionAuthorityID  string    `json:"decision_authority_id"`
	DecidedAt            time.Time `json:"decided_at"`
}

func (r AcceptanceDecisionRecord) ValidateAgainst(attempt Attempt) error {
	if r.Version != ContractVersion || !validID(r.ID) || !validID(r.BoardID) || !validID(r.CardID) ||
		!validID(r.AttemptID) || !validID(r.CandidateID) || !validWorkboardDigest(r.CandidateDigest) ||
		r.CriteriaRevision < 1 || !validWorkboardDigest(r.CriteriaDigest) || r.EvidenceHeadRevision < 1 ||
		!validWorkboardDigest(r.EvidenceSetDigest) || !validWorkboardDigest(r.PolicyDigest) ||
		(r.Decision != "accepted" && r.Decision != "rejected") || !validID(r.DecidedBy) ||
		(r.DecidedByType != "operator" && r.DecidedByType != "validator") || !validID(r.DecisionAuthorityID) ||
		!validWorkboardTime(r.DecidedAt) || attempt.Validate() != nil || attempt.Candidate == nil || len(attempt.Evidence) < 1 ||
		r.BoardID != attempt.BoardID || r.CardID != attempt.CardID || r.AttemptID != attempt.ID ||
		r.CandidateID != attempt.Candidate.ID || r.CandidateDigest != attempt.Candidate.Digest ||
		r.ID != attempt.AcceptanceID || r.DecidedBy != attempt.DecisionBy || r.DecidedByType != attempt.DecisionByType ||
		r.DecisionAuthorityID != attempt.DecisionAuthorityID || attempt.EndedAt == nil || !r.DecidedAt.Equal(*attempt.EndedAt) ||
		(r.Decision == "accepted") != (attempt.State == "accepted") ||
		r.CriteriaRevision != attempt.CriteriaRevision || r.CriteriaDigest != attempt.CriteriaDigest ||
		r.EvidenceHeadRevision != attempt.Evidence[len(attempt.Evidence)-1].Revision || r.EvidenceSetDigest != EvidenceDigest(attempt.Evidence) ||
		r.PolicyDigest != attempt.PolicyDigest || r.DecidedBy == attempt.WorkerID {
		return ErrContract
	}
	return encodedWithin(r, 16<<10)
}

type RecoveryReceipt struct {
	Version              int       `json:"version"`
	ID                   string    `json:"id"`
	BoardID              string    `json:"board_id"`
	CardID               string    `json:"card_id"`
	AttemptID            string    `json:"attempt_id"`
	OldClaimID           string    `json:"old_claim_id"`
	OldClaimRevision     int64     `json:"old_claim_revision"`
	CardRevision         int64     `json:"card_revision"`
	StopProofID          string    `json:"stop_proof_id"`
	TaskHeadDigest       string    `json:"task_head_digest"`
	ProcessProofDigest   string    `json:"process_proof_digest"`
	EffectEvidenceDigest string    `json:"effect_evidence_digest"`
	EffectResolution     string    `json:"effect_resolution"`
	ResultingState       string    `json:"resulting_state"`
	FirstSequence        int64     `json:"first_sequence"`
	LastSequence         int64     `json:"last_sequence"`
	RecoveredAt          time.Time `json:"recovered_at"`
}

func (r RecoveryReceipt) Validate() error {
	if r.Version != ContractVersion || !validID(r.ID) || !validID(r.BoardID) || !validID(r.CardID) ||
		!validID(r.AttemptID) || !validID(r.OldClaimID) || r.OldClaimRevision < 1 || r.CardRevision < 1 || !validID(r.StopProofID) ||
		!validWorkboardDigest(r.TaskHeadDigest) || !validWorkboardDigest(r.ProcessProofDigest) || !validWorkboardDigest(r.EffectEvidenceDigest) ||
		(r.EffectResolution != "effect_free" && r.EffectResolution != "resolved_no_replay") ||
		r.ResultingState != "ready" || r.FirstSequence < 1 || r.LastSequence < r.FirstSequence || r.LastSequence-r.FirstSequence+1 > MaxTransactionEvents ||
		!validWorkboardTime(r.RecoveredAt) {
		return ErrContract
	}
	return encodedWithin(r, 16<<10)
}

func (r OperationReceipt) Validate() error {
	if r.Version != ContractVersion || !validID(r.BoardID) || !validKey(r.OperationID) ||
		!validWorkboardDigest(r.RequestDigest) || !validWorkboardDigest(r.ResponseDigest) ||
		r.FirstSequence < 1 || r.LastSequence < r.FirstSequence ||
		r.EventCount < 1 || r.EventCount > MaxTransactionEvents ||
		int64(r.EventCount) != r.LastSequence-r.FirstSequence+1 ||
		r.TransactionBytes < 1 || r.TransactionBytes > MaxTransactionBytes ||
		r.BoardRevision < 1 || !optionalID(r.CardID) || !positiveOptionalRevision(r.CardRevision) ||
		!positiveOptionalRevision(r.ClaimRevision) || r.Outcome != "committed" ||
		!validWorkboardTime(r.CreatedAt) {
		return ErrContract
	}
	if r.CardID == "" && (r.CardRevision != nil || r.ClaimRevision != nil) ||
		r.ClaimRevision != nil && r.CardRevision == nil {
		return ErrContract
	}
	return encodedWithin(r, 16<<10)
}

// ValidateReceiptJSON applies normative receipt checks that cannot be
// expressed by standard JSON Schema, including sequence arithmetic. Browser
// clients implement the matching x-invariants and run the shared invalid
// fixtures before accepting receipts.
func ValidateReceiptJSON(definition string, body []byte) error {
	if len(body) == 0 || len(body) > 16<<10 {
		return ErrContract
	}
	decode := func(target any) error {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return ErrContract
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return ErrContract
		}
		return nil
	}
	switch definition {
	case "operation_receipt":
		var receipt OperationReceipt
		if decode(&receipt) != nil {
			return ErrContract
		}
		return receipt.Validate()
	case "recovery_receipt":
		var receipt RecoveryReceipt
		if decode(&receipt) != nil {
			return ErrContract
		}
		return receipt.Validate()
	default:
		return ErrContract
	}
}

func AcceptanceCriteriaDigest(criteria []AcceptanceCriterion) string {
	body, err := json.Marshal(criteria)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func EvidenceDigest(evidence []EvidenceRecord) string {
	body, err := json.Marshal(evidence)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validateCriteria(criteria []AcceptanceCriterion, revision int64) error {
	if revision < 1 || len(criteria) < 1 || len(criteria) > MaxAcceptanceCriteria {
		return ErrContract
	}
	seen := map[string]bool{}
	total := 0
	for _, criterion := range criteria {
		if criterion.Validate() != nil || seen[criterion.ID] {
			return ErrContract
		}
		seen[criterion.ID] = true
		body, err := json.Marshal(criterion)
		if err != nil {
			return ErrContract
		}
		total += len(body)
		if total > MaxCriterionAggregate {
			return ErrContract
		}
	}
	return nil
}

func acceptanceSatisfied(criteria []AcceptanceCriterion, evidence []EvidenceRecord) bool {
	for _, criterion := range criteria {
		if !criterion.Required {
			continue
		}
		matched := false
		for _, record := range evidence {
			if record.CriterionID != criterion.ID || record.Source != criterion.RequiredSource {
				continue
			}
			if criterion.RequiredSource == "deterministic" && record.ActorID != criterion.ValidatorID {
				continue
			}
			if criterion.RequiredSource == "user_feedback" && record.ActorType != "operator" {
				continue
			}
			matched = true
			if record.Outcome != "passed" {
				return false
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func validWorkboardDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == hex.EncodeToString(decoded)
}

func validWorkboardTime(value time.Time) bool {
	_, offset := value.Zone()
	return value.Year() >= 1970 && value.Year() < 2261 && offset == 0
}

func validPriority(value string) bool {
	return value == "urgent" || value == "high" || value == "normal" || value == "low"
}

func validActorType(value string) bool {
	return value == "operator" || value == "worker" || value == "validator" || value == "model" || value == "system"
}

func containsID(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func positiveOptionalRevision(value *int64) bool {
	return value == nil || *value >= 1
}
