package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type attemptHistoryCursor struct {
	Version     int    `json:"v"`
	BoardDigest string `json:"b"`
	CardDigest  string `json:"c"`
	High        int    `json:"h"`
	Before      int    `json:"o"`
}

type attemptDetailCursor struct {
	Version         int    `json:"v"`
	BoardDigest     string `json:"b"`
	CardDigest      string `json:"c"`
	AttemptDigest   string `json:"i"`
	AttemptRevision int64  `json:"r"`
	High            int64  `json:"h"`
	Before          int64  `json:"o"`
}

// ListAttemptHistory returns compact attempts newest-first from a frozen
// ordinal high-water. Cursors are authenticated and process-epoch scoped: a
// reopened store rejects an old cursor, while a fresh traversal reads the same
// durable history plus any subsequently committed attempts.
func (s *Store) ListAttemptHistory(ctx context.Context, boardID, cardID string, options workboard.AttemptHistoryOptions) (workboard.AttemptHistoryPage, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !validWorkboardID(boardID) || !validWorkboardID(cardID) || options.Validate() != nil {
		return workboard.AttemptHistoryPage{}, invalidWorkboard("attempt_history")
	}
	cursor, err := s.decodeAttemptHistoryCursor(options.After)
	if err != nil || options.After != "" && (cursor.BoardDigest != digestBytes([]byte(boardID)) || cursor.CardDigest != digestBytes([]byte(cardID))) {
		return workboard.AttemptHistoryPage{}, ErrWorkboardCursor
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.AttemptHistoryPage{}, err
	}
	defer tx.Rollback()
	if err = requireLifecycleCard(ctx, tx, boardID, cardID); err != nil {
		return workboard.AttemptHistoryPage{}, err
	}
	var count, minimum, maximum int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(ordinal),0),COALESCE(max(ordinal),0) FROM workboard_attempts WHERE board_id=? AND card_id=?`, boardID, cardID).
		Scan(&count, &minimum, &maximum); err != nil {
		return workboard.AttemptHistoryPage{}, err
	}
	if count != maximum || count > workboard.MaxAttemptsPerCard || count > 0 && minimum != 1 {
		return workboard.AttemptHistoryPage{}, ErrWorkboardCorrupt
	}
	if options.After == "" {
		cursor = attemptHistoryCursor{Version: 1, BoardDigest: digestBytes([]byte(boardID)), CardDigest: digestBytes([]byte(cardID)), High: maximum, Before: maximum + 1}
	} else if cursor.High > maximum {
		return workboard.AttemptHistoryPage{}, ErrWorkboardCursor
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workboard_attempts WHERE board_id=? AND card_id=? AND ordinal<=? AND ordinal<? ORDER BY ordinal DESC LIMIT ?`,
		boardID, cardID, cursor.High, cursor.Before, options.Limit+1)
	if err != nil {
		return workboard.AttemptHistoryPage{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil || !validWorkboardID(id) || len(ids) > options.Limit {
			rows.Close()
			return workboard.AttemptHistoryPage{}, ErrWorkboardCorrupt
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return workboard.AttemptHistoryPage{}, err
	}
	page := workboard.AttemptHistoryPage{Version: 1, BoardID: boardID, CardID: cardID, HighWaterOrdinal: cursor.High, Items: []workboard.AttemptHistoryRecord{}}
	visible := ids
	if len(visible) > options.Limit {
		visible = visible[:options.Limit]
		page.HasMore = true
	}
	for _, id := range visible {
		attempt, checkpoints, readErr := readCanonicalAttemptSnapshot(ctx, tx, boardID, cardID, id)
		if readErr != nil {
			return workboard.AttemptHistoryPage{}, readErr
		}
		page.Items = append(page.Items, attemptHistoryRecord(attempt, checkpoints))
	}
	if page.HasMore {
		cursor.Before = page.Items[len(page.Items)-1].Ordinal
		page.NextCursor, err = s.encodeAttemptHistoryCursor(cursor)
		if err != nil {
			return workboard.AttemptHistoryPage{}, err
		}
	}
	if page.Validate() != nil {
		return workboard.AttemptHistoryPage{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.AttemptHistoryPage{}, err
	}
	return page, nil
}

// ReadAttemptDetail returns one canonical attempt and a newest-first frozen
// checkpoint page. The cursor also binds the attempt revision so a mutable
// running attempt cannot be spliced across incompatible detail snapshots.
func (s *Store) ReadAttemptDetail(ctx context.Context, boardID, cardID, attemptID string, options workboard.AttemptDetailOptions) (workboard.AttemptDetailPage, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !validWorkboardID(boardID) || !validWorkboardID(cardID) || !validWorkboardID(attemptID) || options.Validate() != nil {
		return workboard.AttemptDetailPage{}, invalidWorkboard("attempt_detail")
	}
	cursor, err := s.decodeAttemptDetailCursor(options.After)
	if err != nil || options.After != "" && (cursor.BoardDigest != digestBytes([]byte(boardID)) || cursor.CardDigest != digestBytes([]byte(cardID)) || cursor.AttemptDigest != digestBytes([]byte(attemptID))) {
		return workboard.AttemptDetailPage{}, ErrWorkboardCursor
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.AttemptDetailPage{}, err
	}
	defer tx.Rollback()
	attempt, count, err := readCanonicalAttemptSnapshot(ctx, tx, boardID, cardID, attemptID)
	if err != nil {
		return workboard.AttemptDetailPage{}, err
	}
	if options.After == "" {
		cursor = attemptDetailCursor{Version: 1, BoardDigest: digestBytes([]byte(boardID)), CardDigest: digestBytes([]byte(cardID)), AttemptDigest: digestBytes([]byte(attemptID)),
			AttemptRevision: attempt.Revision, High: int64(count), Before: int64(count) + 1}
	} else if cursor.AttemptRevision != attempt.Revision || cursor.High > int64(count) {
		return workboard.AttemptDetailPage{}, ErrWorkboardCursor
	}
	checkpoints, err := readCheckpointHistoryPage(ctx, tx, boardID, cardID, attemptID, cursor.High, cursor.Before, options.Limit+1)
	if err != nil {
		return workboard.AttemptDetailPage{}, err
	}
	page := workboard.AttemptDetailPage{Version: 1, Attempt: attempt, CheckpointHighWaterRevision: cursor.High, Checkpoints: checkpoints}
	if len(page.Checkpoints) > options.Limit {
		page.Checkpoints = page.Checkpoints[:options.Limit]
		page.HasMore = true
		cursor.Before = page.Checkpoints[len(page.Checkpoints)-1].Revision
		page.NextCursor, err = s.encodeAttemptDetailCursor(cursor)
		if err != nil {
			return workboard.AttemptDetailPage{}, err
		}
	}
	if page.Validate() != nil {
		return workboard.AttemptDetailPage{}, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.AttemptDetailPage{}, err
	}
	return page, nil
}

func requireLifecycleCard(ctx context.Context, tx *sql.Tx, boardID, cardID string) error {
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workboard_cards WHERE board_id=? AND id=?`, boardID, cardID).Scan(&found); err != nil {
		return err
	}
	if found != 1 {
		return ErrWorkboardNotFound
	}
	return nil
}

func attemptHistoryRecord(a workboard.AttemptSnapshot, checkpoints int) workboard.AttemptHistoryRecord {
	result := workboard.AttemptHistoryRecord{Version: 1, ID: a.ID, BoardID: a.BoardID, CardID: a.CardID, Ordinal: a.Ordinal,
		Revision: a.Revision, State: a.State, WorkerID: a.WorkerID, CriteriaRevision: a.CriteriaRevision,
		CheckpointCount: checkpoints, StartedAt: a.StartedAt, EndedAt: a.EndedAt}
	if a.Candidate != nil {
		result.CandidateID = a.Candidate.ID
	}
	if a.Acceptance != nil {
		result.AcceptanceID = a.Acceptance.ID
	}
	return result
}

func readCanonicalAttemptSnapshot(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) (workboard.AttemptSnapshot, int, error) {
	var id, indexedBoard, indexedCard, state, worker, criteriaDigest, policyDigest string
	var ordinal, attemptLimit int
	var revision, criteriaRevision, timeLimit, tokenLimit, costMicros, started int64
	var ended sql.NullInt64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT id,board_id,card_id,ordinal,revision,state,worker_id,criteria_revision,criteria_digest,policy_digest,
		attempt_limit,time_limit_ms,token_limit,cost_micros,started_at,ended_at,body FROM workboard_attempts WHERE board_id=? AND card_id=? AND id=?`, boardID, cardID, attemptID).
		Scan(&id, &indexedBoard, &indexedCard, &ordinal, &revision, &state, &worker, &criteriaRevision, &criteriaDigest, &policyDigest,
			&attemptLimit, &timeLimit, &tokenLimit, &costMicros, &started, &ended, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardNotFound
	}
	if err != nil {
		return workboard.AttemptSnapshot{}, 0, err
	}
	var stored storedLifecycleAttempt
	var rich *storedEvaluationAttempt
	if state == "review" || state == "accepted" || state == "rejected" {
		var value storedEvaluationAttempt
		if strictJSON(body, &value) != nil {
			return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
		}
		rich = &value
		stored = evaluationBase(value)
	} else if strictJSON(body, &stored) != nil {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	if stored.Version != 1 || stored.ID != id || stored.BoardID != indexedBoard || stored.CardID != indexedCard || id != attemptID || indexedBoard != boardID || indexedCard != cardID ||
		stored.Ordinal != ordinal || stored.Revision != revision || stored.State != state || stored.WorkerID != worker || stored.CriteriaRevision != criteriaRevision ||
		stored.CriteriaDigest != criteriaDigest || stored.PolicyDigest != policyDigest || stored.Budget != (storedWorkboardBudget{AttemptLimit: attemptLimit, TimeLimitMS: timeLimit, TokenLimit: tokenLimit, CostMicros: costMicros}) ||
		!stored.StartedAt.Equal(time.Unix(0, started).UTC()) || ended.Valid != (stored.EndedAt != nil) || ended.Valid && stored.EndedAt.UnixNano() != ended.Int64 || !validAttemptStateValue(state) {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	criteria, err := readAttemptCriteria(ctx, tx, boardID, cardID, stored.CriteriaRevision)
	computedCriteriaDigest, digestErr := digestJSON(stored.Criteria)
	if err != nil || digestErr != nil || computedCriteriaDigest != stored.CriteriaDigest || !reflect.DeepEqual(criteria, stored.Criteria) {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	tasks, err := readAttemptLinks(ctx, tx, "workboard_attempt_tasks", "task_id", boardID, cardID, attemptID)
	if err != nil || !reflect.DeepEqual(tasks, stored.TaskIDs) {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	sessions, err := readAttemptLinks(ctx, tx, "workboard_attempt_sessions", "session_id", boardID, cardID, attemptID)
	if err != nil || !reflect.DeepEqual(sessions, stored.SessionIDs) {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	_, claim, err := readLifecycleClaim(ctx, tx, boardID, cardID, attemptID, stored.Claim.ID)
	if err != nil || !equalStoredClaim(claim, stored.Claim) {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	attempt := attemptSnapshot(stored)
	candidate, candidateFound, err := readSnapshotCandidate(ctx, tx, boardID, cardID, attemptID)
	if err != nil {
		return workboard.AttemptSnapshot{}, 0, err
	}
	if candidateFound {
		attempt.Candidate = &candidate
	}
	evidence, err := readSnapshotEvidence(ctx, tx, boardID, cardID, attemptID)
	if err != nil {
		return workboard.AttemptSnapshot{}, 0, err
	}
	attempt.Evidence = evidence
	acceptance, acceptanceFound, err := readSnapshotAcceptance(ctx, tx, boardID, cardID, attemptID)
	if err != nil {
		return workboard.AttemptSnapshot{}, 0, err
	}
	if acceptanceFound {
		attempt.Acceptance = &acceptance
	}
	if !attemptMatchesRich(attempt, rich, candidateFound, acceptanceFound) {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	var checkpointCount, checkpointMin, checkpointMax int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(revision),0),COALESCE(max(revision),0) FROM workboard_checkpoints WHERE board_id=? AND card_id=? AND attempt_id=?`, boardID, cardID, attemptID).
		Scan(&checkpointCount, &checkpointMin, &checkpointMax); err != nil {
		return workboard.AttemptSnapshot{}, 0, err
	}
	if checkpointCount != checkpointMax || checkpointCount > workboard.MaxCheckpointsPerAttempt || checkpointCount > 0 && checkpointMin != 1 {
		return workboard.AttemptSnapshot{}, 0, ErrWorkboardCorrupt
	}
	return attempt, checkpointCount, nil
}

func readAttemptCriteria(ctx context.Context, tx *sql.Tx, boardID, cardID string, revision int64) ([]storedWorkboardCriterion, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ordinal,id,kind,required_source,validator_id,description,required,body FROM workboard_criteria
		WHERE board_id=? AND card_id=? AND criteria_revision=? ORDER BY ordinal`, boardID, cardID, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []storedWorkboardCriterion{}
	for rows.Next() {
		var ordinal, required int
		indexed := storedWorkboardCriterion{Version: 1}
		var body []byte
		if rows.Scan(&ordinal, &indexed.ID, &indexed.Kind, &indexed.RequiredSource, &indexed.ValidatorID, &indexed.Description, &required, &body) != nil {
			return nil, ErrWorkboardCorrupt
		}
		indexed.Required = required == 1
		var canonical storedWorkboardCriterion
		if ordinal != len(result) || strictJSON(body, &canonical) != nil || canonical != indexed || len(result) >= workboard.MaxAcceptanceCriteria {
			return nil, ErrWorkboardCorrupt
		}
		result = append(result, indexed)
	}
	if err = rows.Err(); err != nil || len(result) == 0 {
		return nil, ErrWorkboardCorrupt
	}
	return result, nil
}

func evaluationBase(a storedEvaluationAttempt) storedLifecycleAttempt {
	return storedLifecycleAttempt{Version: a.Version, ID: a.ID, BoardID: a.BoardID, CardID: a.CardID, Ordinal: a.Ordinal,
		Revision: a.Revision, State: a.State, WorkerID: a.WorkerID, CriteriaRevision: a.CriteriaRevision, CriteriaDigest: a.CriteriaDigest,
		PolicyDigest: a.PolicyDigest, Budget: a.Budget, Criteria: a.Criteria, TaskIDs: a.TaskIDs, SessionIDs: a.SessionIDs,
		Claim: a.Claim, StartedAt: a.StartedAt, EndedAt: a.EndedAt}
}

func attemptMatchesRich(a workboard.AttemptSnapshot, rich *storedEvaluationAttempt, candidate, acceptance bool) bool {
	if rich == nil {
		return !candidate && len(a.Evidence) == 0 && !acceptance
	}
	if !candidate || rich.Candidate == nil || !reflect.DeepEqual(*a.Candidate, *rich.Candidate) || !reflect.DeepEqual(a.Evidence, rich.Evidence) ||
		rich.AcceptanceID != optionalAcceptanceID(a.Acceptance) {
		return false
	}
	if a.Acceptance == nil {
		return rich.DecisionBy == "" && rich.DecisionByType == "" && rich.DecisionAuthorityID == "" && rich.AcceptanceEvidenceDigest == ""
	}
	return rich.DecisionBy == a.Acceptance.DecidedBy && rich.DecisionByType == a.Acceptance.DecidedByType &&
		rich.DecisionAuthorityID == a.Acceptance.DecisionAuthorityID && rich.AcceptanceEvidenceDigest == a.Acceptance.EvidenceSetDigest
}

func optionalAcceptanceID(a *workboard.AcceptanceRecord) string {
	if a == nil {
		return ""
	}
	return a.ID
}

func readAttemptLinks(ctx context.Context, tx *sql.Tx, table, column, boardID, cardID, attemptID string) ([]string, error) {
	query := `SELECT ` + column + ` FROM ` + table + ` WHERE board_id=? AND card_id=? AND attempt_id=? ORDER BY ordinal`
	rows, err := tx.QueryContext(ctx, query, boardID, cardID, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if rows.Scan(&value) != nil || !validWorkboardID(value) || len(result) >= workboard.MaxAttemptLinks {
			return nil, ErrWorkboardCorrupt
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func readCheckpointHistoryPage(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string, high, before int64, limit int) ([]workboard.CheckpointRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,policy_digest,evidence,evidence_digest,
		actor_id,actor_type,created_at,body FROM workboard_checkpoints WHERE board_id=? AND card_id=? AND attempt_id=? AND revision<=? AND revision<? ORDER BY revision DESC LIMIT ?`,
		boardID, cardID, attemptID, high, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workboard.CheckpointRecord{}
	for rows.Next() {
		var body []byte
		var checkpoint, indexed workboard.CheckpointRecord
		var created int64
		if rows.Scan(&indexed.ID, &indexed.ClaimID, &indexed.Revision, &indexed.ClaimRevision, &indexed.CriteriaRevision,
			&indexed.CriteriaDigest, &indexed.PolicyDigest, &indexed.Evidence, &indexed.EvidenceDigest, &indexed.ActorID, &indexed.ActorType, &created, &body) != nil {
			return nil, ErrWorkboardCorrupt
		}
		indexed.Version, indexed.BoardID, indexed.CardID, indexed.AttemptID, indexed.CreatedAt = 1, boardID, cardID, attemptID, time.Unix(0, created).UTC()
		if strictJSON(body, &checkpoint) != nil || checkpoint.Validate() != nil || !reflect.DeepEqual(checkpoint, indexed) {
			return nil, ErrWorkboardCorrupt
		}
		result = append(result, checkpoint)
	}
	return result, rows.Err()
}

func validAttemptStateValue(value string) bool {
	return value == "running" || value == "review" || value == "accepted" || value == "rejected" || value == "failed" || value == "canceled"
}

func (s *Store) encodeAttemptHistoryCursor(cursor attemptHistoryCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeAttemptHistoryCursor(value string) (attemptHistoryCursor, error) {
	if value == "" {
		return attemptHistoryCursor{}, nil
	}
	body, err := s.verifyWorkboardCursor(value)
	var cursor attemptHistoryCursor
	if err != nil || strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validDigest(cursor.BoardDigest) || !validDigest(cursor.CardDigest) ||
		cursor.High < 1 || cursor.High > workboard.MaxAttemptsPerCard || cursor.Before < 2 || cursor.Before > cursor.High+1 {
		return attemptHistoryCursor{}, ErrWorkboardCursor
	}
	canonical, err := s.encodeAttemptHistoryCursor(cursor)
	if err != nil || canonical != value {
		return attemptHistoryCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}

func (s *Store) encodeAttemptDetailCursor(cursor attemptDetailCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return s.signWorkboardCursor(body)
}

func (s *Store) decodeAttemptDetailCursor(value string) (attemptDetailCursor, error) {
	if value == "" {
		return attemptDetailCursor{}, nil
	}
	body, err := s.verifyWorkboardCursor(value)
	var cursor attemptDetailCursor
	if err != nil || strictJSON(body, &cursor) != nil || cursor.Version != 1 || !validDigest(cursor.BoardDigest) || !validDigest(cursor.CardDigest) || !validDigest(cursor.AttemptDigest) ||
		cursor.AttemptRevision < 1 || cursor.High < 1 || cursor.High > workboard.MaxCheckpointsPerAttempt || cursor.Before < 2 || cursor.Before > cursor.High+1 {
		return attemptDetailCursor{}, ErrWorkboardCursor
	}
	canonical, err := s.encodeAttemptDetailCursor(cursor)
	if err != nil || canonical != value {
		return attemptDetailCursor{}, ErrWorkboardCursor
	}
	return cursor, nil
}
