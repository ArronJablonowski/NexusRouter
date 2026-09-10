package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// successorEffectEventID binds an immutable, metadata-only board event to the
// exact successor projection written by an acceptance transaction. The card's
// user content is deliberately excluded; only the fields changed by dependency
// satisfaction are committed to the digest.
func successorEffectEventID(operationID, dependencyID string, card workboard.Card) (string, error) {
	if !validWorkboardID(operationID) || !validWorkboardID(dependencyID) || card.Validate() != nil {
		return "", ErrWorkboardCorrupt
	}
	body, err := json.Marshal(struct {
		Version               int             `json:"version"`
		OperationID           string          `json:"operation_id"`
		DependencyID          string          `json:"dependency_id"`
		BoardID               string          `json:"board_id"`
		CardID                string          `json:"card_id"`
		Revision              int64           `json:"revision"`
		State                 workboard.State `json:"state"`
		Rank                  string          `json:"rank"`
		RemainingDependencies int             `json:"remaining_dependencies"`
		UpdatedAt             int64           `json:"updated_at"`
	}{1, operationID, dependencyID, card.BoardID, card.ID, card.Revision, card.State, card.Rank, card.RemainingDependencies, card.UpdatedAt.UnixNano()})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return "effect_" + hex.EncodeToString(digest[:]), nil
}

func successorEffectAction(card workboard.Card) (workboard.BoardAction, error) {
	switch card.State {
	case workboard.Ready:
		return workboard.CardMoveAction, nil
	case workboard.Backlog:
		return workboard.CardReviseAction, nil
	default:
		return "", ErrWorkboardCorrupt
	}
}
