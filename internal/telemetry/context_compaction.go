package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// validateContextCompactionGate binds a one-shot, mid-task replacement to the
// exact running journal and the currently approved immutable draft. It is
// called only while Append holds SQLite's writer transaction, before the event
// is inserted, so a concurrent review change is ordered before or after it.
func validateContextCompactionGate(ctx context.Context, tx *sql.Tx, event runtime.Event) error {
	compaction := event.Data.Compaction
	if compaction == nil || compaction.SummaryAttemptID == "" || event.Data.ParentTaskID == "" || event.Data.ParentTaskID != compaction.SourceTaskID || event.Data.ReplacedMessages < 1 || len(event.Data.Messages) == 0 {
		return sessions.ErrHistory
	}
	if err := validateSummaryGate(ctx, tx, compaction); err != nil {
		return err
	}

	var attemptTask string
	var attemptBody []byte
	if err := tx.QueryRowContext(ctx, `SELECT task_id,body FROM summary_attempts WHERE id=?`, compaction.SummaryAttemptID).Scan(&attemptTask, &attemptBody); err != nil {
		return err
	}
	attempt, err := decodeSummaryAttempt(attemptBody, compaction.SummaryAttemptID, attemptTask)
	if err != nil || attempt.Status != "drafted" || attempt.Draft == nil {
		return sessions.ErrHistory
	}

	source, err := taskSnapshot(ctx, tx, compaction.SourceTaskID)
	if err != nil || source.State != "completed" || source.Sequence != compaction.SourceSequence {
		return sessions.ErrHistory
	}
	canonicalReplacement, canonicalCheckpoint, err := sessions.PrepareContinuation(source, attempt.Draft.Request)
	if err != nil {
		return sessions.ErrHistory
	}
	canonicalCheckpoint.SummaryAttemptID = compaction.SummaryAttemptID
	canonicalCheckpoint.SummaryReviewID = compaction.SummaryReviewID
	if !canonicalJSONEqual(canonicalCheckpoint, compaction) {
		return sessions.ErrHistory
	}

	var currentEvents []runtime.Event
	current, err := taskSnapshotWithEvents(ctx, tx, event.TaskID, &currentEvents)
	if err != nil || current.State != "running" || current.Sequence != event.Sequence-1 || current.SessionID != event.SessionID || current.ParentTaskID != compaction.SourceTaskID || current.Compaction != nil || len(currentEvents) == 0 || currentEvents[0].Kind != runtime.TaskStarted {
		return sessions.ErrHistory
	}
	if len(currentEvents)+1 >= sessions.MaxTaskEvents {
		return errors.Join(runtime.ErrJournalLimit, sessions.ErrEventTooLarge)
	}
	completedTurn := false
	for _, prior := range currentEvents[1:] {
		if prior.Kind == runtime.TurnCompleted {
			completedTurn = true
			break
		}
	}
	if !completedTurn {
		return sessions.ErrHistory
	}
	historyBytes := 0
	for _, prior := range currentEvents {
		body, encodeErr := prior.Encode()
		if encodeErr != nil {
			return sessions.ErrHistory
		}
		if len(body) > sessions.MaxContextCompactionActivationBytes-historyBytes {
			return errors.Join(runtime.ErrJournalLimit, sessions.ErrEventTooLarge)
		}
		historyBytes += len(body)
	}
	activationBody, err := event.Encode()
	if err != nil {
		return sessions.ErrHistory
	}
	if len(activationBody) > sessions.MaxContextCompactionActivationBytes-historyBytes {
		return errors.Join(runtime.ErrJournalLimit, sessions.ErrEventTooLarge)
	}
	initial := currentEvents[0].Data.Messages
	if len(initial) < len(source.Messages) || event.Data.ReplacedMessages != len(initial) || len(current.Messages) < len(initial) || !canonicalJSONEqual(initial[:len(source.Messages)], source.Messages) || !canonicalJSONEqual(current.Messages[:len(initial)], initial) {
		return sessions.ErrHistory
	}
	stableTail := initial[len(source.Messages):]
	wantReplacement := append([]providers.Message(nil), canonicalReplacement...)
	wantReplacement = append(wantReplacement, stableTail...)
	if !canonicalJSONEqual(event.Data.Messages, wantReplacement) {
		return sessions.ErrHistory
	}
	candidate := append([]providers.Message(nil), event.Data.Messages...)
	candidate = append(candidate, current.Messages[len(initial):]...)
	body, err := json.Marshal(candidate)
	if err != nil || len(body) > 4<<20 || providers.ValidateMessages(candidate) != nil {
		return sessions.ErrHistory
	}
	return nil
}

const postCompactionTerminalReserveBytes = 64 << 10

// validatePostCompactionJournalBudget ensures an activated task always remains
// replayable. Non-terminal work cannot consume the final recovery reserve;
// terminal facts may use it. The optional activation gate leaves the much
// larger output reserve above, while this invariant also protects against
// provider chunking and tool-event amplification.
func validatePostCompactionJournalBudget(ctx context.Context, tx *sql.Tx, event runtime.Event, body []byte) error {
	if event.Kind == runtime.ContextCompacted {
		return nil // The stricter activation budget was checked above.
	}
	var compacted bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM events WHERE task_id=? AND json_extract(body,'$.kind')=?
	)`, event.TaskID, runtime.ContextCompacted).Scan(&compacted); err != nil {
		return err
	}
	if !compacted {
		return nil
	}
	var count, used int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(body AS BLOB))),0) FROM events WHERE task_id=?`, event.TaskID).Scan(&count, &used); err != nil {
		return err
	}
	limit := int64(sessions.MaxEventPageBytes - postCompactionTerminalReserveBytes)
	eventLimit := int64(sessions.MaxTaskEvents - 1)
	if event.Kind == runtime.TaskCompleted || event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled {
		limit = sessions.MaxEventPageBytes
		eventLimit = sessions.MaxTaskEvents
	}
	if count < 1 || count >= eventLimit || used < 0 || int64(len(body)) > limit-used {
		return errors.Join(runtime.ErrJournalLimit, sessions.ErrEventTooLarge)
	}
	return nil
}

func canonicalJSONEqual(left, right any) bool {
	a, err := json.Marshal(left)
	if err != nil {
		return false
	}
	b, err := json.Marshal(right)
	return err == nil && bytes.Equal(a, b)
}
