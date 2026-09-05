package skills

import (
	"math"
	"testing"
	"time"
)

func budgetFixture() GenerationBudget {
	return GenerationBudget{Version: 1, Window: time.Hour, MaxCost: 1, MaxAttempts: 4, MaxInFlight: 2, Cooldown: time.Minute}
}
func budgetAttempt(id, status string, at time.Time, cost float64) GenerationAttempt {
	a := generationAttemptFixture(status)
	a.ID = id
	a.StartedAt = at
	a.EstimatedCost = cost
	if status != "started" {
		a.FinishedAt = at.Add(time.Second)
	}
	return a
}

func TestGenerationBudgetValidationBoundaries(t *testing.T) {
	for _, mode := range []string{"version", "short", "long", "negative-cost", "nan", "inf", "no-attempts", "many-attempts", "no-flight", "many-flight", "negative-cooldown", "long-cooldown"} {
		b := budgetFixture()
		switch mode {
		case "version":
			b.Version = 2
		case "short":
			b.Window = time.Minute - 1
		case "long":
			b.Window = 30*24*time.Hour + 1
		case "negative-cost":
			b.MaxCost = -1
		case "nan":
			b.MaxCost = math.NaN()
		case "inf":
			b.MaxCost = math.Inf(1)
		case "no-attempts":
			b.MaxAttempts = 0
		case "many-attempts":
			b.MaxAttempts = 1001
		case "no-flight":
			b.MaxInFlight = 0
		case "many-flight":
			b.MaxInFlight = 5
		case "negative-cooldown":
			b.Cooldown = -1
		case "long-cooldown":
			b.Cooldown = b.Window + 1
		}
		if b.Validate() == nil {
			t.Fatal("invalid budget", mode)
		}
	}
	for _, window := range []time.Duration{time.Minute, 30 * 24 * time.Hour} {
		b := GenerationBudget{Version: 1, Window: window, MaxAttempts: 1000, MaxInFlight: 1000, Cooldown: window}
		if b.Validate() != nil {
			t.Fatal("boundary denied")
		}
	}
}

func TestGenerationBudgetReservationsCooldownAndClock(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	next := budgetAttempt("next", "started", now, .2)
	for _, test := range []struct {
		name    string
		history []GenerationAttempt
		allowed bool
	}{
		{"empty", nil, true},
		{"failed-charged", []GenerationAttempt{budgetAttempt("old", "failed", now.Add(-time.Minute), .9)}, false},
		{"drafted-charged", []GenerationAttempt{budgetAttempt("old", "drafted", now.Add(-time.Minute), .9)}, false},
		{"window-edge", []GenerationAttempt{budgetAttempt("old", "failed", now.Add(-time.Hour), .9)}, false},
		{"outside-window", []GenerationAttempt{budgetAttempt("old", "failed", now.Add(-time.Hour-1), .9)}, true},
		{"old-uncertain", []GenerationAttempt{budgetAttempt("old-a", "started", now.Add(-2*time.Hour), 0), budgetAttempt("old-b", "started", now.Add(-2*time.Hour), 0)}, false},
		{"cooldown", []GenerationAttempt{budgetAttempt("old", "failed", now.Add(-time.Minute+1), 0)}, false},
		{"cooldown-boundary", []GenerationAttempt{budgetAttempt("old", "failed", now.Add(-time.Minute), 0)}, true},
		{"future-clock", []GenerationAttempt{budgetAttempt("old", "failed", now.Add(time.Hour), 0)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := budgetFixture().Check(next, test.history, now)
			if (err == nil) != test.allowed {
				t.Fatal(err)
			}
		})
	}
	b := budgetFixture()
	b.Cooldown = 0
	future := budgetAttempt("future", "failed", now.Add(time.Hour), .9)
	future.Key.Name = "other"
	future.Result = nil
	if b.Check(next, []GenerationAttempt{future}, now) == nil {
		t.Fatal("future different workflow cost ignored")
	}
	future.EstimatedCost = 0
	future.Key.Name = next.Key.Name
	if b.Check(next, []GenerationAttempt{future}, now) == nil {
		t.Fatal("future timestamp bypassed zero cooldown")
	}
}

func TestGenerationBudgetCountScopeAndMalformedHistory(t *testing.T) {
	now := time.Now().UTC()
	next := budgetAttempt("next", "started", now, 0)
	b := budgetFixture()
	b.Cooldown = 0
	history := []GenerationAttempt{}
	for _, id := range []string{"a", "b", "c", "d"} {
		history = append(history, budgetAttempt(id, "failed", now.Add(-time.Minute), 0))
	}
	if b.Check(next, history, now) == nil {
		t.Fatal("failed attempts not counted")
	}
	for _, mode := range []string{"duplicate", "same-id", "scope", "invalid", "oversized", "old-next", "future-next", "terminal-next", "clock-offset"} {
		n := next
		h := []GenerationAttempt{budgetAttempt("old", "failed", now.Add(-time.Minute), 0)}
		clock := now
		switch mode {
		case "duplicate":
			h = append(h, h[0])
		case "same-id":
			h[0].ID = n.ID
		case "scope":
			h[0].Key.Scope = "other"
		case "invalid":
			h[0].EstimatedCost = math.NaN()
		case "oversized":
			h = make([]GenerationAttempt, 1001)
		case "old-next":
			n.StartedAt = now.Add(-30*time.Second - 1)
		case "future-next":
			n.StartedAt = now.Add(1)
		case "terminal-next":
			n = budgetAttempt("next", "failed", now, 0)
		case "clock-offset":
			clock = now.In(time.FixedZone("offset", 3600))
		}
		if b.Check(n, h, clock) == nil {
			t.Fatal("invalid admission", mode)
		}
	}
	next.StartedAt = now.Add(-30 * time.Second)
	if b.Check(next, nil, now) != nil {
		t.Fatal("valid start boundary denied")
	}
}

func TestGenerationBudgetConservativeFloatCeiling(t *testing.T) {
	now := time.Now().UTC()
	b := budgetFixture()
	b.MaxCost = .3
	b.Cooldown = 0
	next := budgetAttempt("next", "started", now, .1)
	old := budgetAttempt("old", "failed", now.Add(-time.Minute), .2)
	if b.Check(next, []GenerationAttempt{old}, now) == nil {
		t.Fatal("rounding epsilon overspend allowed")
	}
	b.MaxCost = math.MaxFloat64
	next.EstimatedCost = math.MaxFloat64
	old.EstimatedCost = math.MaxFloat64
	if b.Check(next, []GenerationAttempt{old}, now) == nil {
		t.Fatal("overflow accepted")
	}
	old.EstimatedCost = math.SmallestNonzeroFloat64
	if b.Check(next, []GenerationAttempt{old}, now) == nil {
		t.Fatal("small positive reservation rounded away")
	}
}
