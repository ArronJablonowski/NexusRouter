package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type approvedProposalSource struct {
	record              approvals.Record
	argumentsDigest     string
	modelID, providerID string
}

func approvedOperator(actor string) workboard.Actor {
	sum := sha256.Sum256([]byte("darwin.workboard.approved.operator.v1\x00" + actor))
	return workboard.Actor{Type: "operator", ID: "approved-" + hex.EncodeToString(sum[:20])}
}

func (s *Store) approvedProposalSource(ctx context.Context, tx *sql.Tx, origin workboard.AgentProposalOrigin, boardID string, exact any, fresh bool) (approvedProposalSource, error) {
	if fresh {
		consumed, ok := tools.ConsumedApprovalFromContext(ctx)
		if !ok || consumed.ID != origin.ApprovalID {
			return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "consumed_approval"}
		}
	}
	record, err := readApproval(ctx, tx, origin.ApprovalID)
	if err != nil || record.State != approvals.Consumed || len(record.Decisions) != 1 || !record.Decisions[0].Allowed || record.ConsumedAt == nil {
		return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "approval"}
	}
	r := record.Request
	if r.TaskID != origin.TaskID || r.TurnID != origin.TurnID || r.ToolCallID != origin.ToolCallID || r.ToolName != origin.ToolName ||
		r.ToolBehavior != runtime.BehaviorIdempotentWrite || r.Scope != "workboard:"+boardID {
		return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "approval_binding"}
	}
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, origin.TaskID, &events)
	if err != nil || snapshot.SessionID != origin.SessionID {
		return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "runtime_provenance"}
	}
	if fresh {
		pending, ok := snapshot.Pending[origin.ToolCallID]
		if snapshot.State != "running" || !ok || !pending.Dispatched || pending.TurnID != origin.TurnID || pending.AttemptID != origin.RuntimeAttemptID ||
			len(events) == 0 || events[len(events)-1].Kind != runtime.ToolStarted || events[len(events)-1].Data.ToolCallID != origin.ToolCallID {
			return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "proposal_runtime_head"}
		}
	}
	started, completed, toolStarted := -1, -1, -1
	for i := range events {
		e := events[i]
		if e.TurnID != origin.TurnID || e.AttemptID != origin.RuntimeAttemptID {
			continue
		}
		switch e.Kind {
		case runtime.TurnStarted:
			if started >= 0 {
				return approvedProposalSource{}, ErrWorkboardCorrupt
			}
			started = i
		case runtime.TurnCompleted:
			if completed >= 0 {
				return approvedProposalSource{}, ErrWorkboardCorrupt
			}
			completed = i
		case runtime.ToolStarted:
			if e.Data.ToolCallID == origin.ToolCallID {
				if toolStarted >= 0 || e.Data.ToolName != origin.ToolName || e.Data.ToolBehavior != runtime.BehaviorIdempotentWrite {
					return approvedProposalSource{}, ErrWorkboardCorrupt
				}
				toolStarted = i
			}
		}
	}
	if started < 0 || completed <= started || toolStarted <= completed || events[started].Data.ModelID == "" || events[started].Data.ProviderID == "" {
		return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "runtime_provenance"}
	}
	var raw []byte
	for _, call := range events[completed].Data.ToolCalls {
		if call.ID == origin.ToolCallID {
			if raw != nil || call.Name != origin.ToolName {
				return approvedProposalSource{}, ErrWorkboardCorrupt
			}
			raw = call.Arguments
		}
	}
	if raw == nil || digestBytes(raw) != r.ArgumentsDigest {
		return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "proposal_digest"}
	}
	decoded := reflect.New(reflect.TypeOf(exact))
	if strictJSON(raw, decoded.Interface()) != nil || !reflect.DeepEqual(decoded.Elem().Interface(), exact) {
		return approvedProposalSource{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "proposal_arguments"}
	}
	return approvedProposalSource{record: record, argumentsDigest: r.ArgumentsDigest, modelID: events[started].Data.ModelID, providerID: events[started].Data.ProviderID}, nil
}

func criteriaProposalArgs(p workboard.ApprovedCriteriaProposal) workboard.ApprovedCriteriaProposal {
	p.Origin = workboard.AgentProposalOrigin{}
	return p
}
func decisionProposalArgs(p workboard.ApprovedCandidateDecisionProposal) workboard.ApprovedCandidateDecisionProposal {
	p.Origin = workboard.AgentProposalOrigin{}
	return p
}

func criteriaProposalMutation(p workboard.ApprovedCriteriaProposal, source approvedProposalSource, now time.Time) (workboard.ProgressMutation, error) {
	m := workboard.ProgressMutation{Version: workboard.ProgressMutationVersion, Kind: workboard.ProgressCriteriaRevise, BoardID: p.BoardID, CardID: p.CardID,
		IdempotencyKey: p.IdempotencyKey, Actor: approvedOperator(source.record.Decisions[0].Actor), ExpectedCardRevision: p.ExpectedCardRevision,
		ExpectedCriteriaRevision: p.ExpectedCriteriaRevision, Criteria: append([]workboard.AcceptanceCriterion(nil), p.Criteria...), AgentApprovalID: p.Origin.ApprovalID,
		AgentProposalDigest: source.argumentsDigest, ExpectedBoardRevision: p.ExpectedBoardRevision, ExpectedCriteriaDigest: p.ExpectedCriteriaDigest, Now: now.UTC()}
	var err error
	m.RequestDigest, err = workboard.ProgressDigest(m)
	return m, err
}

func decisionProposalMutation(p workboard.ApprovedCandidateDecisionProposal, source approvedProposalSource, now time.Time) (workboard.EvaluationMutation, error) {
	kind := workboard.EvaluationReject
	if p.Decision == workboard.ProposalAccept {
		kind = workboard.EvaluationAccept
	}
	m := workboard.EvaluationMutation{Version: 1, Kind: kind, BoardID: p.BoardID, CardID: p.CardID, AttemptID: p.AttemptID, CandidateID: p.CandidateID,
		IdempotencyKey: p.IdempotencyKey, Actor: approvedOperator(source.record.Decisions[0].Actor), DecisionAuthorityID: p.Origin.ApprovalID,
		ExpectedCardRevision: p.ExpectedCardRevision, CriteriaRevision: p.CriteriaRevision, EvidenceHeadRevision: p.EvidenceHeadRevision,
		CandidateDigest: p.CandidateDigest, CriteriaDigest: p.CriteriaDigest, EvidenceSetDigest: p.EvidenceSetDigest, PolicyDigest: p.PolicyDigest,
		Evidence: p.Rationale, AgentApprovalID: p.Origin.ApprovalID, AgentProposalDigest: source.argumentsDigest,
		ExpectedBoardRevision: p.ExpectedBoardRevision, ExpectedAttemptRevision: p.ExpectedAttemptRevision, Now: now.UTC()}
	var err error
	m.RequestDigest, err = workboard.EvaluationDigest(m)
	return m, err
}

func (s *Store) ApplyApprovedCriteriaProposal(ctx context.Context, p workboard.ApprovedCriteriaProposal, now func() time.Time) (workboard.OperationReceipt, error) {
	if p.Validate() != nil || now == nil {
		return workboard.OperationReceipt{}, invalidWorkboard("criteria_proposal")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	source, err := s.approvedProposalSource(ctx, tx, p.Origin, p.BoardID, criteriaProposalArgs(p), true)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	m, err := criteriaProposalMutation(p, source, now())
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	receipt, err := applyProgressMutationTx(ctx, tx, m)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

func (s *Store) ReconcileApprovedCriteriaProposal(ctx context.Context, p workboard.ApprovedCriteriaProposal) (workboard.OperationReceipt, bool, error) {
	if p.Validate() != nil {
		return workboard.OperationReceipt{}, false, invalidWorkboard("criteria_proposal")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	defer tx.Rollback()
	source, err := s.approvedProposalSource(ctx, tx, p.Origin, p.BoardID, criteriaProposalArgs(p), false)
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	m, err := criteriaProposalMutation(p, source, *source.record.ConsumedAt)
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	receipt, found, err := readWorkboardReceipt(ctx, tx, "board", p.BoardID, digestBytes([]byte(p.IdempotencyKey)), m.RequestDigest)
	if err != nil || !found {
		return workboard.OperationReceipt{}, found, err
	}
	if receipt.CardID != p.CardID || receipt.CardRevision == nil || receipt.ClaimRevision != nil {
		return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	return receipt, true, nil
}

func (s *Store) ApplyApprovedCandidateDecisionProposal(ctx context.Context, p workboard.ApprovedCandidateDecisionProposal, now func() time.Time) (workboard.OperationReceipt, error) {
	if p.Validate() != nil || now == nil {
		return workboard.OperationReceipt{}, invalidWorkboard("candidate_decision_proposal")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	source, err := s.approvedProposalSource(ctx, tx, p.Origin, p.BoardID, decisionProposalArgs(p), true)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	m, err := decisionProposalMutation(p, source, now())
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	receipt, err := applyEvaluationMutationTx(ctx, tx, m, func() time.Time { return m.Now }, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

func (s *Store) ReconcileApprovedCandidateDecisionProposal(ctx context.Context, p workboard.ApprovedCandidateDecisionProposal) (workboard.OperationReceipt, bool, error) {
	if p.Validate() != nil {
		return workboard.OperationReceipt{}, false, invalidWorkboard("candidate_decision_proposal")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	defer tx.Rollback()
	source, err := s.approvedProposalSource(ctx, tx, p.Origin, p.BoardID, decisionProposalArgs(p), false)
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	m, err := decisionProposalMutation(p, source, *source.record.ConsumedAt)
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	receipt, found, err := readEvaluationReplay(ctx, tx, m, digestBytes([]byte(p.IdempotencyKey)), m.RequestDigest)
	if err != nil || !found {
		return workboard.OperationReceipt{}, found, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	return receipt, true, nil
}
