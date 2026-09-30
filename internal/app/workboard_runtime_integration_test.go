package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func TestWorkboardRuntimeBindingRunsOneRealJournalWithoutSyntheticTask(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "workboard-runtime.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	defer store.Close()

	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected provider path %q", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		providerCalls.Add(1)
		events, eventErr := store.Read(r.Context(), "board-real-runtime-task", 0, 10)
		states, stateErr := store.ReadCardLifecycleSnapshots(r.Context(), boardID, []string{cardID})
		state := states[cardID]
		if eventErr != nil || stateErr != nil || len(events) != 2 || events[0].Kind != runtime.TaskStarted ||
			events[1].Kind != runtime.TurnStarted || state.Attempt == nil || state.Attempt.Claim == nil ||
			state.Attempt.Claim.State != "active" {
			t.Errorf("provider ran before durable start/claim: events=%+v state=%+v errors=%v %v", events, state, eventErr, stateErr)
		}
		fmt.Fprintln(w, `{"message":{"content":"real candidate"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = database
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "worker-model", Model: "worker-model", Provider: "local", Locality: "local",
		Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 100}}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	var sinkStarts atomic.Int32
	svc.eventSink = runtime.EventSinkFunc(func(sinkCtx context.Context, event runtime.Event) error {
		if event.Kind != runtime.TaskStarted {
			return nil
		}
		sinkStarts.Add(1)
		events, eventErr := store.Read(sinkCtx, event.TaskID, 0, 10)
		states, stateErr := store.ReadCardLifecycleSnapshots(sinkCtx, boardID, []string{cardID})
		state := states[cardID]
		if eventErr != nil || stateErr != nil || len(events) < 1 || events[0].ID != event.ID ||
			state.Attempt == nil || state.Attempt.Claim == nil || state.Attempt.Claim.State != "active" {
			return fmt.Errorf("sink observed uncommitted start/claim: events=%+v state=%+v errors=%v %v", events, state, eventErr, stateErr)
		}
		return nil
	})
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	evaluator := &capturingWorkboardEvaluator{}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, evaluator, strings.Repeat("3", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID,
		TaskID: "board-real-runtime-task", SessionID: "board-real-runtime-session", ParentTaskID: "board-real-parent",
		Scope: "board-card-" + cardID, ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			request, bindErr := handle.BindRuntimeRequest(Request{ModelID: "worker-model", Prompt: "produce the candidate"})
			if bindErr != nil {
				return WorkboardCandidate{}, bindErr
			}
			result, runErr := svc.Run(run, request)
			return WorkboardCandidate{Summary: result.Text, ArtifactRefs: []string{"task://" + result.TaskID}}, runErr
		},
		Validate: func(_ context.Context, candidate WorkboardCandidate) error {
			if candidate.Summary != "real candidate" {
				return fmt.Errorf("unexpected candidate %q", candidate.Summary)
			}
			return nil
		},
	})
	if err != nil || candidate.Summary != "real candidate" || providerCalls.Load() != 1 || sinkStarts.Load() != 1 {
		t.Fatalf("candidate=%+v provider_calls=%d sink_starts=%d err=%v", candidate, providerCalls.Load(), sinkStarts.Load(), err)
	}
	page, err := store.ListTasks(ctx, sessions.TaskListOptions{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].TaskID != "board-real-runtime-task" ||
		page.Items[0].SessionID != "board-real-runtime-session" || page.Items[0].State != "completed" {
		t.Fatalf("synthetic or incorrect runtime task: page=%+v err=%v", page, err)
	}
	events, err := store.Read(ctx, "board-real-runtime-task", 0, 100)
	if err != nil || len(events) < 4 || events[0].Kind != runtime.TaskStarted || events[len(events)-1].Kind != runtime.TaskCompleted {
		t.Fatalf("real runtime journal unavailable: events=%+v err=%v", events, err)
	}
	workerID := events[0].WorkerID
	if workerID == "" || events[0].Data.ParentTaskID != "board-real-parent" {
		t.Fatalf("host identity missing from start: %+v", events[0])
	}
	for _, event := range events {
		if event.WorkerID != workerID || event.TaskID != "board-real-runtime-task" || event.SessionID != "board-real-runtime-session" {
			t.Fatalf("runtime identity drifted: %+v", event)
		}
	}
	states, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	state := states[cardID]
	if err != nil || state.Attempt == nil || state.Attempt.Claim == nil || state.Attempt.Candidate == nil ||
		state.Attempt.WorkerID != workerID || state.Attempt.Claim.OwnerID != workerID ||
		state.Attempt.Claim.TaskID != "board-real-runtime-task" || state.Attempt.Claim.State != "released" ||
		state.Attempt.Candidate.Summary != "real candidate" {
		t.Fatalf("workboard/runtime binding incorrect: state=%+v err=%v", state, err)
	}
}

func readyWorkboardCardAt(t *testing.T, database string) (*telemetry.Store, string, string, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	title := "Runtime integration"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "runtime-integration-board", Title: &title})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	cardTitle := "Run one real model task"
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Integration passes", Required: true}}
	card, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
		IdempotencyKey: "runtime-integration-card", BoardID: board.BoardID, Title: &cardTitle, Criteria: criteria,
		ExpectedBoardRevision: revisionPointer(board.BoardRevision), ExpectedGraphRevision: revisionPointer(1)})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	snapshot, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	ready, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardMove,
		IdempotencyKey: "runtime-integration-ready", BoardID: board.BoardID, CardID: card.CardID, TargetState: "ready",
		ExpectedBoardRevision: revisionPointer(snapshot.Board.Revision), ExpectedLayoutRevision: revisionPointer(snapshot.Board.LayoutRevision),
		ExpectedCardRevision: card.CardRevision})
	if err != nil || ready.CardRevision == nil {
		store.Close()
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	return store, board.BoardID, card.CardID, *ready.CardRevision
}
