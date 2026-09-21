// Package evaluation resolves trusted validation evidence, not model self-ratings.
package evaluation

import (
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/routing"
)

type Source string

const (
	Deterministic Source = "deterministic"
	ToolResult    Source = "tool_result"
	UserFeedback  Source = "user_feedback"
	LLMJudge      Source = "llm_judge"
)

// Check must be supplied by an authorized evaluator or feedback adapter. Model
// output cannot create these records directly. Reference is an opaque evidence
// identifier, never raw output, test logs, secrets or a self-assessment.
type Check struct {
	Source    Source
	Reference string
	Passed    bool
}
type Outcome struct {
	Source     Source
	Accepted   bool
	References []string
}

var ErrEvidence = errors.New("invalid or missing evaluation evidence")

func Resolve(checks []Check, allowJudge bool) (Outcome, error) {
	best := 5
	out := Outcome{Accepted: true}
	seen := map[string]bool{}
	for _, c := range checks {
		if c.Reference == "" || len(c.Reference) > 128 || seen[c.Reference] {
			return Outcome{}, ErrEvidence
		}
		seen[c.Reference] = true
		rank := 0
		switch c.Source {
		case Deterministic:
			rank = 1
		case ToolResult:
			rank = 2
		case UserFeedback:
			rank = 3
		case LLMJudge:
			if !allowJudge {
				continue
			}
			rank = 4
		default:
			return Outcome{}, ErrEvidence
		}
		if rank < best {
			best = rank
			out = Outcome{Source: c.Source, Accepted: true}
		}
		if rank == best {
			out.Accepted = out.Accepted && c.Passed
			out.References = append(out.References, c.Reference)
		}
	}
	if best == 5 {
		return Outcome{}, ErrEvidence
	}
	return out, nil
}

type Record struct {
	Version               int
	ID, TaskID, AttemptID string
	Key                   routing.Key
	Checks                []Check
	AllowJudge            bool
	SchemaPassed          *bool
	ExecutionSucceeded    bool
	Latency               time.Duration
	ContextTokens         int
	TimedOut              bool
	ProviderError         bool
	PeakMemoryBytes       uint64
	SwapGrowthBytes       uint64
	Cost                  float64
	Time                  time.Time
}

func (r Record) Validate() error {
	if r.Version != 1 || r.ID == "" || r.TaskID == "" || r.AttemptID == "" || r.Key.Model == "" || r.Key.Provider == "" || r.Key.Domain == "" || r.Key.Profile == "" || r.Latency < 0 || r.ContextTokens < 0 || math.IsNaN(r.Cost) || math.IsInf(r.Cost, 0) || r.Cost < 0 || r.Time.IsZero() || len(r.Checks) > 100 {
		return ErrEvidence
	}
	_, err := Resolve(r.Checks, r.AllowJudge)
	return err
}
