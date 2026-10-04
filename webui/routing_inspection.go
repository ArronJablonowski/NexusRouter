package webui

// RoutingInspection is a bounded policy preview, without prompts or evidence bodies.
type RoutingInspection struct {
	Rankings            []SpecialistRankingInspection `json:"rankings"`
	CommanderID         string                        `json:"commander_id,omitempty"`
	CommanderSource     string                        `json:"commander_source,omitempty"`
	CommanderFallbackID string                        `json:"commander_fallback_id,omitempty"`
}

func (p RoutingInspection) Validate(models []ModelInspection) error {
	if p.Rankings == nil || validateSpecialistRankings(p.Rankings, models, Available) != nil {
		return ErrContract
	}
	known := map[string]ModelInspection{}
	for _, m := range models {
		known[m.ID] = m
	}
	if (p.CommanderID == "") != (p.CommanderSource == "") || p.CommanderSource != "" && p.CommanderSource != "configured" && p.CommanderSource != "inferred" {
		return ErrContract
	}
	if p.CommanderID != "" {
		if _, ok := known[p.CommanderID]; !ok {
			return ErrContract
		}
	}
	if p.CommanderFallbackID != "" {
		m, ok := known[p.CommanderFallbackID]
		if !ok || m.Locality != "local" || p.CommanderID == "" || p.CommanderFallbackID == p.CommanderID {
			return ErrContract
		}
	}
	return nil
}
