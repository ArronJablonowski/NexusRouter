package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type ComparisonPolicy struct {
	Key              Key               `json:"key"`
	BaselineVersion  string            `json:"baseline_version"`
	CandidateVersion string            `json:"candidate_version"`
	Execution        routing.Key       `json:"execution"`
	Source           evaluation.Source `json:"source"`
	MinSamples       int               `json:"min_samples"`
	MinDrop          float64           `json:"min_drop"`
}

type ComparisonRequest struct {
	Version          int               `json:"version"`
	ModelID          string            `json:"model_id"`
	Domain           string            `json:"domain"`
	Profile          string            `json:"profile"`
	Name             string            `json:"name"`
	BaselineVersion  string            `json:"baseline_version"`
	CandidateVersion string            `json:"candidate_version"`
	Source           evaluation.Source `json:"source"`
	MinSamples       int               `json:"min_samples"`
	MinDrop          float64           `json:"min_drop"`
	Tasks            []string          `json:"tasks"`
}

type ComparisonCohort struct {
	Version  string  `json:"version"`
	Digest   string  `json:"digest"`
	Samples  int     `json:"samples"`
	Accepted int     `json:"accepted"`
	Rate     float64 `json:"rate"`
	Lower    float64 `json:"lower"`
	Upper    float64 `json:"upper"`
}

// ComparisonReport is an advisory observation, not causal evidence or authority
// to mutate a skill. Wilson bounds are approximate per-cohort 95% intervals,
// not joint coverage or protection against repeated/selective testing.
type ComparisonReport struct {
	// ConfiguredModelID is bound by application adapters; the pure comparator
	// knows only the actual execution key and leaves it empty.
	ConfiguredModelID string           `json:"configured_model_id,omitempty"`
	Version           int              `json:"version"`
	Policy            ComparisonPolicy `json:"policy"`
	Sampled           int              `json:"sampled"`
	Excluded          map[string]int   `json:"excluded"`
	Baseline          ComparisonCohort `json:"baseline"`
	Candidate         ComparisonCohort `json:"candidate"`
	Status            string           `json:"status"`
	AdvisoryOnly      bool             `json:"advisory_only"`
	Method            string           `json:"method"`
	EvidenceDigest    string           `json:"evidence_digest"`
}

// CompareTaskOutcomes never reads stores or grants validator authority. Inputs
// must be trusted projections, not model-authored declarations of skill use.
func CompareTaskOutcomes(input []TaskOutcome, policy ComparisonPolicy) (ComparisonReport, error) {
	return CompareTaskOutcomesWithCorrelatedSessions(input, policy, nil)
}

// CompareTaskOutcomesWithCorrelatedSessions accepts trusted store observations
// of selected sessions having sibling tasks outside the selected window. It
// never infers session independence from a truncated window alone.
func CompareTaskOutcomesWithCorrelatedSessions(input []TaskOutcome, policy ComparisonPolicy, correlated []string) (ComparisonReport, error) {
	if policy.Validate() != nil || len(input) < 1 || len(input) > 200 {
		return ComparisonReport{}, ErrInvalid
	}
	tasks, sessionCounts := map[string]bool{}, map[string]int{}
	digests := map[string]string{}
	for _, o := range input {
		if o.Validate() != nil || tasks[o.TaskID] {
			return ComparisonReport{}, ErrInvalid
		}
		tasks[o.TaskID] = true
		sessionCounts[o.SessionID]++
		if o.SkillContext != nil {
			for _, ref := range o.SkillContext.References {
				if ref.Scope != policy.Key.Scope || ref.Name != policy.Key.Name || ref.Version != policy.BaselineVersion && ref.Version != policy.CandidateVersion {
					continue
				}
				if prior := digests[ref.Version]; prior != "" && prior != ref.Digest {
					return ComparisonReport{}, ErrInvalid
				}
				digests[ref.Version] = ref.Digest
			}
		}
	}
	if len(correlated) > len(input) {
		return ComparisonReport{}, ErrInvalid
	}
	correlated = append([]string(nil), correlated...)
	sort.Strings(correlated)
	for i, id := range correlated {
		if !sessions.ValidEventPageID(id) || sessionCounts[id] == 0 || i > 0 && correlated[i-1] == id {
			return ComparisonReport{}, ErrInvalid
		}
		if sessionCounts[id] < 2 {
			sessionCounts[id] = 2
		}
	}
	ordered := append([]TaskOutcome(nil), input...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].TaskID < ordered[j].TaskID })
	body, err := json.Marshal(struct {
		Policy             ComparisonPolicy `json:"policy"`
		Outcomes           []TaskOutcome    `json:"outcomes"`
		CorrelatedSessions []string         `json:"correlated_sessions,omitempty"`
	}{policy, ordered, correlated})
	if err != nil {
		return ComparisonReport{}, ErrInvalid
	}
	sum := sha256.Sum256(body)
	report := ComparisonReport{Version: 1, Policy: policy, Sampled: len(input), Excluded: map[string]int{}, Baseline: ComparisonCohort{Version: policy.BaselineVersion, Digest: digests[policy.BaselineVersion]}, Candidate: ComparisonCohort{Version: policy.CandidateVersion, Digest: digests[policy.CandidateVersion]}, AdvisoryOnly: true, Method: "wilson_95_separation_v1", EvidenceDigest: hex.EncodeToString(sum[:])}
	for _, o := range ordered {
		reason := comparisonExclusion(o, policy, sessionCounts[o.SessionID])
		if reason != "" {
			report.Excluded[reason]++
			continue
		}
		cohort := &report.Candidate
		if o.SkillContext.References[0].Version == policy.BaselineVersion {
			cohort = &report.Baseline
		}
		cohort.Samples++
		if o.Quality.Accepted {
			cohort.Accepted++
		}
	}
	setComparisonInterval(&report.Baseline)
	setComparisonInterval(&report.Candidate)
	report.Status = comparisonStatus(report)
	if report.Validate() != nil {
		return ComparisonReport{}, ErrInvalid
	}
	return report, nil
}

func comparisonExclusion(o TaskOutcome, p ComparisonPolicy, sessionCount int) string {
	if sessionCount > 1 {
		return "repeated_session"
	}
	if o.ParentTaskID != "" || o.RetryOfTaskID != "" {
		return "related_task"
	}
	if o.State == "running" || o.State == "canceled" {
		return "nonfinal_outcome"
	}
	if o.SkillContext == nil || !o.SkillContext.Complete {
		return "unknown_attribution"
	}
	if len(o.SkillContext.References) != 1 {
		return "ambiguous_attribution"
	}
	ref := o.SkillContext.References[0]
	if ref.Scope != p.Key.Scope || ref.Name != p.Key.Name || ref.Version != p.BaselineVersion && ref.Version != p.CandidateVersion {
		return "other_skill"
	}
	if o.Key == nil || *o.Key != p.Execution {
		return "other_execution"
	}
	if o.Quality == nil {
		return "unknown_quality"
	}
	if o.Quality.Source != p.Source {
		return "other_source"
	}
	return ""
}
