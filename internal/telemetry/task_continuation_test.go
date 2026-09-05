package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func continuationEvents(recovered bool) []runtime.Event {
	start := event("start", 1, runtime.TaskStarted)
	start.Data.Messages = []providers.Message{{Role: "user", Content: "PRIVATE_PROMPT"}}
	turn := event("turn", 2, runtime.TurnStarted)
	turn.TurnID, turn.AttemptID = "turn", "attempt"
	answer := event("answer", 3, runtime.TurnCompleted)
	answer.TurnID, answer.AttemptID = "turn", "attempt"
	answer.Data.Text = "PRIVATE_ANSWER"
	if !recovered {
		return []runtime.Event{start, turn, answer, event("end", 4, runtime.TaskCompleted)}
	}
	answer.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"PRIVATE_ARGUMENT","validation":"text"}`)}}
	dispatch := event("dispatch", 4, runtime.ToolStarted)
	dispatch.TurnID, dispatch.AttemptID = "turn", "attempt"
	dispatch.Data = runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect}
	result := event("result", 5, runtime.ToolCompleted)
	result.TurnID, result.AttemptID = "turn", "attempt"
	result.Data = runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect, Code: "delegation_recovered", Text: "PRIVATE_RESULT"}
	end := event("end", 6, runtime.TaskFailed)
	end.TurnID, end.AttemptID = "turn", "attempt"
	end.CausationID = result.ID
	end.Data.Code = "interrupted_after_delegation"
	return []runtime.Event{start, turn, answer, dispatch, result, end}
}

func TestTaskContinuationCoherentMetadataAndReadOnlyRestart(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		db, path := submissionStore(t)
		ctx := context.Background()
		events := continuationEvents(recovered)
		for i, e := range events {
			if err := db.Append(ctx, int64(i), e); err != nil {
				t.Fatal(err)
			}
			status, err := db.TaskContinuation(ctx, "task")
			if err != nil || status.Validate() != nil || status.Sequence != int64(i+1) {
				t.Fatal("incoherent prefix status", status, err)
			}
			want := "task_running"
			switch {
			case i == 1:
				want = "interrupted_turn"
			case recovered && (i == 2 || i == 3):
				want = "pending_tools"
			case i == len(events)-1:
				want = "completed"
				if recovered {
					want = "recovered_delegation"
				}
			}
			if status.Reason != want || status.HistoryEligible != (i == len(events)-1) {
				t.Fatal("state/checkpoint mismatch", status, want)
			}
		}
		before, err := db.Read(ctx, "task", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		ro, err := OpenReadOnly(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		status, err := ro.TaskContinuation(ctx, "task")
		ro.Close()
		if err != nil || !status.HistoryEligible {
			t.Fatal(status, err)
		}
		body, _ := json.Marshal(status)
		if strings.Contains(string(body), "PRIVATE_") {
			t.Fatal("status leaked history")
		}
		after, err := db.Read(ctx, "task", 0, 100)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("read-only assessment changed journal")
		}
	}
}

func TestTaskContinuationCorruptionHasNoPartialStatus(t *testing.T) {
	for _, mode := range []string{"gap", "head", "session", "id", "body", "oversize", "count", "unknown", "checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			for i, e := range continuationEvents(true) {
				if err := db.Append(ctx, int64(i), e); err != nil {
					t.Fatal(err)
				}
			}
			query := ""
			switch mode {
			case "gap":
				query = `DELETE FROM events WHERE sequence=2`
			case "head":
				query = `UPDATE task_heads SET sequence=4`
			case "session":
				query = `UPDATE task_heads SET session_id='different'`
			case "id":
				query = `UPDATE events SET id='wrong' WHERE sequence=3`
			case "body":
				query = `UPDATE events SET body='{}' WHERE sequence=3`
			case "oversize":
				query = `UPDATE events SET body=json_object('padding',printf('%.*c',8388609,'x')) WHERE sequence=3`
			case "count":
				query = `UPDATE task_heads SET sequence=10001`
			case "unknown":
				query = `UPDATE task_heads SET state='unknown'`
			case "checkpoint":
				query = `UPDATE events SET body=json_set(body,'$.data.tool_call_id','foreign') WHERE sequence=5`
			}
			if _, err := db.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			status, err := db.TaskContinuation(ctx, "task")
			if err == nil || status != (sessions.ContinuationStatus{}) {
				t.Fatal("corruption returned partial status", status, err)
			}
		})
	}
}

func TestTaskContinuationRejectsMissingInvalidAndCanceledReads(t *testing.T) {
	db, _ := submissionStore(t)
	for _, task := range []string{"", "missing", "bad\nidentity"} {
		out, err := db.TaskContinuation(context.Background(), task)
		if err == nil || out != (sessions.ContinuationStatus{}) {
			t.Fatal("invalid task returned metadata")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := db.TaskContinuation(ctx, "task"); err == nil || out != (sessions.ContinuationStatus{}) {
		t.Fatal("canceled read returned metadata")
	}
}
