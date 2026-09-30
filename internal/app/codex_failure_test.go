package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestCodexTaskRejectsInvalidLocalGoWithDurableEvidence(t *testing.T) {
	ctx := context.Background()
	const invalid = "package answer\nfunc Answer( { INVALID_LOCAL_GO_MARKER"
	var calls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("coordinator credential reached worker")
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": invalid}, "done": true, "done_reason": "stop"}); err != nil {
			t.Error(err)
		}
	}))
	defer local.Close()
	cfg := codexTaskConfig(t)
	cfg.Workers.Max, cfg.Workers.DelegateMaxCalls = 2, 1
	cfg.Workers.DelegateModel = "worker"
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
	zero := 0.0
	cfg.Models = append(cfg.Models, config.Model{ID: "worker", Provider: "local", Model: "fixture-local", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 8 << 30, AvailableRAM: 7 << 30}, nil
	}
	p := &codexTaskFixture{}
	dir := ""
	svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		dir = spec.CWD
		return p, nil
	}
	result, err := svc.Run(ctx, Request{ModelID: "brain", Prompt: "Delegate then review", Domain: "code"})
	// A rejection is deliberately returned for the coordinator to review; it
	// need not fail the parent, but it must never contain accepted child output.
	if err != nil || result.TaskID == "" || p.calls != 2 || calls.Load() != 1 {
		t.Fatal(result, err, p.calls, calls.Load())
	}
	if strings.Contains(p.result, "untrusted_output") || strings.Contains(p.result, invalid) {
		t.Fatalf("invalid worker output released: %q", p.result)
	}
	if p.closed != 1 || dir == "" {
		t.Fatal("coordinator lifetime not closed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("private coordinator directory retained", err)
	}
	// Reopen durable storage rather than relying on an in-memory callback.
	raw, err := sql.Open("sqlite", "file:"+cfg.Telemetry.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.QueryContext(ctx, `SELECT body FROM events ORDER BY task_id,sequence LIMIT 101`)
	if err != nil {
		t.Fatal(err)
	}
	var events []runtime.Event
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		var e runtime.Event
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(events) == 101 {
		t.Fatal("fixture exceeded bounded evidence read")
	}
	work, execution := "", ""
	for _, e := range events {
		if e.Kind == runtime.TaskStarted && e.Data.ParentTaskID == result.TaskID {
			work = e.TaskID
		}
	}
	for _, e := range events {
		if e.Kind == runtime.TaskStarted && e.Data.ParentTaskID == work {
			execution = e.TaskID
		}
	}
	if work == "" || execution == "" {
		t.Fatal("missing durable delegation chain")
	}
	rejected, toolError := false, false
	for _, e := range events {
		if e.TaskID == execution && e.Kind == runtime.EvaluationRecorded && e.Data.Code == "deterministic.go_syntax.v1" && e.Data.Accepted != nil && !*e.Data.Accepted {
			rejected = true
		}
		if e.TaskID == work && (e.Kind == runtime.TaskCompleted || (e.Kind == runtime.EvaluationRecorded && e.Data.Accepted != nil && *e.Data.Accepted)) {
			t.Fatal("failed worker accepted")
		}
		if e.TaskID == result.TaskID && e.Kind == runtime.ToolCompleted && e.Data.ToolName == "delegate" {
			toolError = e.Data.Text == p.result
		}
	}
	if !rejected || !toolError {
		t.Fatal("missing persisted rejection or coordinator error result", rejected, toolError)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	verifyCodexDelegateRejection(t, ctx, db, p.result, "invalid_output", work, execution)
	for _, id := range []string{work, execution} {
		snapshot, err := db.TaskSnapshot(ctx, id)
		if err != nil || snapshot.State != "failed" {
			t.Fatal(fmt.Sprintf("child %s not durably failed", id), snapshot, err)
		}
		if id == execution && snapshot.Privacy != "local_only" {
			t.Fatal("local child privacy missing")
		}
	}
}

// Assert the public JSON projection, independently of its production Go type,
// against freshly read terminal evidence. Neither output nor retry permission
// belongs in a rejection; the coordinator gets only bounded diagnostic facts.
func verifyCodexDelegateRejection(t *testing.T, ctx context.Context, db *telemetry.Store, body, reason, work, execution string) {
	t.Helper()
	workPage, err := db.ReadEventPage(ctx, work, 0, 1)
	if err != nil || len(workPage.Events) != 1 {
		t.Fatal("missing work origin record", err)
	}
	start := workPage.Events[0]
	origin := start.Data.DelegationOrigin
	if origin == nil || origin.Validate() != nil || origin.ToolName != "delegate" || origin.BatchIndex != nil {
		t.Fatal("missing single-call provenance")
	}
	parentPage, err := db.ReadEventPage(ctx, start.Data.ParentTaskID, 0, 100)
	if err != nil || parentPage.HasMore || parentPage.SessionID != start.SessionID {
		t.Fatal("parent provenance unavailable", err)
	}
	matched := false
	for _, e := range parentPage.Events {
		if e.Kind == runtime.ToolCompleted && e.Data.Text == body {
			matched = e.TurnID == origin.TurnID && e.AttemptID == origin.AttemptID && e.Data.ToolCallID == origin.ToolCallID && e.Data.ToolName == origin.ToolName
		}
	}
	if !matched {
		t.Fatal("work origin does not match actual coordinator call")
	}
	var report struct {
		Version     int    `json:"version"`
		Error       string `json:"error"`
		Reason      string `json:"reason"`
		WorkID      string `json:"work_task_id"`
		ExecutionID string `json:"execution_task_id"`
		Evidence    []struct {
			TaskID   string       `json:"task_id"`
			Sequence int64        `json:"sequence"`
			Kind     runtime.Kind `json:"kind"`
			Code     string       `json:"code"`
		} `json:"evidence"`
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if len(body) > 2048 || !json.Valid([]byte(body)) || decoder.Decode(&report) != nil || report.Version != 1 || report.Error != "delegate_unavailable_or_rejected" || report.Reason != reason || report.WorkID != work || report.ExecutionID != execution || len(report.Evidence) != 2 {
		t.Fatalf("missing bounded rejection attribution: %s", body)
	}
	seen := map[string]bool{}
	for _, ref := range report.Evidence {
		if ref.Sequence < 1 || seen[ref.TaskID] || (ref.TaskID != work && ref.TaskID != execution) || (ref.Kind != runtime.TaskFailed && ref.Kind != runtime.TaskCanceled) {
			t.Fatal("invalid rejection evidence reference")
		}
		seen[ref.TaskID] = true
		page, err := db.ReadEventPage(ctx, ref.TaskID, ref.Sequence-1, 1)
		if err != nil || len(page.Events) != 1 || page.HeadSequence != ref.Sequence || page.HasMore {
			t.Fatal("rejection did not cite terminal durable evidence", err)
		}
		e := page.Events[0]
		if e.Sequence != ref.Sequence || e.Kind != ref.Kind || e.Data.Code != ref.Code {
			t.Fatal("rejection evidence differs from durable record")
		}
	}
}
