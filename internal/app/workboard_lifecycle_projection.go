package app

import (
	"errors"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func workboardLifecycle(source workboard.CardLifecycleSnapshot) (contract.CardLifecycle, error) {
	if source.Attempt == nil {
		return contract.CardLifecycle{}, errors.New("invalid empty lifecycle snapshot")
	}
	attempt := workboardAttempt(*source.Attempt)
	result := contract.CardLifecycle{Version: contract.ContractVersion, CardID: source.CardID, Attempt: attempt,
		Checkpoints: make([]contract.WorkCheckpoint, len(source.Checkpoints)), CheckpointCount: source.CheckpointCount,
		CheckpointsHasMore: source.CheckpointsHasMore}
	for index, checkpoint := range source.Checkpoints {
		result.Checkpoints[index] = contract.WorkCheckpoint{Version: contract.ContractVersion, ID: checkpoint.ID, BoardID: checkpoint.BoardID,
			CardID: checkpoint.CardID, AttemptID: checkpoint.AttemptID, ClaimID: checkpoint.ClaimID, Revision: checkpoint.Revision,
			ClaimRevision: checkpoint.ClaimRevision, CriteriaRevision: checkpoint.CriteriaRevision, CriteriaDigest: checkpoint.CriteriaDigest,
			PolicyDigest: checkpoint.PolicyDigest, Evidence: checkpoint.Evidence, EvidenceDigest: checkpoint.EvidenceDigest,
			ActorID: checkpoint.ActorID, ActorType: checkpoint.ActorType, CreatedAt: checkpoint.CreatedAt}
	}
	if source.Attempt.Acceptance != nil {
		acceptance := *source.Attempt.Acceptance
		result.Acceptance = &contract.AcceptanceDecisionRecord{Version: contract.ContractVersion, ID: acceptance.ID, BoardID: acceptance.BoardID,
			CardID: acceptance.CardID, AttemptID: acceptance.AttemptID, CandidateID: acceptance.CandidateID,
			CandidateDigest: acceptance.CandidateDigest, CriteriaRevision: acceptance.CriteriaRevision, CriteriaDigest: acceptance.CriteriaDigest,
			PriorEvidenceHeadRevision: acceptance.PriorEvidenceHeadRevision, PriorEvidenceSetDigest: acceptance.PriorEvidenceSetDigest,
			EvidenceHeadRevision: acceptance.EvidenceHeadRevision, EvidenceSetDigest: acceptance.EvidenceSetDigest,
			PolicyDigest: acceptance.PolicyDigest, Decision: acceptance.Decision, DecidedBy: acceptance.DecidedBy,
			DecidedByType: acceptance.DecidedByType, DecisionAuthorityID: acceptance.DecisionAuthorityID,
			Rationale: acceptance.Rationale, DecidedAt: acceptance.DecidedAt}
	}
	if result.Validate() != nil {
		return contract.CardLifecycle{}, errors.New("invalid lifecycle projection")
	}
	return result, nil
}

func workboardAttempt(source workboard.AttemptSnapshot) contract.Attempt {
	criteria := make([]contract.AcceptanceCriterion, len(source.Criteria))
	for index, criterion := range source.Criteria {
		criteria[index] = contract.AcceptanceCriterion{Version: contract.ContractVersion, ID: criterion.ID, Kind: criterion.Kind,
			RequiredSource: criterion.RequiredSource, ValidatorID: criterion.ValidatorID, Description: criterion.Description, Required: criterion.Required}
	}
	evidence := make([]contract.EvidenceRecord, len(source.Evidence))
	for index, item := range source.Evidence {
		evidence[index] = contract.EvidenceRecord{Version: contract.ContractVersion, ID: item.ID, Revision: item.Revision,
			BoardID: item.BoardID, CardID: item.CardID, AttemptID: item.AttemptID, CandidateID: item.CandidateID,
			CriterionID: item.CriterionID, Source: item.Source, Outcome: item.Outcome, ActorID: item.ActorID, ActorType: item.ActorType,
			Reference: item.Reference, CandidateDigest: item.CandidateDigest, CriteriaDigest: item.CriteriaDigest,
			PolicyDigest: item.PolicyDigest, CreatedAt: item.CreatedAt}
	}
	result := contract.Attempt{Version: contract.ContractVersion, ID: source.ID, BoardID: source.BoardID, CardID: source.CardID,
		Ordinal: source.Ordinal, Revision: source.Revision, State: source.State, WorkerID: source.WorkerID,
		CriteriaRevision: source.CriteriaRevision, CriteriaDigest: source.CriteriaDigest, PolicyDigest: source.PolicyDigest,
		Budget: contract.WorkBudget{AttemptLimit: source.Budget.AttemptLimit, TimeLimitMS: source.Budget.TimeLimitMS,
			TokenLimit: source.Budget.TokenLimit, CostMicros: source.Budget.CostMicros}, Criteria: criteria,
		TaskIDs: append([]string{}, source.TaskIDs...), SessionIDs: append([]string{}, source.SessionIDs...), Evidence: evidence,
		StartedAt: source.StartedAt, EndedAt: source.EndedAt}
	if source.Claim != nil {
		claim := source.Claim
		result.Claim = &contract.Claim{Version: contract.ContractVersion, ID: claim.ID, BoardID: claim.BoardID, CardID: claim.CardID,
			AttemptID: claim.AttemptID, Revision: claim.Revision, State: claim.State, OwnerID: claim.OwnerID,
			OwnerType: claim.OwnerType, TaskID: claim.TaskID, ExpiresAt: claim.ExpiresAt, LastHeartbeat: claim.LastHeartbeat, ReleasedAt: claim.ReleasedAt}
	}
	if source.Candidate != nil {
		candidate := source.Candidate
		result.Candidate = &contract.Candidate{Version: contract.ContractVersion, ID: candidate.ID, BoardID: candidate.BoardID,
			CardID: candidate.CardID, AttemptID: candidate.AttemptID, Revision: candidate.Revision, Digest: candidate.Digest,
			CriteriaDigest: candidate.CriteriaDigest, PolicyDigest: candidate.PolicyDigest, EvidenceDigest: candidate.EvidenceDigest,
			EvidenceCount: candidate.EvidenceCount, Summary: candidate.Summary, ArtifactRefs: append([]string{}, candidate.ArtifactRefs...),
			SubmittedBy: candidate.SubmittedBy, CreatedAt: candidate.CreatedAt}
	}
	if source.Acceptance != nil {
		acceptance := source.Acceptance
		result.AcceptanceID, result.DecisionBy, result.DecisionByType = acceptance.ID, acceptance.DecidedBy, acceptance.DecidedByType
		result.DecisionAuthorityID, result.AcceptanceEvidenceDigest = acceptance.DecisionAuthorityID, acceptance.EvidenceSetDigest
	}
	return result
}
