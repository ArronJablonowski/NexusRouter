package v1_test

import (
	"context"
	"encoding/json"
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
	"github.com/ArronJablonowski/NexusRouter/tools"
	"go.yaml.in/yaml/v3"
)

func TestSDKNativeAgentOutcomeAuditAndHeadBinding(t *testing.T) {
	ctx := context.Background()
	evaluator := validSDKEvaluator()
	evaluator.call = func(_ context.Context, r sdk.EvaluatorRequest) (sdk.EvaluatorResponse, error) {
		if r.Candidate != "agent final" || !strings.Contains(r.Requirements, "native execution digest") {
			t.Error("wrong native candidate")
		}
		source, tool := false, false
		for _, e := range r.Evidence {
			if e.ID == "candidate_execution" && strings.Contains(e.Content, `"source_kind":"harness"`) {
				source = true
			}
			if strings.Contains(e.Content, `"tool_call_id":"call"`) && strings.Contains(e.Content, `"code":"tool_failed_recoverable"`) {
				tool = true
			}
			if strings.HasPrefix(e.ID, "execution_") && strings.Contains(e.Content, "untrusted claim of success") {
				t.Error("raw tool output promoted to audit fact")
			}
		}
		if !source || !tool {
			t.Error("missing complete native execution evidence")
		}
		return evaluator.response, nil
	}
	client, options, _ := nativeAuditClient(t, evaluator)
	body, err := os.ReadFile(options.ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	if err = yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	registry := &tools.Registry{}
	effects := 0
	if err = registry.Register(tools.Definition{Tool: providers.Tool{Name: "lookup", Description: "fixture", Parameters: json.RawMessage(`{"type":"object"}`)}, Scope: "fixture", ReadOnly: true, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		effects++
		return runtime.ToolResult{Content: "untrusted claim of success", Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "tools-v1", Provider: "local", Model: "candidate", ModelRevision: "revision", ConfigSHA256: strings.Repeat("c", 64)}
	var completed harness.Execution
	for _, task := range []string{"agent-completed", "agent-failed"} {
		r := runtime.HarnessAgentRequest{Request: runtime.HarnessRequest{TaskID: task, SessionID: task, Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "produce an answer"}}, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: "code", Profile: "fixture", Difficulty: "hard"}}, ContextTokens: 8192, MaxOutputBytes: 4096}, MaxTurns: 2, Tools: tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Allow}}}
		r.Execute = func(c context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
			turn, attempt, e := s.BeginTurn(c)
			if e != nil {
				return runtime.HarnessOutput{}, e
			}
			calls, e := s.CompleteTurn(c, turn, attempt, runtime.HarnessTurnOutput{Actual: identity, Calls: []providers.ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}})
			if e != nil {
				return runtime.HarnessOutput{}, e
			}
			if _, e = s.Invoke(c, calls[0]); e != nil {
				return runtime.HarnessOutput{}, e
			}
			if task == "agent-failed" {
				return runtime.HarnessOutput{}, errors.New("fixture failure")
			}
			turn, attempt, e = s.BeginTurn(c)
			if e != nil {
				return runtime.HarnessOutput{}, e
			}
			_, e = s.CompleteTurn(c, turn, attempt, runtime.HarnessTurnOutput{Actual: identity, Text: "agent final"})
			return runtime.HarnessOutput{Actual: identity, Text: "agent final"}, e
		}
		outcome, _, e := runtime.RunHarnessAgent(ctx, db, r)
		if (e == nil) != (task == "agent-completed") {
			t.Fatal(task, e)
		}
		if task == "agent-completed" {
			completed = outcome
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for range 2 {
		outcome, e := client.ReconcileHarnessOutcome(ctx, ledger, "agent-completed")
		if e != nil || outcome != completed {
			t.Fatal(outcome, e)
		}
	}
	cursor, copied, e := client.ReconcileHarnessEvidencePage(ctx, ledger, sdk.HarnessEvidenceCursor{})
	if e != nil || copied != 2 || cursor.After == 0 {
		t.Fatal("new-format automatic catch-up", cursor, copied, e)
	}
	digest, _ := completed.Digest()
	request := sdk.AuditRequest{Version: 1, TaskID: "agent-completed", IdempotencyKey: "agent-audit-bound-v1", ReviewerModelID: "reviewer"}
	status, err := client.RunAudit(ctx, request, func(sdk.AuditEvent) error { return nil })
	if err != nil || status.Status != "rejected" || status.SourceKind != "harness" || status.SourceAttemptID != digest {
		t.Fatal(status, err)
	}
	reopened, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reopened.RunAudit(ctx, request, func(sdk.AuditEvent) error { return nil })
	if err != nil || replay.AuditID != status.AuditID || evaluator.calls.Load() != 1 || effects != 2 {
		t.Fatal("audit replay executed work", replay, err, effects)
	}
	review, err := client.ReconcileHarnessAudit(ctx, ledger, request.TaskID, status.ID)
	if err != nil {
		t.Fatal(err)
	}
	operator := harness.Review{Version: 1, ID: "agent-confirmed", ExpectedHead: review.ID, ExecutionDigest: digest, Verdict: "passed", Method: "deterministic", MethodVersion: "fixture-v1", Reviewer: "fixture-test", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}
	for range 2 {
		if e := client.ReviewHarnessOutcome(ctx, ledger, request.TaskID, operator); e != nil {
			t.Fatal(e)
		}
	}
	// An exact historical advisory replay is allowed, but it must leave the
	// newer confirmed head (and only one quality sample) authoritative.
	if _, e := client.ReconcileHarnessAudit(ctx, ledger, request.TaskID, status.ID); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	snapshot, e := ledger.Snapshot(ctx, now)
	if e != nil {
		t.Fatal(e)
	}
	selected, e := harness.Select(harness.Request{Version: 1, Task: completed.Task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: identity, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}, snapshot, now, 0)
	if e != nil || selected.Primary.ConfirmedSamples != 1 || selected.Primary.AdvisorySamples != 0 || selected.Primary.PendingOutputs != 0 {
		t.Fatal("current quality head changed", selected, e)
	}
	request.TaskID = "agent-failed"
	request.IdempotencyKey = "agent-audit-failed-v1"
	if _, e := client.ReconcileHarnessOutcome(ctx, ledger, request.TaskID); e == nil {
		t.Fatal("failed agent got outcome evidence")
	}
	if _, e := client.RunAudit(ctx, request, func(sdk.AuditEvent) error { return nil }); e == nil || evaluator.calls.Load() != 1 {
		t.Fatal("failed agent reviewed", e)
	}
	if e := client.ReviewHarnessOutcome(ctx, ledger, request.TaskID, operator); e == nil {
		t.Fatal("failed agent got quality vote")
	}
	if effects != 2 {
		t.Fatal("reconciliation repeated effects")
	}
}
