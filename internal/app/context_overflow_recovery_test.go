package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func approvedOverflowSummary(t *testing.T, svc *Service, cfg config.Settings, source string) (sessions.SummaryAttempt, sessions.SummaryReview) {
	t.Helper()
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Replay(ctx, db, source)
	if err != nil {
		t.Fatal(err)
	}
	request := sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Requirements: []string{"Preserve the provider-overflow source requirement"}}}
	_, checkpoint, err := sessions.PrepareContinuation(snapshot, request)
	if err != nil {
		t.Fatal(err)
	}
	attempt := sessions.SummaryAttempt{Version: 1, ID: "provider-overflow-summary", TaskID: source, SourceDigest: checkpoint.SourceDigest, Model: "a", Provider: "local", Status: "started", SourceSequence: snapshot.Sequence, Keep: 1, StartedAt: time.Unix(700, 0).UTC()}
	if err := db.BeginSummary(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.FinishedAt = "drafted", attempt.StartedAt.Add(time.Second)
	attempt.Draft = &sessions.SummaryDraft{Request: request, Checkpoint: checkpoint, SourceTaskID: source, SourceSequence: snapshot.Sequence, SourceDigest: checkpoint.SourceDigest, Model: "a", Elapsed: time.Second}
	if err := db.CompleteSummary(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	review, err := svc.ReviewSummary(ctx, attempt.ID, "", "approved", "Verified provider-overflow recovery summary")
	if err != nil {
		t.Fatal(err)
	}
	return attempt, review
}

func TestProviderContextOverflowStartsOneApprovedLinkedTask(t *testing.T) {
	for _, selection := range []string{"auto", "a"} {
		t.Run(selection, func(t *testing.T) {
			ctx := context.Background()
			svc, cfg := autoFixture(t)
			source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "provider overflow source", Domain: "overflow-recovery"})
			if err != nil {
				t.Fatal(err)
			}
			attempt, review := approvedOverflowSummary(t, svc, cfg, source.TaskID)
			beforeDB, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			before, err := beforeDB.Read(ctx, source.TaskID, 0, 100)
			beforeDB.Close()
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				var request struct {
					Model    string              `json:"model"`
					Messages []providers.Message `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "a" {
					t.Error("unexpected recovery request")
					return
				}
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					return
				}
				body, _ := json.Marshal(request.Messages)
				if strings.Contains(string(body), "provider overflow source") || !strings.Contains(string(body), "provider-overflow source requirement") {
					t.Error("recovery did not use approved context")
				}
				fmt.Fprintln(w, `{"message":{"content":"recovered"},"done":true,"done_reason":"stop"}`)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			svc.settings.Runtime.AutoApprovedCompaction = true
			cost := .1
			for i := range svc.settings.Models {
				svc.settings.Models[i].EstimatedCost = &cost
			}
			out, err := svc.Run(ctx, Request{ModelID: selection, ContinueTaskID: source.TaskID, Prompt: "continue after provider tokenization", Domain: "overflow-recovery", MaxCost: .2})
			if err != nil || out.Text != "recovered" || calls.Load() != 2 || len(out.PreviousTaskIDs) != 1 || out.RouteEstimatedCost == nil || *out.RouteEstimatedCost != .2 || out.Usage != nil {
				t.Fatal(out, err, calls.Load())
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			failed, err := sessions.Replay(ctx, db, out.PreviousTaskIDs[0])
			if err != nil || failed.State != "failed" || failed.ParentTaskID != source.TaskID {
				t.Fatal("missing failed provider attempt", failed, err)
			}
			recovered, err := sessions.Replay(ctx, db, out.TaskID)
			if err != nil || recovered.State != "completed" || recovered.RetryOfTaskID != out.PreviousTaskIDs[0] || recovered.ParentTaskID != source.TaskID || recovered.Compaction == nil || recovered.Compaction.SummaryAttemptID != attempt.ID || recovered.Compaction.SummaryReviewID != review.ID {
				t.Fatal("missing recovery provenance", recovered, err)
			}
			after, err := db.Read(ctx, source.TaskID, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("source changed", err)
			}
		})
	}
}

type partialOverflowProvider struct{ calls *atomic.Int32 }

func (p partialOverflowProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}
func (p partialOverflowProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	p.calls.Add(1)
	if err := emit(providers.Chunk{Text: "partial"}); err != nil {
		return err
	}
	return &providers.Failure{Code: "context_overflow"}
}

func TestProviderContextOverflowWithOutputNeverRedispatches(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "partial overflow source"})
	if err != nil {
		t.Fatal(err)
	}
	approvedOverflowSummary(t, svc, cfg, source.TaskID)
	var calls atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return partialOverflowProvider{calls: &calls}, nil
	})
	svc.settings.Runtime.AutoApprovedCompaction = true
	out, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "do not repeat partial output"})
	if err == nil || out.TaskID == "" || calls.Load() != 1 || len(out.PreviousTaskIDs) != 0 {
		t.Fatal(out, err, calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(ctx, out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seenDelta := false
	for _, event := range events {
		seenDelta = seenDelta || event.Kind == runtime.ModelDelta
	}
	if !seenDelta || events[len(events)-1].Kind != runtime.TaskFailed || events[len(events)-1].Data.Code != "context_overflow" {
		t.Fatal("partial overflow evidence missing", events)
	}
}

func TestProviderContextOverflowAfterSafeFallbackPreservesChainAndBudget(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "fallback overflow source", Domain: "overflow-chain"})
	if err != nil {
		t.Fatal(err)
	}
	approvedOverflowSummary(t, svc, cfg, source.TaskID)
	cost := .125
	for i := range svc.settings.Models {
		svc.settings.Models[i].EstimatedCost = &cost
	}
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var request struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid provider request")
			return
		}
		calls = append(calls, request.Model)
		switch len(calls) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		case 3:
			body, _ := json.Marshal(request.Messages)
			if strings.Contains(string(body), "fallback overflow source") || !strings.Contains(string(body), "provider-overflow source requirement") {
				t.Error("recovery did not use approved context")
			}
			fmt.Fprintln(w, `{"message":{"content":"chain recovered"},"done":true,"done_reason":"stop"}`)
		default:
			t.Error("unexpected extra provider call")
		}
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.settings.Runtime.AutoApprovedCompaction = true
	out, err := svc.Run(ctx, Request{ModelID: "auto", ContinueTaskID: source.TaskID, Prompt: "continue through chain", Domain: "overflow-chain", MaxCost: .375})
	if err != nil || out.Text != "chain recovered" || !reflect.DeepEqual(calls, []string{"a", "z", "z"}) || len(out.PreviousTaskIDs) != 2 || out.RouteEstimatedCost == nil || *out.RouteEstimatedCost != .375 || out.Usage != nil {
		t.Fatal(out, err, calls)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := sessions.Replay(ctx, db, out.PreviousTaskIDs[0])
	if err != nil || first.State != "failed" || first.ParentTaskID != source.TaskID {
		t.Fatal("missing first failure", first, err)
	}
	overflow, err := sessions.Replay(ctx, db, out.PreviousTaskIDs[1])
	if err != nil || overflow.State != "failed" || overflow.RetryOfTaskID != first.TaskID || overflow.ParentTaskID != source.TaskID {
		t.Fatal("missing overflow fallback", overflow, err)
	}
	recovered, err := sessions.Replay(ctx, db, out.TaskID)
	if err != nil || recovered.State != "completed" || recovered.RetryOfTaskID != overflow.TaskID || recovered.ParentTaskID != source.TaskID {
		t.Fatal("missing final recovery", recovered, err)
	}
}
