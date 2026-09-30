package evaluation

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/routing"
)

type DeprecationRequest struct {
	Version int               `json:"version"`
	ModelID string            `json:"model_id"`
	Domain  string            `json:"domain"`
	Profile string            `json:"profile"`
	Policy  DeprecationPolicy `json:"policy"`
}

func deprecationLabel(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func (r DeprecationRequest) Validate() error {
	if r.Version != 1 || !deprecationLabel(r.ModelID) || !deprecationLabel(r.Domain) || !deprecationLabel(r.Profile) || r.Policy.Validate() != nil {
		return ErrDeprecation
	}
	return nil
}

// Validate verifies transport metadata consistency, not the truth of the
// underlying evidence. The digest is a binding, not proof of trusted provenance.
func (r DeprecationReport) Validate() error {
	if r.Version != 1 || r.Population != "evaluated_attempts" || r.Policy.Validate() != nil || !r.ApprovalRequired || len(r.EvidenceDigest) != 64 {
		return ErrDeprecation
	}
	for _, ch := range r.EvidenceDigest {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return ErrDeprecation
		}
	}
	if r.Sampled < 0 || r.Sampled > r.Policy.Window || r.EligibleSamples < 0 || r.EligibleSamples > r.Sampled || r.ExcludedJudgeOnly < 0 || r.ExcludedWithdrawn < 0 || r.ExcludedJudgeOnly+r.ExcludedWithdrawn != r.Sampled-r.EligibleSamples {
		return ErrDeprecation
	}
	if r.Failures < 0 || r.Failures > r.EligibleSamples {
		return ErrDeprecation
	}
	for _, count := range []int{r.ExecutionFailures, r.QualityFailures, r.SchemaFailures} {
		if count < 0 || count > r.Failures {
			return ErrDeprecation
		}
	}
	if r.Failures > r.ExecutionFailures+r.QualityFailures+r.SchemaFailures {
		return ErrDeprecation
	}
	rate := 0.0
	if r.EligibleSamples > 0 {
		rate = float64(r.Failures) / float64(r.EligibleSamples)
	}
	if math.IsNaN(r.FailureRate) || math.IsInf(r.FailureRate, 0) || r.FailureRate != rate {
		return ErrDeprecation
	}
	reason, candidate := "insufficient_evidence", false
	if r.EligibleSamples >= r.Policy.MinSamples {
		reason = "below_threshold"
		if rate > r.Policy.FailureThreshold {
			reason, candidate = "failure_threshold", true
		}
	}
	if r.Reason != reason || r.Candidate != candidate {
		return ErrDeprecation
	}
	if r.ConfiguredModelID != "" && !deprecationLabel(r.ConfiguredModelID) {
		return ErrDeprecation
	}
	if r.Key == (routing.Key{}) {
		if r.Sampled != 0 || r.ConfiguredModelID != "" {
			return ErrDeprecation
		}
	} else {
		for _, value := range []string{r.Key.Model, r.Key.Provider, r.Key.Domain, r.Key.Profile} {
			if !deprecationLabel(value) {
				return ErrDeprecation
			}
		}
	}
	return nil
}
