package harness

import (
	"sort"
	"time"
)

// Select ranks exact combinations with correctness first and quality second.
// Cost is only a hard budget constraint, never a reward that offsets accuracy.
// It is effect-free: recheck authorization and reserve resources before dispatch.
func Select(r Request, p Policy, candidates []Candidate, evidence *Snapshot, now time.Time, draw float64) (Selection, error) {
	out := Selection{Version: Version, Task: r.Task, AsOf: now, Ranked: []Ranked{}, Excluded: []Exclusion{}}
	if r.Version != Version || r.Task.Validate() != nil || !labels(r.Capabilities) || r.ContextTokens < 1 || !nonnegative(r.MaxCost) || !validTime(now) || !unit(draw) || draw == 1 || len(candidates) > 4096 || evidence == nil || evidence.entries == nil || now.Before(evidence.asOf) {
		return out, ErrInvalid
	}
	if p.Version != Version || p.HalfLife <= 0 || p.MinSamples < 1 || p.MinSamples > MaxRecords || !unit(p.Exploration) || p.Exploration > .25 {
		return out, ErrInvalid
	}
	switch r.Mode {
	case "local_only", "cloud_only", "hybrid":
	default:
		return out, ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		key, err := c.Identity.Key()
		if err != nil || seen[key] || !labels(c.Capabilities) || c.ContextTokens < 1 || !nonnegative(c.EstimatedCost) {
			return Selection{}, ErrInvalid
		}
		seen[key] = true
		reasons := []string{}
		if (r.Mode == "local_only" && !c.Local) || (r.Mode == "cloud_only" && c.Local) {
			reasons = append(reasons, "mode")
		}
		if r.LocalRequired && !c.Local {
			reasons = append(reasons, "privacy")
		}
		if !c.Available {
			reasons = append(reasons, "unavailable")
		}
		if !c.Authorized {
			reasons = append(reasons, "authorization")
		}
		if !c.Compatible {
			reasons = append(reasons, "incompatible")
		}
		if !c.CapacityAvailable {
			reasons = append(reasons, "capacity")
		}
		if !c.CredentialAvailable {
			reasons = append(reasons, "credential")
		}
		if c.ContextTokens < r.ContextTokens {
			reasons = append(reasons, "context")
		}
		if c.EstimatedCost > r.MaxCost {
			reasons = append(reasons, "budget")
		}
		caps := map[string]bool{}
		for _, v := range c.Capabilities {
			caps[v] = true
		}
		for _, v := range r.Capabilities {
			if !caps[v] {
				reasons = append(reasons, "capability")
				break
			}
		}
		if len(reasons) > 0 {
			out.Excluded = append(out.Excluded, Exclusion{Identity: c.Identity, Reasons: reasons})
			continue
		}
		out.Ranked = append(out.Ranked, evidence.summarize(c.Identity, r.Task, p, now))
	}
	sort.Slice(out.Excluded, func(i, j int) bool { return hash(out.Excluded[i].Identity) < hash(out.Excluded[j].Identity) })
	sort.Slice(out.Ranked, func(i, j int) bool {
		a, b := out.Ranked[i], out.Ranked[j]
		if order := accuracyOrder(a, b); order != 0 {
			return order < 0
		}
		return hash(a.Identity) < hash(b.Identity)
	})
	if len(out.Ranked) == 0 {
		return out, ErrNoRoute
	}
	out.Primary = out.Ranked[0]
	out.Reason = "highest_evidence_supported_accuracy"
	if out.Primary.EffectiveSamples == 0 {
		out.Reason = "insufficient_evidence_stable_tiebreak"
	}
	// Exploration uses the least-observed eligible alternative. Ordering above
	// keeps ties stable; a caller must supply a fresh independent uniform draw.
	if r.AllowExploration && draw < p.Exploration && len(out.Ranked) > 1 {
		pick := out.Ranked[1]
		for _, c := range out.Ranked[2:] {
			if c.EffectiveSamples < pick.EffectiveSamples {
				pick = c
			}
		}
		if pick.EffectiveSamples < float64(p.MinSamples) {
			out.Primary = pick
			out.Explored = true
			out.Reason = "explicit_bounded_evaluation"
		}
	}
	return out, nil
}
