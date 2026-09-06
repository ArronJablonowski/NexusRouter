package skills

import (
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// ComparisonSources identifies the exact observed tasks, not a query to rerun.
// Nil Sources on older reports means unrecorded; a nonnil empty Tasks is known empty.
type ComparisonSources struct {
	Version int      `json:"version"`
	Tasks   []string `json:"tasks"`
}

func (s ComparisonSources) Validate() error {
	if s.Version != 1 || s.Tasks == nil || len(s.Tasks) > 200 {
		return ErrInvalid
	}
	for i, id := range s.Tasks {
		if !sessions.ValidEventPageID(id) || i > 0 && s.Tasks[i-1] >= id {
			return ErrInvalid
		}
	}
	return nil
}

// ComparisonSelectionRequest selects an outcome-independent bounded historical
// window; it does not ask a model to nominate successful examples.
type ComparisonSelectionRequest struct {
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
	Privacy          string            `json:"privacy"`
	TasksPerVersion  int               `json:"tasks_per_version"`
}

type ComparisonSelectionPolicy struct {
	Version         int              `json:"version"`
	Comparison      ComparisonPolicy `json:"comparison"`
	Privacy         string           `json:"privacy"`
	TasksPerVersion int              `json:"tasks_per_version"`
}

type ComparisonWindow struct {
	Selected      int   `json:"selected"`
	HasMore       bool  `json:"has_more"`
	OldestOrdinal int64 `json:"oldest_ordinal"`
	NewestOrdinal int64 `json:"newest_ordinal"`
}

// ComparisonSelectionReport describes a latest-window observation, never a
// complete-population or causal claim. Empty selection has no comparison.
type ComparisonSelectionReport struct {
	Version           int                       `json:"version"`
	ConfiguredModelID string                    `json:"configured_model_id,omitempty"`
	Policy            ComparisonSelectionPolicy `json:"policy"`
	Watermark         int64                     `json:"watermark"`
	Baseline          ComparisonWindow          `json:"baseline"`
	Candidate         ComparisonWindow          `json:"candidate"`
	Comparison        *ComparisonReport         `json:"comparison,omitempty"`
	Sources           *ComparisonSources        `json:"sources,omitempty"`
}

func (r ComparisonSelectionRequest) Validate() error {
	if !comparisonSelectionPrivacy(r.Privacy) || r.TasksPerVersion < 20 || r.TasksPerVersion > 100 {
		return ErrInvalid
	}
	return (ComparisonRequest{Version: r.Version, ModelID: r.ModelID, Domain: r.Domain, Profile: r.Profile, Name: r.Name, BaselineVersion: r.BaselineVersion, CandidateVersion: r.CandidateVersion, Source: r.Source, MinSamples: r.MinSamples, MinDrop: r.MinDrop, Tasks: []string{"selection"}}).Validate()
}

func (p ComparisonSelectionPolicy) Validate() error {
	if p.Version != 1 || p.Comparison.Validate() != nil || !comparisonSelectionPrivacy(p.Privacy) || p.TasksPerVersion < 20 || p.TasksPerVersion > 100 {
		return ErrInvalid
	}
	return nil
}

func (r ComparisonSelectionReport) Validate() error {
	if r.Version != 1 || r.Policy.Validate() != nil || r.Watermark < 0 || r.ConfiguredModelID != "" && !identifier.MatchString(r.ConfiguredModelID) {
		return ErrInvalid
	}
	total := 0
	for _, w := range []ComparisonWindow{r.Baseline, r.Candidate} {
		if w.Selected < 0 || w.Selected > r.Policy.TasksPerVersion || w.HasMore && w.Selected != r.Policy.TasksPerVersion {
			return ErrInvalid
		}
		if w.Selected == 0 {
			if w.HasMore || w.OldestOrdinal != 0 || w.NewestOrdinal != 0 {
				return ErrInvalid
			}
		} else {
			if w.OldestOrdinal < 1 || w.NewestOrdinal < w.OldestOrdinal || w.NewestOrdinal > r.Watermark || int64(w.Selected-1) > w.NewestOrdinal-w.OldestOrdinal || w.Selected == 1 && w.OldestOrdinal != w.NewestOrdinal {
				return ErrInvalid
			}
		}
		total += w.Selected
	}
	if r.Sources != nil && (r.Sources.Validate() != nil || len(r.Sources.Tasks) != total) {
		return ErrInvalid
	}
	if total == 0 {
		if r.Comparison != nil {
			return ErrInvalid
		}
		return nil
	}
	if r.Comparison == nil || r.Comparison.Validate() != nil || r.Comparison.Policy != r.Policy.Comparison || r.Comparison.Sampled != total || r.Comparison.ConfiguredModelID != r.ConfiguredModelID || r.Comparison.Baseline.Samples > r.Baseline.Selected || r.Comparison.Candidate.Samples > r.Candidate.Selected {
		return ErrInvalid
	}
	return nil
}

func comparisonSelectionPrivacy(s string) bool { return s == "local_only" || s == "cloud_allowed" }
