package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// Exercise actual runtime/worker journals and dispatcher recovery, then the
// Codex session protocol fixture. No live Codex process or cloud request runs.
func TestCodexRecoveredSourceExplicitContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var localCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"package answer\nfunc Answer() int { return 42 }"},"done":true,"done_reason":"stop"}`)
	}))
	defer local.Close()
	cfg := codexTaskConfig(t)
	cfg.Workers.Max = 2
	cfg.Workers.DelegateModel = "worker"
	cfg.Workers.DelegateMaxCalls = 1
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
	first := &codexTaskFixture{}
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) { return first, nil }
	r := Request{ModelID: "brain", Prompt: "Delegate then review", Domain: "code"}
	status, err := svc.Submit(ctx, "codex-recovered-fixture", r)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claim, err := db.ClaimSubmission(ctx, svc.submissionConfigDigest(), time.Now(), time.Minute)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal(claim.Status.ID, err)
	}
	raw, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`CREATE TRIGGER interrupt_codex_result BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.kind')='tool.completed' AND json_extract(NEW.body,'$.data.tool_name')='delegate' BEGIN SELECT RAISE(ABORT,'fixture interrupted commit'); END`); err != nil {
		t.Fatal(err)
	}
	r.submissionID, r.submissionToken = status.ID, claim.Token
	previous, runErr := svc.Run(ctx, r)
	if runErr == nil || previous.TaskID == "" || first.calls != 1 || first.closed != 1 || localCalls.Load() != 1 {
		t.Fatal("fault boundary not reached", previous, runErr, first.calls, localCalls.Load())
	}
	if _, err = raw.Exec(`DROP TRIGGER interrupt_codex_result`); err != nil {
		t.Fatal(err)
	}
	before, err := db.ReadEventPage(ctx, previous.TaskID, 0, 100)
	if err != nil || before.State != "running" || before.HasMore || before.Events[len(before.Events)-1].Kind != runtime.ToolStarted {
		t.Fatal("source not awaiting result", before, err)
	}
	status, err = svc.SubmissionStatus(ctx, status.ID)
	if err != nil {
		t.Fatal(err)
	}
	children := map[string][]runtime.Event{}
	for _, id := range status.TaskIDs {
		if id == previous.TaskID {
			continue
		}
		page, err := db.ReadEventPage(ctx, id, 0, 100)
		if err != nil || page.State != "completed" || page.HasMore {
			t.Fatal("child not terminal", id, err)
		}
		children[id] = page.Events
	}
	if len(children) != 2 {
		t.Fatal("missing work/execution tree", len(children))
	}
	expireRecoveryClaim(t, svc, status.ID)
	dispatcher := &Dispatcher{db: db}
	if _, err = dispatcher.recoverPage(ctx, svc.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	status, err = svc.SubmissionStatus(ctx, status.ID)
	if err != nil || status.State != "failed" || status.Result == nil || status.Result.TaskID != previous.TaskID || status.Result.Text != "" {
		t.Fatal(status, err)
	}
	recovered, err := db.ReadEventPage(ctx, previous.TaskID, 0, 100)
	if err != nil || recovered.HasMore || recovered.HeadSequence != before.HeadSequence+2 || !reflect.DeepEqual(before.Events, recovered.Events[:len(before.Events)]) {
		t.Fatal("recovery changed original events", err)
	}
	tool, end := recovered.Events[len(recovered.Events)-2], recovered.Events[len(recovered.Events)-1]
	if tool.Kind != runtime.ToolCompleted || tool.Data.Code != "delegation_recovered" || tool.Data.Effect != runtime.NoEffect || end.Kind != runtime.TaskFailed || end.Data.Code != "interrupted_after_delegation" || end.CausationID != tool.ID || end.TurnID != tool.TurnID || end.AttemptID != tool.AttemptID {
		t.Fatal("invalid recovery checkpoint", tool, end)
	}
	fresh, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh.profile = svc.profile
	wire := &codexHistoryWire{}
	launches := 0
	fresh.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		if spec.Privacy != "cloud_allowed" {
			t.Fatal("wrong source privacy")
		}
		return codexbridge.NewSession(ctx, wire, codexbridge.Options{Model: spec.Model, CWD: spec.CWD})
	}
	continued, err := fresh.Run(ctx, Request{ModelID: "brain", ContinueTaskID: previous.TaskID, Prompt: "Review recovered output without repeating work", Domain: "code"})
	if err != nil || continued.Text != "Reviewed saved work without rerunning it." || launches != 1 || localCalls.Load() != 1 || first.calls != 1 || !wire.closed {
		t.Fatal(continued, err, launches, localCalls.Load())
	}
	snapshot, err := db.TaskSnapshot(ctx, continued.TaskID)
	if err != nil || snapshot.State != "completed" || snapshot.ParentTaskID != previous.TaskID || snapshot.SessionID != before.Events[0].SessionID {
		t.Fatal(snapshot, err)
	}
	imported := 0
	for _, sent := range wire.writes {
		if sent.Method == "thread/inject_items" {
			imported++
			if !bytes.Contains(sent.Params, []byte("function_call_output")) || !bytes.Contains(sent.Params, []byte("untrusted_output")) || !bytes.Contains(sent.Params, []byte("local-call")) || !bytes.Contains(sent.Params, []byte("func Answer() int")) {
				t.Fatal("recovered tool result not imported", string(sent.Params))
			}
		}
	}
	if imported != 1 {
		t.Fatal("missing singular history import", imported)
	}
	after, err := db.ReadEventPage(ctx, previous.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(recovered, after) {
		t.Fatal("continuation rewrote source", err)
	}
	for id, original := range children {
		page, err := db.ReadEventPage(ctx, id, 0, 100)
		if err != nil || !reflect.DeepEqual(original, page.Events) {
			t.Fatal("continuation changed child", id, err)
		}
	}
}
