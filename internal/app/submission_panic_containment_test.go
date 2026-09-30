package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const dispatcherPanicSecret = "private-dispatcher-panic-value"

type containmentProvider struct{}

func (containmentProvider) Models(context.Context) ([]string, error) {
	return []string{"fixture"}, nil
}

func (containmentProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
}

func awaitDispatcherHealth(t *testing.T, ctx context.Context, d *Dispatcher, status, code string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		check := d.Health()
		if check.Status == status && check.Code == code {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("dispatcher health did not reach", status, code, check)
		case <-ticker.C:
		}
	}
}

func assertPanicValueNotPersisted(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE instr(body, ?) > 0`, dispatcherPanicSecret).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("panic value entered durable events")
	}
}

func TestDispatcherContainsRootPanicAndRecoversOnlyAfterLeaseExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, _, _ := recoveryFixture(t)
	zero := 0.0
	s.settings.Models[0].ContextTokens = 8192
	s.settings.Models[0].EstimatedCost = &zero
	s.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return containmentProvider{}, nil
	})
	var clockCalls atomic.Int32
	s.now = func() time.Time {
		if clockCalls.Add(1) == 1 {
			panic(dispatcherPanicSecret)
		}
		return time.Now()
	}

	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.renewInterval = 2 * time.Millisecond
	d.mu.Unlock()
	defer d.Close()
	first, err := s.Submit(ctx, "panic-root-key-0001", Request{ModelID: "auto", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	awaitDispatcherHealth(t, ctx, d, "degraded", "supervisor_error")
	panicked, err := s.SubmissionStatus(ctx, first.ID)
	if err != nil || panicked.State != "running" || len(panicked.TaskIDs) != 0 || panicked.LeaseExpiresAt == nil || panicked.Result != nil || panicked.ErrorCode != "" {
		t.Fatal("panic claim was terminalized or lost", panicked, err)
	}
	leaseExpiry := *panicked.LeaseExpiresAt
	time.Sleep(20 * time.Millisecond)
	stillFenced, err := s.SubmissionStatus(ctx, first.ID)
	if err != nil || stillFenced.LeaseExpiresAt == nil || !stillFenced.LeaseExpiresAt.Equal(leaseExpiry) {
		t.Fatal("panic heartbeat continued after containment", stillFenced, err, leaseExpiry)
	}

	second, err := s.Submit(ctx, "panic-root-key-0002", Request{ModelID: "chat", Prompt: "independent"})
	if err != nil {
		t.Fatal(err)
	}
	awaitSubmission(t, ctx, s, second.ID, "succeeded")
	stillFenced, err = s.SubmissionStatus(ctx, first.ID)
	if err != nil || stillFenced.State != "running" || len(stillFenced.TaskIDs) != 0 {
		t.Fatal("panic claim was redispatched before lease recovery", stillFenced, err)
	}

	expireRecoveryClaim(t, s, first.ID)
	if _, err := d.recoverPage(ctx, s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	recovered := awaitSubmission(t, ctx, s, first.ID, "succeeded")
	if recovered.Result == nil || recovered.Result.Text != "answer" || len(recovered.TaskIDs) != 1 {
		t.Fatal("lease recovery did not safely re-run undispatched work", recovered)
	}
	assertPanicValueNotPersisted(t, s.settings.Telemetry.Database)
	if err := d.Close(); !errors.Is(err, ErrSubmission) {
		t.Fatal("contained panic did not remain visible in supervisor health", err)
	}
}

type delegatedPanicProvider struct {
	mu          sync.Mutex
	parentCalls int
}

func (*delegatedPanicProvider) Models(context.Context) ([]string, error) {
	return []string{"parent", "child"}, nil
}

func (p *delegatedPanicProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	if request.Model == "child" {
		panic(dispatcherPanicSecret)
	}
	p.mu.Lock()
	p.parentCalls++
	call := p.parentCalls
	p.mu.Unlock()
	if call == 1 {
		return emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "delegate-call", Name: "delegate", Arguments: []byte(`{"prompt":"bounded child","validation":"text"}`)}, Done: true, FinishReason: "tool_calls"})
	}
	return emit(providers.Chunk{Text: "parent answer", Done: true, FinishReason: "stop"})
}

func TestSubmittedDelegatedChildPanicDoesNotStopDispatcher(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 2
	cfg.Workers.DelegateModel = "child"
	cfg.Workers.DelegateMaxCalls = 1
	cfg.Hardware.Concurrent = "2"
	cfg.Telemetry.Database = t.TempDir() + "/delegated-panic.db"
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	for _, id := range []string{"parent", "child"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Provider: "local", Model: id, Locality: "local", RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	}
	provider := &delegatedPanicProvider{}
	s, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return provider, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	s.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), CPUs: 4, TotalRAM: 1024, AvailableRAM: 1024}, nil
	}
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	first, err := s.Submit(ctx, "panic-child-key-001", Request{ModelID: "parent", Prompt: "delegate"})
	if err != nil {
		t.Fatal(err)
	}
	completed := awaitSubmission(t, ctx, s, first.ID, "succeeded")
	if completed.Result == nil || completed.Result.Text != "parent answer" {
		t.Fatal("parent did not receive bounded child rejection", completed)
	}

	store, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	childFailed := false
	for _, task := range completed.TaskIDs {
		events, readErr := store.Read(ctx, task, 0, 100)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(events) > 0 && events[0].Kind == runtime.TaskStarted && events[0].Data.ModelID == "child" && events[len(events)-1].Kind == runtime.TaskFailed {
			childFailed = true
		}
	}
	if !childFailed {
		t.Fatal("delegated provider panic did not produce a bounded failed child", completed.TaskIDs)
	}
	second, err := s.Submit(ctx, "panic-child-key-002", Request{ModelID: "parent", Prompt: "independent"})
	if err != nil {
		t.Fatal(err)
	}
	awaitSubmission(t, ctx, s, second.ID, "succeeded")
	assertPanicValueNotPersisted(t, cfg.Telemetry.Database)
}
