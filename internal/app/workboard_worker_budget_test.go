package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestWorkboardWorkerRunnerPersistsBudgetedAdmissionAndSettlement(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "budgeted-runner.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	defer store.Close()
	runner := budgetedWorkboardRunner(t, store)
	const (
		taskID    = "budgeted-runner-task"
		sessionID = "budgeted-runner-session"
		workerID  = "budgeted-runner-worker"
		modelID   = "coordinator-model"
		provider  = "coordinator-provider"
	)
	reservation := workboard.ExecutionReservation{Version: 1, ModelID: modelID, ProviderID: provider,
		ConfigID: strings.Repeat("6", 64), TimeLimitMS: 5_000, TokenLimit: 2_000, CostMicros: 500,
		GlobalWIPLimit: 2, BoardWIPLimit: 1}
	cost := .0005
	got, err := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: taskID,
		SessionID: sessionID, WorkerID: workerID, Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, Reservation: &reservation, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			started := time.Now().UTC()
			if err := bindBudgetedWorkboardRuntime(run, handle, modelID, provider, reservation.ConfigID, cost, started); err != nil {
				return WorkboardCandidate{}, err
			}
			completed := runtime.Event{Version: 1, ID: taskID + "-completed", TaskID: taskID, SessionID: sessionID,
				CorrelationID: taskID, WorkerID: workerID, Sequence: 2, Time: started.Add(time.Millisecond),
				Kind: runtime.TaskCompleted}
			if err := store.Append(run, 1, completed); err != nil {
				return WorkboardCandidate{}, err
			}
			return WorkboardCandidate{Summary: "budgeted candidate"}, nil
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if err != nil || got.Summary != "budgeted candidate" {
		t.Fatalf("candidate=%+v err=%v", got, err)
	}
	assertBudgetRows(t, database, taskID, 1, 1)
	events, err := store.Read(ctx, taskID, 0, 10)
	if err != nil || len(events) != 2 || events[0].Data.ModelID != modelID ||
		events[0].Data.ProviderID != provider || events[0].Data.RouteEstimatedCost == nil ||
		*events[0].Data.RouteEstimatedCost != cost {
		t.Fatalf("budgeted runtime journal=%+v err=%v", events, err)
	}
}

func TestWorkboardWorkerRunnerRejectsReservationRouteMismatchAtomically(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "route-mismatch.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	defer store.Close()
	runner := budgetedWorkboardRunner(t, store)
	reservation := workboard.ExecutionReservation{Version: 1, ModelID: "reserved-model", ProviderID: "reserved-provider",
		ConfigID: strings.Repeat("7", 64), TimeLimitMS: 5_000, TokenLimit: 1_000, CostMicros: 500,
		GlobalWIPLimit: 2, BoardWIPLimit: 1}
	var meaningful atomic.Bool
	_, err := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "route-mismatch-task",
		SessionID: "route-mismatch-session", WorkerID: "route-mismatch-worker", Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, Reservation: &reservation, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			if bindErr := bindBudgetedWorkboardRuntime(run, handle, "different-model", "reserved-provider", reservation.ConfigID, .0005, time.Now().UTC()); bindErr != nil {
				return WorkboardCandidate{}, bindErr
			}
			meaningful.Store(true)
			return WorkboardCandidate{Summary: "must not escape"}, nil
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if err == nil || meaningful.Load() {
		t.Fatalf("route mismatch crossed admission boundary: meaningful=%v err=%v", meaningful.Load(), err)
	}
	assertUnstartedBudgetedCard(t, store, boardID, cardID, "route-mismatch-task")
	assertBudgetRows(t, database, "route-mismatch-task", 0, 0)
}

func TestWorkboardWorkerRunnerTimeLimitBeforeTaskStartLeavesNoLifecycle(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "prestart-timeout.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	defer store.Close()
	runner := budgetedWorkboardRunner(t, store)
	reservation := workboard.ExecutionReservation{Version: 1, ModelID: "timeout-model", ProviderID: "timeout-provider",
		ConfigID: strings.Repeat("8", 64), TimeLimitMS: 10, TokenLimit: 1_000, CostMicros: 500,
		GlobalWIPLimit: 2, BoardWIPLimit: 1}
	_, err := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "prestart-timeout-task",
		SessionID: "prestart-timeout-session", WorkerID: "prestart-timeout-worker", Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, Reservation: &reservation, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, _ *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			<-run.Done()
			return WorkboardCandidate{}, run.Err()
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("time-limited callback err=%v", err)
	}
	assertUnstartedBudgetedCard(t, store, boardID, cardID, "prestart-timeout-task")
	assertBudgetRows(t, database, "prestart-timeout-task", 0, 0)
}

func budgetedWorkboardRunner(t *testing.T, store *telemetry.Store) *WorkboardWorkerRunner {
	t.Helper()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("9", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func bindBudgetedWorkboardRuntime(ctx context.Context, handle *WorkboardWorkerHandle, model, provider, configID string, cost float64, at time.Time) error {
	request, err := handle.BindRuntimeRequest(Request{})
	if err != nil || request.runtimeHostAdmission == nil {
		return errors.Join(ErrAdmission, err)
	}
	admission := request.runtimeHostAdmission
	return admission.commit(ctx, runtime.Event{Version: 1, ID: admission.taskID + "-started", TaskID: admission.taskID,
		SessionID: admission.sessionID, CorrelationID: admission.taskID, WorkerID: admission.workerID,
		Sequence: 1, Time: at.UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: admission.parentTaskID,
			ModelID: model, ProviderID: provider, ConfigID: configID, RouteEstimatedCost: &cost}})
}

func assertUnstartedBudgetedCard(t *testing.T, store *telemetry.Store, boardID, cardID, taskID string) {
	t.Helper()
	ctx := context.Background()
	card, cardErr := store.GetCard(ctx, boardID, cardID)
	snapshots, snapshotErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	events, eventErr := store.Read(ctx, taskID, 0, 10)
	if cardErr != nil || snapshotErr != nil || eventErr != nil || card.State != workboard.Ready ||
		card.CurrentAttemptID != "" || card.CurrentClaimID != "" || snapshots[cardID].Attempt != nil || len(events) != 0 {
		t.Fatalf("failed admission changed durable lifecycle: card=%+v snapshot=%+v events=%+v errors=%v/%v/%v",
			card, snapshots[cardID], events, cardErr, snapshotErr, eventErr)
	}
}

func assertBudgetRows(t *testing.T, database, taskID string, admissions, settlements int) {
	t.Helper()
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var gotAdmissions, gotSettlements int
	if err = db.QueryRow(`SELECT count(*) FROM workboard_execution_admissions WHERE task_id=?`, taskID).Scan(&gotAdmissions); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM workboard_execution_settlements WHERE task_id=?`, taskID).Scan(&gotSettlements); err != nil {
		t.Fatal(err)
	}
	if gotAdmissions != admissions || gotSettlements != settlements {
		t.Fatalf("budget rows admissions=%d settlements=%d want=%d/%d", gotAdmissions, gotSettlements, admissions, settlements)
	}
}
