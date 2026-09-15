package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type agentProposalRepositoryFixture struct {
	criteria []workboard.ApprovedCriteriaProposal
	decision []workboard.ApprovedCandidateDecisionProposal
	stale    bool
}

func (r *agentProposalRepositoryFixture) ApplyApprovedCriteriaProposal(_ context.Context, proposal workboard.ApprovedCriteriaProposal, _ func() time.Time) (workboard.OperationReceipt, error) {
	r.criteria = append(r.criteria, proposal)
	if r.stale {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	return agentProposalReceipt(proposal.BoardID, proposal.CardID), nil
}

func (*agentProposalRepositoryFixture) ReconcileApprovedCriteriaProposal(context.Context, workboard.ApprovedCriteriaProposal) (workboard.OperationReceipt, bool, error) {
	return workboard.OperationReceipt{}, false, nil
}

func (r *agentProposalRepositoryFixture) ApplyApprovedCandidateDecisionProposal(_ context.Context, proposal workboard.ApprovedCandidateDecisionProposal, _ func() time.Time) (workboard.OperationReceipt, error) {
	r.decision = append(r.decision, proposal)
	if r.stale {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "attempt_revision"}
	}
	return agentProposalReceipt(proposal.BoardID, proposal.CardID), nil
}

func (*agentProposalRepositoryFixture) ReconcileApprovedCandidateDecisionProposal(context.Context, workboard.ApprovedCandidateDecisionProposal) (workboard.OperationReceipt, bool, error) {
	return workboard.OperationReceipt{}, false, nil
}

func agentProposalReceipt(boardID, cardID string) workboard.OperationReceipt {
	revision := int64(2)
	return workboard.OperationReceipt{Version: 1, BoardID: boardID, OperationID: "proposal-operation", RequestDigest: strings.Repeat("a", 64),
		ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1, EventCount: 1, TransactionBytes: 1,
		BoardRevision: 2, CardID: cardID, CardRevision: &revision, Outcome: "committed", CreatedAt: time.Now().UTC()}
}

type consumedProposalAuthority struct {
	approvalID string
	calls      int
}

func (a *consumedProposalAuthority) ExecuteApproved(ctx context.Context, _ tools.Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	a.calls++
	return invoke(tools.WithConsumedApproval(ctx, tools.ConsumedApproval{ID: a.approvalID}))
}

func executeAgentProposal(t *testing.T, executor tools.Executor, name, callID string, arguments any) (runtime.ToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return executor.ExecuteScoped(context.Background(), runtime.ToolExecution{TaskID: "task-a", SessionID: "session-a", TurnID: "turn-a", AttemptID: "runtime-attempt-a",
		Call: providers.ToolCall{ID: callID, Name: name, Arguments: raw}})
}

func agentCriteriaArguments() map[string]any {
	criterion := map[string]any{"version": 1, "id": "tests", "kind": "objective", "required_source": "deterministic", "validator_id": "go-test", "description": "Focused tests pass.", "required": true}
	return map[string]any{"idempotency_key": "criteria-proposal-key", "board_id": "board-a", "card_id": "card-a",
		"expected_board_revision": 1, "expected_card_revision": 1, "expected_criteria_revision": 1,
		"expected_criteria_digest": strings.Repeat("c", 64), "criteria": []any{criterion}}
}

func agentDecisionArguments() map[string]any {
	return map[string]any{"idempotency_key": "decision-proposal-key", "board_id": "board-a", "card_id": "card-a", "attempt_id": "attempt-a", "candidate_id": "candidate-a",
		"expected_board_revision": 1, "expected_card_revision": 2, "expected_attempt_revision": 3, "criteria_revision": 1, "evidence_head_revision": 2,
		"candidate_digest": strings.Repeat("a", 64), "criteria_digest": strings.Repeat("b", 64), "evidence_set_digest": strings.Repeat("c", 64),
		"policy_digest": strings.Repeat("d", 64), "decision": "accepted", "rationale": "All deterministic evidence passed."}
}

func TestApprovedAgentProposalToolsDeriveRuntimeAndConsumedApprovalOrigin(t *testing.T) {
	repository := &agentProposalRepositoryFixture{}
	registry := &tools.Registry{}
	if err := registerWorkboardAgentProposalTools(registry, repository); err != nil {
		t.Fatal(err)
	}
	authority := &consumedProposalAuthority{approvalID: "approval-a"}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}
	criteria, err := executeAgentProposal(t, executor, workboard.CriteriaProposalTool, "criteria-call", agentCriteriaArguments())
	if err != nil || criteria.Effect != runtime.ConfirmedEffect || criteria.Failed {
		t.Fatalf("criteria=%+v err=%v", criteria, err)
	}
	decision, err := executeAgentProposal(t, executor, workboard.CandidateDecisionRequestTool, "decision-call", agentDecisionArguments())
	if err != nil || decision.Effect != runtime.ConfirmedEffect || decision.Failed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if len(repository.criteria) != 1 || len(repository.decision) != 1 || authority.calls != 2 {
		t.Fatalf("criteria=%d decisions=%d approvals=%d", len(repository.criteria), len(repository.decision), authority.calls)
	}
	assertOrigin := func(origin workboard.AgentProposalOrigin, tool, call string) {
		t.Helper()
		if origin.TaskID != "task-a" || origin.SessionID != "session-a" || origin.TurnID != "turn-a" || origin.RuntimeAttemptID != "runtime-attempt-a" ||
			origin.ToolCallID != call || origin.ToolName != tool || origin.ApprovalID != "approval-a" {
			t.Fatalf("host-derived origin=%+v", origin)
		}
	}
	assertOrigin(repository.criteria[0].Origin, workboard.CriteriaProposalTool, "criteria-call")
	assertOrigin(repository.decision[0].Origin, workboard.CandidateDecisionRequestTool, "decision-call")
}

func TestApprovedAgentProposalToolsRejectModelOriginAndReturnStaleNoEffect(t *testing.T) {
	repository := &agentProposalRepositoryFixture{stale: true}
	registry := &tools.Registry{}
	if err := registerWorkboardAgentProposalTools(registry, repository); err != nil {
		t.Fatal(err)
	}
	authority := &consumedProposalAuthority{approvalID: "approval-a"}
	executor := tools.Executor{Registry: registry, Policy: applicationToolPolicy(), Authority: authority}
	arguments := agentCriteriaArguments()
	arguments["actor"] = "operator"
	if _, err := executeAgentProposal(t, executor, workboard.CriteriaProposalTool, "forged-call", arguments); !errors.Is(err, tools.ErrArguments) || authority.calls != 0 {
		t.Fatalf("model-supplied authority reached approval: calls=%d err=%v", authority.calls, err)
	}
	out, err := executeAgentProposal(t, executor, workboard.CandidateDecisionRequestTool, "stale-call", agentDecisionArguments())
	if err != nil || out.Effect != runtime.NoEffect || !out.Failed || !out.Recoverable || !strings.Contains(out.Content, `"code":"stale_revision"`) {
		t.Fatalf("stale result=%+v err=%v", out, err)
	}
}

func TestApprovedAgentProposalToolSchemasAreClosedAndPolicyGated(t *testing.T) {
	for _, spec := range workboardAgentProposalSpecs() {
		if !strings.Contains(string(spec.Parameters), `"additionalProperties":false`) {
			t.Fatalf("open schema: %s", spec.Name)
		}
		for configured, want := range map[string]tools.Decision{"deny": tools.Deny, "ask": tools.Ask, "allow": tools.Allow} {
			if got := applicationToolPolicyFor(configured).Decide(spec.Name, "workboard:board-a"); got != want {
				t.Fatalf("%s configured=%s got=%s want=%s", spec.Name, configured, got, want)
			}
		}
	}
}
