package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	tracewire "github.com/ArronJablonowski/DarwinRouter/traces"
)

func TestTraceSnapshotPairsOperationsWithoutIdentities(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	parentStart := event("private-parent-start", 1, runtime.TaskStarted)
	parentStart.TaskID, parentStart.SessionID, parentStart.CorrelationID = "private-parent-task", "parent-session", "private-parent-task"
	parentDone := event("private-parent-done", 2, runtime.TaskCompleted)
	parentDone.TaskID, parentDone.SessionID, parentDone.CorrelationID = parentStart.TaskID, parentStart.SessionID, parentStart.CorrelationID
	for i, parent := range []runtime.Event{parentStart, parentDone} {
		if err := db.Append(ctx, int64(i), parent); err != nil {
			t.Fatal(err)
		}
	}
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.RouteSelected, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.WorkerStarted, runtime.WorkerCompleted, runtime.EvaluationRecorded, runtime.ErrorRecorded, runtime.TaskCompleted}
	for i, kind := range kinds {
		e := event("private-event-"+string(rune('a'+i)), int64(i+1), kind)
		e.TaskID, e.SessionID, e.CorrelationID = "private-task", "private-session", "private-correlation"
		e.Time = base.Add(time.Duration(i) * time.Second)
		if kind == runtime.TaskStarted {
			e.Data.RetryOfTaskID, e.Data.ParentTaskID = "private-prior-task", "private-parent-task"
			e.Data.Compaction = &runtime.ContextCompaction{Version: 1, SourceTaskID: "private-parent-task", SourceSequence: 2, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: runtime.ContextSummary{Requirements: []string{"retain"}}}
			e.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true}
		}
		if kind == runtime.RouteSelected {
			e.RouteID, e.Data.ModelID, e.Data.ProviderID = "private-route", "private-model", "private-provider"
			e.Data.Route = &routing.Selection{Explored: true, Excluded: []routing.Exclusion{{Model: "private-excluded-model", Provider: "private-excluded-provider", Reasons: []string{"mode", "privacy", "health", "policy", "credential", "capacity", "context", "budget", "capability", "capacity"}}, {Model: "private-second-model", Provider: "private-second-provider", Reasons: []string{"capacity"}}}}
		}
		if kind == runtime.TurnStarted || kind == runtime.TurnCompleted || kind == runtime.ToolStarted || kind == runtime.ToolCompleted {
			e.TurnID, e.AttemptID = "private-turn", "private-attempt"
		}
		if kind == runtime.ToolStarted || kind == runtime.ToolCompleted {
			e.Data.ToolCallID, e.Data.ToolName, e.Data.Effect = "private-call", "private-tool", runtime.NoEffect
		}
		if kind == runtime.WorkerStarted || kind == runtime.WorkerCompleted {
			e.WorkerID = "private-worker"
		}
		if kind == runtime.EvaluationRecorded {
			accepted := true
			e.Data.Accepted = &accepted
		}
		if kind == runtime.ErrorRecorded {
			e.Data.Code = "private-error-code"
		}
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(kind, err)
		}
	}
	snapshot, err := db.Traces(ctx, 1)
	if err != nil || snapshot.Validate() != nil || len(snapshot.Traces) != 1 || len(snapshot.Traces[0].Spans) != 20 {
		t.Fatal(snapshot, err)
	}
	body, _ := json.Marshal(snapshot)
	if strings.Contains(string(body), "private") || snapshot.Traces[0].Spans[0].Outcome != "completed" {
		t.Fatal(string(body))
	}
	want := map[string]bool{"fallback/selected": true, "compaction/applied": true, "skill_context/loaded": true, "route/explored": true, "provider/completed": true, "tool/completed": true, "tool_effect/none": true, "worker/completed": true, "evaluation/accepted": true, "error/recorded": true}
	for _, reason := range []string{"mode", "privacy", "health", "policy", "credential", "capacity", "context", "budget", "capability"} {
		want["route_constraint/"+reason] = true
	}
	for _, span := range snapshot.Traces[0].Spans[1:] {
		key := span.Name + "/" + span.Outcome
		if !want[key] {
			t.Fatal(span, want)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatal("missing spans", want)
	}
}

func TestTraceSnapshotBucketsTopLevelSubmissionQueueResidency(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "private-queue-key")
	claim := claimSubmission(t, db)
	startedAt := time.Now().UTC().Add(-time.Second)
	createdAt := startedAt.Add(-20 * time.Second)
	if _, err := db.db.ExecContext(ctx, "UPDATE submissions SET created_at=? WHERE id=?", submissionTime(createdAt), claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	start := event("private-queue-start", 1, runtime.TaskStarted)
	start.Time, start.Data.SubmissionID = startedAt, claim.Status.ID
	done := event("private-queue-done", 2, runtime.TaskCompleted)
	done.Time = startedAt.Add(500 * time.Millisecond)
	if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendSubmission(ctx, 1, done, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Traces(ctx, 1)
	if err != nil || len(snapshot.Traces) != 1 || len(snapshot.Traces[0].Spans) != 2 {
		t.Fatal(snapshot, err)
	}
	queue := snapshot.Traces[0].Spans[1]
	if queue.Name != "queue_residency" || queue.Outcome != "lt_1m" || !queue.StartedAt.Equal(startedAt) || !queue.EndedAt.Equal(startedAt) {
		t.Fatal(queue)
	}
	encoded, marshalErr := json.Marshal(snapshot)
	otlp, otlpErr := tracewire.MarshalOTLP(snapshot)
	for _, private := range []string{claim.Status.ID, submissionTime(createdAt), "private-queue"} {
		if strings.Contains(string(encoded), private) || strings.Contains(string(otlp), private) {
			t.Fatal("private queue data escaped", private)
		}
	}
	if marshalErr != nil || otlpErr != nil {
		t.Fatal(marshalErr, otlpErr)
	}
}

func TestQueueResidencyBucketBoundaries(t *testing.T) {
	cases := []struct {
		wait time.Duration
		want string
	}{
		{0, "lt_1s"}, {time.Second - 1, "lt_1s"},
		{time.Second, "lt_10s"}, {10 * time.Second, "lt_1m"},
		{time.Minute, "lt_5m"}, {5 * time.Minute, "lt_30m"},
		{30 * time.Minute, "lt_1h"}, {time.Hour, "gte_1h"},
	}
	for _, tc := range cases {
		if got := queueResidencyBucket(tc.wait); got != tc.want {
			t.Fatalf("wait %s: got %s want %s", tc.wait, got, tc.want)
		}
	}
}

func TestTraceSnapshotClassifiesToolEffects(t *testing.T) {
	for _, effect := range []runtime.Effect{runtime.NoEffect, runtime.ConfirmedEffect, runtime.UncertainEffect} {
		t.Run(string(effect), func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			base := time.Now().UTC().Add(-time.Second)
			start := event("start", 1, runtime.TaskStarted)
			toolStart := event("tool-start", 2, runtime.ToolStarted)
			toolDone := event("tool-done", 3, runtime.ToolCompleted)
			done := event("done", 4, runtime.TaskCompleted)
			start.Time, toolStart.Time, toolDone.Time, done.Time = base, base.Add(time.Millisecond), base.Add(2*time.Millisecond), base.Add(3*time.Millisecond)
			for _, item := range []*runtime.Event{&toolStart, &toolDone} {
				item.TurnID, item.AttemptID = "turn", "attempt"
				item.Data.ToolCallID, item.Data.ToolName = "private-call", "private-tool"
			}
			toolStart.Data.Effect, toolDone.Data.Effect = runtime.UncertainEffect, effect
			for i, item := range []runtime.Event{start, toolStart, toolDone, done} {
				if err := db.Append(ctx, int64(i), item); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := db.Traces(ctx, 1)
			if err != nil || len(snapshot.Traces) != 1 || len(snapshot.Traces[0].Spans) != 3 {
				t.Fatal(snapshot, err)
			}
			if got := snapshot.Traces[0].Spans[2]; got.Name != "tool_effect" || got.Outcome != string(effect) || !got.StartedAt.Equal(toolDone.Time) {
				t.Fatal(got)
			}
		})
	}
}

func TestTraceSnapshotRejectsUnknownToolEffect(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Second)
	start := event("effect-start", 1, runtime.TaskStarted)
	toolStart := event("effect-tool-start", 2, runtime.ToolStarted)
	toolDone := event("effect-tool-done", 3, runtime.ToolCompleted)
	done := event("effect-done", 4, runtime.TaskCompleted)
	start.Time, toolStart.Time, toolDone.Time, done.Time = base, base.Add(time.Millisecond), base.Add(2*time.Millisecond), base.Add(3*time.Millisecond)
	for _, item := range []*runtime.Event{&toolStart, &toolDone} {
		item.TurnID, item.AttemptID = "turn", "attempt"
		item.Data.ToolCallID, item.Data.ToolName, item.Data.Effect = "call", "tool", runtime.NoEffect
	}
	for i, item := range []runtime.Event{start, toolStart, toolDone, done} {
		if err := db.Append(ctx, int64(i), item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE events SET body=json_set(body,'$.data.effect','private-effect') WHERE id='effect-tool-done'"); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := db.Traces(ctx, 1); err == nil || len(snapshot.Traces) != 0 {
		t.Fatal("unknown tool effect escaped", snapshot, err)
	}
}

func TestTraceSnapshotRejectsCorruptSubmissionQueueLinkage(t *testing.T) {
	for _, corruption := range []string{"malformed-time", "future-time", "missing-submission"} {
		t.Run(corruption, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			queuedSubmission(t, db, corruption)
			claim := claimSubmission(t, db)
			start := event("queue-start", 1, runtime.TaskStarted)
			start.Time, start.Data.SubmissionID = time.Now().UTC().Add(-time.Second), claim.Status.ID
			done := event("queue-done", 2, runtime.TaskCompleted)
			done.Time = start.Time.Add(time.Millisecond)
			if db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token) != nil || db.AppendSubmission(ctx, 1, done, claim.Status.ID, claim.Token) != nil {
				t.Fatal("fixture")
			}
			var query string
			var args []any
			switch corruption {
			case "malformed-time":
				query, args = "UPDATE submissions SET created_at='not-a-time' WHERE id=?", []any{claim.Status.ID}
			case "future-time":
				query, args = "UPDATE submissions SET created_at=? WHERE id=?", []any{submissionTime(start.Time.Add(time.Second)), claim.Status.ID}
			case "missing-submission":
				if _, err := db.db.ExecContext(ctx, "DELETE FROM submission_stream_events WHERE submission_id=?", claim.Status.ID); err != nil {
					t.Fatal(err)
				}
				query, args = "DELETE FROM submissions WHERE id=?", []any{claim.Status.ID}
			}
			if _, err := db.db.ExecContext(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
			if snapshot, err := db.Traces(ctx, 1); err == nil || len(snapshot.Traces) != 0 {
				t.Fatal("corrupt queue linkage escaped", snapshot, err)
			}
		})
	}
}

func TestTraceSnapshotRejectsCorruptTerminalTime(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	start := event("start", 1, runtime.TaskStarted)
	start.Time = time.Unix(200, 0).UTC()
	done := event("done", 2, runtime.TaskCompleted)
	done.Time = time.Unix(100, 0).UTC()
	if db.Append(ctx, 0, start) != nil || db.Append(ctx, 1, done) != nil {
		t.Fatal("fixture")
	}
	if snapshot, err := db.Traces(ctx, 1); err == nil || len(snapshot.Traces) != 0 {
		t.Fatal(snapshot, err)
	}
}

func TestTraceSnapshotRejectsUnknownRouteConstraint(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	base := time.Unix(300, 0).UTC()
	start := event("start-route", 1, runtime.TaskStarted)
	start.Time = base
	route := event("route", 2, runtime.RouteSelected)
	route.Time, route.RouteID = base.Add(time.Second), "route"
	route.Data.ModelID, route.Data.ProviderID = "model", "provider"
	route.Data.Route = &routing.Selection{Excluded: []routing.Exclusion{{Model: "model", Provider: "provider", Reasons: []string{"private-new-reason"}}}}
	done := event("done-route", 3, runtime.TaskCompleted)
	done.Time = base.Add(2 * time.Second)
	for i, item := range []runtime.Event{start, route, done} {
		if err := db.Append(ctx, int64(i), item); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot, err := db.Traces(ctx, 1); err == nil || len(snapshot.Traces) != 0 {
		t.Fatal("unknown route reason escaped", snapshot, err)
	}
}
