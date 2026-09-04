// Package routing filters hard constraints before ranking eligible models.
package routing

import (
	"errors"
	"math"
	"sort"
	"time"
)

type Key struct{ Model, Provider, Domain, Profile string }
type Evidence struct {
	Samples                          int
	Quality, Compliance, Reliability float64
	Latency                          time.Duration
	Cost                             float64
	Updated                          time.Time
}
type Candidate struct {
	Model, Provider, FailureDomain            string
	Local                                     bool
	Capabilities                              []string
	ContextTokens                             int
	Healthy, PolicyAllowed, CapacityAvailable bool
	EstimatedCost                             float64
}
type Request struct {
	Mode, Domain, Profile string
	LocalRequired         bool
	Capabilities          []string
	ContextTokens         int
	MaxCost               float64
}
type Weights struct{ Quality, Compliance, Reliability, Latency, Cost, Recency, Uncertainty float64 }
type Policy struct {
	Weights      Weights
	MinSamples   int
	HalfLife     time.Duration
	LatencyScale time.Duration
	CostScale    float64
	Exploration  float64
}
type Ranked struct {
	Model, Provider, FailureDomain string
	Score, Confidence              float64
	Samples                        int
}

// Explanation has no prompt, endpoint, credentials, or model output fields.
type Exclusion struct {
	Model, Provider string
	Reasons         []string
}
type Selection struct {
	Primary   Ranked
	Fallbacks []Ranked
	Ranked    []Ranked
	Excluded  []Exclusion
	Explored  bool
}

var ErrInvalid = errors.New("invalid routing inputs")
var ErrNoRoute = errors.New("no eligible model route")

func Defaults() Policy {
	return Policy{Weights: Weights{.35, .15, .20, .10, .10, .05, .05}, MinSamples: 20, HalfLife: 30 * 24 * time.Hour, LatencyScale: 10 * time.Second, CostScale: .01, Exploration: .05}
}

// Select is deterministic for its inputs, including the injected random draw.
// Callers supply an independent uniform draw in [0,1) for bounded exploration.
// Capacity and cost are admission snapshots; reserve them before dispatch.
func Select(r Request, p Policy, candidates []Candidate, evidence map[Key]Evidence, now time.Time, draw float64) (Selection, error) {
	out := Selection{Ranked: []Ranked{}, Excluded: []Exclusion{}, Fallbacks: []Ranked{}}
	if r.Mode != "local_only" && r.Mode != "cloud_only" && r.Mode != "hybrid" {
		return out, ErrInvalid
	}
	if r.Domain == "" || r.Profile == "" || r.ContextTokens < 1 || !nonnegative(r.MaxCost) || now.IsZero() || !unit(draw) || draw == 1 || p.MinSamples < 1 || p.HalfLife <= 0 || p.LatencyScale <= 0 || !nonnegative(p.CostScale) || p.CostScale == 0 || !unit(p.Exploration) || p.Exploration > .25 {
		return out, ErrInvalid
	}
	w := p.Weights
	sum := 0.0
	for _, v := range []float64{w.Quality, w.Compliance, w.Reliability, w.Latency, w.Cost, w.Recency, w.Uncertainty} {
		if !unit(v) {
			return out, ErrInvalid
		}
		sum += v
	}
	if math.Abs(sum-1) > 1e-9 {
		return out, ErrInvalid
	}
	seen := map[[2]string]bool{}
	for _, c := range candidates {
		id := [2]string{c.Model, c.Provider}
		if c.Model == "" || c.Provider == "" || seen[id] || c.ContextTokens < 1 || !nonnegative(c.EstimatedCost) {
			return out, ErrInvalid
		}
		seen[id] = true
		reasons := []string{}
		if (r.Mode == "local_only" && !c.Local) || (r.Mode == "cloud_only" && c.Local) {
			reasons = append(reasons, "mode")
		}
		if r.LocalRequired && !c.Local {
			reasons = append(reasons, "privacy")
		}
		if !c.Healthy {
			reasons = append(reasons, "health")
		}
		if !c.PolicyAllowed {
			reasons = append(reasons, "policy")
		}
		if !c.CapacityAvailable {
			reasons = append(reasons, "capacity")
		}
		if c.ContextTokens < r.ContextTokens {
			reasons = append(reasons, "context")
		}
		if c.EstimatedCost > r.MaxCost {
			reasons = append(reasons, "budget")
		}
		for _, required := range r.Capabilities {
			found := false
			for _, capability := range c.Capabilities {
				if capability == required {
					found = true
					break
				}
			}
			if !found {
				reasons = append(reasons, "capability")
				break
			}
		}
		if len(reasons) > 0 {
			out.Excluded = append(out.Excluded, Exclusion{c.Model, c.Provider, reasons})
			continue
		}
		e, ok := evidence[Key{c.Model, c.Provider, r.Domain, r.Profile}]
		if ok && (e.Samples < 0 || !unit(e.Quality) || !unit(e.Compliance) || !unit(e.Reliability) || e.Latency < 0 || !nonnegative(e.Cost) || e.Updated.IsZero() || e.Updated.After(now)) {
			return out, ErrInvalid
		}
		fresh := 0.0
		confidence := 0.0
		if ok && e.Samples > 0 {
			fresh = math.Exp2(-float64(now.Sub(e.Updated)) / float64(p.HalfLife))
			confidence = math.Min(1, float64(e.Samples)/float64(p.MinSamples)) * fresh
		}
		// Shrink stale or sparse outcome estimates toward neutral priors. Cost
		// and latency use fixed scales, so adding a candidate cannot change a
		// different candidate's score through pool-relative normalization.
		shrink := func(v float64) float64 { return .5 + confidence*(v-.5) }
		latency := 1 / (1 + float64(e.Latency)/float64(p.LatencyScale))
		cost := 1 / (1 + e.Cost/p.CostScale)
		score := w.Quality*shrink(e.Quality) + w.Compliance*shrink(e.Compliance) + w.Reliability*shrink(e.Reliability) + w.Latency*shrink(latency) + w.Cost*shrink(cost) + w.Recency*fresh + w.Uncertainty*confidence
		out.Ranked = append(out.Ranked, Ranked{c.Model, c.Provider, c.FailureDomain, score, confidence, e.Samples})
	}
	sort.Slice(out.Excluded, func(i, j int) bool {
		a, b := out.Excluded[i], out.Excluded[j]
		if a.Model == b.Model {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	sort.Slice(out.Ranked, func(i, j int) bool {
		a, b := out.Ranked[i], out.Ranked[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Model == b.Model {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	if len(out.Ranked) == 0 {
		return out, ErrNoRoute
	}
	selected := 0
	if p.Exploration > 0 && draw < p.Exploration && len(out.Ranked) > 1 {
		// Uniform exploration among eligible models gives every cold-start
		// candidate nonzero probability without overriding hard constraints.
		selected = int(draw / p.Exploration * float64(len(out.Ranked)))
		out.Explored = true
	}
	out.Primary = out.Ranked[selected]
	// Prefer fallbacks with a known, different infrastructure failure domain.
	for _, different := range []bool{true, false} {
		for i, c := range out.Ranked {
			if i == selected {
				continue
			}
			diverse := c.FailureDomain != "" && out.Primary.FailureDomain != "" && c.FailureDomain != out.Primary.FailureDomain
			if diverse == different {
				out.Fallbacks = append(out.Fallbacks, c)
			}
		}
	}
	return out, nil
}
func nonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func unit(v float64) bool        { return nonnegative(v) && v <= 1 }
