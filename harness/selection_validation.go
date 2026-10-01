package harness

// Validate checks the persisted decision's structural and identity bindings.
// It does not authenticate imported scores; only the trusted selector may create
// a decision from its ledger snapshot and fresh host admission metadata.
func (s Selection) Validate() error {
	if s.Version != Version || s.Task.Validate() != nil || !validTime(s.AsOf) || len(s.Ranked) == 0 || len(s.Ranked)+len(s.Excluded) > 4096 {
		return ErrInvalid
	}
	switch s.Reason {
	case "highest_evidence_supported_accuracy", "insufficient_evidence_stable_tiebreak":
		if s.Explored {
			return ErrInvalid
		}
	case "explicit_bounded_evaluation":
		if !s.Explored {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	seen := map[Identity]bool{}
	found := false
	for _, r := range s.Ranked {
		if r.Identity.Validate() != nil || seen[r.Identity] || !unit(r.Correctness) || !unit(r.Quality) || !unit(r.Confidence) || !nonnegative(r.EffectiveSamples) || r.EffectiveSamples > MaxRecords {
			return ErrInvalid
		}
		for _, n := range []int{r.ConfirmedSamples, r.AdvisorySamples, r.PendingOutputs, r.InfrastructureFailures, r.Canceled, r.Indeterminate} {
			if n < 0 || n > MaxRecords {
				return ErrInvalid
			}
		}
		if !r.LastEvidence.IsZero() && (!validTime(r.LastEvidence) || r.LastEvidence.After(s.AsOf)) {
			return ErrInvalid
		}
		seen[r.Identity] = true
		if r == s.Primary {
			found = true
		}
	}
	if !found || (!s.Explored && s.Primary != s.Ranked[0]) {
		return ErrInvalid
	}
	for _, e := range s.Excluded {
		if e.Identity.Validate() != nil || seen[e.Identity] || len(e.Reasons) == 0 || len(e.Reasons) > 10 {
			return ErrInvalid
		}
		seen[e.Identity] = true
		reasons := map[string]bool{}
		for _, r := range e.Reasons {
			if reasons[r] {
				return ErrInvalid
			}
			reasons[r] = true
			switch r {
			case "mode", "privacy", "unavailable", "authorization", "incompatible", "capacity", "credential", "context", "budget", "capability":
			default:
				return ErrInvalid
			}
		}
	}
	return nil
}
