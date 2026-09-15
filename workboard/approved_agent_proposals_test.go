package workboard

import (
	"context"
	"testing"
	"time"
)

type proposalRepositoryFixture struct {
	criteriaApplied, criteriaReconciled, decisionApplied, decisionReconciled int
}

func (r *proposalRepositoryFixture) ApplyApprovedCriteriaProposal(context.Context, ApprovedCriteriaProposal, func() time.Time) (OperationReceipt, error) {
	r.criteriaApplied++
	return OperationReceipt{}, nil
}
func (r *proposalRepositoryFixture) ReconcileApprovedCriteriaProposal(context.Context, ApprovedCriteriaProposal) (OperationReceipt, bool, error) {
	r.criteriaReconciled++
	return OperationReceipt{}, false, nil
}
func (r *proposalRepositoryFixture) ApplyApprovedCandidateDecisionProposal(context.Context, ApprovedCandidateDecisionProposal, func() time.Time) (OperationReceipt, error) {
	r.decisionApplied++
	return OperationReceipt{}, nil
}
func (r *proposalRepositoryFixture) ReconcileApprovedCandidateDecisionProposal(context.Context, ApprovedCandidateDecisionProposal) (OperationReceipt, bool, error) {
	r.decisionReconciled++
	return OperationReceipt{}, false, nil
}

func proposalOrigin(tool string) AgentProposalOrigin {
	return AgentProposalOrigin{TaskID: "task", SessionID: "session", TurnID: "turn", RuntimeAttemptID: "attempt", ToolCallID: "call", ToolName: tool, ApprovalID: "approval"}
}

func TestApprovedAgentProposalServiceKeepsReconciliationReadOnly(t *testing.T) {
	repo := &proposalRepositoryFixture{}
	service, err := NewApprovedAgentProposalService(repo, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	criteria := []AcceptanceCriterion{{Version: 1, ID: "criterion", Description: "new objective criterion", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "test", Required: true}}
	criteriaProposal := ApprovedCriteriaProposal{Origin: proposalOrigin(CriteriaProposalTool), IdempotencyKey: "criteria-operation", BoardID: "board", CardID: "card",
		ExpectedBoardRevision: 1, ExpectedCardRevision: 1, ExpectedCriteriaRevision: 1, ExpectedCriteriaDigest: AcceptanceCriteriaDigest([]AcceptanceCriterion{{Version: 1, ID: "old", Description: "old objective criterion", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "test", Required: true}}), Criteria: criteria}
	if _, _, err = service.ReconcileCriteria(context.Background(), criteriaProposal); err != nil {
		t.Fatal(err)
	}
	if repo.criteriaApplied != 0 || repo.criteriaReconciled != 1 {
		t.Fatalf("reconcile mutated: %+v", repo)
	}

	decisionProposal := ApprovedCandidateDecisionProposal{Origin: proposalOrigin(CandidateDecisionRequestTool), IdempotencyKey: "decision-operation", BoardID: "board", CardID: "card", AttemptID: "attempt", CandidateID: "candidate",
		ExpectedBoardRevision: 1, ExpectedCardRevision: 1, ExpectedAttemptRevision: 2, CriteriaRevision: 1, EvidenceHeadRevision: 1,
		CandidateDigest: digestText("candidate"), CriteriaDigest: digestText("criteria"), EvidenceSetDigest: digestText("evidence"), PolicyDigest: digestText("policy"), Decision: ProposalAccept, Rationale: "operator reviewed deterministic evidence"}
	if _, _, err = service.ReconcileCandidateDecision(context.Background(), decisionProposal); err != nil {
		t.Fatal(err)
	}
	if repo.decisionApplied != 0 || repo.decisionReconciled != 1 {
		t.Fatalf("reconcile mutated: %+v", repo)
	}
}

func TestApprovedAgentProposalRejectsMissingOriginAndSelfNoop(t *testing.T) {
	p := ApprovedCriteriaProposal{IdempotencyKey: "criteria-operation", BoardID: "board", CardID: "card", ExpectedBoardRevision: 1, ExpectedCardRevision: 1, ExpectedCriteriaRevision: 1,
		ExpectedCriteriaDigest: digestText("criteria"), Criteria: []AcceptanceCriterion{{Version: 1, ID: "criterion", Description: "objective criterion", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "test", Required: true}}}
	if p.Validate() == nil {
		t.Fatal("proposal without host origin accepted")
	}
	p.Origin = proposalOrigin(CriteriaProposalTool)
	p.ExpectedCriteriaDigest = AcceptanceCriteriaDigest(p.Criteria)
	if p.Validate() == nil {
		t.Fatal("no-op criteria proposal accepted")
	}
}
