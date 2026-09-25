package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/diagnostics"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// DiagnosticEvents reads a bounded, read-only projection of the durable journal.
// It authenticates each selected ledger row and its origin, without rescanning
// the entire historical log on each monitoring poll. It is diagnostic output,
// never a replacement for the strict replay or evidence-validation APIs.
func (s *Store) DiagnosticEvents(ctx context.Context, options diagnostics.Options) (diagnostics.Page, error) {
	zero := diagnostics.Page{}
	if s == nil || ctx == nil || options.Validate() != nil {
		return zero, diagnostics.ErrDiagnostic
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, diagnostics.ErrDiagnostic
	}
	defer tx.Rollback()
	var high int64
	if tx.QueryRowContext(ctx, `SELECT COALESCE(max(position),0) FROM event_log`).Scan(&high) != nil || options.After > high {
		return zero, diagnostics.ErrDiagnostic
	}
	if options.After > 0 {
		var exists bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM event_log WHERE position=?)`, options.After).Scan(&exists) != nil || !exists {
			return zero, diagnostics.ErrDiagnostic
		}
	}
	query := `SELECT l.position,l.event_id FROM event_log l WHERE l.position>? AND l.position<=?`
	args := []any{options.After, high}
	if options.TaskID != "" {
		query += ` AND l.task_id=?`
		args = append(args, options.TaskID)
	}
	query += ` ORDER BY l.position LIMIT ?`
	// Scan at most 1,000 raw ledger rows, independently of the output count.
	// Filtering only after authentication prevents a missing/tampered source
	// event from being silently skipped by an SQL join or JSON-kind predicate.
	args = append(args, 1001)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return zero, diagnostics.ErrDiagnostic
	}
	type entry struct {
		position int64
		id       string
	}
	entries := []entry{}
	for rows.Next() {
		var item entry
		if rows.Scan(&item.position, &item.id) != nil || item.position <= options.After || item.position > high || len(item.id) > 128 {
			rows.Close()
			return zero, diagnostics.ErrDiagnostic
		}
		if options.TaskID == "" && item.position != options.After+int64(len(entries))+1 {
			rows.Close()
			return zero, diagnostics.ErrDiagnostic
		}
		entries = append(entries, item)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return zero, diagnostics.ErrDiagnostic
	}
	out := diagnostics.Page{Records: []diagnostics.Record{}, NextAfter: options.After}
	tasks := map[string]runtime.Event{}
	total := 0
	for index, item := range entries {
		if len(out.Records) == options.Limit || index == 1000 {
			out.HasMore = true
			break
		}
		_, body, readErr := readTaskEventLogByPosition(ctx, tx, item.position, item.id)
		var event runtime.Event
		if readErr != nil || json.Unmarshal(body, &event) != nil {
			return zero, diagnostics.ErrDiagnostic
		}
		if event.Kind == runtime.ModelDelta || event.Kind == runtime.WorkerHeartbeat {
			out.NextAfter = item.position
			continue
		}
		start, exists := tasks[event.TaskID]
		if !exists {
			origin, originErr := diagnosticOrigin(ctx, tx, event.TaskID, runtime.TaskStarted, event.Sequence, true)
			if originErr != nil || origin == nil {
				return zero, diagnostics.ErrDiagnostic
			}
			start, tasks[event.TaskID] = *origin, *origin
		}
		turn, originErr := diagnosticOrigin(ctx, tx, event.TaskID, runtime.TurnStarted, event.Sequence, false)
		if originErr != nil {
			return zero, diagnostics.ErrDiagnostic
		}
		record, projectErr := diagnostics.Project(event, item.position, diagnostics.Origins{Task: start, Turn: turn}, options.IncludeContent)
		encoded, encodeErr := json.Marshal(record)
		if projectErr != nil || encodeErr != nil || len(encoded)+1 > diagnostics.MaxPageBytes {
			return zero, diagnostics.ErrDiagnostic
		}
		if total+len(encoded)+1 > diagnostics.MaxPageBytes {
			out.HasMore = true
			break
		}
		total += len(encoded) + 1
		out.Records = append(out.Records, record)
		out.NextAfter = item.position
	}
	if !out.HasMore {
		out.NextAfter = high
	}
	if tx.Commit() != nil {
		return zero, diagnostics.ErrDiagnostic
	}
	return out, nil
}

// DiagnosticHead returns the current committed position so an interactive
// follower can start with new activity rather than replaying the entire store.
func (s *Store) DiagnosticHead(ctx context.Context) (int64, error) {
	if s == nil || ctx == nil {
		return 0, diagnostics.ErrDiagnostic
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var position int64
	if s.db.QueryRowContext(ctx, `SELECT COALESCE(max(position),0) FROM event_log`).Scan(&position) != nil || position < 0 {
		return 0, diagnostics.ErrDiagnostic
	}
	return position, nil
}

func diagnosticOrigin(ctx context.Context, tx *sql.Tx, task string, kind runtime.Kind, sequence int64, first bool) (*runtime.Event, error) {
	order := "DESC"
	if first {
		order = "ASC"
	}
	var position int64
	var id string
	err := tx.QueryRowContext(ctx, `SELECT l.position,l.event_id FROM events e INDEXED BY events_task_kind JOIN event_log l ON l.event_id=e.id
	 WHERE e.task_id=? AND json_extract(e.body,'$.kind')=? AND e.sequence<=? ORDER BY e.sequence `+order+` LIMIT 1`, task, kind, sequence).Scan(&position, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, diagnostics.ErrDiagnostic
	}
	_, body, err := readEventLogByPosition(ctx, tx, position, id)
	var event runtime.Event
	if err != nil || json.Unmarshal(body, &event) != nil {
		return nil, diagnostics.ErrDiagnostic
	}
	// Retain only the verified metadata needed for projection. A page with many
	// tasks must not cache their potentially large input messages.
	d := event.Data
	event.Data = runtime.Data{ModelID: d.ModelID, ProviderID: d.ProviderID, Domain: d.Domain, Profile: d.Profile, ContextTokens: d.ContextTokens}
	return &event, nil
}
