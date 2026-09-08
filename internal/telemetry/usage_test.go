package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func usageTask(t *testing.T, s *Store, task string, retry bool, usage *providers.Usage, terminal runtime.Kind) accounting.Record {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	start := runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: "session", CorrelationID: task, Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ProviderID: "local", ModelID: "model:latest"}}
	if retry {
		start.Data.RetryOfTaskID = "prior-task"
	}
	turnStart := runtime.Event{Version: 1, ID: task + "-turn-start", TaskID: task, SessionID: "session", CorrelationID: task, Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "candidate", Data: runtime.Data{ProviderID: "local", ModelID: "model:latest"}}
	turnDone := turnStart
	turnDone.ID, turnDone.Sequence, turnDone.Time, turnDone.Kind, turnDone.Data.Usage = task+"-turn-done", 3, now.Add(2*time.Second), runtime.TurnCompleted, usage
	done := runtime.Event{Version: 1, ID: task + "-done", TaskID: task, SessionID: "session", CorrelationID: task, Sequence: 4, Time: now.Add(3 * time.Second), Kind: terminal}
	if terminal == runtime.TaskFailed {
		done.TurnID, done.AttemptID = turnStart.TurnID, turnStart.AttemptID
		done.Data.Code = "provider_retryable_no_output"
	}
	for i, e := range []runtime.Event{start, turnStart, turnDone, done} {
		if err := s.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	role := accounting.PrimaryExecution
	if retry {
		role = accounting.Fallback
	}
	r, err := s.CurrentUsage(context.Background(), usageID(role, task))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func safeRetryFailure(t *testing.T, s *Store, task, predecessor string) {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	events := []runtime.Event{
		{Version: 1, ID: task + "-start", TaskID: task, SessionID: task + "-session", CorrelationID: task, Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ProviderID: "local", ModelID: "model:latest", RetryOfTaskID: predecessor}},
		{Version: 1, ID: task + "-turn", TaskID: task, SessionID: task + "-session", CorrelationID: task, Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TurnStarted, TurnID: task + "-turn", AttemptID: task + "-attempt", Data: runtime.Data{ProviderID: "local", ModelID: "model:latest"}},
		{Version: 1, ID: task + "-failed", TaskID: task, SessionID: task + "-session", CorrelationID: task, Sequence: 3, Time: now.Add(2 * time.Second), Kind: runtime.TaskFailed, TurnID: task + "-turn", AttemptID: task + "-attempt", Data: runtime.Data{Code: "provider_retryable_no_output"}},
	}
	for i, event := range events {
		if err := s.Append(context.Background(), int64(i), event); err != nil {
			t.Fatal(err)
		}
	}
}

type usageProvider func(context.Context, providers.Request, func(providers.Chunk) error) error

func (p usageProvider) Models(context.Context) ([]string, error) { return []string{"model"}, nil }
func (p usageProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	return p(ctx, request, emit)
}

func TestUsageRecordReplayCorrectionAndTotals(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	usage := &providers.Usage{InputTokens: 7, OutputTokens: 3}
	r := usageTask(t, s, "task", false, usage, runtime.TaskCompleted)
	if err := s.RecordUsage(ctx, r); err != nil {
		t.Fatal("lost ack replay:", err)
	}
	changedID := r
	changedID.ID = "another-id"
	if err := s.RecordUsage(ctx, changedID); !errors.Is(err, ErrConflict) {
		t.Fatal("logical duplicate:", err)
	}
	zero := providers.Usage{}
	next := accounting.CloneRecord(r)
	next.ID, next.Usage = "task-usage-c1", &zero
	c := accounting.Correction{Version: 1, ID: next.ID, BaseID: r.ID, Supersedes: r.ID, Evidence: "invoice-line", Reason: accounting.UsageReconciliation, Record: next, RecordedAt: r.OccurredAt.Add(time.Second)}
	if err := s.CorrectUsage(ctx, c); err != nil {
		t.Fatal(err)
	}
	third := accounting.CloneRecord(next)
	third.ID, third.Usage = "task-usage-c2", &providers.Usage{InputTokens: 1, OutputTokens: 1}
	c2 := accounting.Correction{Version: 1, ID: third.ID, BaseID: r.ID, Supersedes: next.ID, Evidence: "invoice-line-2", Reason: accounting.ProviderReconciliation, Record: third, RecordedAt: r.OccurredAt.Add(2 * time.Second)}
	if err := s.CorrectUsage(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if err := s.CorrectUsage(ctx, c); err != nil {
		t.Fatal("historical correction ack replay:", err)
	}
	if err := s.RecordUsage(ctx, r); err != nil {
		t.Fatal("base ack replay rejected reconciled measurement:", err)
	}
	h, err := s.UsageHistory(ctx, r.ID)
	if err != nil || len(h.Corrections) != 2 || h.Current.Usage == nil || *h.Current.Usage != *third.Usage {
		t.Fatalf("history: %#v %v", h, err)
	}
	totals, err := s.UsageTotals(ctx, accounting.Scope{TaskID: r.TaskID})
	if err != nil || totals.Coverage != accounting.CompleteCoverage || totals.Scope.SessionID != "session" || totals.Routed.Records != 1 || totals.Routed.InputTokens == nil || *totals.Routed.InputTokens != 1 {
		t.Fatalf("totals: %#v %v", totals, err)
	}
	if _, err = s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.usage.InputTokens',999) WHERE id='task-turn-done'`); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordUsage(ctx, r); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("base ack replay accepted changed source measurement:", err)
	}
	if err = s.CorrectUsage(ctx, c); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("correction ack replay accepted changed source measurement:", err)
	}
}

func TestUsageMissingFallbackCancellationAndConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	safeRetryFailure(t, s, "prior-task", "")
	r := usageTask(t, s, "retry", true, nil, runtime.TaskCanceled)
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func(db *Store) { defer wg.Done(); errs <- db.RecordUsage(ctx, r) }(store)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	totals, err := s.UsageTotals(ctx, accounting.Scope{SessionID: "session"})
	if err != nil || totals.Fallback.Records != 1 || totals.Fallback.UnknownUsageRecords != 1 || totals.Fallback.InputTokens != nil {
		t.Fatalf("fallback totals: %#v %v", totals, err)
	}
	if _, err := s.UsageTotals(ctx, accounting.Scope{TaskID: "missing"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing task:", err)
	}
	if terminalRetryClass("provider_retryable_no_output") != accounting.Retryable || terminalRetryClass("empty_output") != accounting.NonRetryable || terminalRetryClass("interrupted_model") != accounting.Uncertain {
		t.Fatal("terminal retry classification changed")
	}
}

func TestFallbackUsageRequiresSafeRetryLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate string
	}{
		{"completed", `UPDATE events SET body=json_set(body,'$.kind','task.completed') WHERE task_id='prior' AND sequence=3; UPDATE task_heads SET state='completed' WHERE task_id='prior'`},
		{"canceled", `UPDATE events SET body=json_set(body,'$.kind','task.canceled','$.data.code','canceled') WHERE task_id='prior' AND sequence=3; UPDATE task_heads SET state='canceled' WHERE task_id='prior'`},
		{"context overflow", `UPDATE events SET body=json_set(body,'$.data.code','context_overflow') WHERE task_id='prior' AND sequence=3`},
		{"partial output", `UPDATE events SET body=json_set(body,'$.kind','model.delta','$.data.text','partial') WHERE task_id='prior' AND sequence=2`},
		{"completed output", `UPDATE events SET body=json_set(body,'$.kind','turn.completed','$.data.text','answer','$.data.finish_reason','stop') WHERE task_id='prior' AND sequence=2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			safeRetryFailure(t, s, "prior", "")
			if _, err = s.db.Exec(tc.mutate); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = validateRetryChain(ctx, tx, "successor", "prior"); !errors.Is(err, accounting.ErrUsage) {
				t.Fatalf("unsafe predecessor accepted: %v", err)
			}
			_ = tx.Rollback()
		})
	}
}

func TestFallbackUsageAcceptsRoutedNoOutputProviderFailure(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	events := []runtime.Event{
		{Version: 1, ID: "start", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
		{Version: 1, ID: "route", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 2, Time: now, Kind: runtime.RouteSelected, RouteID: "route-id", Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
		{Version: 1, ID: "turn", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 3, Time: now, Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
		{Version: 1, ID: "failed", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 4, Time: now, Kind: runtime.TaskFailed, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Code: "provider_retryable_no_output"}},
	}
	for i, event := range events {
		if err := s.Append(ctx, int64(i), event); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateRetryChain(ctx, tx, "successor", "prior"); err != nil {
		t.Fatal("legitimate routed fallback rejected", err)
	}
	_ = tx.Rollback()
}

func TestProductionLoopFallbackReceivesFallbackUsageRecord(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	request := func(task, predecessor, model string) runtime.RunRequest {
		return runtime.RunRequest{
			TaskID: task, SessionID: "session", ProviderID: "local", RetryOfTaskID: predecessor,
			Route:       &runtime.Data{ProviderID: "local", ModelID: model},
			Inference:   providers.Request{Model: model, Messages: []providers.Message{{Role: "user", Content: "question"}}},
			RequireText: true, MaxTurns: 1, MaxOutputBytes: 1024,
		}
	}
	failing := runtime.Loop{Journal: s, Provider: usageProvider(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		return &providers.Failure{Code: "transport", Retryable: true}
	})}
	out, err := failing.Run(ctx, request("first", "", "model-a"))
	if err == nil || !out.Retryable {
		t.Fatal("production provider failure did not authorize retry", out, err)
	}
	succeeding := runtime.Loop{Journal: s, Provider: usageProvider(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emit(providers.Chunk{Text: "answer", Usage: &providers.Usage{InputTokens: 4, OutputTokens: 1}, Done: true, FinishReason: "stop"})
	})}
	if _, err = succeeding.Run(ctx, request("second", "first", "model-b")); err != nil {
		t.Fatal(err)
	}
	record, err := s.CurrentUsage(ctx, usageID(accounting.Fallback, "second"))
	if err != nil || record.Role != accounting.Fallback || record.Usage == nil || record.Usage.InputTokens != 4 || record.Usage.OutputTokens != 1 {
		t.Fatalf("production fallback usage missing: %#v %v", record, err)
	}
}

func TestFallbackUsageRejectsToolEffectsCyclesAndOverlongChains(t *testing.T) {
	ctx := context.Background()
	t.Run("tool effect", func(t *testing.T) {
		s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
		call := providers.ToolCall{ID: "call", Name: "write", Arguments: []byte(`{}`)}
		events := []runtime.Event{
			{Version: 1, ID: "start", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
			{Version: 1, ID: "turn", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 2, Time: now, Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
			{Version: 1, ID: "proposal", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 3, Time: now, Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{call}, FinishReason: "tool_calls"}},
			{Version: 1, ID: "tool-start", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 4, Time: now, Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "write", ToolBehavior: runtime.BehaviorNonIdempotentWrite, Effect: runtime.UncertainEffect}},
			{Version: 1, ID: "tool-done", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 5, Time: now, Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "write", ToolBehavior: runtime.BehaviorNonIdempotentWrite, Effect: runtime.ConfirmedEffect}},
			{Version: 1, ID: "failed", TaskID: "prior", SessionID: "session", CorrelationID: "prior", Sequence: 6, Time: now, Kind: runtime.TaskFailed, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Code: "provider_retryable_no_output"}},
		}
		for i, event := range events {
			if err := s.Append(ctx, int64(i), event); err != nil {
				t.Fatal(err)
			}
		}
		tx, _ := s.db.BeginTx(ctx, nil)
		if err = validateRetryChain(ctx, tx, "successor", "prior"); !errors.Is(err, accounting.ErrUsage) {
			t.Fatal("tool effect authorized fallback", err)
		}
		_ = tx.Rollback()
	})

	t.Run("cycle", func(t *testing.T) {
		s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		safeRetryFailure(t, s, "root", "")
		safeRetryFailure(t, s, "next", "root")
		if _, err = s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.retry_of_task_id','next') WHERE task_id='root' AND sequence=1`); err != nil {
			t.Fatal(err)
		}
		tx, _ := s.db.BeginTx(ctx, nil)
		if err = validateRetryChain(ctx, tx, "successor", "next"); !errors.Is(err, accounting.ErrUsage) {
			t.Fatal("cycle authorized fallback", err)
		}
		_ = tx.Rollback()
	})

	t.Run("attempt cap includes successor", func(t *testing.T) {
		s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		last := ""
		for i := 0; i < 31; i++ {
			task := fmt.Sprintf("attempt-%02d", i)
			safeRetryFailure(t, s, task, last)
			last = task
		}
		tx, _ := s.db.BeginTx(ctx, nil)
		if err = validateRetryChain(ctx, tx, "successor", last); err != nil {
			t.Fatal("32 total attempts rejected", err)
		}
		_ = tx.Rollback()
		safeRetryFailure(t, s, "attempt-31", last)
		tx, _ = s.db.BeginTx(ctx, nil)
		if err = validateRetryChain(ctx, tx, "successor", "attempt-31"); !errors.Is(err, accounting.ErrUsage) {
			t.Fatal("33 total attempts accepted", err)
		}
		_ = tx.Rollback()
	})
}

func TestUsageCompetingCorrectionCASAndCorruption(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := usageTask(t, s, "race", false, &providers.Usage{InputTokens: 2, OutputTokens: 1}, runtime.TaskCompleted)
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	makeCorrection := func(id string, tokens int64) accounting.Correction {
		next := accounting.CloneRecord(r)
		next.ID, next.Usage = id, &providers.Usage{InputTokens: tokens, OutputTokens: 1}
		return accounting.Correction{Version: 1, ID: id, BaseID: r.ID, Supersedes: r.ID, Evidence: "receipt-" + id, Reason: accounting.ProviderReconciliation, Record: next, RecordedAt: r.OccurredAt.Add(time.Second)}
	}
	a, b := makeCorrection("correction-a", 3), makeCorrection("correction-b", 4)
	errs := make(chan error, 2)
	go func() { errs <- s.CorrectUsage(ctx, a) }()
	go func() { errs <- other.CorrectUsage(ctx, b) }()
	first, second := <-errs, <-errs
	if !((first == nil && errors.Is(second, ErrConflict)) || (second == nil && errors.Is(first, ErrConflict))) {
		t.Fatalf("competing corrections: %v %v", first, second)
	}
	var winner string
	if err = s.db.QueryRow("SELECT current_id FROM usage_heads WHERE base_id=?", r.ID).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE usage_corrections SET session_id='wrong' WHERE id=?", winner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UsageHistory(ctx, r.ID); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("corrupt correction column accepted:", err)
	}
	if _, err = s.UsageTotals(ctx, accounting.Scope{TaskID: r.TaskID}); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("aggregate accepted corrupt correction:", err)
	}
	if _, err = s.db.Exec("UPDATE usage_corrections SET session_id=? WHERE id=?", r.SessionID, winner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA foreign_keys=OFF; UPDATE usage_heads SET current_id='missing' WHERE base_id=?", r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentUsage(ctx, r.ID); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("dangling head accepted:", err)
	}
}

func TestRoutedUsagePublicRoutePendingAndRetryClass(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)
	events := []runtime.Event{
		{Version: 1, ID: "start", TaskID: "pending", SessionID: "session", CorrelationID: "pending", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
		{Version: 1, ID: "route-event", TaskID: "pending", SessionID: "session", CorrelationID: "pending", Sequence: 2, Time: now, Kind: runtime.RouteSelected, RouteID: "public-route", Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
		{Version: 1, ID: "turn-a-start", TaskID: "pending", SessionID: "session", CorrelationID: "pending", Sequence: 3, Time: now, Kind: runtime.TurnStarted, TurnID: "turn-a", AttemptID: "attempt-a", Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
		{Version: 1, ID: "turn-a-done", TaskID: "pending", SessionID: "session", CorrelationID: "pending", Sequence: 4, Time: now, Kind: runtime.TurnCompleted, TurnID: "turn-a", AttemptID: "attempt-a", Data: runtime.Data{Usage: &providers.Usage{InputTokens: 2, OutputTokens: 1}}},
		{Version: 1, ID: "turn-b-start", TaskID: "pending", SessionID: "session", CorrelationID: "pending", Sequence: 5, Time: now, Kind: runtime.TurnStarted, TurnID: "turn-b", AttemptID: "attempt-b", Data: runtime.Data{ProviderID: "local", ModelID: "model"}},
		{Version: 1, ID: "done", TaskID: "pending", SessionID: "session", CorrelationID: "pending", Sequence: 6, Time: now, Kind: runtime.TaskFailed, Data: runtime.Data{Code: "provider_retryable_no_output"}},
	}
	for i, e := range events {
		if err := s.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.CurrentUsage(ctx, usageID(accounting.PrimaryExecution, "pending"))
	if err != nil || r.RouteID != "public-route" || r.Usage != nil || r.RetryClass != accounting.Retryable {
		t.Fatalf("routed accounting: %#v %v", r, err)
	}
}

func TestUsageRejectsUnprovenClassifierAccounting(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := usageTask(t, s, "classifier-source", false, nil, runtime.TaskCompleted)
	r.ID, r.OperationID, r.Role = "classifier-usage", "classifier-operation", accounting.Classifier
	if err := s.RecordUsage(ctx, r); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("unproven classifier accounting accepted:", err)
	}
}

func TestUsageMigration29CoverageAndRejectsPartialSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = usageTask(t, s, "legacy", false, nil, runtime.TaskCompleted)
	if _, err = s.db.Exec(`DROP TABLE IF EXISTS usage_corrections; DROP TABLE IF EXISTS usage_heads; DROP TABLE IF EXISTS usage_records; DROP TABLE IF EXISTS usage_metadata; PRAGMA user_version=29`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	totals, err := ro.UsageTotals(ctx, accounting.Scope{TaskID: "legacy"})
	_ = ro.Close()
	if err != nil || totals.Coverage != accounting.LegacyUnavailableCoverage || totals.UnaccountedRoutedOperations != 1 {
		t.Fatalf("legacy totals: %#v %v", totals, err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if s.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 30 {
		t.Fatal("migration version", version)
	}
	if _, err = s.db.Exec("DROP TABLE IF EXISTS usage_heads; PRAGMA user_version=29"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err = Open(ctx, path); !errors.Is(err, errUsageSchema) {
		t.Fatal("partial schema accepted:", err)
	}
}

func TestUsageTotalsRejectsHeadlessAndExtraTopology(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := usageTask(t, s, "topology", false, nil, runtime.TaskCompleted)
	if _, err = s.db.Exec("DELETE FROM usage_heads WHERE base_id=?", r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordUsage(ctx, r); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("base ack replay accepted headless history:", err)
	}
	if _, err = s.UsageTotals(ctx, accounting.Scope{TaskID: r.TaskID}); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("headless base omitted from totals:", err)
	}
	if _, err = s.db.Exec("INSERT INTO usage_heads(base_id,current_id) VALUES(?,?)", r.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO usage_corrections(id,base_id,supersedes,task_id,session_id,role,evidence_kind,evidence_id,body) VALUES('extra',?,?,?,?,?,?,?,'{}')`, r.ID, r.ID, r.TaskID, r.SessionID, r.Role, r.EvidenceKind, r.EvidenceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UsageTotals(ctx, accounting.Scope{TaskID: r.TaskID}); !errors.Is(err, accounting.ErrUsage) {
		t.Fatal("extra correction topology accepted:", err)
	}
}

type countingUsageQueryer struct {
	q          usageQueryer
	rowQueries int
	setQueries int
}

func (q *countingUsageQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.rowQueries++
	return q.q.QueryRowContext(ctx, query, args...)
}

func (q *countingUsageQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.setQueries++
	return q.q.QueryContext(ctx, query, args...)
}

func TestUsageTotalsUsesBoundedSetScansAtRepresentativeVolume(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tasks, err := tx.Prepare(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES(?, 'scale-session', 1, 'completed')`)
	if err != nil {
		t.Fatal(err)
	}
	records, err := tx.Prepare(`INSERT INTO usage_records(id,task_id,session_id,operation_id,role,evidence_kind,evidence_id,body) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	heads, err := tx.Prepare(`INSERT INTO usage_heads(base_id,current_id) VALUES(?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	corrections, err := tx.Prepare(`INSERT INTO usage_corrections(id,base_id,supersedes,task_id,session_id,role,evidence_kind,evidence_id,body) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	const recordsCount = 1024
	at := time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC)
	for i := range recordsCount {
		taskID, baseID := fmt.Sprintf("scale-task-%04d", i), fmt.Sprintf("scale-usage-%04d", i)
		r := accounting.Record{Version: 1, ID: baseID, TaskID: taskID, SessionID: "scale-session", OperationID: taskID, RouteID: "route-" + taskID, EvidenceID: "event-" + taskID, Provider: "provider", Model: "model", Role: accounting.PrimaryExecution, EvidenceKind: accounting.EventEvidence, Usage: &providers.Usage{InputTokens: 1, OutputTokens: 1}, Disposition: accounting.Completed, RetryClass: accounting.NotApplicable, OccurredAt: at}
		body, marshalErr := json.Marshal(r)
		if marshalErr != nil || r.Validate() != nil {
			t.Fatal("base fixture:", marshalErr)
		}
		if _, err = tasks.Exec(taskID); err != nil {
			t.Fatal(err)
		}
		if _, err = records.Exec(baseID, taskID, r.SessionID, taskID, r.Role, r.EvidenceKind, r.EvidenceID, body); err != nil {
			t.Fatal(err)
		}
		currentID := baseID
		if i%2 == 0 {
			corrected := accounting.CloneRecord(r)
			corrected.ID, corrected.Usage = baseID+"-c1", &providers.Usage{InputTokens: 2, OutputTokens: 1}
			c := accounting.Correction{Version: 1, ID: corrected.ID, BaseID: baseID, Supersedes: baseID, Evidence: "receipt-" + taskID, Reason: accounting.UsageReconciliation, Record: corrected, RecordedAt: at.Add(time.Second)}
			correctionBody, marshalErr := json.Marshal(c)
			if marshalErr != nil || c.Validate(r) != nil {
				t.Fatal("correction fixture:", marshalErr)
			}
			if _, err = corrections.Exec(c.ID, baseID, c.Supersedes, taskID, r.SessionID, r.Role, r.EvidenceKind, r.EvidenceID, correctionBody); err != nil {
				t.Fatal(err)
			}
			currentID = c.ID
		}
		if _, err = heads.Exec(baseID, currentID); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []*sql.Stmt{tasks, records, heads, corrections} {
		if err = statement.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	read, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	counted := &countingUsageQueryer{q: read}
	totals, err := usageTotals(deadline, counted, accounting.Scope{SessionID: "scale-session"})
	if err != nil {
		t.Fatal(err)
	}
	if totals.Overall.Records != recordsCount || totals.Overall.KnownInputTokens != recordsCount+recordsCount/2 || totals.Overall.KnownOutputTokens != recordsCount {
		t.Fatal("aggregate mismatch:", totals.Overall)
	}
	if counted.setQueries != 2 || counted.rowQueries > 7 {
		t.Fatalf("aggregate query count grew with records: rows=%d sets=%d", counted.rowQueries, counted.setQueries)
	}
}

func TestUsageMigrationConcurrent29To30(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE usage_corrections; DROP TABLE usage_heads; DROP TABLE usage_records; DROP TABLE usage_metadata; PRAGMA user_version=29`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			opened, openErr := Open(ctx, path)
			if openErr == nil {
				openErr = opened.Close()
			}
			errs <- openErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent migration:", err)
		}
	}
	final, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	var version, tables int
	if final.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || final.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN('usage_metadata','usage_records','usage_heads','usage_corrections')`).Scan(&tables) != nil || version != 30 || tables != 4 {
		t.Fatalf("migration did not converge: version=%d tables=%d", version, tables)
	}
}
