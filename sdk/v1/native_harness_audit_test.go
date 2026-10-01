package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

func nativeAuditClient(t *testing.T, evaluator sdk.Evaluator) (*sdk.Client, sdk.ConfigOptions, string) {
	t.Helper()
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Evaluation.Judge = true
	cfg.Evaluation.AutoReviewModel = ""
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "reviewer", Provider: "local", Model: "reviewer", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	id := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "local", Model: "candidate", ModelRevision: "weights-1", ConfigSHA256: strings.Repeat("a", 64)}
	var digest string
	for _, task := range []string{"native-completed", "native-failed"} {
		outcome, _, e := runtime.RunHarness(ctx, db, runtime.HarnessRequest{TaskID: task, SessionID: task, Attribution: runtime.HarnessAttribution{Identity: id, Task: harness.TaskClass{Domain: "code", Profile: "fixture", Difficulty: "unknown"}}, ContextTokens: 8192, MaxOutputBytes: 1024, Messages: []providers.Message{{Role: "user", Content: "produce an answer"}}, Privacy: "local_only", Execute: func(context.Context) (runtime.HarnessOutput, error) {
			if task == "native-failed" {
				return runtime.HarnessOutput{}, errors.New("fixture failure")
			}
			return runtime.HarnessOutput{Actual: id, Text: "candidate answer"}, nil
		}})
		if task == "native-completed" {
			if e != nil {
				t.Fatal(e)
			}
			digest, _ = outcome.Digest()
		} else if e == nil {
			t.Fatal("failed fixture succeeded")
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	options := sdk.ConfigOptions{ProjectFile: file, Evaluator: evaluator, ProviderFactory: sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		t.Error("review unexpectedly dispatched provider")
		return nil, errors.New("forbidden")
	})}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return client, options, digest
}

func TestSDKNativeHarnessAuditIsBoundAndReplayable(t *testing.T) {
	evaluator := validSDKEvaluator()
	evaluator.call = func(ctx context.Context, request sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
		if request.Candidate != "candidate answer" || !strings.Contains(request.Requirements, "native execution digest") {
			t.Error("native audit attribution missing")
		}
		found := false
		for _, e := range request.Evidence {
			if e.ID == "candidate_execution" && strings.Contains(e.Content, `"source_kind":"harness"`) {
				found = true
			}
		}
		if !found {
			t.Error("native execution identified as a provider turn")
		}
		return evaluator.response, nil
	}
	client, options, digest := nativeAuditClient(t, evaluator)
	request := sdk.AuditRequest{Version: 1, IdempotencyKey: "native-audit-idempotency-v1", TaskID: "native-completed", ReviewerModelID: "reviewer"}
	status, err := client.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil })
	if err != nil || status.Status != "rejected" || status.SourceKind != "harness" || status.SourceAttemptID != digest || status.Validate() != nil {
		t.Fatal(status, err)
	}
	reopened, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reopened.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil })
	if err != nil || replay.AuditID != status.AuditID || evaluator.calls.Load() != 1 {
		t.Fatal("review reinvoked", replay, err, evaluator.calls.Load())
	}
	page, err := reopened.ReadAuditEvents(context.Background(), request.TaskID, status.ID, 0)
	if err != nil || len(page.Events) != 2 || page.Events[0].Status.SourceKind != "harness" || page.Events[1].Status.SourceAttemptID != digest {
		t.Fatal(page, err)
	}

	ledger, e := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "operator-evidence"))
	if e != nil {
		t.Fatal(e)
	}
	defer ledger.Close()
	operator := harness.Review{Version: 1, ID: "operator-review", ExecutionDigest: digest, Verdict: "passed", Method: "human", MethodVersion: "fixture-rubric", Reviewer: "fixture-operator", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}
	if e = client.ReviewHarnessOutcome(context.Background(), ledger, request.TaskID, operator); e != nil {
		t.Fatal(e)
	}
	if _, e = client.ReconcileHarnessAudit(context.Background(), ledger, request.TaskID, status.ID); !errors.Is(e, harness.ErrConflict) {
		t.Fatal("automatic advisory overwrote operator head", e)
	}
	request.TaskID = "native-failed"
	request.IdempotencyKey = "native-audit-failed-lineage"
	if _, err = reopened.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil }); err == nil || evaluator.calls.Load() != 1 {
		t.Fatal("failed native lineage reviewed", err)
	}
}

func TestSDKNativeHarnessAuditFailureNeverRetries(t *testing.T) {
	evaluator := validSDKEvaluator()
	evaluator.call = func(context.Context, sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
		return sdk.EvaluatorResponse{}, errors.New("private diagnostic")
	}
	client, _, _ := nativeAuditClient(t, evaluator)
	request := sdk.AuditRequest{Version: 1, IdempotencyKey: "native-audit-failure-v1", TaskID: "native-completed", ReviewerModelID: "reviewer"}
	first, err := client.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil })
	if err == nil || first.Status != "failed" || first.AuditID != "" || strings.Contains(err.Error(), "private") {
		t.Fatal(first, err)
	}
	replay, _ := client.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return nil })
	if replay.Status != "failed" || evaluator.calls.Load() != 1 {
		t.Fatal("failed reviewer reran", replay, evaluator.calls.Load())
	}
}

func TestSDKHarnessAuditCatchupLateCompletionAndConflict(t *testing.T) {
	ctx := context.Background()
	evaluator := validSDKEvaluator()
	entered, release := make(chan struct{}), make(chan struct{})
	evaluator.call = func(ctx context.Context, _ sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
		close(entered)
		select {
		case <-release:
			return evaluator.response, nil
		case <-ctx.Done():
			return sdk.EvaluatorResponse{}, ctx.Err()
		}
	}
	client, _, digest := nativeAuditClient(t, evaluator)
	ledgerDir := filepath.Join(t.TempDir(), "catchup")
	ledger, err := harness.OpenEvidenceStore(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ledger.Close() }()
	request := sdk.AuditRequest{Version: 1, IdempotencyKey: "harness-catchup-late-v1", TaskID: "native-completed", ReviewerModelID: "reviewer"}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := client.RunAudit(runCtx, request, func(sdk.AuditEvent) error { return nil }); done <- e }()
	select {
	case <-entered:
	case e := <-done:
		t.Fatal("review exited before evaluator", e)
	case <-time.After(30 * time.Second):
		t.Fatal("review did not start")
	}
	page, err := client.ReconcileHarnessAuditPage(ctx, ledger, sdk.HarnessEvidenceCursor{})
	if err != nil || !page.CycleComplete || page.Copied != 0 || page.Cursor.After != 0 {
		t.Fatal(page, err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	ledger.Close()
	failed, err := client.ReconcileHarnessAuditPage(ctx, ledger, page.Cursor)
	if err == nil || failed.Cursor.After != 0 || failed.Copied != 0 {
		t.Fatal("failed copy skipped", failed, err)
	}
	ledger, err = harness.OpenEvidenceStore(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	page, err = client.ReconcileHarnessAuditPage(ctx, ledger, failed.Cursor)
	if err != nil || page.Copied != 1 || page.Conflicts != 0 || !page.CycleComplete {
		t.Fatal("late review lost", page, err)
	}
	replay, err := client.ReconcileHarnessAuditPage(ctx, ledger, page.Cursor)
	if err != nil || replay.Copied != 1 || evaluator.calls.Load() != 1 {
		t.Fatal("review rerun", replay, err)
	}
	// Existing exact review replays preserve the same sample count.
	events, err := client.ReadEvents(ctx, request.TaskID, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	outcome := *events.Events[1].Data.HarnessOutcome
	now := time.Now().UTC()
	snapshot, err := ledger.Snapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := harness.Select(harness.Request{Version: 1, Task: outcome.Task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: outcome.Actual, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}, snapshot, now, 0)
	if err != nil || selection.Primary.AdvisorySamples != 1 || selection.Primary.ConfirmedSamples != 0 {
		t.Fatal(selection, err)
	}
	operatorLedger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "operator"))
	if err != nil {
		t.Fatal(err)
	}
	defer operatorLedger.Close()
	review := harness.Review{Version: 1, ID: "operator", ExecutionDigest: digest, Verdict: "passed", Method: "human", MethodVersion: "fixture", Reviewer: "fixture-operator", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}
	if err = client.ReviewHarnessOutcome(ctx, operatorLedger, request.TaskID, review); err != nil {
		t.Fatal(err)
	}
	conflict, err := client.ReconcileHarnessAuditPage(ctx, operatorLedger, sdk.HarnessEvidenceCursor{})
	if err != nil || conflict.Conflicts != 1 || conflict.Copied != 0 || !conflict.CycleComplete {
		t.Fatal("operator head changed or blocked cycle", conflict, err)
	}
	if evaluator.calls.Load() != 1 {
		t.Fatal("catch-up reran evaluator")
	}
}

func TestSDKHarnessAuditCatchupSkipsFailedReview(t *testing.T) {
	evaluator := validSDKEvaluator()
	evaluator.call = func(context.Context, sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
		return sdk.EvaluatorResponse{}, errors.New("fixture failure")
	}
	client, _, _ := nativeAuditClient(t, evaluator)
	ctx := context.Background()
	_, err := client.RunAudit(ctx, sdk.AuditRequest{Version: 1, IdempotencyKey: "harness-catchup-failed-v1", TaskID: "native-completed", ReviewerModelID: "reviewer"}, func(sdk.AuditEvent) error { return nil })
	if err == nil {
		t.Fatal("failed review accepted")
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "failed-review"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	page, err := client.ReconcileHarnessAuditPage(ctx, ledger, sdk.HarnessEvidenceCursor{})
	if err != nil || page.Copied != 0 || page.Conflicts != 0 || !page.CycleComplete || evaluator.calls.Load() != 1 {
		t.Fatal(page, err, evaluator.calls.Load())
	}
}
