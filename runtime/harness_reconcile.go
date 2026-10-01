package runtime

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

type HarnessJournalReader interface {
	Read(context.Context, string, int64, int) ([]Event, error)
}

// RecordHarnessOutcome replays this adapter's committed two-event task protocol
// into the separate outcome ledger. Reader must be the authenticated canonical
// runtime store, not a caller-supplied JSON source. A running/failed/canceled task,
// partial log, mismatched start, or altered output never produces an execution
// record. Exact retries use the ledger's immutable identity semantics. This
// operation never executes a harness or creates quality feedback.
func RecordHarnessOutcome(ctx context.Context, reader HarnessJournalReader, ledger *harness.EvidenceStore, taskID string, now time.Time) (harness.Execution, error) {
	if ctx == nil || reader == nil || ledger == nil || taskID == "" {
		return harness.Execution{}, ErrInvalidRun
	}
	events, err := reader.Read(ctx, taskID, 0, 3)
	if err != nil {
		return harness.Execution{}, ErrPersistence
	}
	outcome, err := ValidateHarnessOutcome(events, taskID)
	if err != nil {
		return harness.Execution{}, err
	}
	if err := ledger.AppendExecution(ctx, outcome, now); err != nil {
		return harness.Execution{}, err
	}
	return outcome, nil
}

// ValidateHarnessOutcome verifies the canonical completed native task protocol.
// Callers must obtain events from their trusted journal, not imported JSON.
func ValidateHarnessOutcome(events []Event, taskID string) (harness.Execution, error) {
	if len(events) != 2 {
		return harness.Execution{}, ErrProtocol
	}
	first, last := events[0], events[1]
	if first.Validate() != nil || last.Validate() != nil || first.Kind != TaskStarted || last.Kind != TaskCompleted || first.Sequence != 1 || last.Sequence != 2 || first.ID == last.ID || first.TaskID != taskID || last.TaskID != taskID || first.SessionID != last.SessionID || first.CorrelationID != taskID || last.CorrelationID != taskID || last.Time.Before(first.Time) || first.Data.Harness == nil || last.Data.HarnessOutcome == nil {
		return harness.Execution{}, ErrProtocol
	}
	outcome := *last.Data.HarnessOutcome
	if outcome.Actual != first.Data.Harness.Identity || outcome.Task != first.Data.Harness.Task {
		return harness.Execution{}, ErrProtocol
	}
	return outcome, nil
}
