package telemetry

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// storedWorkboardCard mirrors the canonical version-1 durable body. It is
// deliberately local to persistence; presentation mapping remains in app/BFF.
type storedWorkboardCard struct {
	Version               int                        `json:"version"`
	ID                    string                     `json:"id"`
	BoardID               string                     `json:"board_id"`
	Revision              int64                      `json:"revision"`
	CriteriaRevision      int64                      `json:"criteria_revision"`
	State                 string                     `json:"state"`
	Rank                  string                     `json:"rank"`
	Title                 string                     `json:"title"`
	Description           string                     `json:"description"`
	Priority              string                     `json:"priority"`
	Labels                []string                   `json:"labels"`
	ParentID              string                     `json:"parent_id,omitempty"`
	Dependencies          []string                   `json:"dependencies"`
	RemainingDependencies int                        `json:"remaining_dependencies"`
	AssigneeID            string                     `json:"assignee_id,omitempty"`
	AttemptCount          int                        `json:"attempt_count"`
	CurrentAttemptID      string                     `json:"current_attempt_id,omitempty"`
	CurrentClaimID        string                     `json:"current_claim_id,omitempty"`
	AcceptanceID          string                     `json:"acceptance_id,omitempty"`
	BlockReason           string                     `json:"block_reason,omitempty"`
	CancelRequested       bool                       `json:"cancel_requested"`
	PauseRequested        bool                       `json:"pause_requested"`
	PausePhase            workboard.PausePhase       `json:"pause_phase,omitempty"`
	Budget                storedWorkboardBudget      `json:"budget"`
	Criteria              []storedWorkboardCriterion `json:"criteria"`
	CreatedAt             time.Time                  `json:"created_at"`
	UpdatedAt             time.Time                  `json:"updated_at"`
}

type storedWorkboardBudget struct {
	AttemptLimit int   `json:"attempt_limit"`
	TimeLimitMS  int64 `json:"time_limit_ms"`
	TokenLimit   int64 `json:"token_limit"`
	CostMicros   int64 `json:"cost_micros"`
}

type storedWorkboardCriterion struct {
	Version        int    `json:"version"`
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	RequiredSource string `json:"required_source"`
	ValidatorID    string `json:"validator_id"`
	Description    string `json:"description"`
	Required       bool   `json:"required"`
}

type workboardCardIndex struct {
	Ordinal               int
	ColumnState           string
	ID                    string
	Revision              int64
	CriteriaRevision      int64
	State                 string
	Rank                  string
	Title                 string
	Description           string
	Priority              string
	ParentID              sql.NullString
	AssigneeID            sql.NullString
	BlockReason           sql.NullString
	RemainingDependencies int
	AttemptCount          int
	AttemptLimit          int
	TimeLimitMS           int64
	TokenLimit            int64
	CostMicros            int64
	CurrentAttemptID      sql.NullString
	CurrentClaimID        sql.NullString
	AcceptanceID          sql.NullString
	CancelRequested       int
	PauseRequested        int
	CreatedAt             int64
	UpdatedAt             int64
}

func scanWorkboardCard(row rowScanner, boardID string) (workboard.Card, cardPageCursor, error) {
	var bodyBytes []byte
	var index workboardCardIndex
	if err := row.Scan(&bodyBytes, &index.Ordinal, &index.ColumnState, &index.ID, &index.Revision, &index.CriteriaRevision,
		&index.State, &index.Rank, &index.Title, &index.Description, &index.Priority, &index.ParentID, &index.AssigneeID,
		&index.BlockReason, &index.RemainingDependencies, &index.AttemptCount, &index.AttemptLimit, &index.TimeLimitMS,
		&index.TokenLimit, &index.CostMicros, &index.CurrentAttemptID, &index.CurrentClaimID, &index.AcceptanceID,
		&index.CancelRequested, &index.PauseRequested, &index.CreatedAt, &index.UpdatedAt); err != nil {
		return workboard.Card{}, cardPageCursor{}, err
	}
	var body storedWorkboardCard
	if strictJSON(bodyBytes, &body) != nil {
		return workboard.Card{}, cardPageCursor{}, ErrWorkboardCorrupt
	}
	// Schema 35-38 represented the requested phase with the compatibility
	// boolean alone. Missing phase therefore has exactly one legacy meaning.
	if body.PausePhase == workboard.PauseNone && body.PauseRequested {
		body.PausePhase = workboard.PauseRequested
	}
	if !storedCardMatches(index, body, boardID) {
		return workboard.Card{}, cardPageCursor{}, ErrWorkboardCorrupt
	}
	card := workboard.Card{ID: body.ID, BoardID: body.BoardID, Revision: body.Revision, State: workboard.State(body.State), Rank: body.Rank,
		CriteriaRevision: body.CriteriaRevision,
		Title:            body.Title, Description: body.Description, Priority: body.Priority, Labels: append([]string{}, body.Labels...),
		AssigneeID: body.AssigneeID, ParentID: body.ParentID, Dependencies: append([]string{}, body.Dependencies...),
		RemainingDependencies: body.RemainingDependencies, AttemptCount: body.AttemptCount, CurrentAttemptID: body.CurrentAttemptID,
		CurrentClaimID: body.CurrentClaimID, AcceptanceID: body.AcceptanceID, BlockReason: body.BlockReason,
		CancelRequested: body.CancelRequested, PauseRequested: body.PauseRequested, PausePhase: body.PausePhase, Budget: domainWorkboardBudget(body.Budget),
		Criteria:  domainWorkboardCriteria(body.Criteria),
		CreatedAt: body.CreatedAt, UpdatedAt: body.UpdatedAt}
	if card.Validate() != nil {
		return workboard.Card{}, cardPageCursor{}, ErrWorkboardCorrupt
	}
	return card, cardPageCursor{Column: index.Ordinal, Rank: index.Rank, ID: index.ID}, nil
}

func domainWorkboardBudget(b storedWorkboardBudget) workboard.WorkBudget {
	return workboard.WorkBudget{AttemptLimit: b.AttemptLimit, TimeLimitMS: b.TimeLimitMS, TokenLimit: b.TokenLimit, CostMicros: b.CostMicros}
}

func storedCardBudget(b workboard.WorkBudget) storedWorkboardBudget {
	return storedWorkboardBudget{AttemptLimit: b.AttemptLimit, TimeLimitMS: b.TimeLimitMS, TokenLimit: b.TokenLimit, CostMicros: b.CostMicros}
}

func domainWorkboardCriteria(criteria []storedWorkboardCriterion) []workboard.AcceptanceCriterion {
	result := make([]workboard.AcceptanceCriterion, len(criteria))
	for index, criterion := range criteria {
		result[index] = workboard.AcceptanceCriterion{Version: criterion.Version, ID: criterion.ID, Kind: criterion.Kind,
			RequiredSource: criterion.RequiredSource, ValidatorID: criterion.ValidatorID, Description: criterion.Description, Required: criterion.Required}
	}
	return result
}

func storedCardCriteria(criteria []workboard.AcceptanceCriterion) []storedWorkboardCriterion {
	result := make([]storedWorkboardCriterion, len(criteria))
	for index, criterion := range criteria {
		result[index] = storedWorkboardCriterion{Version: criterion.Version, ID: criterion.ID, Kind: criterion.Kind,
			RequiredSource: criterion.RequiredSource, ValidatorID: criterion.ValidatorID, Description: criterion.Description, Required: criterion.Required}
	}
	return result
}

func storedCardMatches(index workboardCardIndex, body storedWorkboardCard, boardID string) bool {
	return body.Version == 1 && body.BoardID == boardID && body.ID == index.ID && body.Revision == index.Revision &&
		body.CriteriaRevision == index.CriteriaRevision && body.State == index.State && index.ColumnState == index.State &&
		canonicalColumnOrdinal(index.State) == index.Ordinal && body.Rank == index.Rank && body.Title == index.Title &&
		body.Description == index.Description && body.Priority == index.Priority && body.ParentID == nullString(index.ParentID) &&
		body.AssigneeID == nullString(index.AssigneeID) && body.BlockReason == nullString(index.BlockReason) &&
		body.RemainingDependencies == index.RemainingDependencies && body.AttemptCount == index.AttemptCount &&
		body.Budget.AttemptLimit == index.AttemptLimit && body.Budget.TimeLimitMS == index.TimeLimitMS &&
		body.Budget.TokenLimit == index.TokenLimit && body.Budget.CostMicros == index.CostMicros &&
		body.CurrentAttemptID == nullString(index.CurrentAttemptID) && body.CurrentClaimID == nullString(index.CurrentClaimID) &&
		body.AcceptanceID == nullString(index.AcceptanceID) && boolInt(body.CancelRequested) == index.CancelRequested &&
		boolInt(body.PauseRequested) == index.PauseRequested && body.PauseRequested == (body.PausePhase != workboard.PauseNone) &&
		validStoredPausePhase(body.PausePhase) && body.CreatedAt.Equal(time.Unix(0, index.CreatedAt).UTC()) &&
		body.UpdatedAt.Equal(time.Unix(0, index.UpdatedAt).UTC()) && validStoredCardReferences(body)
}

func validStoredPausePhase(phase workboard.PausePhase) bool {
	return phase == workboard.PauseNone || phase == workboard.PauseRequested || phase == workboard.PauseAcknowledged || phase == workboard.ResumeRequested
}

func validStoredCardReferences(body storedWorkboardCard) bool {
	for _, value := range []string{body.ParentID, body.AssigneeID, body.BlockReason, body.CurrentAttemptID, body.CurrentClaimID, body.AcceptanceID} {
		if value != "" && !validWorkboardID(value) {
			return false
		}
	}
	if body.CriteriaRevision < 1 || body.AttemptCount < 0 || body.Budget.AttemptLimit < 1 || body.Labels == nil || body.Dependencies == nil || body.Criteria == nil {
		return false
	}
	_, err := json.Marshal(body)
	return err == nil
}

func canonicalColumnOrdinal(state string) int {
	for ordinal, column := range canonicalWorkboardColumns {
		if string(column.state) == state {
			return ordinal
		}
	}
	return -1
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
