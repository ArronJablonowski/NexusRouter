package app

import (
	"errors"

	contract "github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func projectAttemptHistory(source workboard.AttemptHistoryPage) (contract.AttemptHistoryPage, error) {
	result := contract.AttemptHistoryPage{Version: contract.ContractVersion, BoardID: source.BoardID, CardID: source.CardID,
		HighWaterOrdinal: source.HighWaterOrdinal, Items: make([]contract.AttemptHistoryRecord, len(source.Items)),
		NextCursor: source.NextCursor, HasMore: source.HasMore}
	for index, item := range source.Items {
		result.Items[index] = contract.AttemptHistoryRecord{Version: contract.ContractVersion, ID: item.ID, BoardID: item.BoardID,
			CardID: item.CardID, Ordinal: item.Ordinal, Revision: item.Revision, State: item.State, WorkerID: item.WorkerID,
			CriteriaRevision: item.CriteriaRevision, CheckpointCount: item.CheckpointCount, CandidateID: item.CandidateID,
			AcceptanceID: item.AcceptanceID, StartedAt: item.StartedAt, EndedAt: item.EndedAt}
	}
	if source.Validate() != nil || result.Validate() != nil {
		return contract.AttemptHistoryPage{}, errors.New("invalid attempt history projection")
	}
	return result, nil
}

func projectAttemptDetail(source workboard.AttemptDetailPage) (contract.AttemptDetailPage, error) {
	result := contract.AttemptDetailPage{Version: contract.ContractVersion, Attempt: workboardAttempt(source.Attempt),
		CheckpointHighWaterRevision: source.CheckpointHighWaterRevision, Checkpoints: make([]contract.WorkCheckpoint, len(source.Checkpoints)),
		NextCursor: source.NextCursor, HasMore: source.HasMore}
	for index, checkpoint := range source.Checkpoints {
		result.Checkpoints[index] = workboardCheckpoint(checkpoint)
	}
	if source.Validate() != nil || result.Validate() != nil {
		return contract.AttemptDetailPage{}, errors.New("invalid attempt detail projection")
	}
	return result, nil
}

func workboardCheckpoint(checkpoint workboard.CheckpointRecord) contract.WorkCheckpoint {
	return contract.WorkCheckpoint{Version: contract.ContractVersion, ID: checkpoint.ID, BoardID: checkpoint.BoardID,
		CardID: checkpoint.CardID, AttemptID: checkpoint.AttemptID, ClaimID: checkpoint.ClaimID, Revision: checkpoint.Revision,
		ClaimRevision: checkpoint.ClaimRevision, CriteriaRevision: checkpoint.CriteriaRevision, CriteriaDigest: checkpoint.CriteriaDigest,
		PolicyDigest: checkpoint.PolicyDigest, Evidence: checkpoint.Evidence, EvidenceDigest: checkpoint.EvidenceDigest,
		ActorID: checkpoint.ActorID, ActorType: checkpoint.ActorType, CreatedAt: checkpoint.CreatedAt}
}
