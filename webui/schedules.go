package webui

import (
	"strings"
	"time"
)

// SchedulePage contains only safe scheduler configuration, never job payloads.
// Enabled describes configured scheduling, not proof of a currently running job.
type SchedulePage struct {
	Version    int        `json:"version"`
	ObservedAt time.Time  `json:"observed_at"`
	Items      []Schedule `json:"items"`
}

// AIUsage describes capability, not measured token consumption. Empty means unknown on older hosts.
type Schedule struct {
	AIUsage     string `json:"ai_usage,omitempty"`
	ID          string `json:"id"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Interval    string `json:"interval"`
}

func (p SchedulePage) Validate() error {
	if p.Version != 1 || p.ObservedAt.IsZero() || len(p.Items) > 64 {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, s := range p.Items {
		if !ValidID(s.ID) || seen[s.ID] || len(s.Description) == 0 || len(s.Description) > 256 || strings.ContainsAny(s.Description, "\x00\r\n") {
			return ErrContract
		}
		seen[s.ID] = true
		if s.AIUsage != "" && s.AIUsage != "possible" && s.AIUsage != "none" && s.AIUsage != "unknown" {
			return ErrContract
		}
		d, e := time.ParseDuration(s.Interval)
		if e != nil || d <= 0 || d > 24*time.Hour {
			return ErrContract
		}
	}
	return nil
}
