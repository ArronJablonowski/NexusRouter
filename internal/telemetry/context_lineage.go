package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type contextLineageEvent struct {
	lineage  *runtime.ContextLineage
	mode     string
	previous any
	body     []byte
}

// prepareContextLineageEvent derives lineage from durable parent/current state.
// It is called with the same writer transaction that commits the runtime event.
func prepareContextLineageEvent(ctx context.Context, tx *sql.Tx, event runtime.Event) (*contextLineageEvent, error) {
	if event.Kind != runtime.TaskStarted && event.Kind != runtime.ContextCompacted {
		if event.Data.ContextLineage != nil {
			return nil, sessions.ErrHistory
		}
		return nil, nil
	}
	lineage := event.Data.ContextLineage
	if lineage != nil && lineage.Validate() != nil {
		return nil, sessions.ErrHistory
	}
	if lineage == nil && event.Data.Compaction != nil && event.Data.Compaction.Version == 1 {
		var hasLineage bool
		if event.Kind == runtime.TaskStarted {
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM context_lineage_events WHERE task_id=?)", event.Data.ParentTaskID).Scan(&hasLineage); err != nil {
				return nil, err
			}
		} else {
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM context_lineage_events WHERE task_id=?)", event.TaskID).Scan(&hasLineage); err != nil {
				return nil, err
			}
		}
		// Schema-50 checkpoints did not carry lineage. Preserve that path for an
		// external source or durable lineage-free predecessor, but never let it
		// erase a lineage already established by schema 51.
		if !hasLineage {
			return nil, nil
		}
	}
	var inherited *runtime.ContextLineage
	if event.Kind == runtime.TaskStarted {
		if event.Data.ParentTaskID == "" {
			if lineage != nil {
				return nil, sessions.ErrHistory
			}
			return nil, nil
		}
		if event.Data.Compaction == nil {
			var parentState string
			var hasLineage bool
			err := tx.QueryRowContext(ctx, `SELECT state,EXISTS(
				SELECT 1 FROM context_lineage_events WHERE task_id=task_heads.task_id
			) FROM task_heads WHERE task_id=?`, event.Data.ParentTaskID).Scan(&parentState, &hasLineage)
			if errors.Is(err, sql.ErrNoRows) && lineage == nil {
				// Historical starts may name an external, non-journal parent.
				return nil, nil
			}
			if err != nil {
				return nil, sessions.ErrHistory
			}
			// A running parent denotes live delegation, not continuation. Its child
			// starts an independent context and therefore does not inherit lineage.
			if parentState == "running" && lineage == nil {
				return nil, nil
			}
			if !hasLineage {
				if lineage == nil {
					return nil, nil
				}
				return nil, sessions.ErrHistory
			}
		}
		var parentEvents []runtime.Event
		parent, err := taskSnapshotWithEvents(ctx, tx, event.Data.ParentTaskID, &parentEvents)
		if err != nil {
			return nil, sessions.ErrHistory
		}
		inherited = parent.ContextLineage
		if lineage != nil && (parent.SessionID != event.SessionID || parent.Privacy != event.Data.Privacy) {
			return nil, sessions.ErrHistory
		}
		if event.Data.Compaction == nil {
			if !reflect.DeepEqual(lineage, inherited) {
				return nil, sessions.ErrHistory
			}
			if lineage == nil {
				return nil, nil
			}
			if !contextLineageContinuationEligible(parent, parentEvents) {
				return nil, sessions.ErrHistory
			}
			return sealContextLineageEvent(lineage, "inherited", lineage.Digest)
		}
		if parent.State != "completed" {
			return nil, sessions.ErrHistory
		}
		if err = validateContextCompactionSourceState(event.Data.Compaction, parent); err != nil {
			return nil, err
		}
		expected, err := runtime.ExtendContextLineage(inherited, event.TaskID, event.Sequence, event.Data.Compaction, parent.Messages)
		if lineage == nil {
			// Version-one events written before lineage support remain valid.
			if event.Data.Compaction.Version == 1 && inherited == nil {
				return nil, nil
			}
			return nil, sessions.ErrHistory
		}
		if err != nil || !reflect.DeepEqual(lineage, expected) {
			return nil, sessions.ErrHistory
		}
		return sealContextLineageEvent(lineage, "activated", contextLineagePreviousDigest(inherited))
	}

	current, err := taskSnapshot(ctx, tx, event.TaskID)
	if err != nil || current.State != "running" || current.Sequence != event.Sequence-1 {
		return nil, sessions.ErrHistory
	}
	inherited = current.ContextLineage
	if event.Data.Compaction == nil || event.Data.ParentTaskID == "" {
		return nil, sessions.ErrHistory
	}
	source, err := taskSnapshot(ctx, tx, event.Data.Compaction.SourceTaskID)
	if err != nil || source.State != "completed" {
		return nil, sessions.ErrHistory
	}
	if err = validateContextCompactionSourceState(event.Data.Compaction, source); err != nil {
		return nil, err
	}
	expected, err := runtime.ExtendContextLineage(inherited, event.TaskID, event.Sequence, event.Data.Compaction, current.Messages)
	if lineage == nil {
		if event.Data.Compaction.Version == 1 && inherited == nil {
			return nil, nil
		}
		return nil, sessions.ErrHistory
	}
	if err != nil || !reflect.DeepEqual(lineage, expected) {
		return nil, sessions.ErrHistory
	}
	return sealContextLineageEvent(lineage, "activated", contextLineagePreviousDigest(inherited))
}

func contextLineageContinuationEligible(snapshot sessions.Snapshot, events []runtime.Event) bool {
	if snapshot.State == "completed" {
		return true
	}
	tail := events
	if len(tail) > 2 {
		tail = tail[len(tail)-2:]
	}
	if sessions.AssessContinuation(snapshot, tail).HistoryEligible {
		return true
	}
	if snapshot.State != "failed" || len(snapshot.Pending) != 0 || snapshot.UncertainEffects || len(events) < 2 {
		return false
	}
	terminal := events[len(events)-1]
	if terminal.Kind != runtime.TaskFailed || terminal.Data.Code != "interrupted_model" {
		return false
	}
	plan, err := sessions.PlanInterruptedModel([][]runtime.Event{events[:len(events)-1]}, terminal.Time, false)
	return err == nil && len(plan.Events) == 1 && reflect.DeepEqual(plan.Events[0], terminal)
}

func validateContextCompactionSourceState(compaction *runtime.ContextCompaction, source sessions.Snapshot) error {
	if compaction == nil || compaction.SourceTaskID != source.TaskID || compaction.SourceSequence != source.Sequence {
		return errors.Join(sessions.ErrHistory, errors.New("context lineage source identity mismatch"))
	}
	if compaction.Version == 1 {
		if compaction.SourceStateDigest != "" || source.ContextLineage != nil {
			return errors.Join(sessions.ErrHistory, errors.New("legacy context lineage source mismatch"))
		}
		return nil
	}
	digest, err := runtime.ContextSourceStateDigest(source.Messages, source.ContextLineage)
	if err != nil || digest != compaction.SourceStateDigest {
		return errors.Join(sessions.ErrHistory, errors.New("context lineage source-state digest mismatch"))
	}
	toolIDs, err := runtime.ContextSourceToolCallIDs(source.Messages, source.ContextLineage)
	if err != nil || !slices.Equal(toolIDs, compaction.SourceToolCallIDs) {
		return errors.Join(sessions.ErrHistory, errors.New("context lineage source tool identities mismatch"))
	}
	return nil
}

func contextLineagePreviousDigest(lineage *runtime.ContextLineage) any {
	if lineage == nil {
		return nil
	}
	return lineage.Digest
}

func sealContextLineageEvent(lineage *runtime.ContextLineage, mode string, previous any) (*contextLineageEvent, error) {
	body, err := json.Marshal(lineage)
	if err != nil || len(body) == 0 || len(body) > runtime.MaxContextCompactionPlanBytes {
		return nil, sessions.ErrHistory
	}
	return &contextLineageEvent{lineage: lineage, mode: mode, previous: previous, body: body}, nil
}

func insertContextLineageEvent(ctx context.Context, tx *sql.Tx, event runtime.Event, record *contextLineageEvent) error {
	if record == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO context_lineage_events
		(event_id,task_id,event_sequence,event_kind,mode,lineage_digest,previous_digest,epoch_count,body)
		VALUES(?,?,?,?,?,?,?,?,?)`, event.ID, event.TaskID, event.Sequence, string(event.Kind), record.mode,
		record.lineage.Digest, record.previous, len(record.lineage.Epochs), record.body)
	return err
}

func validateContextLineageEventRetry(ctx context.Context, tx *sql.Tx, event runtime.Event) error {
	var body []byte
	err := tx.QueryRowContext(ctx, "SELECT body FROM context_lineage_events WHERE event_id=?", event.ID).Scan(&body)
	if event.Data.ContextLineage == nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err == nil {
			return ErrConflict
		}
		return err
	}
	if err != nil {
		return err
	}
	want, err := json.Marshal(event.Data.ContextLineage)
	if err != nil || !reflect.DeepEqual(body, want) {
		return ErrConflict
	}
	return nil
}
