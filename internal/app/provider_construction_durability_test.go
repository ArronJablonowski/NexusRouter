package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type constructionDurabilityProvider struct {
	streams *atomic.Int32
}

func (*constructionDurabilityProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}

func (p *constructionDurabilityProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	if p.streams != nil {
		p.streams.Add(1)
	}
	return emit(providers.Chunk{Text: "reusable response", Done: true, FinishReason: "stop"})
}

func constructionDurabilityEvents(t *testing.T, cfg config.Settings, task string) []runtime.Event {
	t.Helper()
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func assertConstructionFailureJournal(t *testing.T, events []runtime.Event) {
	t.Helper()
	if len(events) != 3 || events[0].Kind != runtime.TaskStarted || events[1].Kind != runtime.TurnStarted || events[2].Kind != runtime.TaskFailed || events[2].Data.Code != "execution_failed" {
		t.Fatalf("unexpected construction-failure journal: %+v", events)
	}
	for _, event := range events {
		body, err := event.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "private construction marker") {
			t.Fatal("private construction failure entered durable history")
		}
	}
}

func TestTaskStartedSinkFailurePreventsExecutionProviderConstruction(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			var builds, streams, starts atomic.Int32
			factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
				if connection.Purpose != providers.PurposeExecution {
					t.Fatalf("explicit execution used provider purpose %q", connection.Purpose)
				}
				builds.Add(1)
				return &constructionDurabilityProvider{streams: &streams}, nil
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			if err = installEventSink(svc, runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error {
				if event.Kind != runtime.TaskStarted {
					t.Fatalf("sink invoked after its terminal failure: %s", event.Kind)
				}
				starts.Add(1)
				if mode == "panic" {
					panic("private construction marker")
				}
				return errors.New("private construction marker")
			})); err != nil {
				t.Fatal(err)
			}

			result, runErr := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "durable sink boundary"})
			if result.TaskID == "" || runErr == nil || !errors.Is(runErr, ErrEventDelivery) {
				t.Fatal("sink failure was not surfaced", result, runErr)
			}
			if strings.Contains(runErr.Error(), "private construction marker") || builds.Load() != 0 || streams.Load() != 0 || starts.Load() != 1 {
				t.Fatal("sink failure crossed construction boundary", runErr, builds.Load(), streams.Load(), starts.Load())
			}
			events := constructionDurabilityEvents(t, cfg, result.TaskID)
			if len(events) != 2 || events[0].Kind != runtime.TaskStarted || events[1].Kind != runtime.TaskCanceled || events[1].Data.Code != "canceled" {
				t.Fatalf("sink failure did not durably cancel: %+v", events)
			}

			if err = installEventSink(svc, nil); err != nil {
				t.Fatal(err)
			}
			reused, reuseErr := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "reuse after sink failure"})
			if reuseErr != nil || reused.Text != "reusable response" || builds.Load() != 1 || streams.Load() != 1 {
				t.Fatal("service was not reusable", reused, reuseErr, builds.Load(), streams.Load())
			}
		})
	}
}

func TestTaskStartedPersistenceFailurePreventsExecutionProviderConstruction(t *testing.T) {
	fixture, cfg := autoFixture(t)
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`CREATE TRIGGER reject_task_start BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='task.started' BEGIN SELECT RAISE(ABORT,'private start failure'); END`); err != nil {
		t.Fatal(err)
	}
	var builds, streams atomic.Int32
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return &constructionDurabilityProvider{streams: &streams}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	result, runErr := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "reject the durable start"})
	if result.TaskID == "" || !errors.Is(runErr, runtime.ErrPersistence) || strings.Contains(runErr.Error(), "private start failure") || builds.Load() != 0 || streams.Load() != 0 {
		t.Fatal("failed start crossed execution construction boundary", result, runErr, builds.Load(), streams.Load())
	}
	var count int
	if err = raw.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=?`, result.TaskID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed task start persisted partial history", count, err)
	}
}

func TestExecutionProviderConstructionFailuresAreDurableAndServiceReusable(t *testing.T) {
	for _, mode := range []string{"error", "panic", "typed_nil"} {
		t.Run(mode, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			var builds, streams atomic.Int32
			factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
				if connection.Purpose != providers.PurposeExecution {
					t.Fatalf("explicit execution used provider purpose %q", connection.Purpose)
				}
				if builds.Add(1) > 1 {
					return &constructionDurabilityProvider{streams: &streams}, nil
				}
				switch mode {
				case "error":
					return nil, errors.New("private construction marker")
				case "panic":
					panic("private construction marker")
				default:
					var provider *constructionDurabilityProvider
					return provider, nil
				}
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile

			failed, runErr := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "construct after start"})
			if failed.TaskID == "" || runErr == nil || strings.Contains(runErr.Error(), "private construction marker") || streams.Load() != 0 {
				t.Fatal("construction failure was not contained", failed, runErr, streams.Load())
			}
			assertConstructionFailureJournal(t, constructionDurabilityEvents(t, cfg, failed.TaskID))

			reused, reuseErr := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "reuse after construction failure"})
			if reuseErr != nil || reused.Text != "reusable response" || builds.Load() != 2 || streams.Load() != 1 {
				t.Fatal("service was not reusable", reused, reuseErr, builds.Load(), streams.Load())
			}
		})
	}
}

func TestExecutionProviderConstructionCancellationIsDurable(t *testing.T) {
	fixture, cfg := autoFixture(t)
	entered := make(chan struct{})
	var builds, streams atomic.Int32
	var wrongPurpose atomic.Bool
	factory := applicationProviderFactory(func(ctx context.Context, connection providers.Connection) (providers.Provider, error) {
		if connection.Purpose != providers.PurposeExecution {
			wrongPurpose.Store(true)
			return nil, errors.New("unexpected provider purpose")
		}
		builds.Add(1)
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := svc.Run(ctx, Request{ModelID: "a", Prompt: "cancel cooperative construction"})
		done <- outcome{result: result, err: runErr}
	}()
	<-entered
	cancel()
	got := <-done
	if got.result.TaskID == "" || !errors.Is(got.err, context.Canceled) || builds.Load() != 1 || streams.Load() != 0 || wrongPurpose.Load() {
		t.Fatal("construction cancellation was not preserved", got.result, got.err, builds.Load(), streams.Load())
	}
	events := constructionDurabilityEvents(t, cfg, got.result.TaskID)
	if len(events) != 3 || events[0].Kind != runtime.TaskStarted || events[1].Kind != runtime.TurnStarted || events[2].Kind != runtime.TaskCanceled || events[2].Data.Code != "canceled" {
		t.Fatalf("unexpected canceled construction journal: %+v", events)
	}
}

func TestAutomaticDiscoveryBuildIsDistinctFromFailedExecutionBuild(t *testing.T) {
	fixture, cfg := autoFixture(t)
	var discoveryBuilds, executionBuilds, streams atomic.Int32
	factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		switch connection.Purpose {
		case providers.PurposeDiscovery:
			discoveryBuilds.Add(1)
			return &constructionDurabilityProvider{}, nil
		case providers.PurposeExecution:
			executionBuilds.Add(1)
			return nil, errors.New("private construction marker")
		default:
			t.Fatalf("unexpected provider purpose %q", connection.Purpose)
			return nil, errors.New("unexpected provider purpose")
		}
	})
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	result, runErr := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "separate discovery from execution"})
	if result.TaskID == "" || runErr == nil || strings.Contains(runErr.Error(), "private construction marker") || discoveryBuilds.Load() == 0 || executionBuilds.Load() != 1 || streams.Load() != 0 {
		t.Fatal("provider purposes were not separated", result, runErr, discoveryBuilds.Load(), executionBuilds.Load(), streams.Load())
	}
	events := constructionDurabilityEvents(t, cfg, result.TaskID)
	if len(events) != 4 || events[0].Kind != runtime.TaskStarted || events[1].Kind != runtime.RouteSelected || events[2].Kind != runtime.TurnStarted || events[3].Kind != runtime.TaskFailed || events[3].Data.Code != "execution_failed" {
		t.Fatalf("unexpected automatic construction-failure journal: %+v", events)
	}
}
