package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// The explicit native agent protocol records measured usage per provider turn;
// its terminal carries no aggregate. Validate all tool boundaries before copying
// consumption so a mixed or forged lifecycle cannot double-count a task.
func harnessAgentUsage(ctx context.Context, tx *sql.Tx, start runtime.Event, provider, model string) (*providers.Usage, error) {
	if start.Data.ProviderID != provider || start.Data.ModelID != model {
		return nil, accounting.ErrUsage
	}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM events WHERE task_id=? ORDER BY sequence LIMIT ?", start.TaskID, runtime.MaxHarnessAgentEvents+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []runtime.Event
	bytesRead := 0
	for rows.Next() {
		var raw []byte
		var e runtime.Event
		if rows.Scan(&raw) != nil {
			return nil, accounting.ErrUsage
		}
		bytesRead += len(raw)
		if bytesRead > 32<<20 || json.Unmarshal(raw, &e) != nil {
			return nil, accounting.ErrUsage
		}
		events = append(events, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	usage, err := runtime.ValidateHarnessAgentJournal(events, start.TaskID)
	if err != nil {
		return nil, accounting.ErrUsage
	}
	return usage, nil
}
