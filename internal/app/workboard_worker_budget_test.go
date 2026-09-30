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

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workboard"
	"github.com/ArronJablonowski/NexusRouter/workers"
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
			turnID, attemptID := "budgeted-turn", "budgeted-model-attempt"
			events := []runtime.Event{
				{Version: 1, ID: taskID + "-turn", TaskID: taskID, SessionID: sessionID, CorrelationID: taskID,
					WorkerID: workerID, TurnID: turnID, AttemptID: attemptID, Sequence: 2, Time: started.Add(time.Millisecond),
					Kind: runtime.TurnStarted, Data: runtime.Data{ModelID: modelID, ProviderID: provider}},
				{Version: 1, ID: taskID + "-turn-completed", TaskID: taskID, SessionID: sessionID, CorrelationID: taskID,
					WorkerID: workerID, TurnID: turnID, AttemptID: attemptID, Sequence: 3, Time: started.Add(2 * time.Millisecond),
					Kind: runtime.TurnCompleted, Data: runtime.Data{Text: "budgeted candidate", ModelID: modelID, ProviderID: provider,
						Usage: &providers.Usage{InputTokens: 2, OutputTokens: 2}, FinishReason: "stop"}},
				{Version: 1, ID: taskID + "-completed", TaskID: taskID, SessionID: sessionID, CorrelationID: taskID,
					WorkerID: workerID, TurnID: turnID, AttemptID: attemptID, Sequence: 4, Time: started.Add(3 * time.Millisecond),
					Kind: runtime.TaskCompleted},
			}
			for expected, event := range events {
				if err := store.Append(run, int64(expected+1), event); err != nil {
					return WorkboardCandidate{}, err
				}
			}
			return WorkboardCandidate{Summary: "budgeted candidate"}, nil
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if err != nil || got.Summary != "budgeted candidate" {
		t.Fatalf("candidate=%+v err=%v", got, err)
	}
	assertBudgetRows(t, database, taskID, 1, 1)
	events, err := store.Read(ctx, taskID, 0, 10)
	if err != nil || len(events) != 4 || events[0].Data.ModelID != modelID ||
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

func TestWorkboardWorkerRunnerTimeLimitAfterTaskStartSettlesCanceledRuntime(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "poststart-timeout.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	defer store.Close()
	runner := budgetedWorkboardRunner(t, store)
	const taskID = "poststart-timeout-task"
	reservation := workboard.ExecutionReservation{Version: 1, ModelID: "timeout-model", ProviderID: "timeout-provider",
		ConfigID: strings.Repeat("8", 64), TimeLimitMS: 250, TokenLimit: 1_000, CostMicros: 500,
		GlobalWIPLimit: 2, BoardWIPLimit: 1}
	_, err := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: taskID,
		SessionID: "poststart-timeout-session", WorkerID: "poststart-timeout-worker", Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, Reservation: &reservation, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			started := time.Now().UTC()
			if bindErr := bindBudgetedWorkboardRuntime(run, handle, reservation.ModelID, reservation.ProviderID,
				reservation.ConfigID, .0005, started); bindErr != nil {
				return WorkboardCandidate{}, bindErr
			}
			<-run.Done()
			terminal := runtime.Event{Version: 1, ID: taskID + "-canceled", TaskID: taskID,
				SessionID: "poststart-timeout-session", CorrelationID: taskID, WorkerID: "poststart-timeout-worker",
				Sequence: 2, Time: time.Now().UTC(), Kind: runtime.TaskCanceled, Data: runtime.Data{Code: "canceled"}}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(run), time.Second)
			defer cancel()
			if appendErr := store.Append(cleanup, 1, terminal); appendErr != nil {
				return WorkboardCandidate{}, appendErr
			}
			return WorkboardCandidate{}, run.Err()
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("time-limited callback err=%v", err)
	}
	assertBudgetRows(t, database, taskID, 1, 1)
	lifecycle, readErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	attempt := lifecycle[cardID].Attempt
	if readErr != nil || attempt == nil || attempt.Claim == nil || attempt.State != "failed" || attempt.Claim.State != "released" {
		t.Fatalf("released canceled attempt=%+v err=%v", lifecycle, readErr)
	}
	card, readErr := store.GetCard(ctx, boardID, cardID)
	if readErr != nil || card.State != workboard.Ready || card.CurrentClaimID != "" {
		t.Fatalf("released canceled card=%+v err=%v", card, readErr)
	}
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
	wantTokens := int64(0)
	if handle.reservation != nil {
		wantTokens = handle.reservation.TokenLimit
	}
	if admission.maxOutputTokens != wantTokens {
		return ErrAdmission
	}
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
