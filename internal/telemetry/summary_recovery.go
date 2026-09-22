package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrSummaryRecovery = errors.New("summary recovery unavailable")

type summaryRecoveryCandidate struct {
	RowID, SourceSequence int64
	AttemptID, TaskID     string
	Body                  []byte
	Attempt               sessions.SummaryAttempt
	ProcessID             string
	Reference             processguard.Reference
}

func summaryProcessGate(ctx context.Context, tx *sql.Tx, processID string) error {
	if processID == "" || len(processID) > 128 {
		return ErrSummaryRecovery
	}
	current, err := processguard.Current(ctx)
	if err != nil || current.Validate() != nil || current.ID != processID {
		return ErrSummaryRecovery
	}
	want, err := json.Marshal(current)
	if err != nil || len(want) > 8192 {
		return ErrSummaryRecovery
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8192 THEN body END FROM lease_processes WHERE id=?`, processID).Scan(&stored); err != nil || !bytes.Equal(want, stored) {
		return ErrSummaryRecovery
	}
	return nil
}

// ReconcileSummaryAttemptsPage conservatively closes at most limit attempts
// whose exact guarded local owner is independently proven stopped. Held or
// unverifiable owners are skipped; provider work is never redispatched.
func (s *Store) ReconcileSummaryAttemptsPage(ctx context.Context, after string, limit int, now time.Time) (next string, recovered int, err error) {
	now = now.UTC()
	if ctx == nil || limit < 1 || limit > 100 || now.IsZero() || now.Year() < 1970 || now.Year() >= 2261 || len(after) > 19 {
		return "", 0, fmt.Errorf("%w: invalid page request", ErrSummaryRecovery)
	}
	var cursor int64
	if after != "" {
		cursor, err = strconv.ParseInt(after, 10, 64)
		if err != nil || cursor < 1 || strconv.FormatInt(cursor, 10) != after {
			return "", 0, ErrSummaryRecovery
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT a.rowid,a.id,a.task_id,a.body,a.process_id,p.body
		FROM summary_attempts a JOIN lease_processes p ON p.id=a.process_id
		WHERE a.rowid>? AND json_extract(a.body,'$.Status')='started'
		ORDER BY a.rowid LIMIT ?`, cursor, limit)
	if err != nil {
		return after, 0, fmt.Errorf("%w: list attempts: %w", ErrSummaryRecovery, err)
	}
	candidates := []summaryRecoveryCandidate{}
	for rows.Next() {
		var candidate summaryRecoveryCandidate
		var referenceBody []byte
		if rows.Scan(&candidate.RowID, &candidate.AttemptID, &candidate.TaskID, &candidate.Body, &candidate.ProcessID, &referenceBody) != nil || candidate.RowID <= cursor || len(candidate.Body) > 1<<20 || len(referenceBody) > 8192 {
			rows.Close()
			return after, 0, fmt.Errorf("%w: decode candidate row", ErrSummaryRecovery)
		}
		attempt, decodeErr := decodeSummaryAttempt(candidate.Body, candidate.AttemptID, candidate.TaskID)
		if decodeErr != nil || attempt.Status != "started" || json.Unmarshal(referenceBody, &candidate.Reference) != nil || candidate.Reference.Validate() != nil || candidate.Reference.ID != candidate.ProcessID {
			rows.Close()
			return after, 0, fmt.Errorf("%w: validate candidate", ErrSummaryRecovery)
		}
		candidate.Attempt, candidate.SourceSequence = attempt, attempt.SourceSequence
		candidates = append(candidates, candidate)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return after, 0, fmt.Errorf("%w: close candidate rows", ErrSummaryRecovery)
	}
	next = after
	for _, candidate := range candidates {
		next = strconv.FormatInt(candidate.RowID, 10)
		observation, probeErr := processguard.Probe(ctx, candidate.Reference)
		if probeErr != nil {
			if ctx.Err() != nil {
				return next, recovered, ctx.Err()
			}
			continue
		}
		if observation.State == processguard.Unlocked && observation.ConfirmUnlocked(ctx) == nil {
			changed, recoveryErr := s.recoverSummaryCandidate(ctx, candidate, observation, now)
			closeErr := observation.Close()
			if recoveryErr != nil || closeErr != nil {
				return next, recovered, fmt.Errorf("%w: recover candidate", ErrSummaryRecovery)
			}
			if changed {
				recovered++
			}
		} else if observation.Close() != nil {
			return next, recovered, fmt.Errorf("%w: close owner observation", ErrSummaryRecovery)
		}
	}
	if len(candidates) < limit {
		next = ""
	}
	return next, recovered, nil
}

func (s *Store) recoverSummaryCandidate(ctx context.Context, candidate summaryRecoveryCandidate, observation *processguard.Observation, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE summary_attempts SET body=body WHERE id=?`, candidate.AttemptID); err != nil || observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrSummaryRecovery
	}
	current, err := readSummaryRecoveryCandidate(ctx, tx, candidate.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		var receiptID string
		if receiptErr := tx.QueryRowContext(ctx, `SELECT id FROM summary_attempt_recoveries WHERE attempt_id=?`, candidate.AttemptID).Scan(&receiptID); receiptErr == nil {
			return false, tx.Commit()
		}
		return false, nil
	}
	if err != nil || !sameSummaryCandidate(candidate, current) || observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrSummaryRecovery
	}
	var derived int
	if err = tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM summary_reviews WHERE attempt_id=?)+
		(SELECT count(*) FROM summary_review_heads WHERE attempt_id=?)`, candidate.AttemptID, candidate.AttemptID).Scan(&derived); err != nil || derived != 0 {
		return false, ErrSummaryRecovery
	}
	terminal := current.Attempt
	terminal.Status, terminal.Code, terminal.FinishedAt = "interrupted", "owner_interrupted", now
	terminal.Draft, terminal.Usage, terminal.Elapsed = nil, nil, 0
	if terminal.Validate() != nil {
		return false, ErrSummaryRecovery
	}
	terminalBody, err := json.Marshal(terminal)
	if err != nil {
		return false, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE summary_attempts SET body=? WHERE id=? AND task_id=? AND process_id=? AND body=?`, terminalBody, current.AttemptID, current.TaskID, current.ProcessID, current.Body)
	if err != nil {
		return false, err
	}
	if n, rowsErr := updated.RowsAffected(); rowsErr != nil || n != 1 {
		return false, ErrSummaryRecovery
	}
	// The provider operation may have happened, but its terminal measurement is
	// unknowable after owner loss. Account it exactly once as failed/uncertain;
	// this is operational usage evidence, never summary quality or fitness.
	if err = appendSummaryUsage(ctx, tx, terminal); err != nil {
		return false, err
	}
	recovery := summaryRecoveryReceipt(current, now)
	recoveryBody, err := json.Marshal(recovery)
	if err != nil || recovery.Validate() != nil || len(recoveryBody) > 4096 {
		return false, ErrSummaryRecovery
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO summary_attempt_recoveries(id,attempt_id,task_id,recovered_at,body) VALUES(?,?,?,?,?)`, recovery.ID, recovery.AttemptID, recovery.TaskID, recovery.RecoveredAt.UnixNano(), recoveryBody); err != nil {
		return false, err
	}
	if observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrSummaryRecovery
	}
	var finalAttemptBody, finalRecoveryBody []byte
	if err = tx.QueryRowContext(ctx, `SELECT body FROM summary_attempts WHERE id=?`, current.AttemptID).Scan(&finalAttemptBody); err != nil || !bytes.Equal(finalAttemptBody, terminalBody) {
		return false, ErrSummaryRecovery
	}
	if err = tx.QueryRowContext(ctx, `SELECT body FROM summary_attempt_recoveries WHERE attempt_id=?`, current.AttemptID).Scan(&finalRecoveryBody); err != nil || !bytes.Equal(finalRecoveryBody, recoveryBody) {
		return false, ErrSummaryRecovery
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func readSummaryRecoveryCandidate(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, attemptID string) (summaryRecoveryCandidate, error) {
	var candidate summaryRecoveryCandidate
	var referenceBody []byte
	err := q.QueryRowContext(ctx, `SELECT a.rowid,a.id,a.task_id,a.body,a.process_id,p.body
		FROM summary_attempts a JOIN lease_processes p ON p.id=a.process_id
		WHERE a.id=? AND json_extract(a.body,'$.Status')='started'`, attemptID).Scan(&candidate.RowID, &candidate.AttemptID, &candidate.TaskID, &candidate.Body, &candidate.ProcessID, &referenceBody)
	if err != nil {
		return candidate, err
	}
	attempt, err := decodeSummaryAttempt(candidate.Body, candidate.AttemptID, candidate.TaskID)
	if err != nil || attempt.Status != "started" || json.Unmarshal(referenceBody, &candidate.Reference) != nil || candidate.Reference.Validate() != nil || candidate.Reference.ID != candidate.ProcessID {
		return summaryRecoveryCandidate{}, ErrSummaryRecovery
	}
	candidate.Attempt, candidate.SourceSequence = attempt, attempt.SourceSequence
	return candidate, nil
}

func sameSummaryCandidate(a, b summaryRecoveryCandidate) bool {
	return a.RowID == b.RowID && a.AttemptID == b.AttemptID && a.TaskID == b.TaskID && a.ProcessID == b.ProcessID && a.Reference == b.Reference && bytes.Equal(a.Body, b.Body)
}

func summaryRecoveryReceipt(candidate summaryRecoveryCandidate, now time.Time) sessions.SummaryRecovery {
	digest := sha256.Sum256([]byte("darwin-summary-interruption-v1\x00" + candidate.AttemptID + "\x00" + candidate.ProcessID + "\x00" + candidate.Attempt.SourceDigest))
	return sessions.SummaryRecovery{Version: 1, ID: hex.EncodeToString(digest[:]), AttemptID: candidate.AttemptID, TaskID: candidate.TaskID, SourceSequence: candidate.Attempt.SourceSequence, SourceDigest: candidate.Attempt.SourceDigest, State: "interrupted", Code: "owner_interrupted", RecoveredAt: now.UTC()}
}

func (s *Store) SummaryAttemptRecovery(ctx context.Context, attemptID string) (sessions.SummaryRecovery, error) {
	if !validSummaryRecoveryLabel(attemptID, false) {
		return sessions.SummaryRecovery{}, sessions.ErrHistory
	}
	var id, task string
	var recoveredAt int64
	var body []byte
	if err := s.db.QueryRowContext(ctx, `SELECT id,task_id,recovered_at,body FROM summary_attempt_recoveries WHERE attempt_id=?`, attemptID).Scan(&id, &task, &recoveredAt, &body); err != nil {
		return sessions.SummaryRecovery{}, err
	}
	return decodeSummaryRecovery(body, id, attemptID, task, recoveredAt)
}

func (s *Store) ListSummaryAttemptRecoveries(ctx context.Context, task, after string, limit int) ([]sessions.SummaryRecovery, error) {
	if !validSummaryRecoveryLabel(task, true) || !validSummaryRecoveryLabel(after, true) || limit < 1 || limit > 100 {
		return nil, sessions.ErrHistory
	}
	query := `SELECT id,attempt_id,task_id,recovered_at,body FROM summary_attempt_recoveries WHERE id>? ORDER BY id LIMIT ?`
	args := []any{after, limit}
	if task != "" {
		query = `SELECT id,attempt_id,task_id,recovered_at,body FROM summary_attempt_recoveries WHERE task_id=? AND id>? ORDER BY id LIMIT ?`
		args = []any{task, after, limit}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sessions.SummaryRecovery{}
	for rows.Next() {
		var id, attemptID, taskID string
		var recoveredAt int64
		var body []byte
		if rows.Scan(&id, &attemptID, &taskID, &recoveredAt, &body) != nil {
			return nil, ErrSummaryRecovery
		}
		recovery, decodeErr := decodeSummaryRecovery(body, id, attemptID, taskID, recoveredAt)
		if decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, recovery)
	}
	return out, rows.Err()
}

func decodeSummaryRecovery(body []byte, id, attemptID, taskID string, recoveredAt int64) (sessions.SummaryRecovery, error) {
	var recovery sessions.SummaryRecovery
	if len(body) > 4096 || json.Unmarshal(body, &recovery) != nil || recovery.Validate() != nil || recovery.ID != id || recovery.AttemptID != attemptID || recovery.TaskID != taskID || recovery.RecoveredAt.UnixNano() != recoveredAt {
		return sessions.SummaryRecovery{}, sessions.ErrHistory
	}
	return recovery, nil
}

func validSummaryRecoveryLabel(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return len(value) <= 128 && strings.TrimSpace(value) == value && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}
