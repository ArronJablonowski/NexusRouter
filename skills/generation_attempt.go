package skills

import (
	"encoding/hex"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// GenerationAttempt records inference, not activation or publication into a
// SkillStore. A started attempt after restart is uncertain and must not be
// automatically redispatched. Drafted retains the proposal before publication.
// Hosts bind InputDigest to their admitted, redacted source snapshot and policy;
// syntactic validation here cannot prove source attribution or success evidence.
type GenerationAttempt struct {
	Version                        int
	ID                             string
	Key                            Key
	Model, Provider, InputDigest   string
	SourceSessions, SourceEvidence []string
	Status, Code                   string
	EstimatedCost                  float64
	StartedAt, FinishedAt          time.Time
	Result                         *ModelDraftResult
}

func (a GenerationAttempt) Validate() error {
	if a.Version != 1 || !identifier.MatchString(a.ID) || !a.Key.valid() || !generationLabel(a.Model) || !generationLabel(a.Provider) || len(a.InputDigest) != 64 || strings.ToLower(a.InputDigest) != a.InputDigest || !modelDraftCost(a.EstimatedCost) || !generationTime(a.StartedAt) {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(a.InputDigest); err != nil || !generationIDs(a.SourceSessions, 2, 20) || !generationIDs(a.SourceEvidence, 1, 2000) {
		return ErrInvalid
	}
	switch a.Status {
	case "started":
		if !a.FinishedAt.IsZero() || a.Result != nil || a.Code != "" {
			return ErrInvalid
		}
	case "failed":
		if !generationTime(a.FinishedAt) || a.FinishedAt.Before(a.StartedAt) || a.Result != nil || (a.Code != "generation_failed" && a.Code != "canceled" && a.Code != "persistence_failed") {
			return ErrInvalid
		}
	case "drafted":
		if !generationTime(a.FinishedAt) || a.FinishedAt.Before(a.StartedAt) || a.Code != "" || a.Result == nil {
			return ErrInvalid
		}
		r := a.Result
		if r.Model != a.Model || r.Elapsed < 0 || r.Elapsed > 30*time.Second || r.Draft.Key != a.Key || !slices.Equal(r.Draft.SourceSessions, a.SourceSessions) || !slices.Equal(r.Draft.SourceEvidence, a.SourceEvidence) || validateGeneratedDraft(r.Draft) != nil {
			return ErrInvalid
		}
		if r.Usage != nil && (r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0 || r.Usage.InputTokens > math.MaxInt64-r.Usage.OutputTokens) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func generationIDs(ids []string, min, max int) bool {
	if len(ids) < min || len(ids) > max {
		return false
	}
	for i, id := range ids {
		if !identifier.MatchString(id) || (i > 0 && ids[i-1] >= id) {
			return false
		}
	}
	return true
}

func generationLabel(s string) bool {
	return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func generationTime(t time.Time) bool {
	_, offset := t.Zone()
	return !t.IsZero() && t.Year() >= 1970 && t.Year() < 2261 && offset == 0
}
