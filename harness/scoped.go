package harness

import (
	"errors"
	"sort"
	"time"
)

// ScopedCandidate retains a separate trusted evidence snapshot per execution
// location. Scope is host-owned provenance, never a score from a remote model.
// Identical configurations in different scopes are distinct candidates.
type ScopedCandidate struct {
	Scope     string
	Candidate Candidate
	Evidence  *Snapshot
}
type ScopedRanked struct {
	Scope  string
	Ranked Ranked
}
type ScopedExclusion struct {
	Scope     string
	Exclusion Exclusion
}
type ScopedSelection struct {
	Version  int
	Task     TaskClass
	AsOf     time.Time
	Primary  ScopedRanked
	Ranked   []ScopedRanked
	Excluded []ScopedExclusion
	Explored bool
	Reason   string
}

// SelectScoped applies the same eligibility and scoring as Select using only
// each candidate's scope-local evidence. It never pools or imports scores.
// This is a read-only decision, not execution authority or a reservation.
func SelectScoped(r Request, p Policy, candidates []ScopedCandidate, now time.Time, draw float64) (ScopedSelection, error) {
	out := ScopedSelection{Version: Version, Task: r.Task, AsOf: now, Ranked: []ScopedRanked{}, Excluded: []ScopedExclusion{}}
	// Validate request/policy even for an empty set, using the ordinary selector.
	empty, err := Replay(nil, nil, now)
	if err != nil {
		return out, err
	}
	if _, err = Select(r, p, nil, empty, now, draw); err != nil && !errors.Is(err, ErrNoRoute) {
		return out, err
	}
	if len(candidates) > 4096 {
		return out, ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		if !label(c.Scope) {
			return ScopedSelection{}, ErrInvalid
		}
		key := hash(struct {
			Scope    string
			Identity Identity
		}{c.Scope, c.Candidate.Identity})
		if seen[key] {
			return ScopedSelection{}, ErrInvalid
		}
		seen[key] = true
		scored, err := Select(r, p, []Candidate{c.Candidate}, c.Evidence, now, draw)
		if err != nil && !errors.Is(err, ErrNoRoute) {
			return ScopedSelection{}, err
		}
		for _, e := range scored.Excluded {
			out.Excluded = append(out.Excluded, ScopedExclusion{c.Scope, e})
		}
		for _, v := range scored.Ranked {
			out.Ranked = append(out.Ranked, ScopedRanked{c.Scope, v})
		}
	}
	sort.Slice(out.Excluded, func(i, j int) bool {
		return scopedKey(out.Excluded[i].Scope, out.Excluded[i].Exclusion.Identity) < scopedKey(out.Excluded[j].Scope, out.Excluded[j].Exclusion.Identity)
	})
	sort.Slice(out.Ranked, func(i, j int) bool {
		a, b := out.Ranked[i], out.Ranked[j]
		if order := accuracyOrder(a.Ranked, b.Ranked); order != 0 {
			return order < 0
		}
		return scopedKey(a.Scope, a.Ranked.Identity) < scopedKey(b.Scope, b.Ranked.Identity)
	})
	if len(out.Ranked) == 0 {
		return out, ErrNoRoute
	}
	out.Primary = out.Ranked[0]
	out.Reason = "highest_evidence_supported_accuracy"
	if out.Primary.Ranked.EffectiveSamples == 0 {
		out.Reason = "insufficient_evidence_stable_tiebreak"
	}
	if r.AllowExploration && draw < p.Exploration && len(out.Ranked) > 1 {
		pick := out.Ranked[1]
		for _, c := range out.Ranked[2:] {
			if c.Ranked.EffectiveSamples < pick.Ranked.EffectiveSamples {
				pick = c
			}
		}
		if pick.Ranked.EffectiveSamples < float64(p.MinSamples) {
			out.Primary = pick
			out.Explored = true
			out.Reason = "explicit_bounded_evaluation"
		}
	}
	return out, nil
}
func scopedKey(scope string, i Identity) string {
	return hash(struct {
		Scope    string
		Identity Identity
	}{scope, i})
}
func accuracyOrder(a, b Ranked) int {
	for _, pair := range [][2]float64{{a.Correctness, b.Correctness}, {a.Quality, b.Quality}, {a.Confidence, b.Confidence}} {
		if pair[0] > pair[1] {
			return -1
		}
		if pair[0] < pair[1] {
			return 1
		}
	}
	return 0
}
