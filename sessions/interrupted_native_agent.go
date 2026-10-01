package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// PlanInterruptedNativeAgent terminalizes a versioned native agent without
// continuing a model turn, replaying a tool, resolving an uncertain effect, or
// releasing any resource authority. The host must first fence the expired owner.
// Pending and completed effects remain exactly as originally recorded.
func PlanInterruptedNativeAgent(histories [][]runtime.Event, now time.Time, canceled bool) (InterruptionRecovery, error) {
	bad := func() (InterruptionRecovery, error) { return InterruptionRecovery{}, ErrHistory }
	if len(histories) != 1 || len(histories[0]) < 1 || len(histories[0]) >= runtime.MaxHarnessAgentEvents || now.IsZero() || now.Year() < 1970 || now.Year() >= 2261 {
		return bad()
	}
	history := histories[0]
	start, last := history[0], history[len(history)-1]
	if start.Data.Harness == nil || start.Data.Harness.Protocol != runtime.HarnessAgentProtocol || start.Data.ParentTaskID != "" || start.Data.RetryOfTaskID != "" || !ValidEventPageID(start.TaskID) || !ValidEventPageID(start.SessionID) {
		return bad()
	}
	budget := 8 << 20
	for _, e := range history {
		body, err := e.Encode()
		if err != nil || len(body) > budget || e.Time.After(now) || e.Time.Year() < 1970 || e.Time.Year() >= 2261 || !ValidEventPageID(e.ID) || e.WorkerID != "" || e.Data.DelegationOrigin != nil || e.Data.ParentTaskID != "" || e.Data.RetryOfTaskID != "" {
			return bad()
		}
		budget -= len(body)
	}
	snapshot, err := Replay(context.Background(), terminalReader(history), start.TaskID)
	if err != nil || snapshot.State != "running" {
		return bad()
	}
	kind, code := runtime.TaskFailed, "harness_interrupted"
	if canceled {
		kind, code = runtime.TaskCanceled, "harness_canceled"
	}
	sequence := last.Sequence + 1
	sum := sha256.Sum256([]byte("native-agent-interruption-v1\x00" + start.TaskID + "\x00" + last.ID + "\x00" + strconv.FormatInt(sequence, 10) + "\x00" + string(kind) + "\x00" + now.UTC().Format(time.RFC3339Nano)))
	terminal := runtime.Event{Version: 1, ID: hex.EncodeToString(sum[:]), TaskID: start.TaskID, SessionID: start.SessionID, CorrelationID: start.TaskID, Sequence: sequence, Time: now.UTC(), Kind: kind, CausationID: last.ID, Data: runtime.Data{Code: code}}
	body, err := terminal.Encode()
	if err != nil || len(body) > budget {
		return bad()
	}
	full := append(append([]runtime.Event(nil), history...), terminal)
	if _, err := runtime.ValidateHarnessAgentJournal(full, start.TaskID); err != nil {
		return bad()
	}
	after, err := Replay(context.Background(), terminalReader(full), start.TaskID)
	if err != nil || after.UncertainEffects != snapshot.UncertainEffects || after.InterruptedTurn != snapshot.InterruptedTurn || len(after.Pending) != len(snapshot.Pending) {
		return bad()
	}
	return InterruptionRecovery{ParentTaskID: start.TaskID, ExpectedSequence: last.Sequence, Events: []runtime.Event{terminal}}, nil
}
