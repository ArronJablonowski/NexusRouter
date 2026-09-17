package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

const browserMutationTestSubject = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestBrowserChatUsesConfiguredDefaultModel(t *testing.T) {
	svc, cfg := autoFixture(t)
	svc.settings.WebUI.DefaultModel = "z"
	ctx := context.Background()
	store, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mutations, err := NewBrowserMutations(svc, store)
	if err != nil {
		t.Fatal(err)
	}
	request := contract.ChatRequest{Version: 1, Action: contract.ChatSubmit, IdempotencyKey: "browser-default-model-01", Text: "hello"}
	receipt, err := mutations.Chat(ctx, browserMutationTestSubject, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResumeSubmission(ctx, receipt.OperationID, Request{ModelID: "z", Prompt: "hello"}); err != nil {
		t.Fatal("configured browser model was not persisted", err)
	}
	if _, err = svc.ResumeSubmission(ctx, receipt.OperationID, Request{ModelID: "auto", Prompt: "hello"}); err == nil {
		t.Fatal("browser submission unexpectedly retained automatic routing")
	}
}

func browserApprovalRecord(state string, decisions []approvals.Decision, expires time.Time) approvals.Record {
	now := time.Now().UTC().Truncate(time.Second)
	request := approvals.Request{Version: 1, ID: "approval", TaskID: "task", TurnID: "turn", ToolCallID: "call", ToolName: "replace_file", ToolBehavior: runtime.BehaviorNonIdempotentWrite, Scope: "private-secret-scope", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), CreatedAt: now.Add(-time.Minute), ExpiresAt: expires}
	return approvals.Record{Request: request, State: state, Decisions: decisions}
}

func TestBrowserFeedbackCoexistsWithEvaluationAndReplays(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{Prompt: "hello", Domain: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordFeedback(ctx, cfg.Telemetry.Database, out.TaskID, true, 0); err != nil {
		t.Fatal(err)
	}
	store, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mutations, err := NewBrowserMutations(svc, store)
	if err != nil {
		t.Fatal(err)
	}
	cost := .25
	request := contract.FeedbackRequest{Version: 1, IdempotencyKey: "browser-feedback-key-01", TaskID: out.TaskID, Action: contract.FeedbackRecord, Accepted: false, AttemptCost: &cost}
	first, err := mutations.Feedback(ctx, browserMutationTestSubject, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mutations.Feedback(ctx, browserMutationTestSubject, request)
	if err != nil || second != first {
		t.Fatal(first, second, err)
	}
	revision := first.Revision
	revise := contract.FeedbackRequest{Version: 1, IdempotencyKey: "browser-feedback-key-02", TaskID: out.TaskID, FeedbackID: first.FeedbackID, Action: contract.FeedbackRevise, Accepted: true, ExpectedRevision: &revision}
	revised, err := mutations.Feedback(ctx, browserMutationTestSubject, revise)
	if err != nil || revised.State != "revised" || revised.Revision != 2 || !revised.Accepted {
		t.Fatal(revised, err)
	}
	replayed, err := mutations.Feedback(ctx, browserMutationTestSubject, revise)
	if err != nil || replayed != revised {
		t.Fatal(replayed, err)
	}
	feedbackContext, err := mutations.FeedbackContext(ctx, out.TaskID)
	if err != nil || feedbackContext.Subjective == nil || feedbackContext.Revision != 2 || feedbackContext.Subjective.Outcome != contract.EvidenceAccepted {
		t.Fatal(feedbackContext, err)
	}
	history, err := FeedbackHistory(ctx, cfg.Telemetry.Database, out.TaskID)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
}

func TestBrowserRejectedFeedbackReplaysBeforeChangedRequestConflict(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	store, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mutations, err := NewBrowserMutations(svc, store)
	if err != nil {
		t.Fatal(err)
	}
	cost := float64(0)
	request := contract.FeedbackRequest{Version: 1, IdempotencyKey: "browser-rejected-key-01", TaskID: "missing", Action: contract.FeedbackRecord, Accepted: true, AttemptCost: &cost}
	if _, err = mutations.Feedback(ctx, browserMutationTestSubject, request); !errors.Is(err, ErrAdmission) {
		t.Fatal("first rejection", err)
	}
	if _, err = mutations.Feedback(ctx, browserMutationTestSubject, request); !errors.Is(err, ErrAdmission) {
		t.Fatal("rejected replay", err)
	}
	page, err := mutations.Operations(ctx, browserMutationTestSubject, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "rejected" || page.Items[0].SubjectType != "task" || page.Items[0].SubjectID != "missing" || page.Items[0].OperationID == "" {
		t.Fatal("rejected operation projection", page, err)
	}
	request.Accepted = false
	if _, err = mutations.Feedback(ctx, browserMutationTestSubject, request); !errors.Is(err, browserops.ErrConflict) {
		t.Fatal("changed request reused key", err)
	}
}

func TestBrowserApprovalProjectionIsBoundedAndCapabilityExact(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	service := &Service{secret: func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "private-secret"
		}
		return ""
	}}
	mutations := &BrowserMutations{service: service}
	pending := browserApprovalRecord(approvals.Pending, nil, now.Add(time.Minute))
	summary, err := mutations.approvalSummary(context.Background(), pending)
	if err != nil || summary.Revision != 1 || !summary.CanAllow || !summary.CanDeny || summary.CanRevoke || summary.ScopeSummary == "" || strings.Contains(summary.ScopeSummary, "private-secret") || strings.Contains(summary.Prompt, pending.Request.Scope) || strings.Contains(summary.Prompt, pending.Request.ArgumentsDigest) {
		t.Fatal(summary, err)
	}
	decision := approvals.Decision{ID: "decision", Actor: "operator", Allowed: true, Time: now}
	consumedAt := now.Add(time.Second)
	consumed := browserApprovalRecord(approvals.Consumed, []approvals.Decision{decision}, now.Add(time.Minute))
	consumed.ConsumedAt = &consumedAt
	summary, err = mutations.approvalSummary(context.Background(), consumed)
	if err != nil || summary.Revision != 3 || summary.State != approvals.Consumed || summary.CanAllow || summary.CanDeny || summary.CanRevoke {
		t.Fatal(summary, err)
	}
}

func TestBrowserApprovalExpiredProjectionRemovesAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	record := browserApprovalRecord(approvals.Pending, nil, now.Add(-time.Second))
	mutations := &BrowserMutations{service: &Service{secret: func(string) string { return "" }}}
	summary, err := mutations.approvalSummary(context.Background(), record)
	if err != nil || summary.State != "expired" || summary.Revision != 1 || summary.CanAllow || summary.CanDeny || summary.CanRevoke {
		t.Fatal(summary, err)
	}
}

func TestBrowserWorkboardApprovalProjectionRequiresExactDurableProposal(t *testing.T) {
	if validApprovalProposalKey("short") || validApprovalProposalKey(strings.Repeat("x", contract.MaxIdempotencyBytes+1)) || validApprovalProposalKey("proposal key with spaces") || !validApprovalProposalKey("proposal-key-0001") {
		t.Fatal("proposal idempotency key bounds changed")
	}
	svc, cfg := autoFixture(t)
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "private-secret"
		}
		return ""
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Second)
	criterion := contract.AcceptanceCriterion{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Focused tests pass.", Required: true}
	args, _ := json.Marshal(map[string]any{"idempotency_key": "proposal-key-0001", "board_id": "board", "card_id": "card", "expected_board_revision": 2, "expected_card_revision": 3, "expected_criteria_revision": 1, "expected_criteria_digest": strings.Repeat("a", 64), "criteria": []contract.AcceptanceCriterion{criterion}})
	call := providers.ToolCall{ID: "proposal-call", Name: "workboard_propose_criteria", Arguments: args}
	start := inspectionEvent("proposal-task", 1, runtime.TaskStarted, now)
	turn := inspectionEvent("proposal-task", 2, runtime.TurnStarted, now)
	turn.TurnID, turn.AttemptID, turn.Data.ModelID, turn.Data.ProviderID = "proposal-turn", "proposal-attempt", "model", "provider"
	done := inspectionEvent("proposal-task", 3, runtime.TurnCompleted, now)
	done.TurnID, done.AttemptID, done.Data.FinishReason, done.Data.ToolCalls = turn.TurnID, turn.AttemptID, "tool_calls", []providers.ToolCall{call}
	toolStarted := inspectionEvent("proposal-task", 4, runtime.ToolStarted, now)
	toolStarted.TurnID, toolStarted.AttemptID = turn.TurnID, turn.AttemptID
	toolStarted.Data.ToolCallID, toolStarted.Data.ToolName, toolStarted.Data.ToolBehavior, toolStarted.Data.Effect = call.ID, call.Name, runtime.BehaviorIdempotentWrite, runtime.UncertainEffect
	for _, event := range []runtime.Event{start, turn, done, toolStarted} {
		appendInspectionEvent(t, db, event)
	}
	sum := sha256.Sum256(args)
	record := browserApprovalRecord(approvals.Pending, nil, now.Add(time.Minute))
	record.Request.TaskID, record.Request.TurnID, record.Request.ToolCallID, record.Request.ToolName = "proposal-task", turn.TurnID, call.ID, call.Name
	record.Request.ToolBehavior = runtime.BehaviorIdempotentWrite
	record.Request.ArgumentsDigest = hex.EncodeToString(sum[:])
	mutations := &BrowserMutations{service: svc}
	summary, err := mutations.approvalSummary(context.Background(), record)
	if err != nil || summary.Proposal == nil || summary.Proposal.Kind != "criteria_change" || len(summary.Proposal.Criteria) != 1 || !summary.CanAllow {
		t.Fatal(summary, err)
	}
	record.State, record.Decisions = approvals.Approved, []approvals.Decision{{ID: "proposal-decision", Actor: "operator", Allowed: true, Time: now}}
	summary, err = mutations.approvalSummary(context.Background(), record)
	if err != nil || summary.Proposal == nil || summary.State != approvals.Approved || !summary.CanRevoke {
		t.Fatal("approved proposal was not restart-safe", summary, err)
	}
	record.State, record.Decisions = approvals.Pending, nil
	record.Request.ArgumentsDigest = strings.Repeat("f", 64)
	if _, err = mutations.approvalSummary(context.Background(), record); !errors.Is(err, ErrBrowserMutation) {
		t.Fatal("digest mismatch did not fail closed", err)
	}
	secretArgs := []byte(strings.Replace(string(args), "Focused tests pass.", "private-secret", 1))
	secretCall := providers.ToolCall{ID: "secret-call", Name: call.Name, Arguments: secretArgs}
	secretStart := inspectionEvent("secret-proposal-task", 1, runtime.TaskStarted, now)
	secretTurn := inspectionEvent("secret-proposal-task", 2, runtime.TurnStarted, now)
	secretTurn.TurnID, secretTurn.AttemptID, secretTurn.Data.ModelID, secretTurn.Data.ProviderID = "secret-turn", "secret-attempt", "model", "provider"
	secretDone := inspectionEvent("secret-proposal-task", 3, runtime.TurnCompleted, now)
	secretDone.TurnID, secretDone.AttemptID, secretDone.Data.FinishReason, secretDone.Data.ToolCalls = secretTurn.TurnID, secretTurn.AttemptID, "tool_calls", []providers.ToolCall{secretCall}
	secretToolStarted := inspectionEvent("secret-proposal-task", 4, runtime.ToolStarted, now)
	secretToolStarted.TurnID, secretToolStarted.AttemptID = secretTurn.TurnID, secretTurn.AttemptID
	secretToolStarted.Data.ToolCallID, secretToolStarted.Data.ToolName, secretToolStarted.Data.ToolBehavior, secretToolStarted.Data.Effect = secretCall.ID, secretCall.Name, runtime.BehaviorIdempotentWrite, runtime.UncertainEffect
	for _, event := range []runtime.Event{secretStart, secretTurn, secretDone, secretToolStarted} {
		appendInspectionEvent(t, db, event)
	}
	secretDigest := sha256.Sum256(secretArgs)
	record.Request.TaskID, record.Request.TurnID, record.Request.ToolCallID = "secret-proposal-task", secretTurn.TurnID, secretCall.ID
	record.Request.ArgumentsDigest = hex.EncodeToString(secretDigest[:])
	if _, err = mutations.approvalSummary(context.Background(), record); !errors.Is(err, ErrBrowserMutation) {
		t.Fatal("credential-bearing proposal did not fail closed", err)
	}
}

type browserBlockedProposalProvider struct {
	arguments json.RawMessage
	turn      int
}

func (*browserBlockedProposalProvider) Models(context.Context) ([]string, error) {
	return []string{"coordinator"}, nil
}

func (p *browserBlockedProposalProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	p.turn++
	if p.turn == 1 {
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "browser-proposal-call", Name: "workboard_propose_criteria", Arguments: p.arguments}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
	}
	if p.turn != 2 || len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Role != "tool" || !request.Messages[len(request.Messages)-1].ToolFailed {
		return errors.New("denied proposal result missing")
	}
	return emit(providers.Chunk{Text: "The operator denied the proposal.", Done: true, FinishReason: "stop"})
}

func TestBrowserListsLiveBlockedProposalAfterToolStarted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode, cfg.Telemetry.Database, cfg.Security.ToolPolicy = "local_only", t.TempDir()+"/state.db", "ask"
	cfg.Tools.WorkboardReadEnabled, cfg.Tools.WorkboardWriteEnabled = true, true
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "coordinator", Model: "coordinator", Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 200_000, EstimatedCost: &zero, RAMBytes: 100}}
	arguments := json.RawMessage(` {
  "criteria" : [ { "required" : true, "description" : "Focused tests pass.", "validator_id" : "go-test", "required_source" : "deterministic", "kind" : "objective", "id" : "tests", "version" : 1 } ],
  "expected_criteria_digest" : "` + strings.Repeat("a", 64) + `", "expected_criteria_revision" : 1,
  "expected_card_revision" : 3, "expected_board_revision" : 2, "card_id" : "card", "board_id" : "board",
  "idempotency_key" : "browser-proposal-key"
} `)
	provider := &browserBlockedProposalProvider{arguments: arguments}
	presented := make(chan tools.ApprovalPrompt, 1)
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) { return provider, nil })
	svc.toolPresenter = func(_ context.Context, prompt tools.ApprovalPrompt) error { presented <- prompt; return nil }
	type runOutcome struct {
		result Result
		err    error
	}
	finished := make(chan runOutcome, 1)
	go func() {
		result, runErr := svc.Run(ctx, Request{ModelID: "coordinator", Prompt: "Propose exact criteria."})
		finished <- runOutcome{result: result, err: runErr}
	}()
	var prompt tools.ApprovalPrompt
	select {
	case prompt = <-presented:
		if string(prompt.Arguments) == string(arguments) {
			t.Fatal("provider argument whitespace and key order were not canonicalized before approval")
		}
	case outcome := <-finished:
		t.Fatalf("provider stopped before blocked approval: result=%+v err=%v", outcome.result, outcome.err)
	case <-ctx.Done():
		t.Fatal("provider did not reach blocked approval")
	}
	operationStore, err := browserops.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer operationStore.Close()
	mutations, err := NewBrowserMutations(svc, operationStore)
	if err != nil {
		t.Fatal(err)
	}
	page, err := mutations.Approvals(ctx, prompt.Request.TaskID, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Proposal == nil || !page.Items[0].CanAllow {
		t.Fatalf("blocked proposal unavailable: page=%+v err=%v", page, err)
	}
	snapshot, err := InspectTask(ctx, cfg.Telemetry.Database, prompt.Request.TaskID)
	pending := snapshot.Pending[prompt.Request.ToolCallID]
	if err != nil || !pending.Dispatched || pending.ToolBehavior != runtime.BehaviorIdempotentWrite {
		t.Fatalf("approval was not observed after ToolStarted: pending=%+v err=%v", pending, err)
	}
	decision := contract.ApprovalRequest{Version: 1, IdempotencyKey: "browser-deny-proposal", TaskID: prompt.Request.TaskID, ApprovalID: prompt.Request.ID, Action: contract.ApprovalDeny, ExpectedRevision: 1}
	if receipt, decideErr := mutations.DecideApproval(ctx, browserMutationTestSubject, decision); decideErr != nil || receipt.State != approvals.Denied {
		t.Fatalf("deny receipt=%+v err=%v", receipt, decideErr)
	}
	select {
	case outcome := <-finished:
		if outcome.err == nil || outcome.result.Text != "" || provider.turn != 1 {
			t.Fatalf("result=%+v turns=%d err=%v", outcome.result, provider.turn, outcome.err)
		}
	case <-ctx.Done():
		t.Fatal("denied provider run did not resume")
	}
}
