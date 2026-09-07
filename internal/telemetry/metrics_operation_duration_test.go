package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type operationEventSpec struct {
	kind runtime.Kind
	at   time.Time
	turn string
	call string
}

func appendOperationTask(t *testing.T, db *Store, id string, base time.Time, specs []operationEventSpec) {
	t.Helper()
	ctx := context.Background()
	all := append([]operationEventSpec{{kind: runtime.TaskStarted, at: base}}, specs...)
	all = append(all, operationEventSpec{kind: runtime.TaskFailed, at: base.Add(10 * time.Second)})
	for i, spec := range all {
		e := event(id+"-event-"+string(rune('a'+i)), int64(i+1), spec.kind)
		e.TaskID, e.SessionID, e.CorrelationID, e.Time = id, "private-session-"+id, id, spec.at
		if spec.kind == runtime.TurnStarted || spec.kind == runtime.TurnCompleted || spec.kind == runtime.ToolStarted || spec.kind == runtime.ToolCompleted {
			e.TurnID, e.AttemptID = spec.turn, "private-attempt-"+id
		}
		if spec.kind == runtime.ToolStarted || spec.kind == runtime.ToolCompleted {
			e.Data.ToolCallID, e.Data.ToolName, e.Data.Effect = spec.call, "private-tool", runtime.NoEffect
		}
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMetricsOperationDurationPairsAndUnavailability(t *testing.T) {
	db, _ := submissionStore(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	appendOperationTask(t, db, "private-paired", base, []operationEventSpec{
		{runtime.TurnStarted, base.Add(time.Second), "turn", ""},
		{runtime.TurnCompleted, base.Add(1200 * time.Millisecond), "turn", ""},
		{runtime.ToolStarted, base.Add(2 * time.Second), "turn", "call"},
		{runtime.ToolCompleted, base.Add(2050 * time.Millisecond), "turn", "call"},
	})
	appendOperationTask(t, db, "private-missing", base, []operationEventSpec{
		{runtime.TurnStarted, base.Add(time.Second), "turn", ""},
		{runtime.ToolStarted, base.Add(2 * time.Second), "turn", "call"},
	})
	appendOperationTask(t, db, "private-orphan", base, []operationEventSpec{
		{runtime.TurnCompleted, base.Add(time.Second), "turn", ""},
		{runtime.ToolCompleted, base.Add(2 * time.Second), "turn", "call"},
	})
	appendOperationTask(t, db, "private-invalid", base, []operationEventSpec{
		{runtime.TurnStarted, base.Add(2 * time.Second), "turn", ""},
		{runtime.TurnCompleted, base.Add(time.Second), "turn", ""},
		{runtime.ToolStarted, base.Add(4 * time.Second), "turn", "call"},
		{runtime.ToolCompleted, base.Add(3 * time.Second), "turn", "call"},
	})
	snapshot, err := db.Metrics(context.Background())
	if err != nil || snapshot.Validate() != nil || snapshot.OperationDuration == nil {
		t.Fatal(snapshot, err)
	}
	for i, group := range snapshot.OperationDuration.Groups {
		bucket := 1
		if i == 1 {
			bucket = 0
		}
		if group.Count != 1 || group.BucketCounts[bucket] != 1 || group.Unavailable[0].Value != 1 || group.Unavailable[1].Value != 1 || group.Unavailable[2].Value != 1 {
			t.Fatal(group)
		}
	}
	body, _ := json.Marshal(snapshot.OperationDuration)
	if strings.Contains(string(body), "private") || !snapshot.OperationDuration.StartedAt.Equal(base.Add(time.Second)) {
		t.Fatal(string(body))
	}
}

func TestMetricsOperationDurationLegacyUnpairableEvents(t *testing.T) {
	db, _ := submissionStore(t)
	if _, err := db.db.Exec(`INSERT INTO task_heads VALUES('legacy','private-session',4,'running')`); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"turn.started", "turn.completed", "tool.started", "tool.completed"} {
		if _, err := db.db.Exec(`INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,?,json_object('kind',?))`, "legacy-event-"+kind, "legacy", i+1, kind); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := db.Metrics(context.Background())
	if err != nil || snapshot.Validate() != nil {
		t.Fatal(snapshot, err)
	}
	for _, group := range snapshot.OperationDuration.Groups {
		if group.Count != 0 || group.Unavailable[0].Value != 1 || group.Unavailable[1].Value != 1 || group.Unavailable[2].Value != 0 {
			t.Fatal(group)
		}
	}
}
