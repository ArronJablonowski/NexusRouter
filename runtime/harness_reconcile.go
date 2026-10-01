package runtime

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

type HarnessJournalReader interface {
	Read(context.Context, string, int64, int) ([]Event, error)
}

// RecordHarnessOutcome replays a verified native text or native-tools-v1 task protocol
// into the separate outcome ledger. Reader must be the authenticated canonical
// runtime store, not a caller-supplied JSON source. A running/failed/canceled task,
// partial log, mismatched start, or altered output never produces an execution
// record. Exact retries use the ledger's immutable identity semantics. This
// operation never executes a harness or creates quality feedback.
func RecordHarnessOutcome(ctx context.Context, reader HarnessJournalReader, ledger *harness.EvidenceStore, taskID string, now time.Time) (harness.Execution, error) {
	if ctx == nil || reader == nil || ledger == nil || taskID == "" {
		return harness.Execution{}, ErrInvalidRun
	}
	events, err := ReadHarnessJournal(ctx, reader, taskID)
	if err != nil {
		return harness.Execution{}, err
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
	if len(events) > 0 && events[0].Data.Harness != nil && events[0].Data.Harness.Protocol == HarnessAgentProtocol {
		if _, err := ValidateHarnessAgentJournal(events, taskID); err != nil {
			return harness.Execution{}, err
		}
		last := events[len(events)-1]
		if last.Kind != TaskCompleted || last.Data.HarnessOutcome == nil {
			return harness.Execution{}, ErrProtocol
		}
		return *last.Data.HarnessOutcome, nil
	}
	if len(events) != 2 {
		return harness.Execution{}, ErrProtocol
	}
	first, last := events[0], events[1]
	if first.Validate() != nil || last.Validate() != nil || first.Kind != TaskStarted || last.Kind != TaskCompleted || first.Sequence != 1 || last.Sequence != 2 || first.ID == last.ID || first.TaskID != taskID || last.TaskID != taskID || first.SessionID != last.SessionID || first.CorrelationID != taskID || last.CorrelationID != taskID || last.Time.Before(first.Time) || first.Data.Harness == nil || first.Data.Harness.Protocol != "" || last.Data.HarnessOutcome == nil {
		return harness.Execution{}, ErrProtocol
	}
	outcome := *last.Data.HarnessOutcome
	if outcome.Actual != first.Data.Harness.Identity || outcome.Task != first.Data.Harness.Task {
		return harness.Execution{}, ErrProtocol
	}
	return outcome, nil
}

// ReadHarnessJournal bounds both event count and encoded content. Single-event
// pages respect the canonical store's byte cap even when adjacent events are
// large. The caller should retain one read-only store for the whole operation.
func ReadHarnessJournal(ctx context.Context, reader HarnessJournalReader, task string) ([]Event, error) {
	if ctx == nil || reader == nil || task == "" {
		return nil, ErrInvalidRun
	}
	var events []Event
	after := int64(0)
	total := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		page, err := reader.Read(ctx, task, after, 1)
		if err != nil {
			return nil, ErrPersistence
		}
		if len(page) == 0 {
			return events, nil
		}
		if len(page) != 1 {
			return nil, ErrProtocol
		}
		e := page[0]
		if e.TaskID != task || e.Sequence != after+1 {
			return nil, ErrProtocol
		}
		body, err := e.Encode()
		if err != nil || len(body) > 32<<20-total {
			return nil, ErrProtocol
		}
		total += len(body)
		after = e.Sequence
		events = append(events, e)
		if len(events) > MaxHarnessAgentEvents {
			return nil, ErrProtocol
		}
		if len(events) > 2 && (events[0].Data.Harness == nil || events[0].Data.Harness.Protocol != HarnessAgentProtocol) {
			return events, nil
		}
	}
}
