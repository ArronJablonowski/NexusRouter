package skills

import (
	"context"
	"time"
)

// OutcomeSupervisionGuard is a trusted metadata admission hook. It receives
// scalar copies under the catalog lock and must be bounded, read-only,
// cancellation-cooperative, and must not reenter the store.
type OutcomeSupervisionGuard func(context.Context, OutcomeSupervisionState, OutcomeSupervisionCheck) error

// OutcomeSupervisionState is the durable cursor for one named supervisor.
// PolicyDigest and Interval are immutable after the first preparation.
type OutcomeSupervisionState struct {
	Version        int           `json:"version"`
	Scope          string        `json:"scope"`
	Name           string        `json:"name"`
	PolicyDigest   string        `json:"policy_digest"`
	Interval       time.Duration `json:"interval"`
	Revision       int64         `json:"revision"`
	After          string        `json:"after"`
	PendingCheckID string        `json:"pending_check_id"`
	NextDue        time.Time     `json:"next_due"`
}

// OutcomeSupervisionCheck reserves one exact active skill observation. The
// check is separate from outcome rollback intents: a waiting completion never
// creates or consumes an outcome intent, selection checkpoint, or receipt.
type OutcomeSupervisionCheck struct {
	Version            int                      `json:"version"`
	Scope              string                   `json:"scope"`
	SupervisorName     string                   `json:"supervisor_name"`
	PolicyDigest       string                   `json:"policy_digest"`
	CheckID            string                   `json:"check_id"`
	Sequence           int64                    `json:"sequence"`
	Candidate          OutcomeRollbackCandidate `json:"candidate"`
	PreviousCursor     string                   `json:"previous_cursor"`
	PreparedAt         time.Time                `json:"prepared_at"`
	Status             string                   `json:"status"`
	Code               string                   `json:"code"`
	OutcomeOperationID string                   `json:"outcome_operation_id,omitempty"`
	FinishedAt         time.Time                `json:"finished_at"`
}

// OutcomeSupervisionCompletion is the trusted host's durable scheduling
// result. evaluated must identify an already-committed outcome receipt.
type OutcomeSupervisionCompletion struct {
	Code               string `json:"code"`
	OutcomeOperationID string `json:"outcome_operation_id,omitempty"`
}

func (s OutcomeSupervisionState) Validate() error {
	if s.Version != 1 || !(Key{s.Scope, s.Name}).valid() || !monitorDigest(s.PolicyDigest) ||
		s.Interval < time.Second || s.Interval > 24*time.Hour || s.Revision < 0 ||
		(s.After != "" && !identifier.MatchString(s.After)) ||
		(s.PendingCheckID != "" && !identifier.MatchString(s.PendingCheckID)) || !monitorTime(s.NextDue) {
		return ErrInvalid
	}
	return nil
}

func (c OutcomeSupervisionCompletion) Validate() error {
	switch c.Code {
	case "waiting", "ineligible":
		if c.OutcomeOperationID != "" && !identifier.MatchString(c.OutcomeOperationID) {
			return ErrInvalid
		}
	case "evaluated":
		if !identifier.MatchString(c.OutcomeOperationID) {
			return ErrInvalid
		}
	case "check_failed", "stale_activation":
		if c.OutcomeOperationID != "" && !identifier.MatchString(c.OutcomeOperationID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (c OutcomeSupervisionCheck) Validate() error {
	if c.Version != 1 || !(Key{c.Scope, c.SupervisorName}).valid() || !monitorDigest(c.PolicyDigest) ||
		!identifier.MatchString(c.CheckID) || c.Sequence < 1 || c.Candidate.Validate() != nil ||
		c.Candidate.Current.Key.Scope != c.Scope ||
		(c.PreviousCursor != "" && !identifier.MatchString(c.PreviousCursor)) ||
		c.Candidate.Current.Key.Name <= c.PreviousCursor || !monitorTime(c.PreparedAt) {
		return ErrInvalid
	}
	switch c.Status {
	case "pending":
		if c.Code != "" || c.OutcomeOperationID != "" && !identifier.MatchString(c.OutcomeOperationID) || !c.FinishedAt.IsZero() {
			return ErrInvalid
		}
	case "completed", "failed":
		completion := OutcomeSupervisionCompletion{Code: c.Code, OutcomeOperationID: c.OutcomeOperationID}
		if completion.Validate() != nil || !monitorTime(c.FinishedAt) ||
			(c.Status == "failed") != (c.Code == "check_failed") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validateOutcomeSupervision(c *catalog) error {
	if len(c.OutcomeSupervisors) > 1000 || len(c.OutcomeSupervisorChecks) > 1000 ||
		(len(c.OutcomeSupervisors) > 0 || len(c.OutcomeSupervisorChecks) > 0) && c.Schema < 9 {
		return ErrInvalid
	}
	for index, state := range c.OutcomeSupervisors {
		if state.Validate() != nil || index != (Key{state.Scope, state.Name}).index() {
			return ErrInvalid
		}
		if state.PendingCheckID != "" {
			check, ok := c.OutcomeSupervisorChecks[state.PendingCheckID]
			if !ok || check.Status != "pending" || check.Sequence != state.Revision ||
				check.Scope != state.Scope || check.SupervisorName != state.Name || check.PreviousCursor != state.After {
				return ErrInvalid
			}
		}
	}
	seen := map[string]map[int64]bool{}
	operations := map[string]string{}
	for id, check := range c.OutcomeSupervisorChecks {
		index := (Key{check.Scope, check.SupervisorName}).index()
		state, ok := c.OutcomeSupervisors[index]
		if id != check.CheckID || check.Validate() != nil || !ok || check.PolicyDigest != state.PolicyDigest || check.Sequence > state.Revision {
			return ErrInvalid
		}
		if seen[index] == nil {
			seen[index] = map[int64]bool{}
		}
		if seen[index][check.Sequence] {
			return ErrInvalid
		}
		seen[index][check.Sequence] = true
		if check.Status == "pending" && state.PendingCheckID != id ||
			check.Status != "pending" && (state.PendingCheckID == id || check.Sequence >= state.Revision) {
			return ErrInvalid
		}
		if check.Code == "evaluated" {
			receipt, err := lookupOutcomeOperation(c, check.Candidate.Current.Key, check.OutcomeOperationID)
			if err == nil {
				if receipt.Expected != check.Candidate.Current {
					return ErrInvalid
				}
			} else {
				settlement, settlementErr := lookupOutcomeSettlement(c, check.Candidate.Current.Key, check.OutcomeOperationID)
				if settlementErr != nil || settlement.Expected != check.Candidate.Current {
					return ErrInvalid
				}
			}
		}
		if check.OutcomeOperationID != "" {
			if prior := operations[check.OutcomeOperationID]; prior != "" && prior != id {
				return ErrInvalid
			}
			operations[check.OutcomeOperationID] = id
		}
	}
	return nil
}

func runOutcomeSupervisionGuard(ctx context.Context, guard OutcomeSupervisionGuard, state OutcomeSupervisionState, check OutcomeSupervisionCheck) (err error) {
	if guard == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if recover() != nil {
			err = ErrValidation
		}
	}()
	if guard(ctx, state, check) != nil {
		return ErrValidation
	}
	return ctx.Err()
}

func (s *FileStore) OutcomeSupervisionState(ctx context.Context, scope, name string) (OutcomeSupervisionState, error) {
	if s == nil || ctx == nil || !s.permitted(Key{scope, name}) {
		return OutcomeSupervisionState{}, ErrInvalid
	}
	var out OutcomeSupervisionState
	err := s.with(ctx, func(c *catalog) error {
		var ok bool
		out, ok = c.OutcomeSupervisors[(Key{scope, name}).index()]
		if !ok {
			return ErrNotFound
		}
		return nil
	}, false)
	if err != nil {
		return OutcomeSupervisionState{}, err
	}
	return out, nil
}

func (s *FileStore) OutcomeSupervisionCheck(ctx context.Context, scope, name, checkID string) (OutcomeSupervisionCheck, error) {
	if s == nil || ctx == nil || !s.permitted(Key{scope, name}) || !identifier.MatchString(checkID) {
		return OutcomeSupervisionCheck{}, ErrInvalid
	}
	var out OutcomeSupervisionCheck
	err := s.with(ctx, func(c *catalog) error {
		var ok bool
		out, ok = c.OutcomeSupervisorChecks[checkID]
		if !ok {
			return ErrNotFound
		}
		if out.Scope != scope || out.SupervisorName != name {
			return ErrConflict
		}
		return nil
	}, false)
	if err != nil {
		return OutcomeSupervisionCheck{}, err
	}
	return out, nil
}
