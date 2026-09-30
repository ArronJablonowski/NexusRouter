package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

const summaryPreparationKey = "summary-preparation-key-0001"

func summaryPreparationState(t *testing.T) sessions.ContextCompactionOperationState {
	t.Helper()
	digest := strings.Repeat("a", 64)
	engine, err := runtime.NewContextEngineIdentity("darwin.default", "v1")
	if err != nil {
		t.Fatal(err)
	}
	tiers, err := runtime.NewContextTierPlan(digest, strings.Repeat("b", 64), strings.Repeat("c", 64), []runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierVolatile})
	if err != nil {
		t.Fatal(err)
	}
	start, err := sessions.SealContextCompactionPlanStart(sessions.ContextCompactionPlanStart{OperationID: "opaque-operation", RequestID: "opaque-request",
		TaskID: "task", SourceSequence: 4, SourceDigest: digest, AttemptID: "opaque-attempt", Model: "model", Provider: "provider", Keep: 1,
		ConfigSnapshot: json.RawMessage(`{"mode":"local_only"}`), PolicySnapshot: json.RawMessage(`{"egress":"deny"}`),
		Engine: engine, Tiers: tiers, ProcessID: "process", StartedAt: time.Unix(100, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := sessions.SealContextCompactionLifecycleFact(sessions.ContextCompactionLifecycleFact{ID: "started-fact", OperationID: start.OperationID,
		Sequence: 1, Kind: sessions.ContextCompactionStarted, CreatedAt: start.StartedAt})
	if err != nil {
		t.Fatal(err)
	}
	state, err := sessions.SealContextCompactionOperationState(sessions.ContextCompactionOperationState{Start: start, Status: sessions.ContextCompactionStarted, Facts: []sessions.ContextCompactionLifecycleFact{fact}})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func summaryPreparationRequest(body string) *http.Request {
	r := request(http.MethodPost, "/v1/summary-preparations", body)
	r.Header.Set("Idempotency-Key", summaryPreparationKey)
	return r
}

func TestSummaryPreparationHeaderAndConflictBoundaries(t *testing.T) {
	s := services()
	calls := 0
	s.PrepareSummary = func(_ context.Context, key string, request app.PrepareSummaryRequest) (sessions.ContextCompactionOperationState, error) {
		calls++
		if key != summaryPreparationKey || request.Version != 1 || request.TaskID != "task" || request.ModelID != "model" || request.Keep != 1 || request.MaxCost != 0 {
			t.Fatal("transport contract not bound", key, request)
		}
		return summaryPreparationState(t), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"task_id":"task","model_id":"model","keep":1,"max_cost":0}`
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("Idempotency-Key") },
		func(r *http.Request) {
			r.Header["Idempotency-Key"] = []string{summaryPreparationKey, summaryPreparationKey}
		},
		func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") },
		func(r *http.Request) { r.Header.Set("Idempotency-Key", "summary key with spaces") },
	} {
		r := summaryPreparationRequest(body)
		mutate(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || strings.Contains(w.Body.String(), summaryPreparationKey) {
			t.Fatalf("invalid key response=%d %q", w.Code, w.Body.String())
		}
	}
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, summaryPreparationRequest(body))
		if w.Code != 200 || strings.Contains(w.Body.String(), summaryPreparationKey) {
			t.Fatalf("preparation response=%d %q", w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("service calls=%d", calls)
	}
	s.PrepareSummary = func(context.Context, string, app.PrepareSummaryRequest) (sessions.ContextCompactionOperationState, error) {
		return sessions.ContextCompactionOperationState{}, telemetry.ErrConflict
	}
	h, _ = New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, summaryPreparationRequest(body))
	if w.Code != 409 || strings.Contains(w.Body.String(), summaryPreparationKey) {
		t.Fatalf("conflict response=%d %q", w.Code, w.Body.String())
	}
}

func TestSummaryPreparationInspection(t *testing.T) {
	state := summaryPreparationState(t)
	s := services()
	s.SummaryPreparation = func(_ context.Context, operationID string) (sessions.ContextCompactionOperationState, error) {
		if operationID != state.Start.OperationID {
			t.Fatal(operationID)
		}
		return state, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodGet, "/v1/summary-preparations/"+state.Start.OperationID, ""))
	if w.Code != 200 || strings.Contains(w.Body.String(), summaryPreparationKey) {
		t.Fatalf("inspection response=%d %q", w.Code, w.Body.String())
	}
}
