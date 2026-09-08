package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestSummarizeTaskPersistsDraftBeforeDispatchAndKeepsSource(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Preserve runtime-token and provider-token in source", Domain: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	fitnessKey := routing.Key{Model: "a", Provider: "local", Domain: "creative", Profile: "default"}
	if _, err := db.Fitness(ctx, fitnessKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unexpected initial fitness", err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		attempts, err := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
		if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
			t.Errorf("dispatch preceded durable start: %+v %v", attempts, err)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !json.Valid(body) {
			t.Error("invalid request body", err)
		}
		if strings.Contains(string(body), "runtime-token") || strings.Contains(string(body), "provider-token") || !strings.Contains(string(body), "[REDACTED]") {
			t.Error("input redaction failed", string(body))
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(body, &envelope) != nil {
			t.Error("request not JSON")
		}
		if tools := envelope["tools"]; len(tools) > 0 && string(tools) != "[]" && string(tools) != "null" {
			t.Error("summarizer received tools")
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", `{"version":1,"summary":{"requirements":["Preserve runtime-token and provider-token"]}}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.settings.Providers[0].APIKeyEnv = "SUMMARY_KEY"
	svc.settings.Evaluation.Judge = false // Summarization is not independent judging.
	svc.secret = func(name string) string {
		switch name {
		case "DARWIN_API_TOKEN":
			return "runtime-token"
		case "SUMMARY_KEY":
			return "provider-token"
		}
		return ""
	}
	attempt, err := svc.SummarizeTask(ctx, source.TaskID, "a", 1, 0)
	if err != nil || calls.Load() != 1 || attempt.Status != "drafted" || attempt.Draft == nil {
		t.Fatalf("summary %+v %v calls=%d", attempt, err, calls.Load())
	}
	encoded, _ := json.Marshal(attempt)
	if strings.Contains(string(encoded), "runtime-token") || strings.Contains(string(encoded), "provider-token") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal("draft redaction failed", string(encoded))
	}
	_, expected, err := sessions.PrepareContinuation(before, attempt.Draft.Request)
	if err != nil || attempt.Draft.SourceDigest != expected.SourceDigest || attempt.Draft.SourceSequence != before.Sequence || !reflect.DeepEqual(attempt.Draft.Checkpoint, expected) {
		t.Fatalf("original source provenance lost: %+v expected %+v %v", attempt.Draft, expected, err)
	}
	saved, err := db.SummaryAttempt(ctx, attempt.ID)
	if err != nil || saved.Status != "drafted" || saved.Draft == nil {
		t.Fatal(saved, err)
	}
	totals, err := db.UsageTotals(ctx, accounting.Scope{TaskID: source.TaskID})
	if err != nil || totals.Summarizer.Records != 1 || totals.Summarizer.UnknownUsageRecords != 1 || totals.Summarizer.KnownCostRecords != 1 || totals.Auxiliary.Records != 1 {
		t.Fatal("summary accounting not committed with draft", totals, err)
	}
	after, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source snapshot changed", err)
	}
	afterEvents, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatal("source durable history changed", err)
	}
	if _, err := db.Fitness(ctx, fitnessKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("summary changed candidate fitness", err)
	}
}

func TestSummarizeTaskAdmissionRejectsCloudAndUnknownBudgets(t *testing.T) {
	for _, condition := range []string{"cloud-private", "cloud-local-mode", "local-cloud-mode", "unknown-model", "unknown-context", "unknown-cost", "cost-ceiling", "unknown-ram", "invalid-keep", "noop-keep", "negative-cost", "nan-cost", "infinite-cost"} {
		t.Run(condition, func(t *testing.T) {
			svc, cfg := autoFixture(t)
			ctx := context.Background()
			source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "private history"})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			model, keep, cost := "a", 1, 0.0
			switch condition {
			case "cloud-private":
				svc.settings.Mode = "hybrid"
				svc.settings.Models[0].Locality = "cloud"
			case "cloud-local-mode":
				svc.settings.Models[0].Locality = "cloud"
			case "local-cloud-mode":
				svc.settings.Mode = "cloud_only"
			case "unknown-model":
				model = "missing"
			case "unknown-context":
				svc.settings.Models[0].ContextTokens = 0
			case "unknown-cost":
				svc.settings.Models[0].EstimatedCost = nil
			case "cost-ceiling":
				n := 1.0
				svc.settings.Models[0].EstimatedCost = &n
			case "unknown-ram":
				svc.settings.Models[0].RAMBytes = 0
			case "invalid-keep":
				keep = 0
			case "noop-keep":
				keep = 100
			case "negative-cost":
				cost = -1
			case "nan-cost":
				cost = math.NaN()
			case "infinite-cost":
				cost = math.Inf(1)
			}
			if _, err := svc.SummarizeTask(ctx, source.TaskID, model, keep, cost); err == nil {
				t.Fatal("invalid admission succeeded")
			}
			if calls.Load() != 0 {
				t.Fatal("denied summary dispatched")
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			attempts, err := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
			if err != nil || len(attempts) != 0 {
				t.Fatal("denied admission started lifecycle", attempts, err)
			}
		})
	}
}

func TestSummarizeTaskFailureLifecycleSurvivesCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			svc, cfg := autoFixture(t)
			ctx := context.Background()
			source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "history"})
			if err != nil {
				t.Fatal(err)
			}
			summaryCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
				if err != nil {
					t.Error(err)
					return
				}
				attempts, err := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
				db.Close()
				if err != nil || len(attempts) != 1 || attempts[0].Status != "started" {
					t.Error("missing durable start", attempts, err)
				}
				if canceled {
					cancel()
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"sensitive-invalid-summary"},"done":true,"done_reason":"stop","prompt_eval_count":29,"eval_count":13}`)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			if _, err := svc.SummarizeTask(summaryCtx, source.TaskID, "a", 1, 0); err == nil || strings.Contains(err.Error(), "sensitive-invalid-summary") {
				t.Fatal("failed summary accepted or payload leaked", err)
			}
			if calls.Load() != 1 {
				t.Fatal("summary retried", calls.Load())
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			attempts, err := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
			code := "summary_failed"
			if canceled {
				code = "canceled"
			}
			if err != nil || len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].Code != code || attempts[0].Draft != nil || attempts[0].FinishedAt.Before(attempts[0].StartedAt) || time.Since(attempts[0].FinishedAt) > time.Minute {
				t.Fatal("failure lifecycle", attempts, err)
			}
			if canceled {
				if attempts[0].Usage != nil || attempts[0].Elapsed != 0 {
					t.Fatal("canceled summary retained unverified usage", attempts[0])
				}
			} else if attempts[0].Usage == nil || attempts[0].Usage.InputTokens != 29 || attempts[0].Usage.OutputTokens != 13 || attempts[0].Elapsed <= 0 {
				t.Fatal("verified failed-summary usage was lost", attempts[0])
			}
			totals, err := db.UsageTotals(ctx, accounting.Scope{TaskID: source.TaskID})
			known := int64(1)
			if canceled {
				known = 0
			}
			if err != nil || totals.Summarizer.Records != 1 || totals.Summarizer.KnownUsageRecords != known || totals.Summarizer.UnknownUsageRecords != 1-known || totals.Summarizer.KnownInputTokens != known*29 || totals.Summarizer.KnownOutputTokens != known*13 || totals.Summarizer.KnownCostRecords != 1 || totals.Auxiliary.Records != 1 {
				t.Fatal("summary failure accounting not committed", totals, err)
			}
			encoded, _ := json.Marshal(attempts)
			if strings.Contains(string(encoded), "sensitive-invalid-summary") {
				t.Fatal("raw failure persisted")
			}
			history, err := sessions.Replay(ctx, db, source.TaskID)
			if err != nil || history.State != "completed" {
				t.Fatal("source outcome changed", history, err)
			}
		})
	}
}

func TestSummarizeTaskRequiresCompletedSafeSource(t *testing.T) {
	for _, state := range []string{"running", "failed", "canceled", "interrupted-completed"} {
		t.Run(state, func(t *testing.T) {
			svc, cfg := autoFixture(t)
			ctx := context.Background()
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			task := "unsafe-source"
			start := runtime.Event{Version: 1, ID: "start", TaskID: task, SessionID: task, CorrelationID: task, Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "recent"}}}}
			if err := db.Append(ctx, 0, start); err != nil {
				t.Fatal(err)
			}
			if state != "running" {
				e := start
				e.ID, e.Sequence, e.Data = "end", 2, runtime.Data{}
				switch state {
				case "failed":
					e.Kind = runtime.TaskFailed
				case "canceled":
					e.Kind = runtime.TaskCanceled
				default:
					e.Kind, e.TurnID, e.AttemptID = runtime.TurnStarted, "turn", "attempt"
				}
				if err := db.Append(ctx, 1, e); err != nil {
					t.Fatal(err)
				}
				if state == "interrupted-completed" {
					e.ID, e.Sequence, e.Kind = "premature-complete", 3, runtime.TaskCompleted
					if err := db.Append(ctx, 2, e); err != nil {
						t.Fatal(err)
					}
				}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			if _, err := svc.SummarizeTask(ctx, task, "a", 1, 0); !errors.Is(err, ErrAdmission) {
				t.Fatal("unsafe source admitted", err)
			}
			attempts, err := db.ListSummaryAttempts(ctx, task, "", 100)
			if err != nil || len(attempts) != 0 || calls.Load() != 0 {
				t.Fatal("unsafe source reached summarizer", attempts, calls.Load(), err)
			}
		})
	}
}
