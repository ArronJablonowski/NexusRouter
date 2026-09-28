package usagestats

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// measuredTurns exposes a lower bound, never a replacement accounting record.
// A missing turn measurement must not erase measured turns from the odometer.
// Call only for an uncorrected routed record whose complete usage is unknown.
func measuredTurns(ctx context.Context, tx *sql.Tx, r accounting.Record) (*providers.Usage, error) {
	if r.EvidenceKind != accounting.EventEvidence || (r.Role != accounting.PrimaryExecution && r.Role != accounting.Fallback) {
		return nil, nil
	}
	if r.Validate() != nil || r.OperationID != r.TaskID {
		return nil, accounting.ErrUsage
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,sequence,body FROM events WHERE task_id=? AND json_extract(body,'$.kind') IN ('task.started','turn.started','turn.completed','task.completed','task.failed','task.canceled') ORDER BY sequence`, r.TaskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out providers.Usage
	seen, active := map[string]bool{}, map[string]bool{}
	started, terminal, measured := false, false, false
	var prior int64
	for rows.Next() {
		var id string
		var sequence int64
		var raw []byte
		var e runtime.Event
		if rows.Scan(&id, &sequence, &raw) != nil || json.Unmarshal(raw, &e) != nil || e.Validate() != nil || e.ID != id || e.Sequence != sequence || sequence <= prior || sequence > sessions.MaxTaskEvents || e.TaskID != r.TaskID || e.SessionID != r.SessionID || e.CorrelationID != r.TaskID || terminal {
			return nil, accounting.ErrUsage
		}
		prior = sequence
		if e.Kind == runtime.TaskStarted {
			if started || sequence != 1 || e.Data.ProviderID != r.Provider || e.Data.ModelID != r.Model {
				return nil, accounting.ErrUsage
			}
			started = true
			continue
		}
		if !started {
			return nil, accounting.ErrUsage
		}
		key := e.TurnID + "\x00" + e.AttemptID
		switch e.Kind {
		case runtime.TurnStarted:
			if seen[key] || e.Data.ProviderID != r.Provider || e.Data.ModelID != r.Model {
				return nil, accounting.ErrUsage
			}
			seen[key] = true
			active[key] = true
		case runtime.TurnCompleted:
			if !active[key] || (e.Data.ProviderID != "" && e.Data.ProviderID != r.Provider) || (e.Data.ModelID != "" && e.Data.ModelID != r.Model) {
				return nil, accounting.ErrUsage
			}
			delete(active, key)
			if u := e.Data.Usage; u != nil {
				if u.InputTokens > math.MaxInt64-out.InputTokens || u.OutputTokens > math.MaxInt64-out.OutputTokens {
					return nil, accounting.ErrUsage
				}
				out.InputTokens += u.InputTokens
				out.OutputTokens += u.OutputTokens
				measured = true
			}
		default:
			disposition := map[runtime.Kind]accounting.Disposition{runtime.TaskCompleted: accounting.Completed, runtime.TaskFailed: accounting.Failed, runtime.TaskCanceled: accounting.Canceled}[e.Kind]
			if e.ID != r.EvidenceID || disposition != r.Disposition || !e.Time.Equal(r.OccurredAt) {
				return nil, accounting.ErrUsage
			}
			terminal = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if !started || !terminal {
		return nil, accounting.ErrUsage
	}
	if !measured {
		return nil, nil
	}
	return &out, nil
}
