package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestConfiguredWorkboardTaskFactoryBuildsBoundedRouteWithoutDispatch(t *testing.T) {
	var calls atomic.Int32
	var outputCeiling atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Options struct {
				NumPredict int64 `json:"num_predict"`
			} `json:"options"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		outputCeiling.Store(request.Options.NumPredict)
		if len(request.Tools) != 0 {
			t.Error("host-bound Workboard execution inherited recursive delegation tools")
		}
		fmt.Fprintln(w, `{"message":{"content":"factory candidate"},"prompt_eval_count":8192,"eval_count":8192,"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	settings := configuredWorkerFactorySettings(server.URL)
	settings.Workers.DelegateModel, settings.Workers.DelegateMaxCost = "worker", 1
	database := filepath.Join(t.TempDir(), "factory.db")
	settings.Telemetry.Database = database
	service, err := NewService(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), TotalRAM: 100, AvailableRAM: 100}, nil
	}
	store, boardID, cardID, revision := readyFactoryCard(t, database, workboard.WorkBudget{
		AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 100_000, CostMicros: 1_000_000})
	defer store.Close()
	factory, err := service.ConfiguredWorkboardTaskFactory(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	item := workboard.SupervisionItem{Version: 1, BoardID: boardID, CardID: cardID, CardRevision: revision,
		State: workboard.SupervisionReady, Reason: workboard.SupervisionDependenciesSatisfied,
		Actions: workboard.SupervisionActions{Claim: true}}
	task, err := factory.BuildWorkboardTask(context.Background(), item)
	if err != nil || calls.Load() != 0 || task.Reservation == nil {
		t.Fatalf("task=%+v calls=%d err=%v", task, calls.Load(), err)
	}
	wantConfig, _ := settingsConfigID(settings)
	if task.Reservation.ModelID != "worker-native" || task.Reservation.ProviderID != "local" ||
		task.Reservation.ConfigID != wantConfig || task.Reservation.TimeLimitMS != 59_000 ||
		task.Reservation.TokenLimit != 73_728 || task.MaxOutputTokens != 8_192 || task.Reservation.CostMicros != 200_000 ||
		task.Reservation.GlobalWIPLimit != settings.Workers.Max || task.Reservation.BoardWIPLimit != settings.Workboard.Scheduler.MaxActiveClaims {
		t.Fatalf("wrong frozen reservation: %+v", task.Reservation)
	}
	// These fields are deliberately owned by WorkboardScheduler rather than the
	// injected factory. Mirror that authority step for this direct runner test.
	task.BoardID, task.CardID, task.ExpectedCardRevision = boardID, cardID, revision
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("a", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	invalid := task
	invalid.MaxOutputTokens = invalid.Reservation.TokenLimit + 1
	if _, err = runner.Run(context.Background(), invalid); !errors.Is(err, ErrAdmission) || calls.Load() != 0 {
		t.Fatalf("output ceiling above total reservation admitted: calls=%d err=%v", calls.Load(), err)
	}
	candidate, err := runner.Run(context.Background(), task)
	if err != nil || candidate.Summary != "factory candidate" || calls.Load() != 1 || outputCeiling.Load() != 8_192 {
		events, eventErr := store.Read(context.Background(), task.TaskID, 0, 10)
		lifecycle, lifecycleErr := store.ReadCardLifecycleSnapshots(context.Background(), boardID, []string{cardID})
		t.Fatalf("candidate=%+v calls=%d output_ceiling=%d err=%v events=%+v event_err=%v lifecycle=%+v lifecycle_err=%v", candidate, calls.Load(), outputCeiling.Load(), err, events, eventErr, lifecycle, lifecycleErr)
	}
	projection, err := store.ReadWorkboardBudgetProjection(context.Background(), boardID, cardID)
	if err != nil || projection.RemainingTokens != 83_616 || projection.RemainingCostMicros != 800_000 ||
		projection.RemainingTimeMS <= 0 || projection.RemainingTimeMS >= 60_000 {
		t.Fatalf("settled projection=%+v err=%v", projection, err)
	}
}

func TestConfiguredWorkboardTaskFactoryRejectsUnboundedOrReviewerStarvedCard(t *testing.T) {
	for name, budget := range map[string]workboard.WorkBudget{
		"unbounded": {AttemptLimit: 3},
		"time":      {AttemptLimit: 3, TimeLimitMS: 1_000, TokenLimit: 20_000, CostMicros: 1_000_000},
		"tokens":    {AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 1_000, CostMicros: 1_000_000},
		"cost":      {AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 20_000, CostMicros: 299_999},
	} {
		t.Run(name, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "factory.db")
			settings := configuredWorkerFactorySettings("http://127.0.0.1:11434")
			settings.Telemetry.Database = database
			service, serviceErr := NewService(settings, nil)
			if serviceErr != nil {
				t.Fatal(serviceErr)
			}
			store, boardID, cardID, revision := readyFactoryCard(t, database, budget)
			defer store.Close()
			factory, factoryErr := service.ConfiguredWorkboardTaskFactory(context.Background(), store)
			if factoryErr != nil {
				t.Fatal(factoryErr)
			}
			item := workboard.SupervisionItem{Version: 1, BoardID: boardID, CardID: cardID, CardRevision: revision,
				State: workboard.SupervisionReady, Reason: workboard.SupervisionDependenciesSatisfied,
				Actions: workboard.SupervisionActions{Claim: true}}
			if _, buildErr := factory.BuildWorkboardTask(context.Background(), item); buildErr == nil {
				t.Fatal("card without positive worker-and-review capacity was accepted")
			}
		})
	}
}

func TestConfiguredWorkboardTaskFactoryAllowsExactZeroCostLocalWorker(t *testing.T) {
	database := filepath.Join(t.TempDir(), "factory.db")
	settings := configuredWorkerFactorySettings("http://127.0.0.1:11434")
	settings.Telemetry.Database = database
	*settings.Models[0].EstimatedCost = 0
	service, err := NewService(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, boardID, cardID, revision := readyFactoryCard(t, database, workboard.WorkBudget{
		AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 100_000, CostMicros: 100_000})
	defer store.Close()
	factory, err := service.ConfiguredWorkboardTaskFactory(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	item := workboard.SupervisionItem{Version: 1, BoardID: boardID, CardID: cardID, CardRevision: revision,
		State: workboard.SupervisionReady, Reason: workboard.SupervisionDependenciesSatisfied,
		Actions: workboard.SupervisionActions{Claim: true}}
	task, err := factory.BuildWorkboardTask(context.Background(), item)
	if err != nil || task.Reservation == nil || task.Reservation.CostMicros != 0 {
		t.Fatalf("zero-cost local reservation rejected: task=%+v err=%v", task, err)
	}
}

func configuredWorkerFactorySettings(endpoint string) config.Settings {
	settings := config.Defaults()
	workerCost, reviewerCost := .2, .1
	settings.Mode = "local_only"
	settings.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: endpoint}}
	settings.Models = []config.Model{
		{ID: "worker", Provider: "local", Model: "worker-native", Locality: "local", ContextTokens: 8192, EstimatedCost: &workerCost, RAMBytes: 1, Capabilities: []string{"chat"}},
		{ID: "reviewer", Provider: "local", Model: "reviewer-native", Locality: "local", ContextTokens: 8192, EstimatedCost: &reviewerCost, RAMBytes: 1, Capabilities: []string{"audit"}},
	}
	settings.Workboard.Scheduler.Enabled = true
	settings.Workboard.Scheduler.MaxActiveClaims = 2
	settings.Workboard.Scheduler.WorkerModel = "worker"
	settings.Workboard.Scheduler.AcceptanceJudge = config.WorkboardAcceptanceJudge{Enabled: true, ReviewerModel: "reviewer",
		MaxCost: .1, MaxInputTokens: 800, MaxOutputTokens: 200, Timeout: "1s"}
	return settings
}

func readyFactoryCard(t *testing.T, database string, budget workboard.WorkBudget) (*telemetry.Store, string, string, int64) {
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
	boardTitle := "Factory board"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "factory-board-create", Title: &boardTitle})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	cardTitle, description := "Build production task", "Return a tested implementation."
	contractBudget := webui.WorkBudget{AttemptLimit: budget.AttemptLimit, TimeLimitMS: budget.TimeLimitMS,
		TokenLimit: budget.TokenLimit, CostMicros: budget.CostMicros}
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Focused tests pass", Required: true}}
	card, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
		IdempotencyKey: "factory-card-create", BoardID: board.BoardID, Title: &cardTitle, Description: &description,
		Budget: &contractBudget, Criteria: criteria, ExpectedBoardRevision: revisionPointer(board.BoardRevision), ExpectedGraphRevision: revisionPointer(1)})
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
		IdempotencyKey: "factory-card-ready", BoardID: board.BoardID, CardID: card.CardID, TargetState: "ready",
		ExpectedBoardRevision: revisionPointer(snapshot.Board.Revision), ExpectedLayoutRevision: revisionPointer(snapshot.Board.LayoutRevision),
		ExpectedCardRevision: card.CardRevision})
	if err != nil || ready.CardRevision == nil {
		store.Close()
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	return store, board.BoardID, card.CardID, *ready.CardRevision
}
