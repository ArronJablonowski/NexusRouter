package skills

import (
	"errors"
	"math"
	"math/big"
	"time"
)

var ErrGenerationBudget = errors.New("skill generation budget denied")

// GenerationBudget reserves estimated cost, not actual provider billing. Failed
// attempts are not refunded; uncertain started attempts occupy an in-flight
// slot until a terminal outcome is durably recorded. Limits are scope-wide,
// except cooldown, which applies to the workflow name within that scope.
type GenerationBudget struct {
	Version     int           `json:"version"`
	Window      time.Duration `json:"window"`
	MaxCost     float64       `json:"max_cost"`
	MaxAttempts int           `json:"max_attempts"`
	MaxInFlight int           `json:"max_in_flight"`
	Cooldown    time.Duration `json:"cooldown"`
}

func (b GenerationBudget) Validate() error {
	if b.Version != 1 || b.Window < time.Minute || b.Window > 30*24*time.Hour || math.IsNaN(b.MaxCost) || math.IsInf(b.MaxCost, 0) || b.MaxCost < 0 || b.MaxAttempts < 1 || b.MaxAttempts > 1000 || b.MaxInFlight < 1 || b.MaxInFlight > b.MaxAttempts || b.Cooldown < 0 || b.Cooldown > b.Window {
		return ErrGenerationBudget
	}
	return nil
}

// Check is pure admission over host-provided complete scope history. Storage
// must obtain that history and claim next atomically; this does not grant a
// reservation by itself. Future-clock records remain conservatively chargeable.
func (b GenerationBudget) Check(next GenerationAttempt, history []GenerationAttempt, now time.Time) error {
	if b.Validate() != nil || next.Validate() != nil || next.Status != "started" || !generationTime(now) || next.StartedAt.After(now) || next.StartedAt.Before(now.Add(-30*time.Second)) || len(history) > 1000 {
		return ErrGenerationBudget
	}
	seen := map[string]bool{}
	count, inflight := 1, 1
	cost := new(big.Rat).SetFloat64(next.EstimatedCost)
	ceiling := new(big.Rat).SetFloat64(b.MaxCost)
	for _, attempt := range history {
		if attempt.Validate() != nil || attempt.Key.Scope != next.Key.Scope || attempt.ID == next.ID || seen[attempt.ID] {
			return ErrGenerationBudget
		}
		seen[attempt.ID] = true
		if attempt.Status == "started" {
			inflight++
		}
		if !attempt.StartedAt.Before(now.Add(-b.Window)) {
			count++
			// Exact rational arithmetic over supplied float values prevents both
			// rounding-away small reservations and epsilon-discount overspend.
			cost.Add(cost, new(big.Rat).SetFloat64(attempt.EstimatedCost))
			if cost.Cmp(ceiling) > 0 {
				return ErrGenerationBudget
			}
		}
		if attempt.Key.Name == next.Key.Name && attempt.StartedAt.Add(b.Cooldown).After(now) {
			return ErrGenerationBudget
		}
	}
	if count > b.MaxAttempts || inflight > b.MaxInFlight || cost.Cmp(ceiling) > 0 {
		return ErrGenerationBudget
	}
	return nil
}
