package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// ListAcceptanceCandidates returns a bounded forward page of cards whose
// candidate evidence is durable but whose independent acceptance decision has
// not yet committed. The raw card-ID cursor tolerates decisions between pages;
// each later decision is independently protected by full revision/digest CAS.
func (s *Store) ListAcceptanceCandidates(ctx context.Context, boardID, after string, limit int) (workboard.AcceptanceCandidatePage, error) {
	if s == nil || s.db == nil || ctx == nil || !validWorkboardID(boardID) ||
		(after != "" && !validWorkboardID(after)) || limit < 1 || limit > workboard.MaxAcceptanceCandidatePageItems {
		return workboard.AcceptanceCandidatePage{}, invalidWorkboard("acceptance_candidates")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM workboard_cards WHERE board_id=? AND state='review' AND id>? ORDER BY id LIMIT ?`,
		boardID, after, limit+1)
	if err != nil {
		return workboard.AcceptanceCandidatePage{}, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil || !validWorkboardID(id) {
			return workboard.AcceptanceCandidatePage{}, ErrWorkboardCorrupt
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return workboard.AcceptanceCandidatePage{}, err
	}
	page := workboard.AcceptanceCandidatePage{CardIDs: ids}
	if len(ids) > limit {
		page.CardIDs, page.HasMore = ids[:limit], true
	}
	if len(page.CardIDs) > 0 {
		page.NextCursor = page.CardIDs[len(page.CardIDs)-1]
	}
	if page.Validate() != nil {
		return workboard.AcceptanceCandidatePage{}, ErrWorkboardCorrupt
	}
	return page, nil
}

// ReadAcceptanceDecision returns the card revision and current rich attempt
// from one SQLite read transaction. The later decision mutation rechecks all
// candidate, criteria, evidence, policy, and card revision fences.
func (s *Store) ReadAcceptanceDecision(ctx context.Context, boardID, cardID string) (workboard.AcceptanceDecisionSnapshot, error) {
	if s == nil || s.db == nil || ctx == nil || !validWorkboardID(boardID) || !validWorkboardID(cardID) {
		return workboard.AcceptanceDecisionSnapshot{}, invalidWorkboard("acceptance_snapshot")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.AcceptanceDecisionSnapshot{}, err
	}
	defer tx.Rollback()
	card, _, err := readStoredCard(ctx, tx, boardID, cardID)
	if err != nil {
		return workboard.AcceptanceDecisionSnapshot{}, err
	}
	lifecycle, found, err := readCardLifecycleSnapshot(ctx, tx, boardID, cardID)
	if err != nil {
		return workboard.AcceptanceDecisionSnapshot{}, err
	}
	if !found || lifecycle.Attempt == nil {
		return workboard.AcceptanceDecisionSnapshot{}, ErrWorkboardNotFound
	}
	result := workboard.AcceptanceDecisionSnapshot{CardRevision: card.Revision, Attempt: *lifecycle.Attempt}
	stateMatches := card.State == workboard.Review && result.Attempt.State == "review" ||
		card.State == workboard.Done && result.Attempt.State == "accepted" ||
		card.State == workboard.Ready && result.Attempt.State == "rejected"
	if !stateMatches || result.Attempt.CardID != card.ID {
		return workboard.AcceptanceDecisionSnapshot{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.AcceptanceDecisionSnapshot{}, err
	}
	return result, nil
}

var _ workboard.AcceptanceCandidateRepository = (*Store)(nil)
var _ workboard.AcceptanceDecisionRepository = (*Store)(nil)
