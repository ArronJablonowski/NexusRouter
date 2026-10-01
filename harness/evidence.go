package harness

import (
	"math"
	"sort"
	"time"
)

// Execution is supplied by a trusted runtime journal. Actual must come from
// verified execution provenance, including any fallback, never a requested route.
// Imported JSON or a harness's self-report alone does not establish that trust.
type Execution struct {
	Version      int
	ID           string
	Actual       Identity
	Task         TaskClass
	Status       string // completed, infrastructure_failed, canceled, indeterminate
	OutputSHA256 string
	CompletedAt  time.Time
}

func (e Execution) Validate() error {
	if e.Version != Version || !label(e.ID) || e.Actual.Validate() != nil || e.Task.Validate() != nil || !validTime(e.CompletedAt) {
		return ErrInvalid
	}
	switch e.Status {
	case "completed":
		if !digest(e.OutputSHA256) {
			return ErrInvalid
		}
	case "infrastructure_failed", "canceled", "indeterminate":
		if e.OutputSHA256 != "" && !digest(e.OutputSHA256) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func (e Execution) Digest() (string, error) {
	if e.Validate() != nil {
		return "", ErrInvalid
	}
	return hash(e), nil
}

// Review is host-authenticated evidence bound to immutable execution/output.
// Review IDs are immutable and ExpectedHead is the exact preceding review ID.
// Storage must serialize appends/compare-and-swap; Replay checks a saved log but
// does not provide persistence, access control, or concurrent write authority.
type Review struct {
	Version                           int
	ID, ExecutionDigest, ExpectedHead string
	Verdict                           string // passed, failed, unverified, withdrawn
	Method                            string // deterministic, human, automated_ai; empty for unverified/withdrawn
	MethodVersion, Reviewer           string
	Confidence, Quality               float64
	CreatedAt                         time.Time
}

func (r Review) Validate() error {
	if r.Version != Version || !label(r.ID) || !digest(r.ExecutionDigest) || !validTime(r.CreatedAt) || !unit(r.Confidence) || !unit(r.Quality) {
		return ErrInvalid
	}
	if r.ExpectedHead != "" && !label(r.ExpectedHead) {
		return ErrInvalid
	}
	switch r.Verdict {
	case "passed", "failed":
		if !label(r.MethodVersion) || !label(r.Reviewer) || r.Confidence <= 0 {
			return ErrInvalid
		}
		switch r.Method {
		case "deterministic", "human", "automated_ai":
		default:
			return ErrInvalid
		}
	case "unverified", "withdrawn":
		if r.Method != "" || r.MethodVersion != "" || !label(r.Reviewer) || r.Confidence != 0 || r.Quality != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type boundEvidence struct {
	execution Execution
	head      *Review
}

// Snapshot owns a validated replay of canonical records. It contains no mutable
// caller slices/maps and is safe for concurrent read-only selection.
type Snapshot struct {
	asOf    time.Time
	entries map[string]boundEvidence
	order   []string
}

// Replay rejects conflicting IDs, stale revisions, missing execution bindings,
// future evidence and quality votes on failed lineages. Exact retries are no-ops.
// Each execution contributes at most one current quality observation.
func Replay(executions []Execution, reviews []Review, now time.Time) (*Snapshot, error) {
	if !validTime(now) || len(executions) > MaxRecords || len(reviews) > MaxRecords {
		return nil, ErrInvalid
	}
	out := &Snapshot{asOf: now, entries: map[string]boundEvidence{}}
	ids := map[string]string{}
	for _, e := range executions {
		d, err := e.Digest()
		if err != nil || e.CompletedAt.After(now) {
			return nil, ErrInvalid
		}
		if old, ok := ids[e.ID]; ok && old != d {
			return nil, ErrConflict
		}
		ids[e.ID] = d
		out.entries[d] = boundEvidence{execution: e}
	}
	reviewIDs := map[string]Review{}
	for _, r := range reviews {
		if r.Validate() != nil || r.CreatedAt.After(now) {
			return nil, ErrInvalid
		}
		if old, ok := reviewIDs[r.ID]; ok {
			if hash(old) != hash(r) {
				return nil, ErrConflict
			}
			continue
		}
		e, ok := out.entries[r.ExecutionDigest]
		if !ok {
			return nil, ErrConflict
		}
		if e.execution.Status != "completed" {
			return nil, ErrInvalid
		}
		if r.CreatedAt.Before(e.execution.CompletedAt) {
			return nil, ErrInvalid
		}
		current := ""
		if e.head != nil {
			current = e.head.ID
			if r.CreatedAt.Before(e.head.CreatedAt) {
				return nil, ErrInvalid
			}
		}
		if r.ExpectedHead != current {
			return nil, ErrConflict
		}
		if r.Verdict == "withdrawn" && current == "" {
			return nil, ErrInvalid
		}
		copy := r
		e.head = &copy
		out.entries[r.ExecutionDigest] = e
		reviewIDs[r.ID] = r
	}
	for key := range out.entries {
		out.order = append(out.order, key)
	}
	sort.Strings(out.order)
	return out, nil
}

func (s *Snapshot) summarize(i Identity, t TaskClass, p Policy, now time.Time) Ranked {
	r := Ranked{Identity: i, Correctness: .5, Quality: .5}
	successes, quality := 0.0, 0.0
	for _, key := range s.order {
		b := s.entries[key]
		e := b.execution
		if e.Actual != i || e.Task != t {
			continue
		}
		if e.Status != "completed" {
			switch e.Status {
			case "infrastructure_failed":
				r.InfrastructureFailures++
			case "canceled":
				r.Canceled++
			case "indeterminate":
				r.Indeterminate++
			}
			continue
		}
		h := b.head
		if h == nil || h.Verdict == "unverified" || h.Verdict == "withdrawn" {
			r.PendingOutputs++
			continue
		}
		// Age from the output, so revising old feedback cannot refresh an old model
		// observation. Model-based review is capped advisory evidence, never a
		// confirmed correctness sample.
		weight := h.Confidence * math.Exp2(-float64(now.Sub(e.CompletedAt))/float64(p.HalfLife))
		if h.Method == "automated_ai" {
			weight *= .25
			r.AdvisorySamples++
		} else {
			r.ConfirmedSamples++
		}
		r.EffectiveSamples += weight
		if h.Verdict == "passed" {
			successes += weight
		}
		quality += weight * h.Quality
		if e.CompletedAt.After(r.LastEvidence) {
			r.LastEvidence = e.CompletedAt
		}
	}
	// Symmetric Beta(1,1) correctness prior and the same bounded quality prior.
	r.Correctness = (1 + successes) / (2 + r.EffectiveSamples)
	r.Quality = (1 + quality) / (2 + r.EffectiveSamples)
	r.Confidence = math.Min(1, r.EffectiveSamples/float64(p.MinSamples))
	return r
}
