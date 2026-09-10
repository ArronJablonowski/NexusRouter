package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func openRuntimeHostStore(t *testing.T, name string) *telemetry.Store {
	t.Helper()
	store, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func runtimeHostStart(content string) runtime.Event {
	return runtime.Event{Version: 1, ID: "host-start", TaskID: "host-task", SessionID: "host-session", CorrelationID: "host-task",
		WorkerID: "host-worker", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: "host-parent", Messages: []providers.Message{{Role: "user", Content: content}}}}
}

func TestRuntimeHostAdmissionCommitsRedactedStartBeforeDeliveryAndKeepsStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := openRuntimeHostStore(t, "host.db")
	secret := "private-host-secret"
	callbackCalls := 0
	admission := runtimeHostAdmission{taskID: "host-task", sessionID: "host-session", parentTaskID: "host-parent", workerID: "host-worker", store: store,
		commitFirst: func(ctx context.Context, event runtime.Event) error {
			callbackCalls++
			if event.Data.Messages[0].Content != "[REDACTED]" {
				t.Fatalf("callback received unredacted event: %+v", event)
			}
			if events, err := store.Read(ctx, event.TaskID, 0, 10); err != nil || len(events) != 0 {
				t.Fatalf("start existed before atomic host callback: %+v %v", events, err)
			}
			return store.Append(ctx, 0, event)
		}}
	r, err := withRuntimeHostAdmission(Request{}, admission)
	if err != nil {
		t.Fatal(err)
	}
	delivered := 0
	delivery := newEventDelivery(cancel, runtime.EventSinkFunc(func(ctx context.Context, event runtime.Event) error {
		events, readErr := store.Read(ctx, event.TaskID, 0, 10)
		if readErr != nil || len(events) != int(event.Sequence) || events[len(events)-1].ID != event.ID {
			t.Fatalf("delivery did not observe exact committed redacted start: %+v %+v %v", event, events, readErr)
		}
		if event.Kind == runtime.TaskStarted && event.Data.Messages[0].Content != "[REDACTED]" {
			t.Fatalf("delivery received unredacted start: %+v", event)
		}
		delivered++
		return nil
	}), nil)
	j := redactingJournal{db: store, secrets: []string{secret}, eventDelivery: delivery, runtimeHostAdmission: r.runtimeHostAdmission}
	if err = j.Append(ctx, 0, runtimeHostStart(secret)); err != nil {
		t.Fatal(err)
	}
	if callbackCalls != 1 || delivered != 1 {
		t.Fatal("first commit/delivery count", callbackCalls, delivered)
	}
	completed := runtime.Event{Version: 1, ID: "host-completed", TaskID: "host-task", SessionID: "host-session", CorrelationID: "host-task",
		WorkerID: "host-worker", Sequence: 2, Time: time.Now().UTC(), Kind: runtime.TaskCompleted}
	if err = j.Append(ctx, 1, completed); err != nil {
		t.Fatal("later event did not use injected store", err)
	}
	events, err := store.Read(ctx, "host-task", 0, 10)
	if err != nil || len(events) != 2 || events[1].Kind != runtime.TaskCompleted || callbackCalls != 1 {
		t.Fatal(events, err, callbackCalls)
	}
}

func TestRuntimeHostAdmissionFailsClosedOnReuseSequenceIdentityAndStore(t *testing.T) {
	ctx := context.Background()
	store := openRuntimeHostStore(t, "bound.db")
	other := openRuntimeHostStore(t, "other.db")
	calls := 0
	bound, err := withRuntimeHostAdmission(Request{}, runtimeHostAdmission{taskID: "host-task", sessionID: "host-session", parentTaskID: "host-parent", workerID: "host-worker", store: store,
		commitFirst: func(ctx context.Context, event runtime.Event) error { calls++; return store.Append(ctx, 0, event) }})
	if err != nil {
		t.Fatal(err)
	}
	start := runtimeHostStart("safe")
	for name, journal := range map[string]redactingJournal{
		"wrong store":    {db: other, runtimeHostAdmission: bound.runtimeHostAdmission},
		"skipped first":  {db: store, runtimeHostAdmission: bound.runtimeHostAdmission},
		"wrong identity": {db: store, runtimeHostAdmission: bound.runtimeHostAdmission},
	} {
		t.Run(name, func(t *testing.T) {
			event, expected := start, int64(0)
			switch name {
			case "skipped first":
				event.Sequence, event.Kind, expected = 2, runtime.TaskCompleted, 1
			case "wrong identity":
				event.TaskID, event.CorrelationID = "other-task", "other-task"
			}
			if appendErr := journal.Append(ctx, expected, event); !errors.Is(appendErr, ErrAdmission) {
				t.Fatal("invalid append admitted", appendErr)
			}
		})
	}
	journal := redactingJournal{db: store, runtimeHostAdmission: bound.runtimeHostAdmission}
	if err = journal.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if err = journal.Append(ctx, 0, start); !errors.Is(err, ErrAdmission) || calls != 1 {
		t.Fatal("one-shot admission reused", err, calls)
	}
}

func TestWithRuntimeHostAdmissionRejectsPublicAndMalformedIdentity(t *testing.T) {
	store := openRuntimeHostStore(t, "validation.db")
	valid := runtimeHostAdmission{taskID: "task", sessionID: "session", parentTaskID: "parent", workerID: "worker", store: store,
		commitFirst: func(context.Context, runtime.Event) error { return nil }}
	if _, err := withRuntimeHostAdmission(Request{}, valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		r    Request
		a    runtimeHostAdmission
	}{
		{name: "task", a: func() runtimeHostAdmission { a := valid; a.taskID = ""; return a }()},
		{name: "session", a: func() runtimeHostAdmission { a := valid; a.sessionID = "bad id"; return a }()},
		{name: "parent", a: func() runtimeHostAdmission { a := valid; a.parentTaskID = "bad:id"; return a }()},
		{name: "worker", a: func() runtimeHostAdmission { a := valid; a.workerID = "bad worker"; return a }()},
		{name: "store", a: func() runtimeHostAdmission { a := valid; a.store = nil; return a }()},
		{name: "callback", a: func() runtimeHostAdmission { a := valid; a.commitFirst = nil; return a }()},
		{name: "continuation", r: Request{ContinueTaskID: "old"}, a: valid},
		{name: "compaction", r: Request{Compaction: &sessions.CompactionRequest{}}, a: valid},
		{name: "summary", r: Request{SummaryAttemptID: "summary"}, a: valid},
		{name: "retry", r: Request{retryOfTaskID: "retry"}, a: valid},
		{name: "submission", r: Request{submissionID: "submitted"}, a: valid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := withRuntimeHostAdmission(test.r, test.a); !errors.Is(err, ErrAdmission) {
				t.Fatal("malformed host admission accepted", err)
			}
		})
	}
}

func TestRuntimeHostAdmissionRequiresConfiguredDatabaseAndSingleTaskPolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configuredPath := filepath.Join(dir, "configured.db")
	otherPath := filepath.Join(dir, "copied.db")
	store, err := telemetry.Open(ctx, configuredPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	other, err := telemetry.Open(ctx, otherPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	makeRequest := func(bound *telemetry.Store) Request {
		r, bindErr := withRuntimeHostAdmission(Request{ModelID: "named"}, runtimeHostAdmission{taskID: "task", sessionID: "session", parentTaskID: "parent", workerID: "worker", store: bound,
			commitFirst: func(context.Context, runtime.Event) error { return nil }})
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		return r
	}
	cfg := config.Settings{Telemetry: config.Telemetry{Database: configuredPath}}
	if err = validateRuntimeHostStore(ctx, cfg, makeRequest(store)); err != nil {
		t.Fatal("exact configured store rejected", err)
	}
	if err = validateRuntimeHostStore(ctx, cfg, makeRequest(other)); !errors.Is(err, ErrAdmission) {
		t.Fatal("different database admitted", err)
	}
	r := makeRequest(store)
	r.Compaction = &sessions.CompactionRequest{}
	if err = validateRuntimeHostStore(ctx, cfg, r); !errors.Is(err, ErrAdmission) {
		t.Fatal("mutated multi-task request admitted", err)
	}
	r = makeRequest(store)
	cfg.Workers.DelegateModel = "child"
	if err = validateRuntimeHostStore(ctx, cfg, r); !errors.Is(err, ErrAdmission) {
		t.Fatal("delegating host request admitted", err)
	}
}
