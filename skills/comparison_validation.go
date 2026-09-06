package skills

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func (p ComparisonPolicy) Validate() error {
	if !p.Key.valid() || !comparisonHex(p.BaselineVersion, 32) || !comparisonHex(p.CandidateVersion, 32) || p.BaselineVersion == p.CandidateVersion || p.MinSamples < 20 || p.MinSamples > 100 || math.IsNaN(p.MinDrop) || math.IsInf(p.MinDrop, 0) || p.MinDrop <= 0 || p.MinDrop > 1 {
		return ErrInvalid
	}
	for _, s := range []string{p.Execution.Model, p.Execution.Provider, p.Execution.Domain, p.Execution.Profile} {
		if !comparisonIdentity(s) {
			return ErrInvalid
		}
	}
	if p.Source == evaluation.UserFeedback {
		return nil
	}
	if p.Source != evaluation.Deterministic && p.Source != evaluation.ToolResult {
		return ErrInvalid
	}
	switch p.Execution.Domain {
	case "code", "coding", "debugging", "math", "structured_json":
		return nil
	}
	return ErrInvalid
}

func (r ComparisonRequest) Validate() error {
	if r.Version != 1 || !identifier.MatchString(r.ModelID) || len(r.Tasks) < 1 || len(r.Tasks) > 200 {
		return ErrInvalid
	}
	p := ComparisonPolicy{Key: Key{Scope: "request", Name: r.Name}, BaselineVersion: r.BaselineVersion, CandidateVersion: r.CandidateVersion, Execution: routing.Key{Model: r.ModelID, Provider: "configured", Domain: r.Domain, Profile: r.Profile}, Source: r.Source, MinSamples: r.MinSamples, MinDrop: r.MinDrop}
	if p.Validate() != nil {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, task := range r.Tasks {
		if !sessions.ValidEventPageID(task) || seen[task] {
			return ErrInvalid
		}
		seen[task] = true
	}
	return nil
}

func (r ComparisonReport) Validate() error {
	if r.ConfiguredModelID != "" && !identifier.MatchString(r.ConfiguredModelID) {
		return ErrInvalid
	}
	if r.Version != 1 || r.Policy.Validate() != nil || r.Sampled < 1 || r.Sampled > 200 || r.Excluded == nil || len(r.Excluded) > 9 || !r.AdvisoryOnly || r.Method != "wilson_95_separation_v1" || !comparisonHex(r.EvidenceDigest, 64) || r.Baseline.Version != r.Policy.BaselineVersion || r.Candidate.Version != r.Policy.CandidateVersion {
		return ErrInvalid
	}
	total := 0
	for code, n := range r.Excluded {
		switch code {
		case "repeated_session", "related_task", "nonfinal_outcome", "unknown_attribution", "ambiguous_attribution", "other_skill", "other_execution", "unknown_quality", "other_source":
		default:
			return ErrInvalid
		}
		if n < 1 || n > r.Sampled {
			return ErrInvalid
		}
		total += n
	}
	for _, c := range []ComparisonCohort{r.Baseline, r.Candidate} {
		if c.Samples < 0 || c.Samples > r.Sampled || c.Accepted < 0 || c.Accepted > c.Samples || c.Digest != "" && !comparisonHex(c.Digest, 64) || c.Samples > 0 && c.Digest == "" {
			return ErrInvalid
		}
		want := c
		setComparisonInterval(&want)
		if c.Rate != want.Rate || c.Lower != want.Lower || c.Upper != want.Upper {
			return ErrInvalid
		}
		total += c.Samples
	}
	if total != r.Sampled || r.Status != comparisonStatus(r) {
		return ErrInvalid
	}
	return nil
}

func comparisonIdentity(s string) bool {
	return s != "" && len(s) <= 1024 && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
func comparisonHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func setComparisonInterval(c *ComparisonCohort) {
	c.Rate, c.Lower, c.Upper = 0, 0, 1
	if c.Samples == 0 {
		return
	}
	// Wilson score formula: NIST Engineering Statistics Handbook, 7.2.4.1.
	const z = 1.959963984540054
	n := float64(c.Samples)
	p := float64(c.Accepted) / n
	z2 := z * z
	den := 1 + z2/n
	center := (p + z2/(2*n)) / den
	half := z * math.Sqrt(p*(1-p)/n+z2/(4*n*n)) / den
	c.Rate = p
	c.Lower = math.Max(0, center-half)
	c.Upper = math.Min(1, center+half)
}
func comparisonStatus(r ComparisonReport) string {
	if r.Baseline.Samples < r.Policy.MinSamples || r.Candidate.Samples < r.Policy.MinSamples {
		return "insufficient_evidence"
	}
	if r.Baseline.Lower-r.Candidate.Upper >= r.Policy.MinDrop {
		return "regression_signal"
	}
	return "no_regression_signal"
}
