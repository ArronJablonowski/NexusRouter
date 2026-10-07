package evaluation

// ValidateRevision admits explicit user corrections of subjective evidence.
// Objective validators are not replaceable through this policy. Attempt
// identity and execution measurements are fixed: only subjective judgment may
// change, without creating another performance sample.
func ValidateRevision(prior, next Record) error {
	if prior.Validate() != nil || next.Validate() != nil || prior.ID == next.ID || prior.TaskID != next.TaskID || prior.AttemptID != next.AttemptID || prior.Key != next.Key || prior.ExecutionSucceeded != next.ExecutionSucceeded || prior.Latency != next.Latency || prior.ContextTokens != next.ContextTokens || prior.TimedOut != next.TimedOut || prior.ProviderError != next.ProviderError || prior.PeakMemoryBytes != next.PeakMemoryBytes || prior.SwapGrowthBytes != next.SwapGrowthBytes || prior.Cost != next.Cost || !prior.Time.Equal(next.Time) || !sameSchema(prior.SchemaPassed, next.SchemaPassed) {
		return ErrEvidence
	}
	old, err := Resolve(prior.Checks, prior.AllowJudge)
	if err != nil || (old.Source != LLMJudge && old.Source != UserFeedback && old.Source != Withdrawn) {
		return ErrEvidence
	}
	if next.AllowJudge {
		return ErrEvidence
	}
	for _, check := range next.Checks {
		if check.Source != UserFeedback && check.Source != Withdrawn {
			return ErrEvidence
		}
	}
	return nil
}

func sameSchema(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ValidateObjectiveCorrection admits an operator/evaluator correction of a
// misattributed subjective grade. It preserves the verdict and every measured
// field. Objective evidence can never be revised through this path.
func ValidateObjectiveCorrection(prior, next Record) error {
	if next.Validate() != nil || next.AllowJudge || len(next.Checks) != 1 || next.Checks[0].Source != Deterministic {
		return ErrEvidence
	}
	old, err := Resolve(prior.Checks, prior.AllowJudge)
	if err != nil || old.Source != UserFeedback || old.Accepted != next.Checks[0].Passed {
		return ErrEvidence
	}
	// Reuse identity and measurement invariants without broadening the public
	// subjective-feedback revision policy.
	check := next
	check.Checks = []Check{{Source: UserFeedback, Reference: next.Checks[0].Reference, Passed: next.Checks[0].Passed}}
	return ValidateRevision(prior, check)
}

// ValidateStoredRevision recognizes both authorized writer policies.
func ValidateStoredRevision(prior, next Record) error {
	if ValidateRevision(prior, next) == nil {
		return nil
	}
	return ValidateObjectiveCorrection(prior, next)
}
