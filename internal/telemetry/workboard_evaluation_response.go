package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type evaluationStoredResult struct {
	Candidate  *workboard.CandidateRecord  `json:"candidate"`
	Evidence   []workboard.EvidenceRecord  `json:"evidence"`
	Acceptance *workboard.AcceptanceRecord `json:"acceptance,omitempty"`
	Successors []workboard.Card            `json:"successors,omitempty"`
}

type evaluationMutationResponse struct {
	Version int                        `json:"version"`
	Receipt workboard.OperationReceipt `json:"receipt"`
	Result  evaluationStoredResult     `json:"result"`
}

func finalizeEvaluationReceipt(receipt *workboard.OperationReceipt, result evaluationStoredResult, durableBytes int) ([]byte, error) {
	if durableBytes < 1 || durableBytes > workboard.MaxTransactionBytes || validateEvaluationStoredResult(result) != nil {
		return nil, ErrWorkboardCorrupt
	}
	for range 8 {
		receipt.ResponseDigest = digestBytes(nil)
		response, err := json.Marshal(evaluationMutationResponse{Version: 1, Receipt: *receipt, Result: result})
		if err != nil {
			return nil, err
		}
		total := durableBytes + len(response)
		if total > workboard.MaxTransactionBytes {
			return nil, &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "transaction_bytes"}
		}
		if receipt.TransactionBytes == total {
			envelope := evaluationMutationResponse{Version: 1, Receipt: *receipt, Result: result}
			receipt.ResponseDigest, err = evaluationResponseDigest(envelope)
			if err != nil {
				return nil, err
			}
			envelope.Receipt = *receipt
			response, err = json.Marshal(envelope)
			if err != nil || durableBytes+len(response) != receipt.TransactionBytes || receipt.Validate() != nil {
				return nil, ErrWorkboardCorrupt
			}
			return response, nil
		}
		receipt.TransactionBytes = total
	}
	return nil, ErrWorkboardCorrupt
}

func evaluationResponseDigest(envelope evaluationMutationResponse) (string, error) {
	envelope.Receipt.ResponseDigest = ""
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func readEvaluationResponse(ctx context.Context, tx *sql.Tx, boardID string, keyDigest, requestDigest string) (evaluationMutationResponse, bool, error) {
	var operationID, storedBoardID, storedRequestDigest, responseDigest, outcome string
	var firstSequence, lastSequence int64
	var eventCount, transactionBytes int
	var createdAt int64
	var response []byte
	err := tx.QueryRowContext(ctx, `SELECT operation_id,board_id,request_digest,response_digest,first_sequence,last_sequence,event_count,transaction_bytes,outcome,response,created_at
		FROM workboard_operations WHERE scope_kind='board' AND scope_id=? AND key_digest=?`, boardID, keyDigest).
		Scan(&operationID, &storedBoardID, &storedRequestDigest, &responseDigest, &firstSequence, &lastSequence, &eventCount, &transactionBytes, &outcome, &response, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return evaluationMutationResponse{}, false, nil
	}
	if err != nil {
		return evaluationMutationResponse{}, false, err
	}
	if storedRequestDigest != requestDigest {
		return evaluationMutationResponse{}, true, ErrConflict
	}
	var envelope evaluationMutationResponse
	if strictJSON(response, &envelope) != nil {
		return evaluationMutationResponse{}, true, ErrWorkboardCorrupt
	}
	indexed := workboard.OperationReceipt{Version: 1, BoardID: storedBoardID, OperationID: operationID, RequestDigest: storedRequestDigest,
		ResponseDigest: responseDigest, FirstSequence: firstSequence, LastSequence: lastSequence, EventCount: eventCount, TransactionBytes: transactionBytes,
		BoardRevision: envelope.Receipt.BoardRevision, CardID: envelope.Receipt.CardID, CardRevision: envelope.Receipt.CardRevision,
		ClaimRevision: envelope.Receipt.ClaimRevision, Outcome: outcome, CreatedAt: time.Unix(0, createdAt).UTC()}
	want, digestErr := evaluationResponseDigest(envelope)
	if digestErr != nil || envelope.Version != 1 || envelope.Receipt != indexed || envelope.Receipt.Validate() != nil ||
		envelope.Receipt.ResponseDigest != want || validateEvaluationStoredResult(envelope.Result) != nil {
		return evaluationMutationResponse{}, true, ErrWorkboardCorrupt
	}
	if err = verifyWorkboardReceiptEvents(ctx, tx, envelope.Receipt); err != nil {
		return evaluationMutationResponse{}, true, err
	}
	return envelope, true, nil
}

func validateEvaluationStoredResult(result evaluationStoredResult) error {
	if result.Candidate == nil || result.Candidate.Validate() != nil || result.Evidence == nil ||
		len(result.Evidence) < result.Candidate.EvidenceCount ||
		workboard.EvidenceSetDigest(result.Evidence[:result.Candidate.EvidenceCount]) != result.Candidate.EvidenceDigest {
		return ErrWorkboardCorrupt
	}
	for index, item := range result.Evidence {
		if item.Validate() != nil || item.Revision != int64(index+1) || item.BoardID != result.Candidate.BoardID ||
			item.CardID != result.Candidate.CardID || item.AttemptID != result.Candidate.AttemptID || item.CandidateID != result.Candidate.ID ||
			item.CandidateDigest != result.Candidate.Digest || item.CriteriaDigest != result.Candidate.CriteriaDigest || item.PolicyDigest != result.Candidate.PolicyDigest {
			return ErrWorkboardCorrupt
		}
	}
	if result.Acceptance != nil {
		a := result.Acceptance
		if a.Validate() != nil || a.BoardID != result.Candidate.BoardID || a.CardID != result.Candidate.CardID ||
			a.AttemptID != result.Candidate.AttemptID || a.CandidateID != result.Candidate.ID || a.CandidateDigest != result.Candidate.Digest ||
			a.CriteriaDigest != result.Candidate.CriteriaDigest || a.PolicyDigest != result.Candidate.PolicyDigest ||
			a.PriorEvidenceHeadRevision > int64(len(result.Evidence)) ||
			a.PriorEvidenceSetDigest != workboard.EvidenceSetDigest(result.Evidence[:a.PriorEvidenceHeadRevision]) ||
			a.EvidenceHeadRevision != int64(len(result.Evidence)) || a.EvidenceSetDigest != workboard.EvidenceSetDigest(result.Evidence) {
			return ErrWorkboardCorrupt
		}
	}
	seenSuccessors := make(map[string]bool, len(result.Successors))
	seenPositions := make(map[string]bool, len(result.Successors))
	for index, successor := range result.Successors {
		if successor.Validate() != nil || successor.BoardID != result.Candidate.BoardID ||
			!containsString(successor.Dependencies, result.Candidate.CardID) ||
			(successor.State != workboard.Backlog && successor.State != workboard.Ready) ||
			(successor.State == workboard.Ready) != (successor.RemainingDependencies == 0) ||
			seenSuccessors[successor.ID] || index > 0 && result.Successors[index-1].ID >= successor.ID {
			return ErrWorkboardCorrupt
		}
		position := string(successor.State) + "\x00" + successor.Rank
		if seenPositions[position] {
			return ErrWorkboardCorrupt
		}
		seenSuccessors[successor.ID] = true
		seenPositions[position] = true
	}
	return nil
}

func sameEvaluationStoredResult(left, right evaluationStoredResult) bool {
	return reflect.DeepEqual(left, right)
}
