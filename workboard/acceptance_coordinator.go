package workboard

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

const MaxAcceptanceCandidatePageItems = 100

// AcceptanceDecisionSnapshot is the durable, criterion-bound view used by an
// automatic validator authority. Implementations must read the card and its
// current attempt from one storage snapshot.
type AcceptanceDecisionSnapshot struct {
	CardRevision int64
	Attempt      AttemptSnapshot
}

func (s AcceptanceDecisionSnapshot) Validate() error {
	if s.CardRevision < 1 || s.Attempt.Validate() != nil {
		return fail(CodeInvalid, "acceptance_snapshot")
	}
	return nil
}

type AcceptanceDecisionRepository interface {
	ReadAcceptanceDecision(context.Context, string, string) (AcceptanceDecisionSnapshot, error)
}

// AcceptanceCandidatePage is a bounded, forward-only view of cards currently
// awaiting acceptance. The cursor is a card identifier, not ownership state;
// every decision still re-reads and CAS-fences durable candidate truth.
type AcceptanceCandidatePage struct {
	CardIDs    []string
	NextCursor string
	HasMore    bool
}

func (p AcceptanceCandidatePage) Validate() error {
	if len(p.CardIDs) > MaxAcceptanceCandidatePageItems || p.HasMore && len(p.CardIDs) == 0 ||
		(p.NextCursor != "" && !validID(p.NextCursor)) {
		return fail(CodeInvalid, "acceptance_candidate_page")
	}
	previous := ""
	for _, id := range p.CardIDs {
		if !validID(id) || previous != "" && id <= previous {
			return fail(CodeInvalid, "acceptance_candidate_page")
		}
		previous = id
	}
	if len(p.CardIDs) == 0 && p.NextCursor != "" || len(p.CardIDs) > 0 && p.NextCursor != p.CardIDs[len(p.CardIDs)-1] {
		return fail(CodeInvalid, "acceptance_candidate_page")
	}
	return nil
}

type AcceptanceCandidateRepository interface {
	ListAcceptanceCandidates(context.Context, string, string, int) (AcceptanceCandidatePage, error)
}

type CandidateDecisionWriter interface {
	AcceptCandidate(context.Context, DecideCandidateRequest) (OperationReceipt, error)
	RejectCandidate(context.Context, DecideCandidateRequest) (OperationReceipt, error)
}

type CoordinatedDecision string

const (
	DecisionPending  CoordinatedDecision = "pending"
	DecisionAccepted CoordinatedDecision = "accepted"
	DecisionRejected CoordinatedDecision = "rejected"
	DecisionRaced    CoordinatedDecision = "decided_concurrently"
)

type AcceptanceCoordinationResult struct {
	Decision CoordinatedDecision
	Receipt  *OperationReceipt
}

type AcceptanceReconciliationResult struct {
	Scanned, Pending, Accepted, Rejected, Raced int
}

// AcceptanceCoordinator permits a validator authority to decide only from
// criterion-matched deterministic evidence. Advisory model audits are never
// consulted. Required subjective criteria deliberately remain in review for
// an authenticated operator.
type AcceptanceCoordinator struct {
	repository AcceptanceDecisionRepository
	candidates AcceptanceCandidateRepository
	decisions  CandidateDecisionWriter
	cursorMu   sync.Mutex
	cursors    map[string]string
}

func NewAcceptanceCoordinator(repository AcceptanceDecisionRepository, decisions CandidateDecisionWriter) (*AcceptanceCoordinator, error) {
	if repository == nil || decisions == nil {
		return nil, fail(CodeInvalid, "acceptance_coordinator")
	}
	candidates, _ := repository.(AcceptanceCandidateRepository)
	return &AcceptanceCoordinator{repository: repository, candidates: candidates, decisions: decisions, cursors: map[string]string{}}, nil
}

// ReconcileBoard re-drives only the acceptance decision over already durable
// Review candidates. It never evaluates a candidate or dispatches a worker or
// reviewer. A bounded rotating cursor prevents subjective pending candidates
// from starving later objective candidates; the cursor is an optimization and
// is not required for correctness after restart.
func (c *AcceptanceCoordinator) ReconcileBoard(ctx context.Context, boardID string, limit int) (AcceptanceReconciliationResult, error) {
	result := AcceptanceReconciliationResult{}
	if c == nil || ctx == nil || c.candidates == nil || !validID(boardID) || limit < 1 || limit > MaxCardsPerBoard {
		return result, fail(CodeInvalid, "acceptance_reconciliation")
	}
	c.cursorMu.Lock()
	after := c.cursors[boardID]
	c.cursorMu.Unlock()
	initial, wrapped := after, false
	seen := make(map[string]struct{}, limit)
	for result.Scanned < limit {
		page, err := c.candidates.ListAcceptanceCandidates(ctx, boardID, after, min(limit-result.Scanned, MaxAcceptanceCandidatePageItems))
		if err != nil {
			return result, err
		}
		if page.Validate() != nil {
			return result, fail(CodeInvalid, "acceptance_candidate_page")
		}
		if len(page.CardIDs) == 0 {
			if after != "" && !wrapped {
				after, wrapped = "", true
				continue
			}
			break
		}
		for _, cardID := range page.CardIDs {
			if _, duplicate := seen[cardID]; duplicate {
				c.rememberCursor(boardID, after)
				return result, nil
			}
			seen[cardID] = struct{}{}
			decision, err := c.Coordinate(ctx, boardID, cardID)
			if err != nil {
				return result, err
			}
			result.Scanned++
			switch decision.Decision {
			case DecisionPending:
				result.Pending++
			case DecisionAccepted:
				result.Accepted++
			case DecisionRejected:
				result.Rejected++
			case DecisionRaced:
				result.Raced++
			default:
				return result, fail(CodeInvalid, "acceptance_coordination")
			}
			after = cardID
			if result.Scanned == limit {
				break
			}
		}
		if result.Scanned == limit {
			break
		}
		if page.HasMore {
			after = page.NextCursor
			continue
		}
		if !wrapped && initial != "" {
			after, wrapped = "", true
			continue
		}
		break
	}
	c.rememberCursor(boardID, after)
	return result, nil
}

func (c *AcceptanceCoordinator) rememberCursor(boardID, cursor string) {
	c.cursorMu.Lock()
	defer c.cursorMu.Unlock()
	if _, exists := c.cursors[boardID]; !exists && len(c.cursors) >= MaxBoards {
		// Cursor state is a bounded fairness hint, never durable authority. Evict
		// one old hint so boards created after archival still receive rotation.
		for old := range c.cursors {
			delete(c.cursors, old)
			break
		}
	}
	c.cursors[boardID] = cursor
}

func (c *AcceptanceCoordinator) Coordinate(ctx context.Context, boardID, cardID string) (AcceptanceCoordinationResult, error) {
	if c == nil || ctx == nil || !validLifecycleIDs(boardID, cardID) {
		return AcceptanceCoordinationResult{}, fail(CodeInvalid, "acceptance_coordination")
	}
	snapshot, err := c.repository.ReadAcceptanceDecision(ctx, boardID, cardID)
	if err != nil {
		return AcceptanceCoordinationResult{}, err
	}
	if snapshot.Validate() != nil {
		return AcceptanceCoordinationResult{}, fail(CodeInvalid, "acceptance_snapshot")
	}
	if (snapshot.Attempt.State == "accepted" || snapshot.Attempt.State == "rejected") && snapshot.Attempt.Acceptance != nil {
		return AcceptanceCoordinationResult{Decision: DecisionRaced}, nil
	}
	if snapshot.Attempt.State != "review" || snapshot.Attempt.Candidate == nil || snapshot.Attempt.Acceptance != nil {
		return AcceptanceCoordinationResult{}, fail(CodeInvalid, "acceptance_snapshot")
	}
	kind, refs := coordinateDeterministicEvidence(snapshot.Attempt.Criteria, snapshot.Attempt.Evidence)
	if kind == DecisionPending {
		return AcceptanceCoordinationResult{Decision: kind}, nil
	}
	candidate := snapshot.Attempt.Candidate
	request := DecideCandidateRequest{
		BoardID:              boardID,
		CardID:               cardID,
		AttemptID:            snapshot.Attempt.ID,
		CandidateID:          candidate.ID,
		IdempotencyKey:       "criterion-decision-" + candidate.ID + "-" + string(kind),
		ExpectedCardRevision: snapshot.CardRevision,
		CriteriaRevision:     snapshot.Attempt.CriteriaRevision,
		EvidenceHeadRevision: int64(len(snapshot.Attempt.Evidence)),
		CandidateDigest:      candidate.Digest,
		CriteriaDigest:       snapshot.Attempt.CriteriaDigest,
		EvidenceSetDigest:    EvidenceSetDigest(snapshot.Attempt.Evidence),
		PolicyDigest:         snapshot.Attempt.PolicyDigest,
		Evidence:             coordinatedRationale(kind, refs),
	}
	var receipt OperationReceipt
	if kind == DecisionAccepted {
		receipt, err = c.decisions.AcceptCandidate(ctx, request)
	} else {
		receipt, err = c.decisions.RejectCandidate(ctx, request)
	}
	if err != nil {
		// An authenticated operator or another scheduler may win the same CAS.
		// Re-read durable truth and treat a completed independent decision as a
		// successful race, without rewriting its actor, reason, or evidence.
		current, readErr := c.repository.ReadAcceptanceDecision(ctx, boardID, cardID)
		if readErr == nil && current.Attempt.Validate() == nil &&
			(current.Attempt.State == "accepted" || current.Attempt.State == "rejected") && current.Attempt.Acceptance != nil {
			return AcceptanceCoordinationResult{Decision: DecisionRaced}, nil
		}
		return AcceptanceCoordinationResult{}, errors.Join(err, readErr)
	}
	return AcceptanceCoordinationResult{Decision: kind, Receipt: &receipt}, nil
}

func coordinateDeterministicEvidence(criteria []AcceptanceCriterion, evidence []EvidenceRecord) (CoordinatedDecision, []string) {
	refs := []string{}
	allObjectivePassed := true
	hasRequiredSubjective := false
	for _, criterion := range criteria {
		if !criterion.Required {
			continue
		}
		if criterion.Kind == "subjective" {
			hasRequiredSubjective = true
			continue
		}
		passed := false
		for _, item := range evidence {
			if item.CriterionID != criterion.ID || item.Source != "deterministic" || item.ActorType != "validator" || item.ActorID != criterion.ValidatorID {
				continue
			}
			if item.Outcome == "failed" {
				return DecisionRejected, []string{item.Reference}
			}
			if item.Outcome == "passed" {
				passed = true
				refs = append(refs, item.Reference)
			}
		}
		allObjectivePassed = allObjectivePassed && passed
	}
	if allObjectivePassed && !hasRequiredSubjective {
		sort.Strings(refs)
		return DecisionAccepted, refs
	}
	return DecisionPending, nil
}

func coordinatedRationale(decision CoordinatedDecision, refs []string) string {
	return "criterion coordinator " + string(decision) + "; deterministic evidence references: " + strings.Join(refs, ",")
}
