package webui

// SpecialistRankingInspection is a deterministic, read-only policy preview.
// It neither reserves capacity nor promises admission for a future request.
type SpecialistRankingInspection struct {
	Key              string                     `json:"key"`
	Domain           string                     `json:"domain"`
	Profile          string                     `json:"profile"`
	RequiresEvidence bool                       `json:"requires_evidence,omitempty"`
	Models           []SpecialistRankInspection `json:"models"`
}
type SpecialistRankInspection struct {
	ModelID    string  `json:"model_id"`
	Domain     string  `json:"domain"`
	Profile    string  `json:"profile"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
	Samples    int     `json:"samples"`
}

func validateSpecialistRankings(rows []SpecialistRankingInspection, models []ModelInspection, availability Availability) error {
	if len(rows) > 14 || availability == Unavailable && len(rows) > 0 {
		return ErrContract
	}
	known := map[string]bool{}
	for _, m := range models {
		known[m.ID] = true
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !modelIDPattern.MatchString(row.Key) || !modelIDPattern.MatchString(row.Domain) || !modelIDPattern.MatchString(row.Profile) || seen[row.Key] || row.Models == nil || len(row.Models) > 3 {
			return ErrContract
		}
		seen[row.Key] = true
		ids := map[string]bool{}
		for _, m := range row.Models {
			if !known[m.ModelID] || ids[m.ModelID] || !modelIDPattern.MatchString(m.Domain) || !modelIDPattern.MatchString(m.Profile) || !finite(m.Score) || m.Score < 0 || m.Score > 1 || !finite(m.Confidence) || m.Confidence < 0 || m.Confidence > 1 || m.Samples < 0 || row.RequiresEvidence && m.Samples == 0 {
				return ErrContract
			}
			ids[m.ModelID] = true
		}
	}
	return nil
}
