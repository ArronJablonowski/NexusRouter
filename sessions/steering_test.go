package sessions

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func steeringFixture() reader {
	events := reader{}
	add := func(kind runtime.Kind, turn string, data runtime.Data) {
		e := runtime.Event{Version: 1, ID: fmt.Sprint("event", len(events)+1), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(len(events) + 1), Time: time.Unix(100, 0), Kind: kind, TurnID: turn, Data: data}
		if turn != "" {
			e.AttemptID = turn + "-attempt"
		}
		events = append(events, e)
	}
	add(runtime.TaskStarted, "", runtime.Data{Messages: []providers.Message{{Role: "system", Content: "Keep policy"}, {Role: "user", Content: "Original task"}}})
	add(runtime.TurnStarted, "one", runtime.Data{})
	add(runtime.TurnCompleted, "one", runtime.Data{Text: "First answer"})
	add(runtime.SteeringApplied, "", runtime.Data{SteeringID: "steer-1", Text: "Use the revised requirement"})
	add(runtime.TurnStarted, "two", runtime.Data{})
	add(runtime.TurnCompleted, "two", runtime.Data{Text: "Revised answer"})
	add(runtime.TaskCompleted, "two", runtime.Data{})
	return events
}

func TestSteeringReplayProvenanceCompactionAndContinuation(t *testing.T) {
	events := steeringFixture()
	s, err := Replay(context.Background(), events, "task")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "completed" || len(s.Messages) != 5 || s.Messages[3].Role != "user" || s.Messages[3].Content != events[3].Data.Text || !reflect.DeepEqual(s.MessageSequences, []int64{1, 1, 3, 4, 6}) {
		t.Fatal(s)
	}
	messages, checkpoint, err := PrepareContinuation(s, CompactionRequest{Keep: 2, Summary: Summary{Requirements: []string{"Follow revised requirement"}}})
	if err != nil || checkpoint == nil || !reflect.DeepEqual(messages[len(messages)-2:], s.Messages[3:]) {
		t.Fatal(messages, checkpoint, err)
	}
	if checkpoint.FirstRetainedSequence != 4 || checkpoint.SourceSequence != 7 {
		t.Fatal("steering provenance lost", checkpoint)
	}
	continued := events[:1]
	continued[0].Data.Messages = messages
	continued[0].Data.ParentTaskID = s.TaskID
	continued[0].Data.Compaction = checkpoint
	// New task attribution must match the compaction checkpoint source.
	continued[0].TaskID, continued[0].SessionID, continued[0].CorrelationID = "next", "next", "next"
	continued[0].Data.ContextLineage, err = runtime.ExtendContextLineage(nil, "next", 1, checkpoint, s.Messages)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Replay(context.Background(), continued, "next")
	if err != nil || !reflect.DeepEqual(got.Messages, messages) {
		t.Fatal(got, err)
	}
}

func TestSteeringReplayRejectsUnsafeBoundariesAndDuplicates(t *testing.T) {
	for _, name := range []string{"active turn", "pending tool", "uncertain effect", "duplicate", "turn attribution", "attempt attribution"} {
		t.Run(name, func(t *testing.T) {
			steer := steeringFixture()[3]
			var events reader
			switch name {
			case "active turn":
				events = steeringFixture()[:2]
			case "pending tool":
				events = fixture()[:4]
			case "uncertain effect":
				events = fixture()[:6]
				events[5].Data.Effect = runtime.UncertainEffect
			case "duplicate":
				events = steeringFixture()[:4]
			case "turn attribution":
				events = steeringFixture()[:3]
				steer.TurnID = "one"
			case "attempt attribution":
				events = steeringFixture()[:3]
				steer.AttemptID = "one-attempt"
			}
			steer.ID = "added"
			steer.Sequence = int64(len(events) + 1)
			events = append(events, steer)
			if _, err := Replay(context.Background(), events, "task"); err == nil {
				t.Fatal("unsafe steering accepted")
			}
		})
	}
}

func TestSteeringReplayBoundAndEventPage(t *testing.T) {
	events := steeringFixture()[:1]
	for i := 0; i < 33; i++ {
		e := steeringFixture()[3]
		e.ID = fmt.Sprint("s", i)
		e.Data.SteeringID = e.ID
		e.Sequence = int64(len(events) + 1)
		events = append(events, e)
		_, err := Replay(context.Background(), events, "task")
		if (err == nil) != (i < 32) {
			t.Fatal(i, err)
		}
	}
	events = steeringFixture()[:4]
	page := EventPage{Version: 1, TaskID: "task", SessionID: "session", State: "running", NextSequence: 4, HeadSequence: 4, Events: events}
	if err := page.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSteeringTerminalProjectionRequiresSubsequentCompletedTurn(t *testing.T) {
	for _, late := range []bool{false, true} {
		events := terminalHistory(t, "tools")
		index := len(events) - 1
		if !late {
			for i, e := range events {
				if e.Kind == runtime.ToolCompleted {
					index = i + 1
					break
				}
			}
		}
		steer := events[0]
		steer.Kind, steer.ID = runtime.SteeringApplied, "steering"
		steer.TurnID, steer.AttemptID = "", ""
		steer.Data = runtime.Data{SteeringID: "steer", Text: "Revise the answer"}
		updated := append([]runtime.Event{}, events[:index]...)
		updated = append(updated, steer)
		updated = append(updated, events[index:]...)
		for i := range updated {
			updated[i].Sequence = int64(i + 1)
		}
		out, err := ProjectTerminalSubmission(updated)
		if late {
			if err == nil {
				t.Fatal("recovered answer predating steering", out)
			}
		} else if err != nil || out.State != "succeeded" || out.Result.Text != "answer" {
			t.Fatal(out, err)
		}
	}
}

func TestSteeringTerminalProjectionUsesFinalEvaluationWindow(t *testing.T) {
	for _, mode := range []string{"valid", "stale final attempt", "duplicate final check", "missing final check"} {
		t.Run(mode, func(t *testing.T) {
			first := terminalHistory(t, "success")
			final := terminalHistory(t, "success")
			events := append([]runtime.Event{}, first[:len(first)-1]...)
			steer := first[0]
			steer.ID, steer.Kind, steer.TurnID, steer.AttemptID = "steering", runtime.SteeringApplied, "", ""
			steer.Data = runtime.Data{SteeringID: "steering", Text: "Apply the revised requirement"}
			events = append(events, steer)
			for _, e := range final[1:] {
				e.ID = "final-" + e.ID
				if e.Kind == runtime.TurnCompleted {
					e.Data.Text = "revised answer"
				}
				if e.Kind == runtime.EvaluationRecorded {
					if mode == "missing final check" {
						continue
					}
					if mode == "stale final attempt" {
						e.AttemptID = first[1].AttemptID
					}
					if mode == "duplicate final check" {
						duplicate := e
						duplicate.ID = "duplicate-final-check"
						events = append(events, duplicate)
					}
				}
				events = append(events, e)
			}
			for i := range events {
				events[i].Sequence = int64(i + 1)
			}
			out, err := ProjectTerminalSubmission(events)
			if mode == "valid" {
				if err != nil || out.State != "succeeded" || out.Result.Text != "revised answer" || out.Result.Turns != 2 {
					t.Fatal(out, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid final evidence", mode, out)
			}
		})
	}
}
