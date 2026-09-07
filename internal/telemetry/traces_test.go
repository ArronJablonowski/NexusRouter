package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestTraceSnapshotPairsOperationsWithoutIdentities(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.RouteSelected, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.WorkerStarted, runtime.WorkerCompleted, runtime.EvaluationRecorded, runtime.ErrorRecorded, runtime.TaskCompleted}
	for i, kind := range kinds {
		e := event("private-event-"+string(rune('a'+i)), int64(i+1), kind)
		e.TaskID, e.SessionID, e.CorrelationID = "private-task", "private-session", "private-correlation"
		e.Time = base.Add(time.Duration(i) * time.Second)
		if kind == runtime.TaskStarted {
			e.Data.RetryOfTaskID, e.Data.ParentTaskID = "private-prior-task", "private-parent-task"
			e.Data.Compaction = &runtime.ContextCompaction{Version: 1, SourceTaskID: "private-parent-task", SourceSequence: 1, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: runtime.ContextSummary{Requirements: []string{"retain"}}}
			e.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true}
		}
		if kind == runtime.RouteSelected {
			e.RouteID, e.Data.ModelID, e.Data.ProviderID = "private-route", "private-model", "private-provider"
			e.Data.Route = &routing.Selection{Explored: true}
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
	if err != nil || snapshot.Validate() != nil || len(snapshot.Traces) != 1 || len(snapshot.Traces[0].Spans) != 10 {
		t.Fatal(snapshot, err)
	}
	body, _ := json.Marshal(snapshot)
	if strings.Contains(string(body), "private") || snapshot.Traces[0].Spans[0].Outcome != "completed" {
		t.Fatal(string(body))
	}
	want := map[string]string{"fallback": "selected", "compaction": "applied", "skill_context": "loaded", "route": "explored", "provider": "completed", "tool": "completed", "worker": "completed", "evaluation": "accepted", "error": "recorded"}
	for _, span := range snapshot.Traces[0].Spans[1:] {
		if want[span.Name] != span.Outcome {
			t.Fatal(span, want)
		}
		delete(want, span.Name)
	}
	if len(want) != 0 {
		t.Fatal("missing spans", want)
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
