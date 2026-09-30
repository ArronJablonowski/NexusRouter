package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// WorkboardBudgetProjection is a read-only view of card-owned capacity after
// every settled and unresolved execution and auxiliary review charge. A zero
// card limit remains zero: callers must retain the domain's unbounded meaning.
type WorkboardBudgetProjection struct {
	CardRevision        int64
	Budget              workboard.WorkBudget
	RemainingTimeMS     int64
	RemainingTokens     int64
	RemainingCostMicros int64
}

// ReadWorkboardBudgetProjection derives remaining capacity from canonical,
// append-only accounting records in one database snapshot. Admission still
// rechecks the same ledgers transactionally; this projection grants no work.
func (s *Store) ReadWorkboardBudgetProjection(ctx context.Context, boardID, cardID string) (WorkboardBudgetProjection, error) {
	zero := WorkboardBudgetProjection{}
	if s == nil || ctx == nil || ctx.Err() != nil || !validWorkboardID(boardID) || !validWorkboardID(cardID) {
		return zero, invalidWorkboard("execution_budget_projection")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	card, body, err := readStoredCard(ctx, tx, boardID, cardID)
	if err != nil {
		return zero, err
	}
	if body.Budget != (storedWorkboardBudget{AttemptLimit: card.Budget.AttemptLimit, TimeLimitMS: card.Budget.TimeLimitMS,
		TokenLimit: card.Budget.TokenLimit, CostMicros: card.Budget.CostMicros}) {
		return zero, ErrWorkboardCorrupt
	}
	accounts, err := validatedExecutionAccounts(ctx, tx, boardID, cardID)
	if err != nil {
		return zero, err
	}
	settled, unresolved, err := executionCardCharges(accounts, boardID, cardID)
	if err != nil {
		return zero, err
	}
	reviewSettled, reviewUnresolved, err := auxiliaryReviewCardCharges(ctx, tx, boardID, cardID)
	if err != nil {
		return zero, err
	}
	for _, charge := range []executionCharges{unresolved, reviewSettled, reviewUnresolved} {
		if err = addExecutionCharge(&settled, charge.timeMS, charge.tokens, charge.costMicros); err != nil {
			return zero, err
		}
	}
	projection := WorkboardBudgetProjection{CardRevision: card.Revision, Budget: card.Budget,
		RemainingTimeMS:     remainingWorkboardCapacity(card.Budget.TimeLimitMS, settled.timeMS),
		RemainingTokens:     remainingWorkboardCapacity(card.Budget.TokenLimit, settled.tokens),
		RemainingCostMicros: remainingWorkboardCapacity(card.Budget.CostMicros, settled.costMicros)}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return projection, nil
}

func remainingWorkboardCapacity(limit, charged int64) int64 {
	if limit == 0 || charged >= limit {
		return 0
	}
	return limit - charged
}
