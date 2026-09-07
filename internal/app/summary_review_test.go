package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func summaryReviewFixture(t *testing.T) (*Service, config.Settings, Result, sessions.SummaryAttempt, *atomic.Int32) {
	t.Helper()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "original source body", Domain: "review-summary"})
	if err != nil {
		t.Fatal(err)
	}
	var summarizing atomic.Bool
	summarizing.Store(true)
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		calls.Add(1)
		text := "continued"
		if summarizing.Load() {
			text = `{"version":1,"summary":{"requirements":["Keep frozen-marker requirement"],"decisions":["Preserve original decision"]}}`
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
	}))
	t.Cleanup(server.Close)
	svc.settings.Providers[0].Endpoint = server.URL
	attempt, err := svc.SummarizeTask(context.Background(), source.TaskID, "a", 1, 0)
	if err != nil || attempt.Draft == nil {
		t.Fatal(attempt, err)
	}
	summarizing.Store(false)
	return svc, cfg, source, attempt, calls
}

func TestSummaryReviewHistoryCASAndRedactedNotes(t *testing.T) {
	svc, cfg, source, attempt, calls := summaryReviewFixture(t)
	ctx := context.Background()
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "note-secret"
		}
		return ""
	}
	approved, err := svc.ReviewSummary(ctx, attempt.ID, "", "approved", "Checked note-secret against source")
	if err != nil || approved.ID == "" || approved.PreviousID != "" || approved.Decision != "approved" {
		t.Fatal(approved, err)
	}
	encoded, _ := json.Marshal(approved)
	if strings.Contains(string(encoded), "note-secret") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal("review note not redacted", string(encoded))
	}
	if _, err := svc.ReviewSummary(ctx, attempt.ID, "", "rejected", "stale review"); err == nil {
		t.Fatal("stale review overwrote approval")
	}
	rejected, err := svc.ReviewSummary(ctx, attempt.ID, approved.ID, "rejected", "New discrepancy found")
	if err != nil || rejected.PreviousID != approved.ID || rejected.Decision != "rejected" {
		t.Fatal(rejected, err)
	}
	if _, err := svc.ReviewSummary(ctx, attempt.ID, approved.ID, "approved", "stale approval"); err == nil {
		t.Fatal("stale approval overwrote rejection")
	}
	history, err := SummaryReviewHistory(ctx, cfg.Telemetry.Database, attempt.ID)
	if err != nil || len(history) != 2 || history[0].ID != approved.ID || history[1].ID != rejected.ID {
		t.Fatal(history, err)
	}
	after, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("review changed source", err)
	}
	saved, err := db.SummaryAttempt(ctx, attempt.ID)
	if err != nil || !reflect.DeepEqual(saved.Draft, attempt.Draft) {
		t.Fatal("review rewrote draft", err)
	}
	key := routing.Key{Model: "a", Provider: "local", Domain: "review-summary", Profile: "default"}
	if _, err := db.Fitness(ctx, key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("summary review changed fitness", err)
	}
	if calls.Load() != 1 {
		t.Fatal("review dispatched a model", calls.Load())
	}
}

func TestApprovedSummaryContinuationPersistsFrozenCheckpoint(t *testing.T) {
	for _, model := range []string{"a", "auto"} {
		t.Run(model, func(t *testing.T) {
			svc, cfg, source, attempt, calls := summaryReviewFixture(t)
			ctx := context.Background()
			approved, err := svc.ReviewSummary(ctx, attempt.ID, "", "approved", "Compared draft against source")
			if err != nil {
				t.Fatal(err)
			}
			out, err := svc.Run(ctx, Request{ModelID: model, ContinueTaskID: source.TaskID, SummaryAttemptID: attempt.ID, Prompt: "continue approved summary"})
			if err != nil || out.Text != "continued" || calls.Load() != 2 {
				t.Fatal(out, err, calls.Load())
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			replayed, err := sessions.Replay(ctx, db, out.TaskID)
			if err != nil || replayed.Compaction == nil || replayed.Compaction.SummaryAttemptID != attempt.ID || replayed.Compaction.SummaryReviewID != approved.ID {
				t.Fatal("frozen attribution missing", replayed, err)
			}
			checkpoint := *replayed.Compaction
			checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = "", ""
			if !reflect.DeepEqual(&checkpoint, attempt.Draft.Checkpoint) {
				t.Fatal("approved checkpoint changed", checkpoint, attempt.Draft.Checkpoint)
			}
			if replayed.ParentTaskID != source.TaskID || replayed.Privacy != "local_only" {
				t.Fatal("source relationship/privacy changed", replayed)
			}
			encoded, _ := json.Marshal(replayed.Messages)
			if strings.Contains(string(encoded), "original source body") || !strings.Contains(string(encoded), "Keep frozen-marker requirement") {
				t.Fatal("wrong continuation context", string(encoded))
			}
			original, err := sessions.Replay(ctx, db, source.TaskID)
			if err != nil || original.Messages[0].Content != "original source body" || original.Compaction != nil {
				t.Fatal("source mutated", original, err)
			}
		})
	}
}

func TestAutomaticRoutingUsesApprovedSummaryOnlyAfterFullHistoryHasNoRoute(t *testing.T) {
	ctx := context.Background()
	svc, cfg := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: strings.Repeat("long immutable history ", 200), Domain: "auto-compaction"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	request := sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Requirements: []string{"Preserve the original long-history requirement"}}}
	_, checkpoint, err := sessions.PrepareContinuation(snapshot, request)
	if err != nil {
		t.Fatal(err)
	}
	attempt := sessions.SummaryAttempt{Version: 1, ID: "auto-approved-summary", TaskID: source.TaskID, SourceDigest: checkpoint.SourceDigest, Model: "a", Provider: "local", Status: "started", SourceSequence: snapshot.Sequence, Keep: 1, StartedAt: time.Unix(600, 0).UTC()}
	if err := db.BeginSummary(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.FinishedAt = "drafted", attempt.StartedAt.Add(time.Second)
	attempt.Draft = &sessions.SummaryDraft{Request: request, Checkpoint: checkpoint, SourceTaskID: source.TaskID, SourceSequence: snapshot.Sequence, SourceDigest: checkpoint.SourceDigest, Model: "a", Elapsed: time.Second}
	if err := db.CompleteSummary(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	review, err := svc.ReviewSummary(ctx, attempt.ID, "", "approved", "Compared the compact summary to the immutable source")
	if err != nil {
		t.Fatal(err)
	}
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 3000
	}
	if _, err := svc.Run(ctx, Request{ModelID: "auto", ContinueTaskID: source.TaskID, Prompt: "continue without implicit compaction", Domain: "auto-compaction"}); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatal("disabled automatic compaction admitted full history", err)
	}
	disabled, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "explicit continuation without implicit compaction", Domain: "auto-compaction"})
	if disabled.TaskID == "" || !errors.Is(err, runtime.ErrContextOverflow) {
		t.Fatal("disabled explicit compaction did not preserve durable overflow", disabled, err)
	}
	svc.settings.Runtime.AutoApprovedCompaction = true
	out, err := svc.Run(ctx, Request{ModelID: "auto", ContinueTaskID: source.TaskID, Prompt: "continue with approved compact history", Domain: "auto-compaction"})
	if err != nil || out.TaskID == "" {
		t.Fatal(out, err)
	}
	db, err = telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	replayed, err := sessions.Replay(ctx, db, out.TaskID)
	if err != nil || replayed.Compaction == nil || replayed.Compaction.SummaryAttemptID != attempt.ID || replayed.Compaction.SummaryReviewID != review.ID || replayed.ParentTaskID != source.TaskID {
		t.Fatal("automatic compaction attribution missing", replayed, err)
	}
	var estimates atomic.Int32
	svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) {
		estimates.Add(1)
		return 1, nil
	})
	explicit, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "explicit continuation with approved compact history", Domain: "auto-compaction"})
	if err != nil || explicit.TaskID == "" || estimates.Load() != 1 {
		t.Fatal(explicit, err, estimates.Load())
	}
	explicitReplay, err := sessions.Replay(ctx, db, explicit.TaskID)
	if err != nil || explicitReplay.Compaction == nil || explicitReplay.Compaction.SummaryAttemptID != attempt.ID || explicitReplay.Compaction.SummaryReviewID != review.ID || explicitReplay.ParentTaskID != source.TaskID {
		t.Fatal("explicit automatic compaction attribution missing", explicitReplay, err)
	}
	svc.contextEstimator = nil
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 1000
	}
	tooSmall, err := svc.Run(ctx, Request{ModelID: "a", ContinueTaskID: source.TaskID, Prompt: "approved form still cannot fit", Domain: "auto-compaction"})
	if tooSmall.TaskID == "" || !errors.Is(err, runtime.ErrContextOverflow) {
		t.Fatal("still-oversized approved form was activated", tooSmall, err)
	}
	tooSmallReplay, err := sessions.Replay(ctx, db, tooSmall.TaskID)
	if err != nil || tooSmallReplay.Compaction != nil {
		t.Fatal("futile compaction was persisted", tooSmallReplay, err)
	}
}

func TestSummaryContinuationRejectsMissingApprovalAndMismatches(t *testing.T) {
	for _, condition := range []string{"unreviewed", "rejected", "wrong-source", "missing-source", "inline-compaction", "missing-attempt", "new-secret", "cloud-private"} {
		t.Run(condition, func(t *testing.T) {
			svc, _, source, attempt, calls := summaryReviewFixture(t)
			ctx := context.Background()
			if condition != "unreviewed" {
				decision := "approved"
				if condition == "rejected" {
					decision = "rejected"
				}
				if _, err := svc.ReviewSummary(ctx, attempt.ID, "", decision, "Operator review"); err != nil {
					t.Fatal(err)
				}
			}
			request := Request{ModelID: "a", ContinueTaskID: source.TaskID, SummaryAttemptID: attempt.ID, Prompt: "continue"}
			switch condition {
			case "wrong-source":
				other, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "other source"})
				if err != nil {
					t.Fatal(err)
				}
				request.ContinueTaskID = other.TaskID
			case "missing-source":
				request.ContinueTaskID = ""
			case "inline-compaction":
				request.Compaction = &sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"different inline summary"}}}
			case "missing-attempt":
				request.SummaryAttemptID = "missing"
			case "new-secret":
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return "frozen-marker"
					}
					return ""
				}
			case "cloud-private":
				svc.settings.Mode = "hybrid"
				svc.settings.Models[0].Locality = "cloud"
			}
			beforeCalls := calls.Load()
			if _, err := svc.Run(ctx, request); err == nil {
				t.Fatal("unapproved or mismatched summary admitted")
			}
			if calls.Load() != beforeCalls {
				t.Fatal("denied continuation dispatched")
			}
		})
	}
}

func TestSummaryRevocationBetweenAdmissionAndTaskStart(t *testing.T) {
	svc, cfg, source, attempt, calls := summaryReviewFixture(t)
	ctx := context.Background()
	approved, err := svc.ReviewSummary(ctx, attempt.ID, "", "approved", "Initial approval")
	if err != nil {
		t.Fatal(err)
	}
	profile := svc.profile
	var once sync.Once
	var revoked sessions.SummaryReview
	var revokeErr error
	svc.profile = func(ctx context.Context) (resources.Snapshot, error) {
		once.Do(func() {
			revoked, revokeErr = svc.ReviewSummary(ctx, attempt.ID, approved.ID, "rejected", "Revoked before task starts")
		})
		return profile(ctx)
	}
	out, err := svc.Run(ctx, Request{ModelID: "auto", ContinueTaskID: source.TaskID, SummaryAttemptID: attempt.ID, Prompt: "continue"})
	if revokeErr != nil || revoked.ID == "" {
		t.Fatal("revocation fixture did not run", revokeErr)
	}
	if err == nil || calls.Load() != 1 {
		t.Fatal("revoked snapshot dispatched", out, err, calls.Load())
	}
	db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	if out.TaskID != "" {
		events, readErr := db.Read(ctx, out.TaskID, 0, 100)
		if readErr != nil || len(events) != 0 {
			t.Fatal("revoked TaskStarted persisted", events, readErr)
		}
	}
}
