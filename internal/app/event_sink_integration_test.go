package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type applicationSinkFixture struct {
	mu                     sync.Mutex
	path, secret           string
	events                 []runtime.Event
	last                   map[string]int64
	workParent             map[string]string
	failure                error
	workerTerminalReleased bool
}

func (s *applicationSinkFixture) Emit(ctx context.Context, event runtime.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		s.failure = errors.New("sink received an event after cancellation")
		return nil
	}
	body, err := event.Encode()
	if err != nil {
		s.failure = fmt.Errorf("invalid delivered event: %w", err)
		return nil
	}
	if strings.Contains(string(body), s.secret) {
		s.failure = errors.New("configured sink received an unredacted secret")
		return nil
	}
	if event.CorrelationID != event.TaskID || event.Sequence != s.last[event.TaskID]+1 {
		s.failure = fmt.Errorf("invalid task-local order for %s at %d", event.TaskID, event.Sequence)
		return nil
	}
	s.last[event.TaskID] = event.Sequence
	if event.Kind == runtime.TaskStarted && event.WorkerID != "" && event.Data.ParentTaskID != "" {
		s.workParent[event.TaskID] = event.Data.ParentTaskID
	}
	db, err := telemetry.OpenReadOnly(context.Background(), s.path)
	if err != nil {
		s.failure = fmt.Errorf("open committed journal: %w", err)
		return nil
	}
	stored, readErr := db.Read(context.Background(), event.TaskID, event.Sequence-1, 1)
	if readErr != nil || len(stored) != 1 {
		db.Close()
		s.failure = fmt.Errorf("read committed event: count=%d err=%v", len(stored), readErr)
		return nil
	}
	want, encodeErr := stored[0].Encode()
	if encodeErr != nil || !reflect.DeepEqual(body, want) {
		db.Close()
		s.failure = errors.New("configured sink event differs from committed event")
		return nil
	}
	if event.Kind == runtime.TaskCompleted && event.WorkerID != "" {
		parent := s.workParent[event.TaskID]
		if parent == "" {
			s.failure = errors.New("worker terminal missing remembered parent")
		}
		leases, leaseErr := db.InspectLeases(context.Background(), "delegation-"+parent)
		if leaseErr != nil || len(leases) != 0 {
			s.failure = fmt.Errorf("worker terminal preceded lease release: count=%d err=%v", len(leases), leaseErr)
		} else {
			s.workerTerminalReleased = true
		}
	}
	if err := db.Close(); err != nil && s.failure == nil {
		s.failure = fmt.Errorf("close committed journal: %w", err)
	}
	s.events = append(s.events, event)
	return nil
}

func (s *applicationSinkFixture) snapshot() ([]runtime.Event, error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runtime.Event(nil), s.events...), s.failure, s.workerTerminalReleased
}

func eventSinkDelegateService(t *testing.T, sink runtime.EventSink) (*Service, config.Settings) {
	t.Helper()
	var parentCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("decode provider request")
			return
		}
		if request.Model == "child" {
			fmt.Fprintln(w, `{"message":{"content":"delegated fixture-secret answer"},"done":true,"done_reason":"stop"}`)
			return
		}
		parentCalls++
		if parentCalls == 1 {
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"inspect fixture-secret privately","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"parent final"},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(server.Close)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 2
	cfg.Workers.DelegateModel = "child"
	cfg.Workers.DelegateMaxCalls = 1
	cfg.Workers.DelegateMaxCost = 0
	cfg.Hardware.Concurrent = "2"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "event-sink.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "SINK_FIXTURE_KEY"}}
	zero := 0.0
	for _, id := range []string{"parent", "child"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Provider: "local", Model: id, Locality: "local", RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	}
	svc, err := NewServiceWithContextEstimatorEvaluatorAndEventSink(cfg, func(string) string { return "fixture-secret" }, nil, nil, nil, nil, nil, nil, nil, nil, nil, sink)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	return svc, cfg
}

func TestConfiguredEventSinkIncludesDelegationWhileRunStreamRemainsRootOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sink := &applicationSinkFixture{secret: "fixture-secret", last: map[string]int64{}, workParent: map[string]string{}}
	svc, cfg := eventSinkDelegateService(t, sink)
	sink.path = cfg.Telemetry.Database
	var perCall []runtime.Event
	result, err := svc.RunStream(ctx, Request{ModelID: "parent", Prompt: "delegate fixture-secret"}, func(event runtime.Event) error {
		perCall = append(perCall, event)
		return nil
	})
	if err != nil || result.Text != "parent final" {
		t.Fatal(result, err)
	}
	events, sinkErr, leaseReleased := sink.snapshot()
	if sinkErr != nil || !leaseReleased {
		t.Fatal("configured sink invariant", sinkErr, leaseReleased)
	}
	if len(perCall) == 0 {
		t.Fatal("per-call stream received no root events")
	}
	for _, event := range perCall {
		if event.TaskID != result.TaskID {
			t.Fatal("per-call stream escaped the root journal", event.TaskID, result.TaskID)
		}
	}
	starts := map[string]runtime.Event{}
	for _, event := range events {
		if event.Kind == runtime.TaskStarted {
			starts[event.TaskID] = event
		}
	}
	if len(starts) != 3 {
		t.Fatal("configured sink did not receive root, work, and child journals", starts)
	}
	root, ok := starts[result.TaskID]
	if !ok || root.Data.ParentTaskID != "" || root.SessionID != result.TaskID {
		t.Fatal("invalid root correlation", root)
	}
	var work, child runtime.Event
	for _, event := range starts {
		switch {
		case event.WorkerID != "" && event.Data.ParentTaskID == result.TaskID:
			work = event
		case event.WorkerID == "" && event.Data.ParentTaskID != "":
			child = event
		}
	}
	if work.TaskID == "" || child.TaskID == "" || child.Data.ParentTaskID != work.TaskID {
		t.Fatal("invalid parent chain", work, child)
	}
	if work.SessionID != root.SessionID || child.SessionID == "" {
		t.Fatal("invalid session correlation", root.SessionID, work.SessionID, child.SessionID)
	}
}

type failOnChildStartSink struct {
	mu     sync.Mutex
	root   string
	work   string
	child  string
	calls  int
	failed bool
}

func (s *failOnChildStartSink) Emit(_ context.Context, event runtime.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("callback continued after failure")
	}
	s.calls++
	if event.Kind != runtime.TaskStarted {
		return nil
	}
	if event.Data.ParentTaskID == "" {
		s.root = event.TaskID
	} else if event.WorkerID != "" {
		s.work = event.TaskID
	} else {
		s.child = event.TaskID
		s.failed = true
		return errors.New("private configured sink failure")
	}
	return nil
}

func (s *failOnChildStartSink) state() (string, string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root, s.work, s.child, s.calls
}

func TestConfiguredEventSinkChildFailureCancelsAndReleasesWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sink := &failOnChildStartSink{}
	svc, cfg := eventSinkDelegateService(t, sink)
	result, err := svc.Run(ctx, Request{ModelID: "parent", Prompt: "delegate privately"})
	if !errors.Is(err, ErrEventDelivery) || strings.Contains(err.Error(), "private configured sink failure") {
		t.Fatal("sink failure was not sanitized", result, err)
	}
	root, work, child, calls := sink.state()
	if root == "" || work == "" || child == "" || root != result.TaskID || calls < 3 {
		t.Fatal("sink did not reach delegated child", root, work, child, calls, result.TaskID)
	}
	db, openErr := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	for _, id := range []string{root, work, child} {
		snapshot, inspectErr := db.TaskSnapshot(context.Background(), id)
		if inspectErr != nil || snapshot.State == "running" {
			t.Fatal("sink cancellation left running durable state", id, snapshot.State, inspectErr)
		}
	}
	leases, leaseErr := db.InspectLeases(context.Background(), "delegation-"+root)
	if leaseErr != nil || len(leases) != 0 {
		t.Fatal("sink cancellation left worker lease", len(leases), leaseErr)
	}
}

type failAtWorkerBoundarySink struct {
	mu     sync.Mutex
	target runtime.Kind
	root   string
	work   string
	child  string
	failed bool
}

func (s *failAtWorkerBoundarySink) Emit(_ context.Context, event runtime.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Kind == runtime.TaskStarted {
		switch {
		case event.Data.ParentTaskID == "":
			s.root = event.TaskID
		case event.WorkerID != "":
			s.work = event.TaskID
		case event.Data.ParentTaskID != "":
			s.child = event.TaskID
		}
	}
	if !s.failed && event.TaskID == s.work && event.Kind == s.target {
		s.failed = true
		return errors.New("private worker-boundary sink failure")
	}
	return nil
}

func (s *failAtWorkerBoundarySink) state() (root, work, child string, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root, s.work, s.child, s.failed
}

func TestConfiguredEventSinkWorkerBoundaryFailureFinalizesAndReleases(t *testing.T) {
	for _, target := range []runtime.Kind{runtime.EvaluationRecorded, runtime.WorkerCompleted} {
		t.Run(string(target), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			sink := &failAtWorkerBoundarySink{target: target}
			svc, cfg := eventSinkDelegateService(t, sink)
			result, err := svc.Run(ctx, Request{ModelID: "parent", Prompt: "delegate then stop at worker boundary"})
			if !errors.Is(err, ErrEventDelivery) || strings.Contains(err.Error(), "private worker-boundary sink failure") {
				t.Fatal("worker-boundary sink failure was not sanitized", result, err)
			}
			root, work, child, failed := sink.state()
			if !failed || root == "" || work == "" || child == "" || result.TaskID != root {
				t.Fatal("configured sink did not reach requested worker boundary", target, root, work, child, failed, result.TaskID)
			}
			db, openErr := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer db.Close()
			for label, id := range map[string]string{"parent": root, "work": work, "child": child} {
				snapshot, inspectErr := db.TaskSnapshot(context.Background(), id)
				if inspectErr != nil || snapshot.State == "running" {
					t.Fatal("sink failure left nonterminal task", target, label, id, snapshot.State, inspectErr)
				}
			}
			workSnapshot, inspectErr := db.TaskSnapshot(context.Background(), work)
			if inspectErr != nil || workSnapshot.State != "canceled" {
				t.Fatal("worker boundary did not produce a cancellation terminal", target, workSnapshot.State, inspectErr)
			}
			leases, leaseErr := db.InspectLeases(context.Background(), "delegation-"+root)
			if leaseErr != nil || len(leases) != 0 {
				t.Fatal("worker-boundary sink failure left a lease", target, len(leases), leaseErr)
			}
		})
	}
}
