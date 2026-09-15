package config

import (
	"errors"
	"time"
)

// OutcomeRollbackSupervisor configures a bounded unattended observation cycle.
// It selects retained outcomes for one exact configured execution model; it
// does not grant model judgment authority or weaken the rollback operation's
// independent gates.
type OutcomeRollbackSupervisor struct {
	Version         int     `yaml:"version" json:"version"`
	Enabled         bool    `yaml:"enabled" json:"enabled"`
	Interval        string  `yaml:"interval" json:"interval"`
	ModelID         string  `yaml:"model_id" json:"model_id"`
	Domain          string  `yaml:"domain" json:"domain"`
	Profile         string  `yaml:"profile" json:"profile"`
	Source          string  `yaml:"source" json:"source"`
	Privacy         string  `yaml:"privacy" json:"privacy"`
	MinSamples      int     `yaml:"min_samples" json:"min_samples"`
	MinDrop         float64 `yaml:"min_drop" json:"min_drop"`
	TasksPerVersion int     `yaml:"tasks_per_version" json:"tasks_per_version"`
}

func (s Settings) validateOutcomeRollbackSupervisor() error {
	p := s.Skills.OutcomeRollbackSupervisor
	interval, err := Duration(p.Interval)
	if p.Version != 1 || err != nil || interval < time.Second || interval > 24*time.Hour ||
		(p.ModelID != "" && !identifier.MatchString(p.ModelID)) || !identifier.MatchString(p.Profile) ||
		(p.Privacy != "local_only" && p.Privacy != "cloud_allowed") ||
		p.MinSamples < 20 || p.MinSamples > 100 || !finite(p.MinDrop) || p.MinDrop <= 0 || p.MinDrop > 1 ||
		p.TasksPerVersion < 20 || p.TasksPerVersion > 100 || p.MinSamples > p.TasksPerVersion ||
		!outcomeRollbackDomainSource(p.Domain, p.Source) {
		return errors.New("invalid outcome rollback supervisor settings")
	}
	if !p.Enabled {
		return nil
	}
	if !s.Skills.Enabled || !s.Skills.Rollback || !s.Skills.OutcomeRollback || s.Skills.Root == "" || s.Skills.Scope == "" || p.ModelID == "" {
		return errors.New("outcome rollback supervisor requires enabled rollback skills, outcome rollback, configured root and scope, and an exact model")
	}
	for _, model := range s.Models {
		if model.ID == p.ModelID {
			return nil
		}
	}
	return errors.New("outcome rollback supervisor model is not configured")
}

func outcomeRollbackDomainSource(domain, source string) bool {
	switch domain {
	case "creative", "unknown":
		return source == "user_feedback"
	case "code", "coding", "debugging", "math", "structured_json":
		return source == "deterministic" || source == "tool_result"
	default:
		return false
	}
}
