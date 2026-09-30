package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func workboardAgentProposalSpecs() []providers.Tool {
	return []providers.Tool{
		{Name: workboard.CriteriaProposalTool, Description: "Propose an exact acceptance-criteria revision for operator approval. A committed result means the authenticated operator applied this exact proposal; the model never receives operator authority.", Parameters: json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"expected_board_revision":{"type":"integer","minimum":1},"expected_card_revision":{"type":"integer","minimum":1},"expected_criteria_revision":{"type":"integer","minimum":1},"expected_criteria_digest":{"type":"string","pattern":"^[a-f0-9]{64}$"},"criteria":{"type":"array","minItems":1,"maxItems":32,"items":` + workboardCriterionSchema + `}},"required":["idempotency_key","board_id","card_id","expected_board_revision","expected_card_revision","expected_criteria_revision","expected_criteria_digest","criteria"],"additionalProperties":false}`)},
		{Name: workboard.CandidateDecisionRequestTool, Description: "Request an accept or reject decision for one exact candidate and evidence head after operator approval. The authenticated operator, never the model or worker, becomes the durable decision actor.", Parameters: json.RawMessage(`{"type":"object","properties":{"idempotency_key":` + workboardKeySchema + `,"board_id":` + workboardIDSchema + `,"card_id":` + workboardIDSchema + `,"attempt_id":` + workboardIDSchema + `,"candidate_id":` + workboardIDSchema + `,"expected_board_revision":{"type":"integer","minimum":1},"expected_card_revision":{"type":"integer","minimum":1},"expected_attempt_revision":{"type":"integer","minimum":1},"criteria_revision":{"type":"integer","minimum":1},"evidence_head_revision":{"type":"integer","minimum":0},"candidate_digest":{"type":"string","pattern":"^[a-f0-9]{64}$"},"criteria_digest":{"type":"string","pattern":"^[a-f0-9]{64}$"},"evidence_set_digest":{"type":"string","pattern":"^[a-f0-9]{64}$"},"policy_digest":{"type":"string","pattern":"^[a-f0-9]{64}$"},"decision":{"enum":["accepted","rejected"]},"rationale":{"type":"string","minLength":1,"maxLength":65536}},"required":["idempotency_key","board_id","card_id","attempt_id","candidate_id","expected_board_revision","expected_card_revision","expected_attempt_revision","criteria_revision","evidence_head_revision","candidate_digest","criteria_digest","evidence_set_digest","policy_digest","decision","rationale"],"additionalProperties":false}`)},
	}
}

func registerWorkboardAgentProposalTools(registry *tools.Registry, repository workboard.ApprovedAgentProposalRepository) error {
	if registry == nil || repository == nil {
		return ErrAdmission
	}
	service, err := workboard.NewApprovedAgentProposalService(repository, defaultWorkboardNow)
	if err != nil {
		return ErrAdmission
	}
	boardScope, err := tools.IdentifierScope("workboard", "board_id")
	if err != nil {
		return ErrAdmission
	}
	definitions := []tools.Definition{
		{Tool: workboardAgentProposalSpecs()[0], Scope: "workboard", ResolveScope: boardScope, Behavior: runtime.BehaviorIdempotentWrite,
			Handler: approvedCriteriaProposalHandler(service)},
		{Tool: workboardAgentProposalSpecs()[1], Scope: "workboard", ResolveScope: boardScope, Behavior: runtime.BehaviorIdempotentWrite,
			Handler: approvedCandidateDecisionProposalHandler(service)},
	}
	for _, definition := range definitions {
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

func agentProposalOrigin(ctx context.Context, toolName string) (workboard.AgentProposalOrigin, error) {
	identity, identityOK := tools.ExecutionIdentityFromContext(ctx)
	approval, approvalOK := tools.ConsumedApprovalFromContext(ctx)
	if !identityOK || !approvalOK || identity.ToolName != toolName {
		return workboard.AgentProposalOrigin{}, ErrAdmission
	}
	origin := workboard.AgentProposalOrigin{TaskID: identity.TaskID, SessionID: identity.SessionID, TurnID: identity.TurnID,
		RuntimeAttemptID: identity.AttemptID, ToolCallID: identity.ToolCallID, ToolName: identity.ToolName, ApprovalID: approval.ID}
	if origin.Validate(toolName) != nil {
		return workboard.AgentProposalOrigin{}, ErrAdmission
	}
	return origin, nil
}

func approvedCriteriaProposalHandler(service *workboard.ApprovedAgentProposalService) func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
	return func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
		var proposal workboard.ApprovedCriteriaProposal
		if json.Unmarshal(raw, &proposal) != nil || ctx.Err() != nil {
			return invalidAgentProposalResult(), nil
		}
		origin, err := agentProposalOrigin(ctx, workboard.CriteriaProposalTool)
		if err != nil {
			return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard proposal origin unavailable")
		}
		proposal.Origin = origin
		if proposal.Validate() != nil {
			return invalidAgentProposalResult(), nil
		}
		receipt, err := service.ApplyCriteria(ctx, proposal)
		if err != nil {
			if reconciled, found, reconcileErr := service.ReconcileCriteria(ctx, proposal); reconcileErr == nil && found {
				return confirmedAgentProposalResult(reconciled)
			} else if reconcileErr != nil {
				return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard proposal acknowledgement uncertain")
			}
			return failedAgentProposalResult(err)
		}
		return confirmedAgentProposalResult(receipt)
	}
}

func approvedCandidateDecisionProposalHandler(service *workboard.ApprovedAgentProposalService) func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
	return func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
		var proposal workboard.ApprovedCandidateDecisionProposal
		if json.Unmarshal(raw, &proposal) != nil || ctx.Err() != nil {
			return invalidAgentProposalResult(), nil
		}
		origin, err := agentProposalOrigin(ctx, workboard.CandidateDecisionRequestTool)
		if err != nil {
			return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard proposal origin unavailable")
		}
		proposal.Origin = origin
		if proposal.Validate() != nil {
			return invalidAgentProposalResult(), nil
		}
		receipt, err := service.ApplyCandidateDecision(ctx, proposal)
		if err != nil {
			if reconciled, found, reconcileErr := service.ReconcileCandidateDecision(ctx, proposal); reconcileErr == nil && found {
				return confirmedAgentProposalResult(reconciled)
			} else if reconcileErr != nil {
				return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard proposal acknowledgement uncertain")
			}
			return failedAgentProposalResult(err)
		}
		return confirmedAgentProposalResult(receipt)
	}
}

func invalidAgentProposalResult() runtime.ToolResult {
	return runtime.ToolResult{Content: `{"error":"workboard_proposal_invalid"}`, Effect: runtime.NoEffect, Failed: true, Recoverable: true}
}

func failedAgentProposalResult(err error) (runtime.ToolResult, error) {
	if violation := definitiveWorkboardNoEffect(err); violation != nil {
		body, marshalErr := json.Marshal(struct {
			Error string              `json:"error"`
			Code  workboard.ErrorCode `json:"code"`
		}{Error: "workboard_proposal_rejected", Code: violation.Code})
		if marshalErr == nil {
			return runtime.ToolResult{Content: string(body), Effect: runtime.NoEffect, Failed: true, Recoverable: true}, nil
		}
	}
	return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard proposal acknowledgement uncertain")
}

func confirmedAgentProposalResult(receipt workboard.OperationReceipt) (runtime.ToolResult, error) {
	if receipt.Validate() != nil {
		return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard proposal receipt unavailable")
	}
	body, err := json.Marshal(receipt)
	if err != nil || len(body) > maxWorkboardToolResultBytes {
		return runtime.ToolResult{Effect: runtime.UncertainEffect, Failed: true}, errors.New("workboard proposal receipt unavailable")
	}
	return runtime.ToolResult{Content: string(body), Effect: runtime.ConfirmedEffect}, nil
}
