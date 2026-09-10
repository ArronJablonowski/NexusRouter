package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func openExecutionAdmissionStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func budgetedStart(worker, task, session string, at time.Time, cost float64) runtime.Event {
	return runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: session, CorrelationID: task, WorkerID: worker,
		Sequence: 1, Time: at.UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ModelID: "worker-model", ProviderID: "worker-provider",
			ConfigID: strings.Repeat("b", 64), RouteEstimatedCost: &cost}}
}

func executionReservation(event runtime.Event, timeMS, tokens, costMicros int64, globalWIP, boardWIP int) workboard.ExecutionReservation {
	return workboard.ExecutionReservation{Version: 1, ModelID: event.Data.ModelID, ProviderID: event.Data.ProviderID,
		ConfigID: event.Data.ConfigID, TimeLimitMS: timeMS, TokenLimit: tokens, CostMicros: costMicros,
		GlobalWIPLimit: globalWIP, BoardWIPLimit: boardWIP}
}

func createReadyBudgetCard(t *testing.T, ctx context.Context, store *Store, now time.Time, suffix string, budget workboard.WorkBudget) (workboard.Card, string) {
	t.Helper()
	operator := workboard.Actor{ID: "operator-" + suffix, Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session-"+suffix, createBoardRequest("budget-board-"+suffix, "Budget "+suffix, ""), operator, now)
	if err != nil {
		t.Fatalf("create budget board %q: %v", suffix, err)
	}
	cards, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session-" + suffix, Actor: operator}})
	if err != nil {
		t.Fatalf("construct budget card service %q: %v", suffix, err)
	}
	createdCard, err := cards.CreateCard(ctx, workboard.CreateCardRequest{BoardID: created.BoardID, IdempotencyKey: "budget-card-" + suffix,
		ExpectedBoardRevision: 1, ExpectedGraphRevision: 1, Card: workboard.NewCard{Title: "Budget card " + suffix, Priority: "normal",
			Labels: []string{}, Dependencies: []string{}, Budget: budget, Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatalf("create budget card %q: %v", suffix, err)
	}
	moved, err := cards.MoveCard(ctx, workboard.MoveCardRequest{BoardID: created.BoardID, CardID: createdCard.ID,
		IdempotencyKey: "budget-ready-" + suffix, TargetState: workboard.Ready, ExpectedBoardRevision: 2,
		ExpectedCardRevision: 1, ExpectedLayoutRevision: 2})
	if err != nil {
		t.Fatalf("move budget card ready %q: %v", suffix, err)
	}
	return moved.Card, created.BoardID
}

func claimBudgetedStart(t *testing.T, ctx context.Context, store *Store, clock *time.Time, card workboard.Card, boardID string,
	event runtime.Event, reservation workboard.ExecutionReservation, key string,
) (workboard.OperationReceipt, error) {
	t.Helper()
	request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: key, ExpectedCardRevision: card.Revision,
		TaskID: event.TaskID, SessionID: event.SessionID}
	return atomicClaimService(t, store, event.WorkerID, clock).ClaimBudgetedTaskStart(ctx, event, request, reservation)
}

func TestExecutionAdmissionAtomicCommitReplayAndPresenceFence(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "atomic", workboardTestBudget())
	clock = card.UpdatedAt.Add(time.Second)
	event := budgetedStart("budget-worker", "budget-task", "budget-session", clock, .0005)
	reservation := executionReservation(event, 10_000, 2_000, 500, 4, 2)
	receipt, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, reservation, "budget-claim-atomic")
	if err != nil {
		t.Fatal(err)
	}
	var admissions, markers, claims int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_execution_admissions WHERE task_id=?`, event.TaskID).Scan(&admissions); err != nil ||
		store.db.QueryRow(`SELECT count(*) FROM workboard_task_start_claims WHERE task_id=?`, event.TaskID).Scan(&markers) != nil ||
		store.db.QueryRow(`SELECT count(*) FROM workboard_claims WHERE task_id=?`, event.TaskID).Scan(&claims) != nil ||
		admissions != 1 || markers != 1 || claims != 1 {
		t.Fatalf("admissions=%d markers=%d claims=%d err=%v", admissions, markers, claims, err)
	}
	replayed, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, reservation, "budget-claim-atomic")
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("replay=%+v want=%+v err=%v", replayed, receipt, err)
	}
	drifted := reservation
	drifted.ConfigID = strings.Repeat("c", 64)
	if _, err = claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, drifted, "budget-claim-atomic"); err == nil {
		t.Fatalf("reservation drift accepted: %v", err)
	}
	request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "budget-claim-atomic",
		ExpectedCardRevision: card.Revision, TaskID: event.TaskID, SessionID: event.SessionID}
	if _, err = atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("unbudgeted replay accepted: %v", err)
	}
}

func TestExecutionAdmissionRoundsPositiveFractionalMicroCostUp(t *testing.T) {
	cost := .0000001
	micros, err := routeCostMicros(&cost)
	if err != nil || micros != 1 {
		t.Fatalf("fractional micro-dollar reservation=%d err=%v", micros, err)
	}
	zero := 0.0
	if micros, err = routeCostMicros(&zero); err != nil || micros != 0 {
		t.Fatalf("zero-dollar reservation=%d err=%v", micros, err)
	}
}

func TestExecutionAdmissionFailsClosedWhenSettlementBindingGuardIsWeakened(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 18, 15, 0, 0, time.UTC)
	card, boardID := createReadyBudgetCard(t, ctx, store, clock, "binding-guard", workboardTestBudget())
	if _, err := store.db.Exec(`DROP TRIGGER workboard_execution_settlement_binding;
		CREATE TRIGGER workboard_execution_settlement_binding BEFORE INSERT ON workboard_execution_settlements
		WHEN 0 BEGIN SELECT RAISE(ABORT,'workboard execution settlement binding mismatch'); END`); err != nil {
		t.Fatal(err)
	}
	clock = card.UpdatedAt.Add(time.Second)
	event := budgetedStart("guard-worker", "guard-task", "guard-session", clock, .0005)
	_, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event,
		executionReservation(event, 1_000, 100, 500, 2, 1), "binding-guard-claim")
	if !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("weakened settlement binding guard admitted work: %v", err)
	}
	stored, readErr := store.GetCard(ctx, boardID, card.ID)
	var heads, admissions int
	if queryErr := store.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id=?`, event.TaskID).Scan(&heads); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := store.db.QueryRow(`SELECT count(*) FROM workboard_execution_admissions WHERE task_id=?`, event.TaskID).Scan(&admissions); queryErr != nil {
		t.Fatal(queryErr)
	}
	if readErr != nil || stored.State != workboard.Ready || stored.Revision != card.Revision || heads != 0 || admissions != 0 {
		t.Fatalf("guard failure changed state: card=%+v heads=%d admissions=%d err=%v", stored, heads, admissions, readErr)
	}
}

func TestExecutionAdmissionRejectsMissingOrDriftedAdmission(t *testing.T) {
	ctx := context.Background()
	t.Run("missing", func(t *testing.T) {
		store := openExecutionAdmissionStore(t, ctx)
		defer store.Close()
		clock := time.Date(2026, 9, 10, 18, 30, 0, 0, time.UTC)
		card, boardID := createReadyBudgetCard(t, ctx, store, clock, "missing", workboardTestBudget())
		clock = card.UpdatedAt.Add(time.Second)
		event := budgetedStart("legacy-worker", "legacy-task", "legacy-session", clock, .0005)
		request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "legacy-budget-claim", ExpectedCardRevision: card.Revision,
			TaskID: event.TaskID, SessionID: event.SessionID}
		if _, err := atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request); err != nil {
			t.Fatal(err)
		}
		reservation := executionReservation(event, 1_000, 100, 500, 2, 1)
		if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, reservation, "legacy-budget-claim"); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing admission accepted: %v", err)
		}
	})

	t.Run("canonical drift", func(t *testing.T) {
		store := openExecutionAdmissionStore(t, ctx)
		defer store.Close()
		clock := time.Date(2026, 9, 10, 18, 45, 0, 0, time.UTC)
		card, boardID := createReadyBudgetCard(t, ctx, store, clock, "drift", workboardTestBudget())
		clock = card.UpdatedAt.Add(time.Second)
		event := budgetedStart("drift-worker", "drift-task", "drift-session", clock, .0005)
		reservation := executionReservation(event, 1_000, 100, 500, 2, 1)
		if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, reservation, "drift-budget-claim"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`DROP TRIGGER workboard_execution_admission_immutable_update;
			UPDATE workboard_execution_admissions SET token_limit=101 WHERE task_id=?`, event.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, reservation, "drift-budget-claim"); !errors.Is(err, ErrWorkboardCorrupt) {
			t.Fatalf("drifted admission accepted: %v", err)
		}
	})
}

func TestExecutionAdmissionCapacityFailuresRollbackAllDomains(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		cost        float64
		reservation func(runtime.Event) workboard.ExecutionReservation
	}{
		{name: "route cost mismatch", cost: .0005, reservation: func(e runtime.Event) workboard.ExecutionReservation {
			return executionReservation(e, 1_000, 100, 499, 2, 1)
		}},
		{name: "zero bounded time", cost: .0005, reservation: func(e runtime.Event) workboard.ExecutionReservation {
			return executionReservation(e, 0, 100, 500, 2, 1)
		}},
		{name: "aggregate token bound", cost: .0005, reservation: func(e runtime.Event) workboard.ExecutionReservation {
			return executionReservation(e, 1_000, workboardTestBudget().TokenLimit+1, 500, 2, 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openExecutionAdmissionStore(t, ctx)
			defer store.Close()
			clock := time.Date(2026, 9, 10, 19, 0, 0, 0, time.UTC)
			card, boardID := createReadyBudgetCard(t, ctx, store, clock, strings.ReplaceAll(tc.name, " ", "-"), workboardTestBudget())
			clock = card.UpdatedAt.Add(time.Second)
			event := budgetedStart("rollback-worker", "rollback-"+strings.ReplaceAll(tc.name, " ", "-"), "rollback-session", clock, tc.cost)
			if _, err := claimBudgetedStart(t, ctx, store, &clock, card, boardID, event, tc.reservation(event), "rollback-budget-claim"); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLimitExceeded}) &&
				!errors.Is(err, &workboard.Violation{Code: workboard.CodeInvalid}) {
				t.Fatalf("capacity failure=%v", err)
			}
			var heads, claims, markers, admissions int
			for query, target := range map[string]*int{
				`SELECT count(*) FROM task_heads WHERE task_id='` + event.TaskID + `'`:                     &heads,
				`SELECT count(*) FROM workboard_claims WHERE task_id='` + event.TaskID + `'`:               &claims,
				`SELECT count(*) FROM workboard_task_start_claims WHERE task_id='` + event.TaskID + `'`:    &markers,
				`SELECT count(*) FROM workboard_execution_admissions WHERE task_id='` + event.TaskID + `'`: &admissions,
			} {
				if err := store.db.QueryRow(query).Scan(target); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := store.GetCard(ctx, boardID, card.ID)
			if err != nil || heads+claims+markers+admissions != 0 || stored.State != workboard.Ready || stored.Revision != card.Revision {
				t.Fatalf("heads=%d claims=%d markers=%d admissions=%d card=%+v err=%v", heads, claims, markers, admissions, stored, err)
			}
		})
	}
}

func TestExecutionAdmissionAllowsExactZeroCostAndEnforcesBoardWIP(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
	first, boardID := createReadyBudgetCard(t, ctx, store, clock, "zero-cost-wip", workboardTestBudget())
	operator := workboard.Actor{ID: "operator-zero-cost-wip", Type: "operator"}
	cards, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session-zero-cost-wip", Actor: operator}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadWorkboard(ctx, boardID, workboard.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	created, err := cards.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "second-zero-cost-card",
		ExpectedBoardRevision: snapshot.Board.Revision, ExpectedGraphRevision: snapshot.GraphRevision,
		Card: workboard.NewCard{Title: "Second", Priority: "normal", Labels: []string{}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.ReadWorkboard(ctx, boardID, workboard.BoardSnapshotOptions{Limit: 100})
	second, err := cards.MoveCard(ctx, workboard.MoveCardRequest{BoardID: boardID, CardID: created.ID, IdempotencyKey: "second-zero-cost-ready",
		TargetState: workboard.Ready, ExpectedBoardRevision: snapshot.Board.Revision, ExpectedCardRevision: created.Revision,
		ExpectedLayoutRevision: snapshot.Board.LayoutRevision})
	if err != nil {
		t.Fatal(err)
	}
	clock = second.Card.UpdatedAt.Add(time.Second)
	firstEvent := budgetedStart("zero-worker-a", "zero-task-a", "zero-session-a", clock, 0)
	if _, err = claimBudgetedStart(t, ctx, store, &clock, first, boardID, firstEvent,
		executionReservation(firstEvent, 1_000, 100, 0, 2, 1), "zero-cost-claim-a"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	secondEvent := budgetedStart("zero-worker-b", "zero-task-b", "zero-session-b", clock, 0)
	if _, err = claimBudgetedStart(t, ctx, store, &clock, second.Card, boardID, secondEvent,
		executionReservation(secondEvent, 1_000, 100, 0, 2, 1), "zero-cost-claim-b"); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLimitExceeded}) {
		t.Fatalf("board WIP admitted: %v", err)
	}
	var admissions int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_execution_admissions`).Scan(&admissions); err != nil || admissions != 1 {
		t.Fatalf("admissions=%d err=%v", admissions, err)
	}
	stored, err := store.GetCard(ctx, boardID, second.Card.ID)
	if err != nil || stored.State != workboard.Ready {
		t.Fatalf("second card=%+v err=%v", stored, err)
	}
}

func TestExecutionAdmissionEnforcesGlobalWIPAcrossBoards(t *testing.T) {
	ctx := context.Background()
	store := openExecutionAdmissionStore(t, ctx)
	defer store.Close()
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	first, firstBoard := createReadyBudgetCard(t, ctx, store, clock, "global-a", workboardTestBudget())
	second, secondBoard := createReadyBudgetCard(t, ctx, store, clock.Add(time.Second), "global-b", workboardTestBudget())
	clock = second.UpdatedAt.Add(time.Second)
	firstEvent := budgetedStart("global-worker-a", "global-task-a", "global-session-a", clock, .0005)
	if _, err := claimBudgetedStart(t, ctx, store, &clock, first, firstBoard, firstEvent,
		executionReservation(firstEvent, 1_000, 100, 500, 1, 1), "global-budget-claim-a"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	secondEvent := budgetedStart("global-worker-b", "global-task-b", "global-session-b", clock, .0005)
	if _, err := claimBudgetedStart(t, ctx, store, &clock, second, secondBoard, secondEvent,
		executionReservation(secondEvent, 1_000, 100, 500, 1, 1), "global-budget-claim-b"); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLimitExceeded}) {
		t.Fatalf("global WIP admitted: %v", err)
	}
	stored, err := store.GetCard(ctx, secondBoard, second.ID)
	if err != nil || stored.State != workboard.Ready {
		t.Fatalf("second card=%+v err=%v", stored, err)
	}
}

func TestExecutionAdmissionGlobalWIPContentionHasOneAtomicWinner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	firstStore, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	clock := time.Date(2026, 9, 10, 12, 30, 0, 0, time.UTC)
	first, firstBoard := createReadyBudgetCard(t, ctx, firstStore, clock, "race-a", workboardTestBudget())
	second, secondBoard := createReadyBudgetCard(t, ctx, firstStore, clock.Add(time.Second), "race-b", workboardTestBudget())
	secondStore, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	clock = second.UpdatedAt.Add(time.Second)
	type contender struct {
		store                             *Store
		card                              workboard.Card
		board, worker, task, session, key string
	}
	contenders := []contender{
		{firstStore, first, firstBoard, "race-worker-a", "race-task-a", "race-session-a", "race-budget-claim-a"},
		{secondStore, second, secondBoard, "race-worker-b", "race-task-b", "race-session-b", "race-budget-claim-b"},
	}
	errorsOut := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, contender := range contenders {
		service := atomicClaimService(t, contender.store, contender.worker, &clock)
		event := budgetedStart(contender.worker, contender.task, contender.session, clock, .0005)
		reservation := executionReservation(event, 1_000, 100, 500, 1, 1)
		request := workboard.ClaimRequest{BoardID: contender.board, CardID: contender.card.ID, IdempotencyKey: contender.key,
			ExpectedCardRevision: contender.card.Revision, TaskID: event.TaskID, SessionID: event.SessionID}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, callErr := service.ClaimBudgetedTaskStart(ctx, event, request, reservation)
			errorsOut <- callErr
		}()
	}
	close(start)
	wg.Wait()
	close(errorsOut)
	winners, limited := 0, 0
	for callErr := range errorsOut {
		if callErr == nil {
			winners++
		} else if errors.Is(callErr, &workboard.Violation{Code: workboard.CodeLimitExceeded}) {
			limited++
		} else {
			t.Fatalf("unexpected contender error: %v", callErr)
		}
	}
	var admissions, heads, claims int
	if firstStore.db.QueryRow(`SELECT count(*) FROM workboard_execution_admissions`).Scan(&admissions) != nil ||
		firstStore.db.QueryRow(`SELECT count(*) FROM task_heads`).Scan(&heads) != nil ||
		firstStore.db.QueryRow(`SELECT count(*) FROM workboard_claims`).Scan(&claims) != nil ||
		winners != 1 || limited != 1 || admissions != 1 || heads != 1 || claims != 1 {
		t.Fatalf("winners=%d limited=%d admissions=%d heads=%d claims=%d", winners, limited, admissions, heads, claims)
	}
}
