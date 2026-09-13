package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type panicWorkboardAcceptanceReconciler struct{}

func (panicWorkboardAcceptanceReconciler) ReconcileBoard(context.Context, string, int) (workboard.AcceptanceReconciliationResult, error) {
	panic("injected acceptance reconciler panic")
}

func TestWorkboardSchedulerContainsAcceptanceReconcilerPanic(t *testing.T) {
	scheduler, err := NewWorkboardScheduler(&schedulerReaderStub{}, &schedulerFactoryStub{}, &schedulerRunnerStub{},
		WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	scheduler.acceptance = panicWorkboardAcceptanceReconciler{}
	if _, err = scheduler.RunCycle(context.Background(), "board-a"); !errors.Is(err, ErrWorkboardSchedule) {
		t.Fatalf("panic error=%v", err)
	}
}

// This reproduces the narrow crash window after the candidate/review commit
// and before the coordinator call. Restart must decide from durable evidence
// without re-entering task construction, worker inference, or model review.
func TestConfiguredSchedulerRestartReconcilesDurableReviewWithoutRedispatch(t *testing.T) {
	var workerCalls, reviewerCalls atomic.Int32
	workerRequests := make(chan workboardE2EProviderRequest, 2)
	workerServer := httptest.NewServer(countWorkboardRequests(&workerCalls,
		workboardE2EOllamaHandler(t, workerRequests, "restart candidate", 40, 10)))
	defer workerServer.Close()
	reviewRequests := make(chan workboardE2EProviderRequest, 2)
	reviewServer := httptest.NewServer(countWorkboardRequests(&reviewerCalls,
		workboardE2EOllamaHandler(t, reviewRequests, workboardE2EAuditResponse(t), 20, 5)))
	defer reviewServer.Close()

	database := filepath.Join(t.TempDir(), "acceptance-restart.db")
	settings := configuredWorkboardE2ESettings(database, workerServer.URL, reviewServer.URL)
	service := configuredWorkboardRestartService(t, settings)
	store, boardID, cardID, _ := readyFactoryCard(t, database, workboard.WorkBudget{
		AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 50_000, CostMicros: 1_000_000})
	ctx := context.Background()
	plan, err := prepareConfiguredWorkboardSchedule(ctx, service, store, time.Now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	first := plan.scheduler.(*WorkboardScheduler)
	// Simulate process death at the exact boundary: submission is allowed to
	// commit, but neither the runner nor scheduler can coordinate afterward.
	first.acceptance = nil
	first.runner.(*WorkboardWorkerRunner).acceptance = nil
	if result, runErr := first.RunCycle(ctx, boardID); runErr != nil || result.Launched != 1 || result.Succeeded != 1 {
		store.Close()
		t.Fatalf("first cycle=%+v err=%v", result, runErr)
	}
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil || card.State != workboard.Review || workerCalls.Load() != 1 || reviewerCalls.Load() != 1 {
		store.Close()
		t.Fatalf("pre-restart card=%+v calls=%d/%d err=%v", card, workerCalls.Load(), reviewerCalls.Load(), err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := prepareConfiguredWorkboardSchedule(ctx, configuredWorkboardRestartService(t, settings), reopened, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.scheduler.RunCycle(ctx, boardID)
	if err != nil || result.Launched != 0 || result.Ready != 0 {
		t.Fatalf("restart cycle=%+v err=%v", result, err)
	}
	card, err = reopened.GetCard(ctx, boardID, cardID)
	if err != nil || card.State != workboard.Done || workerCalls.Load() != 1 || reviewerCalls.Load() != 1 {
		t.Fatalf("post-restart card=%+v calls=%d/%d err=%v", card, workerCalls.Load(), reviewerCalls.Load(), err)
	}
}

func configuredWorkboardRestartService(t *testing.T, settings config.Settings) *Service {
	t.Helper()
	service, err := NewService(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), TotalRAM: 100, AvailableRAM: 100}, nil
	}
	return service
}

func countWorkboardRequests(calls *atomic.Int32, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		next.ServeHTTP(w, r)
	})
}
