package workboard

import "encoding/json"

const (
	MaxAttemptsPerCard    = 32
	MaxAcceptanceCriteria = 32
	MaxCriterionTextBytes = 4 << 10
	MaxCriterionAggregate = 64 << 10
	MaxWorkDurationMillis = int64(30 * 24 * 60 * 60 * 1000)
	MaxWorkTokens         = int64(1_000_000_000)
	MaxWorkCostMicros     = int64(1_000_000_000_000)
)

// WorkBudget bounds attempts and aggregate resources available to a card.
// Zero resource limits are unbounded; AttemptLimit is always explicit.
type WorkBudget struct {
	AttemptLimit int   `json:"attempt_limit"`
	TimeLimitMS  int64 `json:"time_limit_ms"`
	TokenLimit   int64 `json:"token_limit"`
	CostMicros   int64 `json:"cost_micros"`
}

func (b WorkBudget) Validate() error {
	if b.AttemptLimit < 1 || b.AttemptLimit > MaxAttemptsPerCard || b.TimeLimitMS < 0 || b.TimeLimitMS > MaxWorkDurationMillis ||
		b.TokenLimit < 0 || b.TokenLimit > MaxWorkTokens || b.CostMicros < 0 || b.CostMicros > MaxWorkCostMicros {
		return fail(CodeInvalid, "budget")
	}
	return nil
}

// AcceptanceCriterion preserves the objective/subjective evidence boundary in
// the domain without depending on any transport or presentation package.
type AcceptanceCriterion struct {
	Version        int    `json:"version"`
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	RequiredSource string `json:"required_source"`
	ValidatorID    string `json:"validator_id"`
	Description    string `json:"description"`
	Required       bool   `json:"required"`
}

func (c AcceptanceCriterion) Validate() error {
	if c.Version != SchemaVersion || !validID(c.ID) || !validID(c.ValidatorID) || !boundedText(c.Description, MaxCriterionTextBytes, false) {
		return fail(CodeInvalid, "criterion")
	}
	if c.Kind == "objective" && c.RequiredSource == "deterministic" || c.Kind == "subjective" && c.RequiredSource == "user_feedback" {
		return nil
	}
	return fail(CodeInvalid, "criterion")
}

func validateAcceptanceCriteria(criteria []AcceptanceCriterion, revision int64) error {
	if revision < 1 || len(criteria) < 1 || len(criteria) > MaxAcceptanceCriteria {
		return fail(CodeInvalid, "criteria")
	}
	seen, total := map[string]bool{}, 0
	for _, criterion := range criteria {
		if criterion.Validate() != nil || seen[criterion.ID] {
			return fail(CodeInvalid, "criteria")
		}
		seen[criterion.ID] = true
		body, err := json.Marshal(criterion)
		if err != nil {
			return fail(CodeInvalid, "criteria")
		}
		total += len(body)
		if total > MaxCriterionAggregate {
			return fail(CodeLimitExceeded, "criteria")
		}
	}
	return nil
}

func copyCriteria(criteria []AcceptanceCriterion) []AcceptanceCriterion {
	return append([]AcceptanceCriterion{}, criteria...)
}
