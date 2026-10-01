package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// Harness measurement is already the adapter's operation aggregate. Never add
// it to ordinary turn events: mixed lifecycles could double count consumption.
func harnessTerminalUsage(ctx context.Context, tx *sql.Tx, start runtime.Event, provider, model string) (*providers.Usage, error) {
	if start.Kind != runtime.TaskStarted || start.Sequence != 1 || start.Data.ProviderID != provider || start.Data.ModelID != model {
		return nil, accounting.ErrUsage
	}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence>1 ORDER BY sequence", start.TaskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var terminal runtime.Event
	count := 0
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &terminal) != nil || terminal.Validate() != nil {
			return nil, accounting.ErrUsage
		}
		count++
		if count > 1 {
			return nil, accounting.ErrUsage
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count != 1 || terminal.Sequence != 2 || terminal.TaskID != start.TaskID || terminal.SessionID != start.SessionID || terminal.CorrelationID != start.CorrelationID || terminal.TurnID != "" || terminal.AttemptID != "" {
		return nil, accounting.ErrUsage
	}
	switch terminal.Kind {
	case runtime.TaskCompleted:
		o := terminal.Data.HarnessOutcome
		if o == nil || o.Actual != start.Data.Harness.Identity || o.Task != start.Data.Harness.Task {
			return nil, accounting.ErrUsage
		}
	case runtime.TaskFailed, runtime.TaskCanceled:
		if terminal.Data.HarnessOutcome != nil || terminal.Data.Text != "" {
			return nil, accounting.ErrUsage
		}
	default:
		return nil, accounting.ErrUsage
	}
	usage := terminal.Data.Usage
	if usage == nil {
		return nil, nil
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > 1<<40 || usage.OutputTokens > 1<<40 {
		return nil, accounting.ErrUsage
	}
	copy := *usage
	return &copy, nil
}
