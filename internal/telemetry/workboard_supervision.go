package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type supervisionCursor struct {
	Version       int    `json:"v"`
	BoardID       string `json:"b"`
	BoardRevision int64  `json:"r"`
	ObservedAt    int64  `json:"o"`
	StaleBefore   int64  `json:"s"`
	CardID        string `json:"c"`
}

// ReadSupervisionPage derives actionable worker state from one bounded SQLite
// snapshot. It never probes processes, changes attention state, releases a
// claim, or dispatches work.
func (s *Store) ReadSupervisionPage(ctx context.Context, query workboard.SupervisionQuery) (workboard.SupervisionPage, error) {
	zero := workboard.SupervisionPage{}
	if s == nil || ctx == nil || ctx.Err() != nil || query.Validate() != nil {
		return zero, invalidWorkboard("supervision")
	}
	cursor, err := s.decodeSupervisionCursor(query.After)
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	board, _, _, err := readBoardRow(ctx, tx, query.BoardID)
	if err != nil {
		return zero, err
	}
	if board.State != "active" {
		return zero, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "board_state"}
	}
	observedAt, staleBefore, after := query.ObservedAt, query.StaleBefore, ""
	if query.After != "" {
		if cursor.BoardID != query.BoardID || cursor.BoardRevision != board.Revision {
			return zero, ErrWorkboardCursor
		}
		observedAt, staleBefore, after = time.Unix(0, cursor.ObservedAt).UTC(), time.Unix(0, cursor.StaleBefore).UTC(), cursor.CardID
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workboard_cards
		WHERE board_id=? AND id>? AND state IN('ready','in_progress','blocked')
		ORDER BY id LIMIT ?`, query.BoardID, after, query.Limit+1)
	if err != nil {
		return zero, err
	}
	ids := make([]string, 0, query.Limit+1)
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil || !validWorkboardID(id) {
			rows.Close()
			return zero, ErrWorkboardCorrupt
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return zero, err
	}
	if err = rows.Close(); err != nil {
		return zero, err
	}
	hasMore := len(ids) > query.Limit
	if hasMore {
		ids = ids[:query.Limit]
	}
	items := make([]workboard.SupervisionItem, 0, len(ids))
	for _, id := range ids {
		item, readErr := readSupervisionItem(ctx, tx, board.ID, id, observedAt, staleBefore)
		if readErr != nil {
			return zero, readErr
		}
		items = append(items, item)
	}
	page := workboard.SupervisionPage{Version: 1, BoardID: board.ID, BoardRevision: board.Revision,
		ObservedAt: observedAt, Items: items, HasMore: hasMore}
	if hasMore {
		page.NextCursor, err = s.encodeSupervisionCursor(supervisionCursor{Version: 1, BoardID: board.ID,
			BoardRevision: board.Revision, ObservedAt: observedAt.UnixNano(), StaleBefore: staleBefore.UnixNano(), CardID: ids[len(ids)-1]})
		if err != nil {
			return zero, err
		}
	}
	if page.Validate() != nil {
		return zero, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return page, nil
}

func readSupervisionItem(ctx context.Context, tx *sql.Tx, boardID, cardID string, observedAt, staleBefore time.Time) (workboard.SupervisionItem, error) {
	card, body, err := readStoredCard(ctx, tx, boardID, cardID)
	if err != nil {
		return workboard.SupervisionItem{}, err
	}
	base := workboard.SupervisionItem{Version: 1, BoardID: boardID, CardID: card.ID, CardRevision: card.Revision}
	if card.State == workboard.Ready {
		if card.RemainingDependencies != 0 || card.CurrentAttemptID != "" || card.CurrentClaimID != "" || body.CurrentAttemptID != "" || body.CurrentClaimID != "" {
			return workboard.SupervisionItem{}, ErrWorkboardCorrupt
		}
		base.State, base.Reason, base.Actions = workboard.SupervisionReady, workboard.SupervisionDependenciesSatisfied, workboard.SupervisionActions{Claim: true}
		return base, nil
	}
	if card.State != workboard.InProgress && card.State != workboard.Blocked || card.CurrentAttemptID == "" || card.CurrentClaimID == "" {
		return workboard.SupervisionItem{}, ErrWorkboardCorrupt
	}
	attempt, _, err := readCanonicalAttemptSnapshot(ctx, tx, boardID, cardID, card.CurrentAttemptID)
	if err != nil || attempt.State != "running" || attempt.Claim == nil || attempt.Claim.ID != card.CurrentClaimID ||
		(attempt.Claim.State != string(workboard.LeaseActive) && attempt.Claim.State != string(workboard.LeaseAttention)) {
		if err != nil {
			return workboard.SupervisionItem{}, err
		}
		return workboard.SupervisionItem{}, ErrWorkboardCorrupt
	}
	claim := attempt.Claim
	base.AttemptID, base.ClaimID, base.ClaimRevision = attempt.ID, claim.ID, claim.Revision
	base.PausePhase = card.PausePhase
	base.WorkerID, base.TaskID = attempt.WorkerID, claim.TaskID
	base.LastHeartbeat, base.ExpiresAt = claim.LastHeartbeat, claim.ExpiresAt
	base.Actions.CancelRequest = !card.CancelRequested
	terminal, err := supervisionTaskTerminal(ctx, tx, claim.TaskID)
	if err != nil {
		return workboard.SupervisionItem{}, err
	}
	if terminal != "" {
		base.State, base.Actions.RecoveryCheck = workboard.SupervisionOrphaned, true
		switch terminal {
		case "completed":
			base.Reason = workboard.SupervisionTaskCompleted
		case "failed":
			base.Reason = workboard.SupervisionTaskFailed
		case "canceled":
			base.Reason = workboard.SupervisionTaskCanceled
		}
		return base, nil
	}
	if !observedAt.Before(claim.ExpiresAt) {
		base.State, base.Reason, base.Actions.RecoveryCheck = workboard.SupervisionStalled, workboard.SupervisionLeaseExpired, true
		return base, nil
	}
	if !claim.LastHeartbeat.After(staleBefore) {
		base.State, base.Reason, base.Actions.RecoveryCheck = workboard.SupervisionStalled, workboard.SupervisionHeartbeatStale, true
		return base, nil
	}
	base.State, base.Reason = workboard.SupervisionRunning, workboard.SupervisionLeaseHealthy
	base.Actions.PauseRequest = card.PausePhase == workboard.PauseNone
	base.Actions.ResumeRequest = card.PausePhase == workboard.PauseAcknowledged
	return base, nil
}

func supervisionTaskTerminal(ctx context.Context, tx *sql.Tx, taskID string) (string, error) {
	if taskID == "" {
		return "", nil
	}
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM task_heads WHERE task_id=?`, taskID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrWorkboardCorrupt
	}
	if err != nil {
		return "", err
	}
	switch state {
	case "completed", "failed", "canceled":
		return state, nil
	case "running":
		return "", nil
	default:
		return "", ErrWorkboardCorrupt
	}
}

func (s *Store) encodeSupervisionCursor(cursor supervisionCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeSupervisionCursor(value string) (supervisionCursor, error) {
	if value == "" {
		return supervisionCursor{}, nil
	}
	body, err := s.verifyWorkboardCursor(value)
	if err != nil {
		return supervisionCursor{}, ErrWorkboardCursor
	}
	var cursor supervisionCursor
	if strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validWorkboardID(cursor.BoardID) || cursor.BoardRevision < 1 ||
		cursor.ObservedAt < 0 || cursor.StaleBefore < 0 || cursor.StaleBefore > cursor.ObservedAt || !validWorkboardID(cursor.CardID) {
		return supervisionCursor{}, ErrWorkboardCursor
	}
	canonical, _ := s.encodeSupervisionCursor(cursor)
	if canonical != value {
		return supervisionCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}

var _ workboard.SupervisionRepository = (*Store)(nil)
