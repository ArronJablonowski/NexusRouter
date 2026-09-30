package telemetry

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func persistConsumedProposal(t *testing.T, store *Store, origin workboard.AgentProposalOrigin, boardID string, raw []byte, now time.Time) context.Context {
	t.Helper()
	events := []runtime.Event{
		{Version: 1, ID: "proposal-task-start", TaskID: origin.TaskID, SessionID: origin.SessionID, CorrelationID: origin.TaskID, Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "propose a board change"}}}},
		{Version: 1, ID: "proposal-turn-start", TaskID: origin.TaskID, SessionID: origin.SessionID, CorrelationID: origin.TaskID, Sequence: 2, Time: now.Add(time.Millisecond), Kind: runtime.TurnStarted, TurnID: origin.TurnID, AttemptID: origin.RuntimeAttemptID, Data: runtime.Data{ModelID: "coordinator-model", ProviderID: "coordinator-provider"}},
		{Version: 1, ID: "proposal-turn-complete", TaskID: origin.TaskID, SessionID: origin.SessionID, CorrelationID: origin.TaskID, Sequence: 3, Time: now.Add(2 * time.Millisecond), Kind: runtime.TurnCompleted, TurnID: origin.TurnID, AttemptID: origin.RuntimeAttemptID, Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: origin.ToolCallID, Name: origin.ToolName, Arguments: raw}}}},
		{Version: 1, ID: "proposal-tool-start", TaskID: origin.TaskID, SessionID: origin.SessionID, CorrelationID: origin.TaskID, Sequence: 4, Time: now.Add(3 * time.Millisecond), Kind: runtime.ToolStarted, TurnID: origin.TurnID, AttemptID: origin.RuntimeAttemptID, Data: runtime.Data{ToolCallID: origin.ToolCallID, ToolName: origin.ToolName, ToolBehavior: runtime.BehaviorIdempotentWrite, Effect: runtime.UncertainEffect}},
	}
	for i, event := range events {
		if err := store.Append(context.Background(), int64(i), event); err != nil {
			t.Fatal(err)
		}
	}
	decisionAt, consumedAt := now.Add(4*time.Millisecond), now.Add(5*time.Millisecond)
	record := approvals.Record{Request: approvals.Request{Version: 1, ID: origin.ApprovalID, TaskID: origin.TaskID, TurnID: origin.TurnID, ToolCallID: origin.ToolCallID,
		ToolName: origin.ToolName, ToolBehavior: runtime.BehaviorIdempotentWrite, Scope: "workboard:" + boardID, ArgumentsDigest: digestBytes(raw),
		SchemaDigest: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, State: approvals.Consumed,
		Decisions: []approvals.Decision{{ID: "approval-decision", Actor: "authenticated-operator", Allowed: true, Time: decisionAt}}, ConsumedAt: &consumedAt}
	body, err := json.Marshal(record)
	if err != nil || record.Validate() != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO tool_approvals(id,task_id,tool_call_id,state,body) VALUES(?,?,?,?,?)`, origin.ApprovalID, origin.TaskID, origin.ToolCallID, record.State, body); err != nil {
		t.Fatal(err)
	}
	return tools.WithConsumedApproval(context.Background(), tools.ConsumedApproval{ID: origin.ApprovalID})
}

func TestApprovedCriteriaProposalExactBindingReplayAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, context.Background(), store, now)
	proposalTime := card.UpdatedAt.Add(time.Second)
	var boardRevision int64
	if err = store.db.QueryRow(`SELECT revision FROM workboard_boards WHERE id=?`, boardID).Scan(&boardRevision); err != nil {
		t.Fatal(err)
	}
	criteria := []workboard.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "All deterministic tests pass.", Required: true}}
	origin := workboard.AgentProposalOrigin{TaskID: "proposal-task", SessionID: "proposal-session", TurnID: "proposal-turn", RuntimeAttemptID: "proposal-attempt", ToolCallID: "proposal-call", ToolName: workboard.CriteriaProposalTool, ApprovalID: "proposal-approval"}
	p := workboard.ApprovedCriteriaProposal{Origin: origin, IdempotencyKey: "approved-criteria-01", BoardID: boardID, CardID: card.ID, ExpectedBoardRevision: boardRevision,
		ExpectedCardRevision: card.Revision, ExpectedCriteriaRevision: card.CriteriaRevision, ExpectedCriteriaDigest: workboard.AcceptanceCriteriaDigest(card.Criteria), Criteria: criteria}
	raw, err := json.Marshal(criteriaProposalArgs(p))
	if err != nil {
		t.Fatal(err)
	}
	ctx := persistConsumedProposal(t, store, origin, boardID, raw, proposalTime)
	receipt, err := store.ApplyApprovedCriteriaProposal(ctx, p, func() time.Time { return proposalTime.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetCard(context.Background(), boardID, card.ID)
	if err != nil || !reflect.DeepEqual(stored.Criteria, criteria) {
		t.Fatalf("card=%+v err=%v", stored, err)
	}
	replayed, err := store.ApplyApprovedCriteriaProposal(ctx, p, func() time.Time { return proposalTime.Add(2 * time.Second) })
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := p
	changed.ExpectedBoardRevision++
	if _, err = store.ApplyApprovedCriteriaProposal(ctx, changed, time.Now); err == nil {
		t.Fatal("changed proposal escaped exact raw argument binding")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reconciled, found, err := store.ReconcileApprovedCriteriaProposal(context.Background(), p)
	if err != nil || !found || !reflect.DeepEqual(reconciled, receipt) {
		t.Fatalf("reconcile=%+v found=%v err=%v", reconciled, found, err)
	}
}

func TestApprovedProposalRequiresTrustedConsumedContext(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, context.Background(), store, now)
	var boardRevision int64
	_ = store.db.QueryRow(`SELECT revision FROM workboard_boards WHERE id=?`, boardID).Scan(&boardRevision)
	origin := workboard.AgentProposalOrigin{TaskID: "proposal-task", SessionID: "proposal-session", TurnID: "proposal-turn", RuntimeAttemptID: "proposal-attempt", ToolCallID: "proposal-call", ToolName: workboard.CriteriaProposalTool, ApprovalID: "proposal-approval"}
	p := workboard.ApprovedCriteriaProposal{Origin: origin, IdempotencyKey: "approved-criteria-02", BoardID: boardID, CardID: card.ID, ExpectedBoardRevision: boardRevision, ExpectedCardRevision: card.Revision,
		ExpectedCriteriaRevision: card.CriteriaRevision, ExpectedCriteriaDigest: workboard.AcceptanceCriteriaDigest(card.Criteria), Criteria: []workboard.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "New test condition.", Required: true}}}
	raw, _ := json.Marshal(criteriaProposalArgs(p))
	persistConsumedProposal(t, store, origin, boardID, raw, now.Add(time.Second))
	if _, err = store.ApplyApprovedCriteriaProposal(context.Background(), p, time.Now); err == nil {
		t.Fatal("apply accepted without consumed approval context")
	}
}

func TestApprovedCandidateDecisionUsesOperatorAuthorityAndExactAttemptFence(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, context.Background(), store, clock)
	clock = card.UpdatedAt.Add(time.Second)
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("proposal-proof", workboard.EffectFree), &clock)
	if _, err = lifecycle.Claim(context.Background(), workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "proposal-claim-01", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	evaluator := &evaluationFixture{evidence: []workboard.EvidenceInput{{CriterionID: "tests", Source: "deterministic", Outcome: "passed", ActorID: "go-test", ActorType: "validator", Reference: "test-report"}}}
	clock = clock.Add(time.Second)
	workerService := newTestEvaluationService(t, store, worker, evaluator, &clock)
	if _, err = workerService.SubmitCandidate(context.Background(), workboard.SubmitCandidateRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		IdempotencyKey: "proposal-candidate-01", ExpectedCardRevision: card.Revision + 1, ExpectedClaimRevision: 1, CriteriaRevision: card.CriteriaRevision, Summary: "tested candidate", ArtifactRefs: []string{}}); err != nil {
		t.Fatal(err)
	}
	candidate, evidence := evaluationRows(t, store, boardID, card.ID, attemptID)
	current, err := store.GetCard(context.Background(), boardID, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	var boardRevision, attemptRevision int64
	if err = store.db.QueryRow(`SELECT revision FROM workboard_boards WHERE id=?`, boardID).Scan(&boardRevision); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT revision FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptRevision); err != nil {
		t.Fatal(err)
	}
	origin := workboard.AgentProposalOrigin{TaskID: "proposal-task", SessionID: "proposal-session", TurnID: "proposal-turn", RuntimeAttemptID: "proposal-attempt", ToolCallID: "proposal-call", ToolName: workboard.CandidateDecisionRequestTool, ApprovalID: "proposal-approval"}
	p := workboard.ApprovedCandidateDecisionProposal{Origin: origin, IdempotencyKey: "approved-decision-01", BoardID: boardID, CardID: card.ID, AttemptID: attemptID, CandidateID: candidate.ID,
		ExpectedBoardRevision: boardRevision, ExpectedCardRevision: current.Revision, ExpectedAttemptRevision: attemptRevision, CriteriaRevision: card.CriteriaRevision,
		EvidenceHeadRevision: int64(len(evidence)), CandidateDigest: candidate.Digest, CriteriaDigest: candidate.CriteriaDigest, EvidenceSetDigest: workboard.EvidenceSetDigest(evidence),
		PolicyDigest: candidate.PolicyDigest, Decision: workboard.ProposalAccept, Rationale: "deterministic test evidence passed"}
	raw, _ := json.Marshal(decisionProposalArgs(p))
	proposalTime := current.UpdatedAt.Add(time.Second)
	ctx := persistConsumedProposal(t, store, origin, boardID, raw, proposalTime)
	receipt, err := store.ApplyApprovedCandidateDecisionProposal(ctx, p, func() time.Time { return proposalTime.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.GetCard(context.Background(), boardID, card.ID)
	if err != nil || accepted.State != workboard.Done {
		t.Fatalf("card=%+v err=%v", accepted, err)
	}
	var actorType, authority string
	if err = store.db.QueryRow(`SELECT decided_by_type,decision_authority_id FROM workboard_acceptances WHERE attempt_id=?`, attemptID).Scan(&actorType, &authority); err != nil {
		t.Fatal(err)
	}
	if actorType != "operator" || authority != origin.ApprovalID {
		t.Fatalf("actor=%s authority=%s", actorType, authority)
	}
	reconciled, found, err := store.ReconcileApprovedCandidateDecisionProposal(context.Background(), p)
	if err != nil || !found || !reflect.DeepEqual(reconciled, receipt) {
		t.Fatalf("reconcile=%+v found=%v err=%v", reconciled, found, err)
	}
}
