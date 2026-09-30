package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type candidateRuntimeSource struct {
	completion       runtime.Event
	completionDigest string
	terminal         runtime.Event
	terminalDigest   string
	outputDigest     string
	output           string
	domain           string
	profile          string
	privacy          string
}

// candidateSourceCompletion derives the immutable final model result from a
// fully validated runtime journal. The task terminal must name the same turn
// and attempt; its optional envelope fields are not accepted as authority on
// their own.
func candidateSourceCompletion(ctx context.Context, tx *sql.Tx, taskID, sessionID string) (candidateRuntimeSource, error) {
	events := []runtime.Event{}
	snapshot, err := taskSnapshotWithEvents(ctx, tx, taskID, &events)
	if err != nil || snapshot.State != "completed" || snapshot.SessionID != sessionID || len(events) < 3 || events[len(events)-1].Kind != runtime.TaskCompleted {
		return candidateRuntimeSource{}, ErrWorkboardCorrupt
	}
	for i := len(events) - 2; i > 0; i-- {
		if events[i].Kind == runtime.TurnCompleted {
			terminal := events[len(events)-1]
			if events[i].TurnID == "" || events[i].AttemptID == "" || terminal.TurnID != events[i].TurnID ||
				terminal.AttemptID != events[i].AttemptID {
				return candidateRuntimeSource{}, ErrWorkboardCorrupt
			}
			return candidateRuntimeSource{completion: events[i], completionDigest: streamBodyDigestMust(events[i]),
				terminal: terminal, terminalDigest: streamBodyDigestMust(terminal),
				output: events[i].Data.Text, outputDigest: digestBytes([]byte(events[i].Data.Text)), domain: events[0].Data.Domain,
				profile: events[0].Data.Profile, privacy: events[0].Data.Privacy}, nil
		}
	}
	return candidateRuntimeSource{}, ErrWorkboardCorrupt
}
