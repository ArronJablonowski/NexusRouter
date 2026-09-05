package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"

	"github.com/ArronJablonowski/DarwinRouter/routing"
)

var ErrDeprecation = errors.New("invalid deprecation evidence or policy")

type DeprecationPolicy struct {
	Window           int     `json:"window"`
	MinSamples       int     `json:"min_samples"`
	FailureThreshold float64 `json:"failure_threshold"`
}

func (p DeprecationPolicy) Validate() error {
	if p.Window < 1 || p.Window > 1000 || p.MinSamples < 1 || p.MinSamples > p.Window || math.IsNaN(p.FailureThreshold) || math.IsInf(p.FailureThreshold, 0) || p.FailureThreshold <= 0 || p.FailureThreshold > 1 {
		return ErrDeprecation
	}
	return nil
}

// DeprecationReport is a recommendation, never permission to disable/uninstall
// a model. Resource estimates describe configured requirements, not confirmed
// reclaimable or freed memory. No source text or evidence identifiers escape.
type DeprecationReport struct {
	Population         string            `json:"population"`
	Version            int               `json:"version"`
	Key                routing.Key       `json:"key"`
	Policy             DeprecationPolicy `json:"policy"`
	ConfiguredModelID  string            `json:"configured_model_id,omitempty"`
	EstimatedRAMBytes  uint64            `json:"estimated_ram_bytes,omitempty"`
	EstimatedVRAMBytes uint64            `json:"estimated_vram_bytes,omitempty"`
	Sampled            int               `json:"sampled"`
	EligibleSamples    int               `json:"eligible_samples"`
	ExcludedJudgeOnly  int               `json:"excluded_judge_only"`
	ExecutionFailures  int               `json:"execution_failures"`
	QualityFailures    int               `json:"quality_failures"`
	SchemaFailures     int               `json:"schema_failures"`
	Failures           int               `json:"failures"`
	FailureRate        float64           `json:"failure_rate"`
	Candidate          bool              `json:"candidate"`
	Reason             string            `json:"reason"`
	ApprovalRequired   bool              `json:"approval_required"`
	EvidenceDigest     string            `json:"evidence_digest"`
}

// SummarizeDeprecation accepts one current revision per base task/attempt from
// one already-selected bounded window. It does not choose chronology or follow
// revisions. Execution failures count independently; judge-only successful
// executions cannot trigger deprecation, even when their judge rejects quality.
func SummarizeDeprecation(records []Record, p DeprecationPolicy) (DeprecationReport, error) {
	bad := func() (DeprecationReport, error) { return DeprecationReport{}, ErrDeprecation }
	if p.Validate() != nil || len(records) > p.Window {
		return bad()
	}
	out := DeprecationReport{Version: 1, Population: "evaluated_attempts", Policy: p, Sampled: len(records), ApprovalRequired: true, Reason: "insufficient_evidence"}
	seen := map[[2]string]bool{}
	owned := make([]Record, 0, len(records))
	for _, record := range records {
		if record.Validate() != nil {
			return bad()
		}
		identity := [2]string{record.TaskID, record.AttemptID}
		if seen[identity] {
			return bad()
		}
		seen[identity] = true
		if len(owned) == 0 {
			out.Key = record.Key
		} else if record.Key != out.Key {
			return bad()
		}
		outcome, err := Resolve(record.Checks, false)
		authoritative := err == nil
		schemaFailure := record.SchemaPassed != nil && !*record.SchemaPassed
		if !authoritative && record.ExecutionSucceeded && !schemaFailure {
			out.ExcludedJudgeOnly++
		} else {
			out.EligibleSamples++
			executionFailure := !record.ExecutionSucceeded
			qualityFailure := authoritative && !outcome.Accepted
			if executionFailure {
				out.ExecutionFailures++
			}
			if qualityFailure {
				out.QualityFailures++
			}
			if schemaFailure {
				out.SchemaFailures++
			}
			if executionFailure || qualityFailure || schemaFailure {
				out.Failures++
			}
		}
		copy := record
		copy.Checks = append([]Check(nil), record.Checks...)
		sort.Slice(copy.Checks, func(i, j int) bool {
			a, b := copy.Checks[i], copy.Checks[j]
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			return a.Reference < b.Reference
		})
		if record.SchemaPassed != nil {
			value := *record.SchemaPassed
			copy.SchemaPassed = &value
		}
		copy.Time = copy.Time.UTC()
		owned = append(owned, copy)
	}
	sort.Slice(owned, func(i, j int) bool {
		if owned[i].TaskID != owned[j].TaskID {
			return owned[i].TaskID < owned[j].TaskID
		}
		return owned[i].AttemptID < owned[j].AttemptID
	})
	body, err := json.Marshal(owned)
	if err != nil {
		return bad()
	}
	digest := sha256.Sum256(body)
	out.EvidenceDigest = hex.EncodeToString(digest[:])
	if out.EligibleSamples > 0 {
		out.FailureRate = float64(out.Failures) / float64(out.EligibleSamples)
	}
	if out.EligibleSamples >= p.MinSamples {
		out.Reason = "below_threshold"
		if out.FailureRate > p.FailureThreshold {
			out.Candidate = true
			out.Reason = "failure_threshold"
		}
	}
	return out, nil
}
