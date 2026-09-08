package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// PlanInterruptedWorkerWithoutChild plans failure only for a supervisor that
// has not recorded execution or acceptance, including the gap between lease
// acquisition and WorkerStarted persistence. A history slice cannot prove that
// an execution child is absent or that its owner died: the caller must establish
// and retain both observations transactionally through the failure commit.
func PlanInterruptedWorkerWithoutChild(history []runtime.Event, now time.Time) (InterruptionRecovery, error) {
	bad := func() (InterruptionRecovery, error) { return InterruptionRecovery{}, ErrHistory }
	now = now.UTC()
	if len(history) < 1 || len(history) > 9999 || now.Year() < 1970 || now.Year() >= 2261 {
		return bad()
	}
	start := history[0]
	if start.Kind != runtime.TaskStarted || (len(history) > 1 && history[1].Kind != runtime.WorkerStarted) || !ValidEventPageID(start.TaskID) || !ValidEventPageID(start.SessionID) || !ValidEventPageID(start.WorkerID) || !ValidEventPageID(start.Data.ParentTaskID) || start.Data.ParentTaskID == start.TaskID || (start.Data.SubmissionID != "" && !ValidEventPageID(start.Data.SubmissionID)) || start.Data.DelegationOrigin == nil || start.Data.DelegationOrigin.Validate() != nil {
		return bad()
	}
	budget := 8 << 20
	for i, event := range history {
		body, err := event.Encode()
		if err != nil || len(body) > budget || !ValidEventPageID(event.ID) || event.TaskID != start.TaskID || event.SessionID != start.SessionID || event.WorkerID != start.WorkerID || event.CorrelationID != start.TaskID || event.Sequence != int64(i+1) || event.Time.After(now) || event.Time.UTC().Year() < 1970 || event.Time.UTC().Year() >= 2261 || (i > 0 && event.Time.Before(history[i-1].Time)) || event.TurnID != "" || event.AttemptID != "" || event.RouteID != "" || event.CausationID != "" {
			return bad()
		}
		budget -= len(body)
		allowed := runtime.Data{}
		if i == 0 {
			allowed = runtime.Data{ParentTaskID: start.Data.ParentTaskID, SubmissionID: start.Data.SubmissionID, DelegationOrigin: start.Data.DelegationOrigin, DelegationAuditIntent: start.Data.DelegationAuditIntent}
		} else if (i == 1 && event.Kind != runtime.WorkerStarted) || (i > 1 && event.Kind != runtime.WorkerHeartbeat) {
			return bad()
		}
		if !reflect.DeepEqual(event.Data, allowed) {
			return bad()
		}
	}
	state, err := Replay(context.Background(), terminalReader(history), start.TaskID)
	if err != nil || state.State != "running" || state.InterruptedTurn || state.UncertainEffects || len(state.Pending) != 0 {
		return bad()
	}
	last := history[len(history)-1]
	sequence := last.Sequence + 1
	sum := sha256.Sum256([]byte(start.TaskID + "\x00" + last.ID + "\x00" + strconv.FormatInt(sequence, 10) + "\x00worker_owner_interrupted\x00" + now.Format(time.RFC3339Nano)))
	end := runtime.Event{Version: 1, ID: hex.EncodeToString(sum[:]), TaskID: start.TaskID, SessionID: start.SessionID, CorrelationID: start.TaskID, WorkerID: start.WorkerID, CausationID: last.ID, Sequence: sequence, Time: now, Kind: runtime.TaskFailed, Data: runtime.Data{Code: "worker_owner_interrupted"}}
	body, err := end.Encode()
	if err != nil || len(body) > budget {
		return bad()
	}
	validation := append(append([]runtime.Event(nil), history...), end)
	// Only the private projection copy receives the legacy submitted marker.
	if validation[0].Data.SubmissionID == "" {
		validation[0].Data.SubmissionID = "unsubmitted"
	}
	if _, accepted, err := projectWorkerTerminal(validation); err != nil || accepted {
		return bad()
	}
	return InterruptionRecovery{ParentTaskID: start.TaskID, ExpectedSequence: state.Sequence, Events: []runtime.Event{end}}, nil
}
