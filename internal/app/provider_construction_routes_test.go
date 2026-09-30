package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type constructionRouteTracker struct {
	mu       sync.Mutex
	path     string
	starts   []runtime.Event
	claimed  map[string]bool
	terminal map[string]runtime.Event
}

func newConstructionRouteTracker(path string) *constructionRouteTracker {
	return &constructionRouteTracker{path: path, claimed: map[string]bool{}, terminal: map[string]runtime.Event{}}
}

func (t *constructionRouteTracker) Emit(_ context.Context, event runtime.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if event.Kind == runtime.TaskStarted {
		t.starts = append(t.starts, event)
	}
	if event.Kind == runtime.TaskCompleted || event.Kind == runtime.TaskFailed || event.Kind == runtime.TaskCanceled {
		t.terminal[event.TaskID] = event
	}
	return nil
}

func (t *constructionRouteTracker) claim(tst *testing.T) runtime.Event {
	tst.Helper()
	t.mu.Lock()
	var start runtime.Event
	for i := len(t.starts) - 1; i >= 0; i-- {
		candidate := t.starts[i]
		// Supervisor work journals have no provider execution of their own.
		if candidate.WorkerID == "" && !t.claimed[candidate.TaskID] {
			start = candidate
			t.claimed[candidate.TaskID] = true
			break
		}
	}
	t.mu.Unlock()
	if start.TaskID == "" {
		tst.Error("execution construction had no unclaimed task start")
		return runtime.Event{}
	}
	db, err := telemetry.OpenReadOnly(context.Background(), t.path)
	if err != nil {
		tst.Error("open durable task start", err)
		return runtime.Event{}
	}
	defer db.Close()
	events, err := db.Read(context.Background(), start.TaskID, 0, 1)
	if err != nil || len(events) != 1 || events[0].Kind != runtime.TaskStarted || events[0].ID != start.ID {
		tst.Error("execution construction preceded its committed task start", events, err)
		return runtime.Event{}
	}
	return start
}

func (t *constructionRouteTracker) terminalRecorded(task string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.terminal[task]
	return ok
}

func (t *constructionRouteTracker) startCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.starts)
}

type constructionRouteProvider struct {
	test     *testing.T
	start    runtime.Event
	tracker  *constructionRouteTracker
	fallback bool
}

func (*constructionRouteProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}

func (p *constructionRouteProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	if p.start.Data.ModelID != request.Model {
		p.test.Errorf("execution build for task %s bound model %q, streamed %q", p.start.TaskID, p.start.Data.ModelID, request.Model)
	}
	if p.fallback && request.Model == "a" {
		return &providers.Failure{Code: "unavailable", Retryable: true}
	}
	return emit(providers.Chunk{Text: "route answer", Done: true, FinishReason: "stop"})
}

type constructionDiscoveryProvider struct{}

func (constructionDiscoveryProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}
func (constructionDiscoveryProvider) Stream(context.Context, providers.Request, func(providers.Chunk) error) error {
	return errors.New("discovery provider cannot execute")
}

func TestAutomaticAndFallbackExecutionConstructionFollowOwnDurableStarts(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "fallback"}[fallback], func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			tracker := newConstructionRouteTracker(cfg.Telemetry.Database)
			var discoveries, executions atomic.Int32
			var firstTask string
			factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
				switch connection.Purpose {
				case providers.PurposeDiscovery:
					discoveries.Add(1)
					return constructionDiscoveryProvider{}, nil
				case providers.PurposeExecution:
					start := tracker.claim(t)
					if executions.Add(1) == 1 {
						firstTask = start.TaskID
					} else if !tracker.terminalRecorded(firstTask) {
						t.Error("fallback construction preceded the primary terminal event")
					}
					return &constructionRouteProvider{test: t, start: start, tracker: tracker, fallback: fallback}, nil
				default:
					t.Errorf("unexpected provider purpose %q", connection.Purpose)
					return nil, errors.New("unexpected purpose")
				}
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			if err := installEventSink(svc, tracker); err != nil {
				t.Fatal(err)
			}
			result, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "route through durable construction"})
			wantExecutions, wantStarts := int32(1), 1
			if fallback {
				wantExecutions, wantStarts = 2, 2
			}
			if err != nil || result.Text != "route answer" || result.TaskID == "" || discoveries.Load() == 0 || executions.Load() != wantExecutions || tracker.startCount() != wantStarts {
				t.Fatal("route construction boundary failed", result, err, discoveries.Load(), executions.Load(), tracker.startCount())
			}
			if fallback && (len(result.PreviousTaskIDs) != 1 || result.PreviousTaskIDs[0] != firstTask) {
				t.Fatal("fallback lineage mismatch", result.PreviousTaskIDs, firstTask)
			}
		})
	}
}

func TestDelegatedChildExecutionConstructionFollowsChildStart(t *testing.T) {
	tracker := newConstructionRouteTracker("")
	svc, cfg := eventSinkDelegateService(t, tracker)
	tracker.path = cfg.Telemetry.Database
	var executions atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(ctx context.Context, connection providers.Connection) (providers.Provider, error) {
		if connection.Purpose != providers.PurposeExecution {
			t.Errorf("unexpected delegated provider purpose %q", connection.Purpose)
			return nil, errors.New("unexpected purpose")
		}
		start := tracker.claim(t)
		executions.Add(1)
		provider, err := providers.NewHTTPWithTimeout(connection.Endpoint, connection.Kind, connection.APIKey, connection.Transport, connection.Timeout)
		if err != nil {
			return nil, err
		}
		return &constructionHTTPProvider{Provider: provider, test: t, start: start}, nil
	})
	result, err := svc.Run(context.Background(), Request{ModelID: "parent", Prompt: "delegate after durable starts"})
	if err != nil || result.Text != "parent final" || executions.Load() != 2 {
		t.Fatal("delegated construction boundary failed", result, err, executions.Load())
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	claimedModels := map[string]bool{}
	for _, start := range tracker.starts {
		if tracker.claimed[start.TaskID] {
			claimedModels[start.Data.ModelID] = true
		}
	}
	if !claimedModels["parent"] || !claimedModels["child"] || len(claimedModels) != 2 {
		t.Fatal("root/child execution starts were not claimed exactly", claimedModels)
	}
}

type constructionHTTPProvider struct {
	providers.Provider
	test  *testing.T
	start runtime.Event
}

func (p *constructionHTTPProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	if p.start.Data.ModelID != request.Model {
		p.test.Errorf("delegated execution build for %s bound %q, streamed %q", p.start.TaskID, p.start.Data.ModelID, request.Model)
	}
	return p.Provider.Stream(ctx, request, emit)
}

func TestChildStartSinkFailurePreventsChildExecutionConstruction(t *testing.T) {
	sink := &failOnChildStartSink{}
	svc, cfg := eventSinkDelegateService(t, sink)
	var executions atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		if connection.Purpose != providers.PurposeExecution {
			return nil, errors.New("unexpected purpose")
		}
		executions.Add(1)
		return providers.NewHTTPWithTimeout(connection.Endpoint, connection.Kind, connection.APIKey, connection.Transport, connection.Timeout)
	})
	result, err := svc.Run(context.Background(), Request{ModelID: "parent", Prompt: "stop before child construction"})
	if !errors.Is(err, ErrEventDelivery) || executions.Load() != 1 {
		t.Fatal("child provider constructed across failed start delivery", result, err, executions.Load())
	}
	root, work, child, _ := sink.state()
	if root == "" || work == "" || child == "" {
		t.Fatal("fixture did not reach child start", root, work, child)
	}
	db, openErr := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	for _, task := range []string{root, work, child} {
		snapshot, inspectErr := db.TaskSnapshot(context.Background(), task)
		if inspectErr != nil || snapshot.State == "running" {
			t.Fatal("failed child start left running state", task, snapshot.State, inspectErr)
		}
	}
}

type failOnSecondRootStart struct {
	mu     sync.Mutex
	starts int
}

func (s *failOnSecondRootStart) Emit(_ context.Context, event runtime.Event) error {
	if event.Kind != runtime.TaskStarted || event.WorkerID != "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	if s.starts == 2 {
		return errors.New("private fallback start sink failure")
	}
	return nil
}

func (s *failOnSecondRootStart) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

func TestFallbackStartSinkFailurePreventsFallbackExecutionConstruction(t *testing.T) {
	fixture, cfg := autoFixture(t)
	sink := &failOnSecondRootStart{}
	var executions atomic.Int32
	factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		switch connection.Purpose {
		case providers.PurposeDiscovery:
			return constructionDiscoveryProvider{}, nil
		case providers.PurposeExecution:
			executions.Add(1)
			return &constructionRouteProvider{test: t, start: runtime.Event{Data: runtime.Data{ModelID: "a"}}, fallback: true}, nil
		default:
			return nil, errors.New("unexpected purpose")
		}
	})
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	if err := installEventSink(svc, sink); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "fail fallback start delivery"})
	if !errors.Is(err, ErrEventDelivery) || executions.Load() != 1 || sink.count() != 2 || result.TaskID == "" {
		t.Fatal("fallback construction crossed failed start sink", result, err, executions.Load(), sink.count())
	}
}

type failConstructionBoundarySink struct {
	kind runtime.Kind
}

func (s failConstructionBoundarySink) Emit(_ context.Context, event runtime.Event) error {
	if event.Kind == s.kind {
		return errors.New("private construction boundary sink failure")
	}
	return nil
}

func assertConstructionBoundaryKinds(t *testing.T, cfgPath, task string, want ...runtime.Kind) {
	t.Helper()
	db, err := telemetry.OpenReadOnly(context.Background(), cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(want) {
		t.Fatalf("unexpected boundary journal length: got=%+v want=%v", events, want)
	}
	for i := range want {
		if events[i].Kind != want[i] {
			t.Fatalf("unexpected boundary journal at %d: got=%+v want=%v", i, events, want)
		}
	}
}

func TestExecutionConstructionWaitsForDeliveredRouteAndTurnBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		model string
		fail  runtime.Kind
		want  []runtime.Kind
	}{
		{name: "automatic route", model: "auto", fail: runtime.RouteSelected, want: []runtime.Kind{runtime.TaskStarted, runtime.RouteSelected, runtime.TaskCanceled}},
		{name: "explicit turn", model: "a", fail: runtime.TurnStarted, want: []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TaskCanceled}},
		{name: "automatic turn", model: "auto", fail: runtime.TurnStarted, want: []runtime.Kind{runtime.TaskStarted, runtime.RouteSelected, runtime.TurnStarted, runtime.TaskCanceled}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			var discoveries, builds atomic.Int32
			factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
				switch connection.Purpose {
				case providers.PurposeDiscovery:
					discoveries.Add(1)
					return constructionDiscoveryProvider{}, nil
				case providers.PurposeExecution:
					builds.Add(1)
					return &constructionDurabilityProvider{}, nil
				default:
					return nil, errors.New("unexpected provider purpose")
				}
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			if err = installEventSink(svc, failConstructionBoundarySink{kind: test.fail}); err != nil {
				t.Fatal(err)
			}
			result, runErr := svc.Run(context.Background(), Request{ModelID: test.model, Prompt: "stop before execution construction"})
			if result.TaskID == "" || !errors.Is(runErr, ErrEventDelivery) || builds.Load() != 0 {
				t.Fatal("execution crossed failed durable delivery boundary", result, runErr, builds.Load())
			}
			if test.model == "auto" && discoveries.Load() == 0 {
				t.Fatal("automatic fixture never performed control-plane discovery")
			}
			assertConstructionBoundaryKinds(t, cfg.Telemetry.Database, result.TaskID, test.want...)
		})
	}
}

func TestTurnStartPersistenceFailurePreventsExecutionConstruction(t *testing.T) {
	for _, model := range []string{"a", "auto"} {
		t.Run(model, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			store, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err = raw.Exec(`CREATE TRIGGER reject_turn_start BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='turn.started' BEGIN SELECT RAISE(ABORT,'private turn start failure'); END`); err != nil {
				t.Fatal(err)
			}
			var builds atomic.Int32
			factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
				if connection.Purpose == providers.PurposeDiscovery {
					return constructionDiscoveryProvider{}, nil
				}
				if connection.Purpose != providers.PurposeExecution {
					return nil, errors.New("unexpected provider purpose")
				}
				builds.Add(1)
				return &constructionDurabilityProvider{}, nil
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			result, runErr := svc.Run(context.Background(), Request{ModelID: model, Prompt: "reject turn start"})
			if result.TaskID == "" || !errors.Is(runErr, runtime.ErrPersistence) || builds.Load() != 0 {
				t.Fatal("execution crossed failed turn commit", result, runErr, builds.Load())
			}
			want := []runtime.Kind{runtime.TaskStarted}
			if model == "auto" {
				want = append(want, runtime.RouteSelected)
			}
			assertConstructionBoundaryKinds(t, cfg.Telemetry.Database, result.TaskID, want...)
		})
	}
}

func TestOwnedCodexLaunchWaitsForTurnStartDeliveryAndCommit(t *testing.T) {
	for _, mode := range []string{"sink", "commit"} {
		t.Run(mode, func(t *testing.T) {
			cfg := codexTaskConfig(t)
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			var launches atomic.Int32
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				launches.Add(1)
				return nil, errors.New("launcher must remain inert")
			}
			if mode == "sink" {
				if err = installEventSink(svc, failConstructionBoundarySink{kind: runtime.TurnStarted}); err != nil {
					t.Fatal(err)
				}
			} else {
				store, openErr := telemetry.Open(context.Background(), cfg.Telemetry.Database)
				if openErr != nil {
					t.Fatal(openErr)
				}
				if openErr = store.Close(); openErr != nil {
					t.Fatal(openErr)
				}
				raw, openErr := sql.Open("sqlite", cfg.Telemetry.Database)
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer raw.Close()
				if _, err = raw.Exec(`CREATE TRIGGER reject_codex_turn_start BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='turn.started' BEGIN SELECT RAISE(ABORT,'private codex turn start failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			result, runErr := svc.Run(context.Background(), Request{ModelID: "brain", Prompt: "do not launch before turn start"})
			if result.TaskID == "" || launches.Load() != 0 {
				t.Fatal("owned Codex launched across turn boundary failure", result, runErr, launches.Load())
			}
			if mode == "sink" {
				if !errors.Is(runErr, ErrEventDelivery) {
					t.Fatal("sink failure not surfaced", runErr)
				}
				assertConstructionBoundaryKinds(t, cfg.Telemetry.Database, result.TaskID, runtime.TaskStarted, runtime.TurnStarted, runtime.TaskCanceled)
			} else {
				if !errors.Is(runErr, runtime.ErrPersistence) {
					t.Fatal("commit failure not surfaced", runErr)
				}
				assertConstructionBoundaryKinds(t, cfg.Telemetry.Database, result.TaskID, runtime.TaskStarted)
			}
		})
	}
}
