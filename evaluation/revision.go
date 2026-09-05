package evaluation

// ValidateRevision admits explicit user corrections of subjective evidence.
// Objective validators are not replaceable through this policy. Attempt
// identity and execution measurements are fixed: only subjective judgment may
// change, without creating another performance sample.
func ValidateRevision(prior, next Record) error {
	if prior.Validate() != nil || next.Validate() != nil || prior.ID == next.ID || prior.TaskID != next.TaskID || prior.AttemptID != next.AttemptID || prior.Key != next.Key || prior.ExecutionSucceeded != next.ExecutionSucceeded || prior.Latency != next.Latency || prior.Cost != next.Cost || !prior.Time.Equal(next.Time) || !sameSchema(prior.SchemaPassed, next.SchemaPassed) {
		return ErrEvidence
	}
	old, err := Resolve(prior.Checks, prior.AllowJudge)
	if err != nil || (old.Source != LLMJudge && old.Source != UserFeedback) {
		return ErrEvidence
	}
	if next.AllowJudge {
		return ErrEvidence
	}
	for _, check := range next.Checks {
		if check.Source != UserFeedback {
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
