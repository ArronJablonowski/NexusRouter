package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/sessions"
	"go.yaml.in/yaml/v3"
)

func TestCLISummaryDraftAndReadOnlyInspection(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Error("unexpected provider path")
			http.NotFound(w, r)
			return
		}
		text := "source answer"
		if calls.Add(1) > 1 {
			text = `{"version":1,"summary":{"requirements":["Preserve the source requirement"],"activity":["No files changed"]}}`
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	// Cloud designation intentionally bypasses physical resource profiling;
	// all inference remains an immediate local HTTP fixture.
	cfg.Mode = "cloud_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "summary.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "summary", Provider: "fixture", Model: "fixture", Locality: "cloud", Capabilities: []string{"chat"}, ContextTokens: 32768, EstimatedCost: &zero}}
	source, err := app.RunExplicit(ctx, cfg, app.Request{ModelID: "summary", Prompt: "Preserve the source requirement"}, nil)
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
	configPath := filepath.Join(t.TempDir(), "summary.yaml")
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"summary", "--config", configPath, "--task", source.TaskID, "--model", "summary", "--keep", "1", "--max-cost", "0"}, &stdout, &stderr, "dev"); code != 0 {
		t.Fatalf("summary exit=%d stderr=%q", code, stderr.String())
	}
	var attempt sessions.SummaryAttempt
	if err := json.Unmarshal(stdout.Bytes(), &attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.Status != "drafted" || attempt.ID == "" || attempt.Draft == nil || attempt.TaskID != source.TaskID || attempt.Draft.Request.Keep != 1 || attempt.Draft.Checkpoint == nil || attempt.Draft.Checkpoint.RemovedMessages != 1 || attempt.Validate() != nil {
		t.Fatalf("invalid CLI summary attempt: %+v", attempt)
	}
	if !reflect.DeepEqual(attempt.Draft.Request.Summary.Requirements, []string{"Preserve the source requirement"}) {
		t.Fatal("draft output lost summary")
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"summaries", "show", "--db", cfg.Telemetry.Database, "--id", attempt.ID}, &stdout, &stderr, "dev"); code != 0 {
		t.Fatalf("show exit=%d stderr=%q", code, stderr.String())
	}
	var shown sessions.SummaryAttempt
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil || !reflect.DeepEqual(shown, attempt) {
		t.Fatalf("inspection differs: %+v %v", shown, err)
	}
	for _, taskFilter := range [][]string{nil, {"--task", source.TaskID}} {
		stdout.Reset()
		stderr.Reset()
		args := append([]string{"summaries", "list", "--db", cfg.Telemetry.Database}, taskFilter...)
		if code := Run(args, &stdout, &stderr, "dev"); code != 0 {
			t.Fatalf("list exit=%d stderr=%q", code, stderr.String())
		}
		var attempts []sessions.SummaryAttempt
		if err := json.Unmarshal(stdout.Bytes(), &attempts); err != nil || len(attempts) != 1 || attempts[0].ID != attempt.ID {
			t.Fatalf("unexpected list: %s %v", stdout.String(), err)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"summary-review", "--config", configPath, "--attempt", attempt.ID, "--decision", "approved", "--note", "Compared all categories against source"}, &stdout, &stderr, "dev"); code != 0 {
		t.Fatalf("review exit=%d stderr=%q", code, stderr.String())
	}
	var review sessions.SummaryReview
	if err := json.Unmarshal(stdout.Bytes(), &review); err != nil || review.ID == "" || review.AttemptID != attempt.ID || review.Decision != "approved" || review.Validate() != nil {
		t.Fatalf("unexpected review=%+v error=%v", review, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"summary-reviews", "--db", cfg.Telemetry.Database, "--attempt", attempt.ID}, &stdout, &stderr, "dev"); code != 0 {
		t.Fatalf("review history exit=%d stderr=%q", code, stderr.String())
	}
	var reviews []sessions.SummaryReview
	if err := json.Unmarshal(stdout.Bytes(), &reviews); err != nil || len(reviews) != 1 || !reflect.DeepEqual(reviews[0], review) {
		t.Fatalf("unexpected review history=%+v error=%v", reviews, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"summary-review", "--config", configPath, "--attempt", attempt.ID, "--expected", review.ID, "--decision", "rejected", "--note", "Needs further correction"}, &stdout, &stderr, "dev"); code != 0 {
		t.Fatalf("review revision exit=%d stderr=%q", code, stderr.String())
	}
	var revised sessions.SummaryReview
	if err := json.Unmarshal(stdout.Bytes(), &revised); err != nil || revised.PreviousID != review.ID || revised.Decision != "rejected" {
		t.Fatalf("unexpected revised review=%+v error=%v", revised, err)
	}
	after, err := sessions.Replay(ctx, db, source.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("summary activated or changed source", err)
	}
	afterEvents, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatal("source events changed", err)
	}
	inspection, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: cfg.Telemetry.Database, RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var eventCount, taskCount int
	if err := inspection.QueryRowContext(ctx, "SELECT count(*),count(DISTINCT task_id) FROM events").Scan(&eventCount, &taskCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != len(beforeEvents) || taskCount != 1 || calls.Load() != 2 {
		t.Fatalf("draft created runtime execution: events=%d tasks=%d calls=%d", eventCount, taskCount, calls.Load())
	}
}
