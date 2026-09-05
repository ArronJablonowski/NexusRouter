package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type InterruptionRecovery struct {
	ParentTaskID     string
	ExpectedSequence int64
	Events           []runtime.Event
}

// DelegationRecovery retains source compatibility for the original planner.
type DelegationRecovery = InterruptionRecovery

type recoveryCall struct {
	pending   Pending
	count     int
	completed *runtime.Event
	works     map[int][]runtime.Event
}

// PlanInterruptedDelegation only closes a dispatched delegation whose entire
// owned work tree already has durable terminal evidence. It never retries,
// infers missing work, dispatches a model, or reports parent task success.
// Unsupported/incomplete histories return ErrHistory without a partial plan.
func PlanInterruptedDelegation(histories [][]runtime.Event, now time.Time, canceled bool) (DelegationRecovery, error) {
	bad := func() (DelegationRecovery, error) { return DelegationRecovery{}, ErrHistory }
	if now.IsZero() || len(histories) < 2 || len(histories) > 66 {
		return bad()
	}
	nodes := make(map[string][]runtime.Event, len(histories))
	budget, count := 8<<20, 0
	for _, history := range histories {
		if len(history) < 2 || nodes[history[0].TaskID] != nil || !ValidEventPageID(history[0].TaskID) {
			return bad()
		}
		for _, e := range history {
			body, err := e.Encode()
			if err != nil || len(body) > budget || count >= 10000 || e.Time.After(now) {
				return bad()
			}
			budget -= len(body)
			count++
		}
		nodes[history[0].TaskID] = history
	}
	var root []runtime.Event
	for _, history := range histories {
		start := history[0]
		if start.WorkerID == "" && nodes[start.Data.ParentTaskID] == nil {
			if root != nil {
				return bad()
			}
			root = history
		}
	}
	if root == nil {
		return bad()
	}
	parent := root[0]
	snapshot, err := Replay(context.Background(), terminalReader(root), parent.TaskID)
	if err != nil || snapshot.State != "running" || snapshot.InterruptedTurn || len(snapshot.Pending) != 1 {
		return bad()
	}
	var pending Pending
	for _, p := range snapshot.Pending {
		pending = p
	}
	if !pending.Dispatched || (pending.Call.Name != "delegate" && pending.Call.Name != "delegate_batch") {
		return bad()
	}
	calls := map[string]*recoveryCall{}
	for _, e := range root {
		if e.WorkerID != "" {
			return bad()
		}
		switch e.Kind {
		case runtime.TurnCompleted:
			for _, call := range e.Data.ToolCalls {
				if call.Name != "delegate" && call.Name != "delegate_batch" {
					continue
				}
				n, err := recoveryArgumentCount(call.Name, call.Arguments)
				if err != nil || calls[call.ID] != nil {
					return bad()
				}
				calls[call.ID] = &recoveryCall{pending: Pending{Call: call, TurnID: e.TurnID, AttemptID: e.AttemptID}, count: n, works: map[int][]runtime.Event{}}
			}
		case runtime.ToolCompleted:
			if e.Data.Effect == runtime.UncertainEffect {
				return bad()
			}
			if call := calls[e.Data.ToolCallID]; call != nil {
				copy := e
				call.completed = &copy
			}
		}
	}
	active := calls[pending.Call.ID]
	if active == nil || active.completed != nil {
		return bad()
	}
	children := map[string][]runtime.Event{}
	for _, history := range histories {
		start := history[0]
		if start.TaskID == parent.TaskID {
			continue
		}
		if start.WorkerID == "" {
			work := nodes[start.Data.ParentTaskID]
			if work == nil || work[0].WorkerID == "" || children[start.Data.ParentTaskID] != nil {
				return bad()
			}
			children[start.Data.ParentTaskID] = history
			continue
		}
		origin := start.Data.DelegationOrigin
		if start.Data.ParentTaskID != parent.TaskID || start.SessionID != parent.SessionID || origin == nil || origin.Validate() != nil {
			return bad()
		}
		call := calls[origin.ToolCallID]
		if call == nil || origin.ToolName != call.pending.Call.Name || origin.TurnID != call.pending.TurnID || origin.AttemptID != call.pending.AttemptID {
			return bad()
		}
		index := 0
		if origin.BatchIndex != nil {
			index = *origin.BatchIndex
		}
		if index >= call.count || call.works[index] != nil {
			return bad()
		}
		call.works[index] = history
	}
	var payload string
	for id, call := range calls {
		items := make([]json.RawMessage, call.count)
		for index := range items {
			work := call.works[index]
			if work == nil {
				if id == pending.Call.ID {
					return bad()
				}
				items[index] = json.RawMessage(`{"error":"delegate_unavailable_or_rejected"}`)
				continue
			}
			item, err := recoveryWorkResult(work, children[work[0].TaskID])
			if err != nil {
				return bad()
			}
			if call.pending.Call.Name == "delegate_batch" && len(item) > 128<<10 {
				return bad()
			}
			items[index] = item
		}
		body := items[0]
		if call.pending.Call.Name == "delegate_batch" {
			body, err = json.Marshal(struct {
				Results []json.RawMessage `json:"results"`
			}{items})
			if err != nil {
				return bad()
			}
		}
		if len(body) >= 1<<20 {
			return bad()
		}
		if id == pending.Call.ID {
			payload = string(body)
		} else {
			if call.completed == nil {
				return bad()
			}
			actual := bytes.TrimSpace([]byte(call.completed.Data.Text))
			generic := bytes.Equal(actual, []byte(`{"error":"delegate_unavailable_or_rejected"}`))
			if !bytes.Equal(actual, body) && !(generic && len(call.works) == 0) {
				return bad()
			}
		}
	}
	if payload == "" {
		return bad()
	}
	makeEvent := func(offset int64, kind runtime.Kind) runtime.Event {
		sequence := snapshot.Sequence + offset
		sum := sha256.Sum256([]byte(parent.TaskID + "\x00" + root[len(root)-1].ID + "\x00" + strconv.FormatInt(sequence, 10) + "\x00" + string(kind) + "\x00" + now.UTC().Format(time.RFC3339Nano)))
		return runtime.Event{Version: 1, ID: hex.EncodeToString(sum[:]), TaskID: parent.TaskID, SessionID: parent.SessionID, CorrelationID: parent.TaskID, Sequence: sequence, Time: now.UTC(), Kind: kind, TurnID: pending.TurnID, AttemptID: pending.AttemptID}
	}
	tool := makeEvent(1, runtime.ToolCompleted)
	tool.Data = runtime.Data{ToolName: pending.Call.Name, ToolCallID: pending.Call.ID, Effect: runtime.NoEffect, Code: "delegation_recovered", Text: payload}
	terminal := makeEvent(2, runtime.TaskFailed)
	terminal.Data.Code = "interrupted_after_delegation"
	if canceled {
		terminal = makeEvent(2, runtime.TaskCanceled)
		terminal.Data.Code = "canceled"
	}
	terminal.CausationID = tool.ID
	full := make([][]runtime.Event, len(histories))
	for i, h := range histories {
		full[i] = h
		if h[0].TaskID == parent.TaskID {
			full[i] = append(append([]runtime.Event(nil), h...), tool, terminal)
		}
	}
	if _, err := ProjectTerminalTree(full); err != nil {
		return bad()
	}
	return DelegationRecovery{ParentTaskID: parent.TaskID, ExpectedSequence: snapshot.Sequence, Events: []runtime.Event{tool, terminal}}, nil
}
