package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestHarnessEvidenceCatchupRestartFailureAndWorkspace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "local", Model: "model", ModelRevision: "1", ConfigSHA256: strings.Repeat("a", 64)}
	task := harness.TaskClass{Domain: "writing", Profile: "rubric-1", Difficulty: "unknown"}
	calls := 0
	for _, id := range []string{"failed", "completed"} {
		_, _, e := runtime.RunHarness(ctx, db, runtime.HarnessRequest{TaskID: id, SessionID: id, Attribution: runtime.HarnessAttribution{Identity: identity, Task: task}, ContextTokens: 8192, MaxOutputBytes: 1024, Execute: func(context.Context) (runtime.HarnessOutput, error) {
			calls++
			if id == "failed" {
				return runtime.HarnessOutput{}, errors.New("failure")
			}
			return runtime.HarnessOutput{Actual: identity, Text: "fixture result"}, nil
		}})
		if (id == "failed") != (e != nil) {
			t.Fatal(e)
		}
	}
	dir := filepath.Join(t.TempDir(), "ledger")
	ledger, err := harness.OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	cursor, n, err := ReconcileHarnessEvidencePage(ctx, path, ledger, HarnessEvidenceCursor{})
	if err == nil || n != 0 || cursor.After != 3 {
		t.Fatalf("failed copy advanced: %+v %d %v", cursor, n, err)
	}
	ledger, err = harness.OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	next, n, err := ReconcileHarnessEvidencePage(ctx, path, ledger, cursor)
	if err != nil || n != 1 || next.After != 4 {
		t.Fatal(next, n, err)
	}
	for _, start := range []HarnessEvidenceCursor{next, {}} {
		if _, _, err = ReconcileHarnessEvidencePage(ctx, path, ledger, start); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	snapshot, err := ledger.Snapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := harness.Select(harness.Request{Version: 1, Task: task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: identity, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}, snapshot, now, 0)
	if err != nil || selected.Primary.PendingOutputs != 1 || selected.Primary.EffectiveSamples != 0 || calls != 2 {
		t.Fatal(selected, err, calls)
	}

	// A newly started daemon repairs a previously completed task without a Run.
	background, e := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "background"))
	if e != nil {
		t.Fatal(e)
	}
	defer background.Close()
	service := &Service{harnessEvidence: background}
	service.settings.Telemetry.Database = path
	worker, e := StartHarnessEvidenceCatchup(ctx, service, db)
	if e != nil {
		t.Fatal(e)
	}
	defer worker.Close()
	timeout := time.After(3 * time.Second)
	for worker.Health().Status != "healthy" {
		select {
		case <-timeout:
			t.Fatal("catch-up did not finish", worker.Health())
		case <-time.After(10 * time.Millisecond):
		}
	}
	worker.Close()
	events, e := db.Read(ctx, "completed", 0, 3)
	if e != nil {
		t.Fatal(e)
	}
	outcome := *events[1].Data.HarnessOutcome
	digest, _ := outcome.Digest()
	// AppendReview requires an existing execution. This proves the worker copied it.
	e = background.AppendReview(ctx, harness.Review{Version: 1, ID: "fixture-review", ExecutionDigest: digest, Verdict: "passed", Method: "deterministic", MethodVersion: "fixture-1", Reviewer: "test", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}, time.Now().UTC())
	if e != nil || calls != 2 {
		t.Fatal("worker failed or reran inference", e, calls)
	}
	other := filepath.Join(t.TempDir(), "other.db")
	otherDB, err := telemetry.Open(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	otherDB.Close()
	if _, _, err = ReconcileHarnessEvidencePage(ctx, other, ledger, next); err == nil {
		t.Fatal("accepted foreign cursor")
	}
	rows, err := db.ScanHarnessCompletions(ctx, 0, 2)
	if err != nil || len(rows) != 2 || rows[1].Position != 2 || rows[1].TaskID != "" {
		t.Fatal(rows, err)
	}
	rows, err = db.ScanHarnessCompletions(ctx, 2, 2)
	if err != nil || len(rows) != 2 || rows[1].TaskID != "completed" {
		t.Fatal(rows, err)
	}
	for _, limit := range []int{0, 101} {
		if _, err = db.ScanHarnessCompletions(ctx, 0, limit); err == nil {
			t.Fatal("unbounded scan")
		}
	}
}

func TestHarnessEvidenceCatchupLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := &Service{}
	w, err := StartHarnessEvidenceCatchup(ctx, s, db)
	if err != nil {
		t.Fatal(err)
	}
	if c := w.Health(); c.Validate() != nil || c.Status != "disabled" {
		t.Fatal(c)
	}
	w.Close()
	w.Close()
	dir := filepath.Join(t.TempDir(), "ledger")
	ledger, err := harness.OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	s.harnessEvidence = ledger
	s.settings.Telemetry.Database = filepath.Join(t.TempDir(), "missing.db")
	w, err = StartHarnessEvidenceCatchup(ctx, s, db)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	deadline := time.After(3 * time.Second)
	for w.Health().Status != "degraded" {
		select {
		case <-deadline:
			t.Fatal("failure not observable")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if c := w.Health(); c.Validate() != nil || c.Code != "supervisor_error" {
		t.Fatal(c)
	}
	w.Close()
	if w.Health().Code != "supervisor_stopped" {
		t.Fatal(w.Health())
	}
}
