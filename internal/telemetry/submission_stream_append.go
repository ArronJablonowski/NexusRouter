package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func streamBodyDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func appendSubmissionStreamEvent(ctx context.Context, tx *sql.Tx, event runtime.Event, body []byte, submission string) error {
	if submission == "" {
		return nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(sequence),0)+1 FROM submission_stream_events WHERE submission_id=?`, submission).Scan(&sequence); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO submission_stream_events(submission_id,sequence,event_id,task_id,task_sequence,body_digest) VALUES(?,?,?,?,?,?)`, submission, sequence, event.ID, event.TaskID, event.Sequence, streamBodyDigest(body))
	return err
}

func validateSubmissionStreamRetry(ctx context.Context, tx *sql.Tx, event runtime.Event, body []byte, submission string) error {
	var storedSubmission, task, digest string
	var streamSequence, taskSequence int64
	err := tx.QueryRowContext(ctx, `SELECT submission_id,sequence,task_id,task_sequence,body_digest FROM submission_stream_events WHERE event_id=?`, event.ID).Scan(&storedSubmission, &streamSequence, &task, &taskSequence, &digest)
	if submission == "" {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return ErrConflict
	}
	if err != nil || storedSubmission != submission || streamSequence < 1 || task != event.TaskID || taskSequence != event.Sequence || digest != streamBodyDigest(body) {
		return ErrConflict
	}
	return nil
}

func taskSubmissionID(ctx context.Context, tx *sql.Tx, task string) (string, error) {
	var submission string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(body,'$.data.submission_id'),'') FROM events WHERE task_id=? AND sequence=1`, task).Scan(&submission)
	return submission, err
}
