package workboard

import "time"

const (
	SchemaVersion        = 1
	MaxBoards            = 100
	MaxTransactionEvents = 128
	MaxTransactionBytes  = 1 << 20
)

type BoardAction string

const (
	BoardCreateAction          BoardAction = "board.create"
	BoardReviseAction          BoardAction = "board.revise"
	BoardArchiveAction         BoardAction = "board.archive"
	CardCreateAction           BoardAction = "card.create"
	CardReviseAction           BoardAction = "card.revise"
	CardMoveAction             BoardAction = "card.move"
	CardReorderAction          BoardAction = "card.reorder"
	CardDependencyAddAction    BoardAction = "dependency.add"
	CardDependencyRemoveAction BoardAction = "dependency.remove"
	CardClaimAction            BoardAction = "card.claim"
	ClaimHeartbeatAction       BoardAction = "claim.heartbeat"
	ClaimRecoverAction         BoardAction = "claim.recover"
	ClaimFailAction            BoardAction = "claim.fail"
	ClaimAttentionAction       BoardAction = "claim.attention"
	CriteriaReviseAction       BoardAction = "criteria.revise"
	CheckpointAppendAction     BoardAction = "checkpoint.append"
	CandidateSubmitAction      BoardAction = "candidate.submit"
	AcceptanceAcceptAction     BoardAction = "acceptance.accept"
	AcceptanceRejectAction     BoardAction = "acceptance.reject"
	CardPauseRequestAction     BoardAction = "card.pause_request"
	CardPauseAckAction         BoardAction = "card.pause_acknowledge"
	CardResumeRequestAction    BoardAction = "card.resume_request"
	CardResumeAckAction        BoardAction = "card.resume_acknowledge"
	CardCancelRequestAction    BoardAction = "card.cancel_request"
	CardCancelFinalizeAction   BoardAction = "card.cancel_finalize"
	CardBlockAction            BoardAction = "card.block"
	CardUnblockAction          BoardAction = "card.unblock"
)

type Actor struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func (a Actor) Validate() error {
	if !validID(a.ID) || a.Type != "operator" && a.Type != "worker" && a.Type != "validator" && a.Type != "model" && a.Type != "system" {
		return fail(CodeInvalid, "actor")
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

func (b Board) Validate() error {
	if b.Version != SchemaVersion || !validID(b.ID) || b.Revision < 1 || b.LayoutRevision < 1 || b.EventSequence < 1 ||
		b.State != "active" && b.State != "archived" || !bounded(b.Title, 1, MaxTitleBytes) || !bounded(b.Description, 0, MaxDescriptionBytes) ||
		b.CardCount < 0 || b.CardCount > MaxCardsPerBoard || b.ActiveClaims < 0 || b.ActiveClaims > b.CardCount ||
		b.State == "archived" && b.ActiveClaims != 0 || !validTime(b.CreatedAt) || !validTime(b.UpdatedAt) || b.UpdatedAt.Before(b.CreatedAt) {
		return fail(CodeInvalid, "board")
	}
	return nil
}

type Column struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	BoardID string `json:"board_id"`
	State   State  `json:"state"`
	Title   string `json:"title"`
	Rank    string `json:"rank"`
}

func (c Column) Validate() error {
	if c.Version != SchemaVersion || !validID(c.BoardID) || !validState(c.State) || c.ID != string(c.State) ||
		!bounded(c.Title, 1, MaxTitleBytes) || !bounded(c.Rank, 1, MaxRankBytes) {
		return fail(CodeInvalid, "column")
	}
	return nil
}

type CreateBoardRequest struct {
	Version        int    `json:"version"`
	IdempotencyKey string `json:"-"`
	Title          string `json:"title"`
	Description    string `json:"description"`
}

func (r CreateBoardRequest) Validate() error {
	if r.Version != SchemaVersion || !validKey(r.IdempotencyKey) || !bounded(r.Title, 1, MaxTitleBytes) || !bounded(r.Description, 0, MaxDescriptionBytes) {
		return fail(CodeInvalid, "request")
	}
	return nil
}

type ArchiveBoardRequest struct {
	Version          int    `json:"version"`
	BoardID          string `json:"board_id"`
	IdempotencyKey   string `json:"-"`
	ExpectedRevision int64  `json:"expected_board_revision"`
}

// ReviseBoardRequest updates board metadata behind a revision fence. Pointer
// fields distinguish an omitted value from intentionally clearing description.
type ReviseBoardRequest struct {
	Version          int     `json:"version"`
	BoardID          string  `json:"board_id"`
	IdempotencyKey   string  `json:"-"`
	ExpectedRevision int64   `json:"expected_board_revision"`
	Title            *string `json:"title"`
	Description      *string `json:"description"`
}

func (r ReviseBoardRequest) Validate() error {
	if r.Version != SchemaVersion || !validID(r.BoardID) || !validKey(r.IdempotencyKey) || r.ExpectedRevision < 1 ||
		r.Title == nil && r.Description == nil || r.Title != nil && !bounded(*r.Title, 1, MaxTitleBytes) ||
		r.Description != nil && !bounded(*r.Description, 0, MaxDescriptionBytes) {
		return fail(CodeInvalid, "request")
	}
	return nil
}

func (r ArchiveBoardRequest) Validate() error {
	if r.Version != SchemaVersion || !validID(r.BoardID) || !validKey(r.IdempotencyKey) || r.ExpectedRevision < 1 {
		return fail(CodeInvalid, "request")
	}
	return nil
}

type BoardListOptions struct {
	After string
	Limit int
	State string
}

func (o BoardListOptions) Validate() error {
	if !validCursor(o.After) || o.Limit < 1 || o.Limit > MaxPageItems || o.State != "" && o.State != "active" && o.State != "archived" {
		return fail(CodeInvalid, "list")
	}
	return nil
}

type BoardPage struct {
	Version    int
	Items      []Board
	NextCursor string
	HasMore    bool
}

func (p BoardPage) Validate() error {
	if p.Version != SchemaVersion || len(p.Items) > MaxPageItems || p.HasMore != (p.NextCursor != "") || !validCursor(p.NextCursor) || p.HasMore && len(p.Items) == 0 {
		return fail(CodeInvalid, "page")
	}
	seen := map[string]bool{}
	for _, board := range p.Items {
		if board.Validate() != nil || seen[board.ID] {
			return fail(CodeInvalid, "page")
		}
		seen[board.ID] = true
	}
	return nil
}

type BoardSnapshotOptions struct {
	After      string
	Limit      int
	State      State
	AssigneeID string
	OwnerID    string
	ClaimState string
}

func (o BoardSnapshotOptions) Validate() error {
	if !validCursor(o.After) || o.Limit < 1 || o.Limit > MaxPageItems || o.State != "" && !validState(o.State) ||
		!validActorFilter(o.AssigneeID) || !validActorFilter(o.OwnerID) || !validClaimFilter(o.ClaimState) {
		return fail(CodeInvalid, "snapshot")
	}
	return nil
}

type BoardSnapshot struct {
	Version       int
	Board         Board
	Columns       []Column
	Cards         []Card
	NextCursor    string
	HasMore       bool
	GraphRevision int64
	GraphDigest   string
}

func (s BoardSnapshot) Validate() error {
	if s.Version != SchemaVersion || s.Board.Validate() != nil || len(s.Columns) != 7 || len(s.Cards) > MaxPageItems || s.HasMore != (s.NextCursor != "") ||
		!validCursor(s.NextCursor) || s.HasMore && len(s.Cards) == 0 || s.GraphRevision < 1 || !digest(s.GraphDigest) {
		return fail(CodeInvalid, "snapshot")
	}
	for index, column := range s.Columns {
		if column.Validate() != nil || column.BoardID != s.Board.ID || int(column.StateOrdinal()) != index {
			return fail(CodeInvalid, "snapshot")
		}
	}
	seen := map[string]bool{}
	for _, card := range s.Cards {
		if card.Validate() != nil || card.BoardID != s.Board.ID || seen[card.ID] {
			return fail(CodeInvalid, "snapshot")
		}
		seen[card.ID] = true
	}
	return nil
}

func (c Column) StateOrdinal() int {
	switch c.State {
	case Backlog:
		return 0
	case Ready:
		return 1
	case InProgress:
		return 2
	case Blocked:
		return 3
	case Review:
		return 4
	case Done:
		return 5
	case Canceled:
		return 6
	default:
		return -1
	}
}

type BoardEvent struct {
	Version     int         `json:"version"`
	ID          string      `json:"id"`
	BoardID     string      `json:"board_id"`
	Sequence    int64       `json:"sequence"`
	OperationID string      `json:"operation_id"`
	Kind        BoardAction `json:"kind"`
	ActorID     string      `json:"actor_id"`
	ActorType   string      `json:"actor_type"`
	CardID      string      `json:"card_id,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
}

func (e BoardEvent) Validate() error {
	if e.Version != SchemaVersion || !validID(e.ID) || !validID(e.BoardID) || e.Sequence < 1 || !validKey(e.OperationID) ||
		!validBoardAction(e.Kind) || (Actor{e.ActorID, e.ActorType}).Validate() != nil || !optionalID(e.CardID) ||
		boardActionRequiresCard(e.Kind) != (e.CardID != "") || !validTime(e.CreatedAt) {
		return fail(CodeInvalid, "event")
	}
	return nil
}

func boardActionRequiresCard(action BoardAction) bool {
	switch action {
	case CardCreateAction, CardReviseAction, CardMoveAction, CardReorderAction, CardDependencyAddAction, CardDependencyRemoveAction,
		CardClaimAction, ClaimHeartbeatAction, ClaimRecoverAction, ClaimFailAction, ClaimAttentionAction, CriteriaReviseAction, CheckpointAppendAction,
		CandidateSubmitAction, AcceptanceAcceptAction, AcceptanceRejectAction,
		CardPauseRequestAction, CardPauseAckAction, CardResumeRequestAction, CardResumeAckAction,
		CardCancelRequestAction, CardCancelFinalizeAction, CardBlockAction, CardUnblockAction:
		return true
	default:
		return false
	}
}

type BoardEventOptions struct {
	After             string
	Limit             int
	TailAfterSequence int64
}

func (o BoardEventOptions) Validate() error {
	if !validCursor(o.After) || o.Limit < 1 || o.Limit > MaxPageItems || o.TailAfterSequence < 0 ||
		o.After != "" && o.TailAfterSequence != 0 {
		return fail(CodeInvalid, "events")
	}
	return nil
}

// BoardEventPage is a bounded high-water snapshot over the immutable board
// journal. The cursor, when present, is opaque and authenticated by the store.
type BoardEventPage struct {
	Version           int          `json:"version"`
	BoardID           string       `json:"board_id"`
	HighWaterSequence int64        `json:"high_water_sequence"`
	Items             []BoardEvent `json:"items"`
	NextCursor        string       `json:"next_cursor,omitempty"`
	HasMore           bool         `json:"has_more"`
}

func (p BoardEventPage) Validate() error {
	if p.Version != SchemaVersion || !validID(p.BoardID) || p.HighWaterSequence < 1 || len(p.Items) < 1 || len(p.Items) > MaxPageItems ||
		p.HasMore != (p.NextCursor != "") || !validCursor(p.NextCursor) {
		return fail(CodeInvalid, "event_page")
	}
	var previous int64
	seen := make(map[string]bool, len(p.Items))
	for _, event := range p.Items {
		if event.Validate() != nil || event.BoardID != p.BoardID || event.Sequence > p.HighWaterSequence ||
			previous != 0 && event.Sequence != previous+1 || seen[event.ID] {
			return fail(CodeInvalid, "event_page")
		}
		previous = event.Sequence
		seen[event.ID] = true
	}
	if !p.HasMore && previous != p.HighWaterSequence {
		return fail(CodeInvalid, "event_page")
	}
	return nil
}

func validBoardAction(action BoardAction) bool {
	switch action {
	case BoardCreateAction, BoardReviseAction, BoardArchiveAction, CardCreateAction, CardReviseAction,
		CardMoveAction, CardReorderAction, CardDependencyAddAction, CardDependencyRemoveAction,
		CardClaimAction, ClaimHeartbeatAction, ClaimRecoverAction, ClaimFailAction, ClaimAttentionAction, CriteriaReviseAction, CheckpointAppendAction,
		CandidateSubmitAction, AcceptanceAcceptAction, AcceptanceRejectAction,
		CardPauseRequestAction, CardPauseAckAction, CardResumeRequestAction, CardResumeAckAction,
		CardCancelRequestAction, CardCancelFinalizeAction, CardBlockAction, CardUnblockAction:
		return true
	default:
		return false
	}
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

func (r OperationReceipt) Validate() error {
	if r.Version != SchemaVersion || !validID(r.BoardID) || !validKey(r.OperationID) || !digest(r.RequestDigest) || !digest(r.ResponseDigest) ||
		r.FirstSequence < 1 || r.LastSequence < r.FirstSequence || r.EventCount < 1 || r.EventCount > MaxTransactionEvents ||
		int64(r.EventCount) != r.LastSequence-r.FirstSequence+1 || r.TransactionBytes < 1 || r.TransactionBytes > MaxTransactionBytes ||
		r.BoardRevision < 1 || !optionalID(r.CardID) || r.CardRevision != nil && *r.CardRevision < 1 || r.ClaimRevision != nil && *r.ClaimRevision < 1 ||
		r.CardID == "" && (r.CardRevision != nil || r.ClaimRevision != nil) || r.ClaimRevision != nil && r.CardRevision == nil || r.Outcome != "committed" || !validTime(r.CreatedAt) {
		return fail(CodeInvalid, "receipt")
	}
	return nil
}

func validActorFilter(value string) bool {
	return value == "" || value == "unassigned" || validID(value)
}
func validClaimFilter(value string) bool {
	return value == "" || value == "unclaimed" || value == "active" || value == "attention"
}
