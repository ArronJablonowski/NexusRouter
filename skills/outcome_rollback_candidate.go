package skills

import "context"

// OutcomeRollbackCandidate identifies the exact current activation and its
// immediate, validated predecessor. It is an observation for constructing an
// outcome comparison, not authorization to collect evidence or roll back.
type OutcomeRollbackCandidate struct {
	Version     int             `json:"version"`
	Current     ActivationState `json:"current"`
	Predecessor string          `json:"predecessor"`
}

// Validate checks the candidate's structural binding. Eligibility is stronger:
// it is established from the catalog activation timeline by
// FileStore.OutcomeRollbackCandidate.
func (c OutcomeRollbackCandidate) Validate() error {
	if c.Version != 1 || c.Current.Validate() != nil || !versionID(c.Current.Active) || !versionID(c.Predecessor) || c.Predecessor == c.Current.Active {
		return ErrInvalid
	}
	return nil
}

// OutcomeRollbackCandidateStore is an optional read-only extension for hosts
// which construct outcome comparisons from catalog-owned activation history.
type OutcomeRollbackCandidateStore interface {
	Store
	OutcomeRollbackCandidate(context.Context, Key) (OutcomeRollbackCandidate, error)
}

var _ OutcomeRollbackCandidateStore = (*FileStore)(nil)

// OutcomeRollbackCandidate reads and validates the current activation and its
// immediate predecessor from one catalog snapshot. It never initializes,
// repairs, validates a draft, or changes the activation timeline.
func (s *FileStore) OutcomeRollbackCandidate(ctx context.Context, key Key) (OutcomeRollbackCandidate, error) {
	if s == nil || ctx == nil || !s.permitted(key) {
		return OutcomeRollbackCandidate{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return OutcomeRollbackCandidate{}, err
	}
	var result OutcomeRollbackCandidate
	err := s.with(ctx, func(c *catalog) error {
		e, ok := c.Skills[key.index()]
		if !ok {
			return ErrNotFound
		}
		state, err := stateForEntry(e)
		if err != nil {
			return err
		}
		result, err = outcomeRollbackCandidateForEntry(e, state)
		return err
	}, false)
	if err != nil {
		return OutcomeRollbackCandidate{}, err
	}
	return result, nil
}

func outcomeRollbackCandidateForEntry(e entry, state ActivationState) (OutcomeRollbackCandidate, error) {
	stack, err := activationStack(e)
	if err != nil {
		return OutcomeRollbackCandidate{}, err
	}
	if len(stack) == 0 {
		return OutcomeRollbackCandidate{}, ErrConflict
	}
	last := stack[len(stack)-1]
	if !versionID(last.From) || last.To != state.Active {
		return OutcomeRollbackCandidate{}, ErrConflict
	}
	count := 0
	for _, a := range e.Activations {
		if a.To == state.Active {
			count++
		}
	}
	if count != 1 || len(e.Activations) == 0 || e.Activations[len(e.Activations)-1].Rollback || e.Activations[len(e.Activations)-1].To != state.Active {
		return OutcomeRollbackCandidate{}, ErrConflict
	}
	proof := e.Validated[last.From]
	if !proof.Passed || !proof.Deterministic || !identifier.MatchString(proof.ID) {
		return OutcomeRollbackCandidate{}, ErrValidation
	}
	result := OutcomeRollbackCandidate{Version: 1, Current: state, Predecessor: last.From}
	if result.Validate() != nil {
		return OutcomeRollbackCandidate{}, ErrInvalid
	}
	return result, nil
}
