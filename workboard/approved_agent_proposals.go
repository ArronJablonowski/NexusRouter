package workboard

import (
	"context"
	"time"
)

const (
	CriteriaProposalTool         = "workboard_propose_criteria"
	CandidateDecisionRequestTool = "workboard_request_candidate_decision"
)

// AgentProposalOrigin is host-derived runtime and approval provenance. None of
// these fields may be accepted from model-authored tool arguments.
type AgentProposalOrigin struct {
	TaskID, SessionID, TurnID, RuntimeAttemptID string
	ToolCallID, ToolName, ApprovalID            string
}

func (o AgentProposalOrigin) Validate(tool string) error {
	if !validLifecycleIDs(o.TaskID, o.SessionID, o.TurnID, o.RuntimeAttemptID, o.ToolCallID, o.ApprovalID) || o.ToolName != tool {
		return fail(CodeInvalid, "proposal_origin")
	}
	return nil
}

// ApprovedCriteriaProposal is the exact model-authored proposal whose
// authenticated, consumed approval must be revalidated by durable storage.
// Origin is deliberately excluded from the JSON tool argument shape.
type ApprovedCriteriaProposal struct {
	Origin                   AgentProposalOrigin   `json:"-"`
	IdempotencyKey           string                `json:"idempotency_key"`
	BoardID                  string                `json:"board_id"`
	CardID                   string                `json:"card_id"`
	ExpectedBoardRevision    int64                 `json:"expected_board_revision"`
	ExpectedCardRevision     int64                 `json:"expected_card_revision"`
	ExpectedCriteriaRevision int64                 `json:"expected_criteria_revision"`
	ExpectedCriteriaDigest   string                `json:"expected_criteria_digest"`
	Criteria                 []AcceptanceCriterion `json:"criteria"`
}

func (p ApprovedCriteriaProposal) Validate() error {
	if p.Origin.Validate(CriteriaProposalTool) != nil || !validKey(p.IdempotencyKey) || !validLifecycleIDs(p.BoardID, p.CardID) ||
		p.ExpectedBoardRevision < 1 || p.ExpectedCardRevision < 1 || p.ExpectedCriteriaRevision < 1 ||
		!digest(p.ExpectedCriteriaDigest) || ValidateCriteriaSet(p.Criteria, p.ExpectedCriteriaRevision+1) != nil ||
		AcceptanceCriteriaDigest(p.Criteria) == p.ExpectedCriteriaDigest {
		return fail(CodeInvalid, "criteria_proposal")
	}
	return nil
}

type CandidateProposalDecision string

const (
	ProposalAccept CandidateProposalDecision = "accepted"
	ProposalReject CandidateProposalDecision = "rejected"
)

// ApprovedCandidateDecisionProposal freezes a model's requested verdict. The
// request itself is advisory; only the operator named by its consumed approval
// can become the durable acceptance actor.
type ApprovedCandidateDecisionProposal struct {
	Origin                  AgentProposalOrigin       `json:"-"`
	IdempotencyKey          string                    `json:"idempotency_key"`
	BoardID                 string                    `json:"board_id"`
	CardID                  string                    `json:"card_id"`
	AttemptID               string                    `json:"attempt_id"`
	CandidateID             string                    `json:"candidate_id"`
	ExpectedBoardRevision   int64                     `json:"expected_board_revision"`
	ExpectedCardRevision    int64                     `json:"expected_card_revision"`
	ExpectedAttemptRevision int64                     `json:"expected_attempt_revision"`
	CriteriaRevision        int64                     `json:"criteria_revision"`
	EvidenceHeadRevision    int64                     `json:"evidence_head_revision"`
	CandidateDigest         string                    `json:"candidate_digest"`
	CriteriaDigest          string                    `json:"criteria_digest"`
	EvidenceSetDigest       string                    `json:"evidence_set_digest"`
	PolicyDigest            string                    `json:"policy_digest"`
	Decision                CandidateProposalDecision `json:"decision"`
	Rationale               string                    `json:"rationale"`
}

func (p ApprovedCandidateDecisionProposal) Validate() error {
	if p.Origin.Validate(CandidateDecisionRequestTool) != nil || !validKey(p.IdempotencyKey) ||
		!validLifecycleIDs(p.BoardID, p.CardID, p.AttemptID, p.CandidateID) || p.ExpectedBoardRevision < 1 ||
		p.ExpectedCardRevision < 1 || p.ExpectedAttemptRevision < 1 || p.CriteriaRevision < 1 || p.EvidenceHeadRevision < 0 ||
		!digest(p.CandidateDigest) || !digest(p.CriteriaDigest) || !digest(p.EvidenceSetDigest) || !digest(p.PolicyDigest) ||
		(p.Decision != ProposalAccept && p.Decision != ProposalReject) || !boundedText(p.Rationale, MaxCheckpointBytes, false) {
		return fail(CodeInvalid, "candidate_decision_proposal")
	}
	return nil
}

type ApprovedAgentProposalRepository interface {
	ApplyApprovedCriteriaProposal(context.Context, ApprovedCriteriaProposal, func() time.Time) (OperationReceipt, error)
	ReconcileApprovedCriteriaProposal(context.Context, ApprovedCriteriaProposal) (OperationReceipt, bool, error)
	ApplyApprovedCandidateDecisionProposal(context.Context, ApprovedCandidateDecisionProposal, func() time.Time) (OperationReceipt, error)
	ReconcileApprovedCandidateDecisionProposal(context.Context, ApprovedCandidateDecisionProposal) (OperationReceipt, bool, error)
}

// ApprovedAgentProposalService separates applying a freshly consumed approval
// from read-only acknowledgement reconciliation. Reconciliation never invokes
// a mutation method or revives spent authority.
type ApprovedAgentProposalService struct {
	repository ApprovedAgentProposalRepository
	now        func() time.Time
}

func NewApprovedAgentProposalService(repository ApprovedAgentProposalRepository, now func() time.Time) (*ApprovedAgentProposalService, error) {
	if repository == nil || now == nil {
		return nil, fail(CodeInvalid, "approved_agent_proposal_service")
	}
	return &ApprovedAgentProposalService{repository: repository, now: now}, nil
}

func (s *ApprovedAgentProposalService) ApplyCriteria(ctx context.Context, proposal ApprovedCriteriaProposal) (OperationReceipt, error) {
	if ctx == nil || proposal.Validate() != nil {
		return OperationReceipt{}, fail(CodeInvalid, "criteria_proposal")
	}
	return s.repository.ApplyApprovedCriteriaProposal(ctx, proposal, s.now)
}

func (s *ApprovedAgentProposalService) ReconcileCriteria(ctx context.Context, proposal ApprovedCriteriaProposal) (OperationReceipt, bool, error) {
	if ctx == nil || proposal.Validate() != nil {
		return OperationReceipt{}, false, fail(CodeInvalid, "criteria_proposal")
	}
	return s.repository.ReconcileApprovedCriteriaProposal(ctx, proposal)
}

func (s *ApprovedAgentProposalService) ApplyCandidateDecision(ctx context.Context, proposal ApprovedCandidateDecisionProposal) (OperationReceipt, error) {
	if ctx == nil || proposal.Validate() != nil {
		return OperationReceipt{}, fail(CodeInvalid, "candidate_decision_proposal")
	}
	return s.repository.ApplyApprovedCandidateDecisionProposal(ctx, proposal, s.now)
}

func (s *ApprovedAgentProposalService) ReconcileCandidateDecision(ctx context.Context, proposal ApprovedCandidateDecisionProposal) (OperationReceipt, bool, error) {
	if ctx == nil || proposal.Validate() != nil {
		return OperationReceipt{}, false, fail(CodeInvalid, "candidate_decision_proposal")
	}
	return s.repository.ReconcileApprovedCandidateDecisionProposal(ctx, proposal)
}
