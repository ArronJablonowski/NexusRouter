package webui

import "time"

func allowedBoardFields(action BoardAction) []string {
	switch action {
	case BoardCreate:
		return []string{"title", "description"}
	case BoardRevise:
		return []string{"board_id", "title", "description", "expected_board_revision"}
	case BoardArchive:
		return []string{"board_id", "expected_board_revision"}
	case CardCreate:
		return []string{"board_id", "title", "description", "priority", "labels", "dependencies", "criteria", "parent_id", "assignee_id", "budget", "expected_board_revision", "expected_graph_revision"}
	case CardRevise:
		return []string{"board_id", "card_id", "title", "description", "priority", "labels", "parent_id", "assignee_id", "clear_parent", "clear_assignee", "budget", "expected_card_revision", "expected_graph_revision"}
	case CardMove:
		return []string{"board_id", "card_id", "target_state", "before_card_id", "after_card_id", "expected_board_revision", "expected_layout_revision", "expected_card_revision"}
	case CardReorder:
		return []string{"board_id", "card_id", "before_card_id", "after_card_id", "expected_board_revision", "expected_layout_revision", "expected_card_revision"}
	case DependencyAdd, DependencyRemove:
		return []string{"board_id", "card_id", "dependency_id", "expected_card_revision", "expected_graph_revision"}
	case CriteriaRevise:
		return []string{"board_id", "card_id", "criteria", "expected_card_revision", "expected_criteria_revision"}
	case AcceptanceAccept, AcceptanceReject:
		return []string{"board_id", "card_id", "attempt_id", "candidate_id", "criteria_revision", "expected_card_revision", "evidence", "candidate_digest", "criteria_digest", "evidence_head_revision", "evidence_set_digest", "policy_digest"}
	case CardPauseRequest, CardResumeRequest, CardCancelRequest:
		return []string{"board_id", "card_id", "expected_card_revision"}
	default:
		return nil
	}
}

func (r BoardRequest) hasOnlyBoardFields(fields ...string) bool {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	present := map[string]bool{
		"board_id": r.BoardID != "", "card_id": r.CardID != "", "dependency_id": r.DependencyID != "",
		"claim_id": r.ClaimID != "", "attempt_id": r.AttemptID != "", "candidate_id": r.CandidateID != "",
		"criteria_revision": r.CriteriaRevision != nil, "expected_board_revision": r.ExpectedBoardRevision != nil,
		"expected_card_revision": r.ExpectedCardRevision != nil, "expected_claim_revision": r.ExpectedClaimRevision != nil,
		"title": r.Title != nil, "description": r.Description != nil, "target_state": r.TargetState != "",
		"reason_code": r.ReasonCode != "", "evidence": r.Evidence != "", "labels": r.Labels != nil,
		"dependencies": r.Dependencies != nil,
		"criteria":     r.Criteria != nil, "expected_criteria_revision": r.ExpectedCriteriaRevision != nil,
		"candidate_digest": r.CandidateDigest != "", "criteria_digest": r.CriteriaDigest != "",
		"evidence_head_revision": r.EvidenceHeadRevision != nil, "evidence_set_digest": r.EvidenceSetDigest != "",
		"policy_digest": r.PolicyDigest != "", "stop_proof_id": r.StopProofID != "",
		"effect_resolution": r.EffectResolution != "", "task_head_digest": r.TaskHeadDigest != "",
		"process_proof_digest": r.ProcessProofDigest != "", "effect_evidence_digest": r.EffectEvidenceDigest != "",
		"priority": r.Priority != "", "parent_id": r.ParentID != "", "assignee_id": r.AssigneeID != "",
		"clear_parent": r.ClearParent != nil, "clear_assignee": r.ClearAssignee != nil,
		"budget": r.Budget != nil, "before_card_id": r.BeforeCardID != "", "after_card_id": r.AfterCardID != "",
		"expected_layout_revision": r.ExpectedLayoutRevision != nil, "expected_graph_revision": r.ExpectedGraphRevision != nil,
	}
	for field, exists := range present {
		if exists && !allowed[field] {
			return false
		}
	}
	return true
}

const (
	MaxBoardEventPageItems = 100
	MaxDependencyPageItems = 100
)

// BoardListOptions is the closed query contract for board discovery.
type BoardListOptions struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit"`
	State string `json:"state,omitempty"`
}

func (o BoardListOptions) Validate() error {
	if !validPageCursor(o.After) || o.Limit < 1 || o.Limit > MaxBoardPageItems ||
		(o.State != "" && o.State != "active" && o.State != "archived") {
		return ErrContract
	}
	return nil
}

// BoardSnapshotOptions binds a card page to explicit lifecycle and ownership
// filters. Empty filters mean all values; an absent owner is expressed as
// "unassigned", never as an empty actor identity.
type BoardSnapshotOptions struct {
	After      string `json:"after,omitempty"`
	Limit      int    `json:"limit"`
	State      string `json:"state,omitempty"`
	AssigneeID string `json:"assignee_id,omitempty"`
	OwnerID    string `json:"owner_id,omitempty"`
	ClaimState string `json:"claim_state,omitempty"`
}

func (o BoardSnapshotOptions) Validate() error {
	if !validPageCursor(o.After) || o.Limit < 1 || o.Limit > MaxCardPageItems ||
		(o.State != "" && !validBoardState(o.State)) || !validActorFilter(o.AssigneeID) ||
		!validActorFilter(o.OwnerID) || !validClaimFilter(o.ClaimState) {
		return ErrContract
	}
	return nil
}

type DependencyDirection string

const (
	DependencyPrerequisites DependencyDirection = "prerequisites"
	DependencyDependents    DependencyDirection = "dependents"
)

type DependencyOptions struct {
	After     string              `json:"after,omitempty"`
	Limit     int                 `json:"limit"`
	Direction DependencyDirection `json:"direction"`
}

func (o DependencyOptions) Validate() error {
	if !validPageCursor(o.After) || o.Limit < 1 || o.Limit > MaxDependencyPageItems ||
		(o.Direction != DependencyPrerequisites && o.Direction != DependencyDependents) {
		return ErrContract
	}
	return nil
}

type DependencyLink struct {
	Version      int    `json:"version"`
	BoardID      string `json:"board_id"`
	CardID       string `json:"card_id"`
	DependencyID string `json:"dependency_id"`
}

func (l DependencyLink) Validate() error {
	if l.Version != ContractVersion || !validID(l.BoardID) || !validID(l.CardID) ||
		!validID(l.DependencyID) || l.CardID == l.DependencyID {
		return ErrContract
	}
	return nil
}

type DependencyPage struct {
	Version       int                 `json:"version"`
	BoardID       string              `json:"board_id"`
	CardID        string              `json:"card_id"`
	Direction     DependencyDirection `json:"direction"`
	GraphRevision int64               `json:"graph_revision"`
	GraphDigest   string              `json:"graph_digest"`
	Items         []DependencyLink    `json:"items"`
	NextCursor    string              `json:"next_cursor,omitempty"`
	HasMore       bool                `json:"has_more"`
}

func (p DependencyPage) Validate() error {
	if p.Version != ContractVersion || !validID(p.BoardID) || !validID(p.CardID) ||
		(p.Direction != DependencyPrerequisites && p.Direction != DependencyDependents) ||
		p.GraphRevision < 1 || !validWorkboardDigest(p.GraphDigest) ||
		len(p.Items) > MaxDependencyPageItems || !validPageTail(p.NextCursor, p.HasMore, len(p.Items)) {
		return ErrContract
	}
	seen := make(map[string]bool, len(p.Items))
	for _, item := range p.Items {
		if item.Validate() != nil || item.BoardID != p.BoardID || seen[item.CardID+"\x00"+item.DependencyID] {
			return ErrContract
		}
		if p.Direction == DependencyPrerequisites && item.CardID != p.CardID ||
			p.Direction == DependencyDependents && item.DependencyID != p.CardID {
			return ErrContract
		}
		seen[item.CardID+"\x00"+item.DependencyID] = true
	}
	return encodedWithin(p, MaxTransactionBytes)
}

type BoardEventOptions struct {
	After             string `json:"after,omitempty"`
	Limit             int    `json:"limit"`
	TailAfterSequence int64  `json:"-"`
}

func (o BoardEventOptions) Validate() error {
	if !validPageCursor(o.After) || o.Limit < 1 || o.Limit > MaxBoardEventPageItems || o.TailAfterSequence < 0 ||
		o.After != "" && o.TailAfterSequence != 0 {
		return ErrContract
	}
	return nil
}

// BoardEvent is a redacted invalidation/audit summary. It deliberately cannot
// represent command bodies, evidence, tool payloads, or credentials; clients
// reconcile authoritative state through the snapshot endpoints.
type BoardEvent struct {
	Version       int                            `json:"version"`
	ID            string                         `json:"id"`
	BoardID       string                         `json:"board_id"`
	Sequence      int64                          `json:"sequence"`
	OperationID   string                         `json:"operation_id"`
	Kind          BoardAction                    `json:"kind"`
	ActorID       string                         `json:"actor_id"`
	ActorType     string                         `json:"actor_type"`
	CardID        string                         `json:"card_id,omitempty"`
	ClaimID       string                         `json:"claim_id,omitempty"`
	CreatedAt     time.Time                      `json:"created_at"`
	Decomposition *DecompositionAdmissionSummary `json:"decomposition,omitempty"`
}

// DecompositionAdmissionSummary is a redacted proof that host policy admitted
// a model/worker hierarchy mutation. It exposes immutable identities, digests,
// and numeric bounds only; runtime origin and raw configuration remain private.
type DecompositionAdmissionSummary struct {
	Version         int                        `json:"version"`
	AdmissionID     string                     `json:"admission_id"`
	AdmissionDigest string                     `json:"admission_digest"`
	DecisionDigest  string                     `json:"decision_digest"`
	ConfigDigest    string                     `json:"config_digest"`
	PolicyDigest    string                     `json:"policy_digest"`
	Limits          DecompositionLimitsSummary `json:"limits"`
	Depth           int                        `json:"depth"`
	DirectChildren  int                        `json:"direct_children"`
}

type DecompositionLimitsSummary struct {
	Version     int `json:"version"`
	MaxDepth    int `json:"max_depth"`
	MaxChildren int `json:"max_children"`
}

func (s DecompositionAdmissionSummary) Validate() error {
	if s.Version != ContractVersion || !validID(s.AdmissionID) || !validWorkboardDigest(s.AdmissionDigest) ||
		!validWorkboardDigest(s.DecisionDigest) || !validWorkboardDigest(s.ConfigDigest) || !validWorkboardDigest(s.PolicyDigest) ||
		s.Limits.Version != ContractVersion || s.Limits.MaxDepth < 1 || s.Limits.MaxDepth > 64 ||
		s.Limits.MaxChildren < 1 || s.Limits.MaxChildren > 64 || s.Depth < 1 || s.Depth > s.Limits.MaxDepth ||
		s.DirectChildren < 0 || s.DirectChildren > s.Limits.MaxChildren {
		return ErrContract
	}
	return nil
}

func (e BoardEvent) Validate() error {
	if e.Version != ContractVersion || !validID(e.ID) || !validID(e.BoardID) || e.Sequence < 1 ||
		!validKey(e.OperationID) || !validBoardAction(e.Kind) || !validID(e.ActorID) ||
		!validActorType(e.ActorType) || !optionalID(e.CardID) || !optionalID(e.ClaimID) ||
		(e.ClaimID != "" && e.CardID == "") || !validWorkboardTime(e.CreatedAt) {
		return ErrContract
	}
	if e.Decomposition != nil && (e.Decomposition.Validate() != nil || e.CardID == "" ||
		(e.Kind != CardCreate && e.Kind != CardRevise) || (e.ActorType != "model" && e.ActorType != "worker")) {
		return ErrContract
	}
	return encodedWithin(e, 16<<10)
}

type BoardEventPage struct {
	Version           int          `json:"version"`
	BoardID           string       `json:"board_id"`
	Items             []BoardEvent `json:"items"`
	NextCursor        string       `json:"next_cursor,omitempty"`
	HasMore           bool         `json:"has_more"`
	HighWaterSequence int64        `json:"high_water_sequence"`
}

func (p BoardEventPage) Validate() error {
	if p.Version != ContractVersion || !validID(p.BoardID) || len(p.Items) > MaxBoardEventPageItems ||
		p.HighWaterSequence < 0 || !validPageTail(p.NextCursor, p.HasMore, len(p.Items)) {
		return ErrContract
	}
	var previous int64
	seen := make(map[string]bool, len(p.Items))
	for _, item := range p.Items {
		if item.Validate() != nil || item.BoardID != p.BoardID || item.Sequence <= previous ||
			item.Sequence > p.HighWaterSequence || seen[item.ID] {
			return ErrContract
		}
		previous, seen[item.ID] = item.Sequence, true
	}
	return encodedWithin(p, MaxTransactionBytes)
}

func validPageCursor(value string) bool {
	return value == "" || boundedPrintable(value, 1, MaxCursorBytes)
}

func validPageTail(cursor string, more bool, count int) bool {
	return more == (cursor != "") && (!more || count > 0) && validPageCursor(cursor)
}

func validActorFilter(value string) bool {
	return value == "" || value == "unassigned" || validID(value)
}

func validClaimFilter(value string) bool {
	return value == "" || value == "unclaimed" || value == "active" || value == "attention"
}

func validBoardAction(value BoardAction) bool {
	switch value {
	case BoardCreate, BoardRevise, BoardArchive, CardCreate, CardRevise, CardMove, CardReorder,
		DependencyAdd, DependencyRemove, CardClaim, ClaimHeartbeat, ClaimAttention, CheckpointAppend,
		CandidateSubmit, AcceptanceAccept, AcceptanceReject, CardPauseRequest, CardPauseAck, CardResumeRequest, CardResumeAck,
		CardCancelRequest, CardCancelFinalize, CardBlock, CardUnblock, CriteriaRevise,
		ClaimRecover, ClaimFail:
		return true
	default:
		return false
	}
}
