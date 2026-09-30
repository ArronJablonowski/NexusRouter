package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

type approvedAgentProposalProvider struct {
	tool      string
	arguments json.RawMessage
	stage     atomic.Int32
}

func (*approvedAgentProposalProvider) Models(context.Context) ([]string, error) {
	return []string{"coordinator"}, nil
}

func (p *approvedAgentProposalProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	names := make([]string, len(request.Tools))
	for index := range request.Tools {
		names[index] = request.Tools[index].Name
	}
	for _, required := range []string{workboard.CriteriaProposalTool, workboard.CandidateDecisionRequestTool} {
		if !slices.Contains(names, required) {
			return errors.New("approved proposal tool missing from provider catalog")
		}
	}
	switch p.stage.Add(1) {
	case 1:
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "proposal-call", Name: p.tool, Arguments: p.arguments}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
	case 2:
		if err := requireLastWorkboardToolResult(request.Messages, "proposal-call"); err != nil {
			return err
		}
		return emit(providers.Chunk{Text: "operator-approved proposal applied", Done: true, FinishReason: "stop"})
	default:
		return errors.New("unexpected proposal provider turn")
	}
}

func runApprovedAgentProposalProvider(t *testing.T, database string, provider *approvedAgentProposalProvider) Result {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = database
	cfg.Tools.WorkboardReadEnabled = true
	cfg.Tools.WorkboardWriteEnabled = true
	cfg.Security.ToolPolicy = "ask"
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "coordinator", Model: "coordinator", Provider: "local", Locality: "local",
		Capabilities: []string{"chat"}, ContextTokens: 200_000, EstimatedCost: &zero, RAMBytes: 100}}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	svc.toolReviewer = func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
		return "authenticated-operator", true, nil
	}
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) { return provider, nil })
	result, err := svc.Run(context.Background(), Request{ModelID: "coordinator", Prompt: "Submit the exact reviewed Workboard proposal."})
	if err != nil || result.Text != "operator-approved proposal applied" || provider.stage.Load() != 2 {
		t.Fatalf("result=%+v turns=%d err=%v", result, provider.stage.Load(), err)
	}
	return result
}

func TestProviderApprovedCriteriaProposalUsesConsumedOperatorAuthority(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "criteria-proposal.db")
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	title := "Criteria proposal"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate, IdempotencyKey: "proposal-board-create", Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	oldCriterion := webui.AcceptanceCriterion{Version: 1, ID: "old-tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Old tests pass.", Required: true}
	cardReceipt, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate, IdempotencyKey: "proposal-card-create1",
		BoardID: board.BoardID, Title: &title, Criteria: []webui.AcceptanceCriterion{oldCriterion}, ExpectedBoardRevision: &board.BoardRevision, ExpectedGraphRevision: revisionPointer(1)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(snapshot.Cards) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	oldDomain := []workboard.AcceptanceCriterion{{Version: 1, ID: oldCriterion.ID, Kind: oldCriterion.Kind, RequiredSource: oldCriterion.RequiredSource, ValidatorID: oldCriterion.ValidatorID, Description: oldCriterion.Description, Required: true}}
	newCriterion := map[string]any{"version": 1, "id": "new-tests", "kind": "objective", "required_source": "deterministic", "validator_id": "go-test", "description": "New focused tests pass.", "required": true}
	arguments, err := json.Marshal(map[string]any{"idempotency_key": "provider-criteria-proposal", "board_id": board.BoardID, "card_id": cardReceipt.CardID,
		"expected_board_revision": snapshot.Board.Revision, "expected_card_revision": snapshot.Cards[0].Revision,
		"expected_criteria_revision": snapshot.Cards[0].CriteriaRevision, "expected_criteria_digest": workboard.AcceptanceCriteriaDigest(oldDomain), "criteria": []any{newCriterion}})
	if err != nil {
		t.Fatal(err)
	}
	runApprovedAgentProposalProvider(t, database, &approvedAgentProposalProvider{tool: workboard.CriteriaProposalTool, arguments: arguments})
	card, err := store.GetCard(ctx, board.BoardID, cardReceipt.CardID)
	if err != nil || card.CriteriaRevision != 2 || len(card.Criteria) != 1 || card.Criteria[0].ID != "new-tests" {
		t.Fatalf("approved criteria not applied: card=%+v err=%v", card, err)
	}
	assertApprovedProposalEvent(t, bridge, board.BoardID, webui.CriteriaRevise)
}

func TestProviderApprovedCandidateDecisionUsesConsumedOperatorAuthority(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "decision-proposal.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	defer store.Close()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("e", 64), 20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "proposal-worker-task", SessionID: "proposal-worker-session",
		ParentTaskID: "proposal-parent", Scope: "board-card-" + cardID, ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, worker *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			if bindErr := bindTestWorkboardRuntime(run, worker); bindErr != nil {
				return WorkboardCandidate{}, bindErr
			}
			return WorkboardCandidate{Summary: "validated candidate"}, nil
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := bridge.NativeRead(ctx, boardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(snapshot.Cards) != 1 || len(snapshot.Lifecycle) != 1 || snapshot.Lifecycle[0].Attempt.Candidate == nil {
		t.Fatalf("review snapshot=%+v err=%v", snapshot, err)
	}
	attempt := snapshot.Lifecycle[0].Attempt
	candidate := attempt.Candidate
	arguments, err := json.Marshal(map[string]any{"idempotency_key": "provider-decision-proposal", "board_id": boardID, "card_id": cardID,
		"attempt_id": attempt.ID, "candidate_id": candidate.ID, "expected_board_revision": snapshot.Board.Revision,
		"expected_card_revision": snapshot.Cards[0].Revision, "expected_attempt_revision": attempt.Revision,
		"criteria_revision": attempt.CriteriaRevision, "evidence_head_revision": len(attempt.Evidence), "candidate_digest": candidate.Digest,
		"criteria_digest": attempt.CriteriaDigest, "evidence_set_digest": webui.EvidenceDigest(attempt.Evidence), "policy_digest": attempt.PolicyDigest,
		"decision": "accepted", "rationale": "The deterministic validator passed the required criterion."})
	if err != nil {
		t.Fatal(err)
	}
	runApprovedAgentProposalProvider(t, database, &approvedAgentProposalProvider{tool: workboard.CandidateDecisionRequestTool, arguments: arguments})
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil || card.State != workboard.Done || card.AcceptanceID == "" {
		t.Fatalf("approved decision not applied: card=%+v err=%v", card, err)
	}
	assertApprovedProposalEvent(t, bridge, boardID, webui.AcceptanceAccept)
}

func assertApprovedProposalEvent(t *testing.T, bridge *WorkboardBridge, boardID string, kind webui.BoardAction) {
	t.Helper()
	page, err := bridge.NativeEvents(context.Background(), boardID, webui.BoardEventOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for index := len(page.Items) - 1; index >= 0; index-- {
		if page.Items[index].Kind == kind {
			if page.Items[index].ActorType != "operator" || page.Items[index].ActorID == "" {
				t.Fatalf("proposal applied without operator actor: %+v", page.Items[index])
			}
			return
		}
	}
	t.Fatalf("proposal event %s missing", kind)
}
