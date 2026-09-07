package config

import (
	"errors"
	"time"
)

// Learning enables bounded daemon-driven discovery and draft generation.
// Optional validator selections name trusted host code, never evidence supplied
// by configuration. Empty selections preserve the existing draft-only policy.
type Learning struct {
	Enabled            bool    `yaml:"enabled" json:"enabled"`
	Name               string  `yaml:"name" json:"name"`
	Domain             string  `yaml:"domain" json:"domain"`
	ModelID            string  `yaml:"model_id" json:"model_id"`
	Interval           string  `yaml:"interval" json:"interval"`
	MaxCost            float64 `yaml:"max_cost" json:"max_cost"`
	ScanLimit          int     `yaml:"scan_limit" json:"scan_limit"`
	ValidatorID        string  `yaml:"validator_id" json:"validator_id,omitempty"`
	RegressionName     string  `yaml:"regression_name" json:"regression_name,omitempty"`
	RegressionInterval string  `yaml:"regression_interval" json:"regression_interval,omitempty"`
}

func (s Settings) validateLearning() error {
	l := s.Skills.Learning
	interval, err := Duration(l.Interval)
	if !skillScope.MatchString(l.Name) || !skillScope.MatchString(l.Domain) || (l.ModelID != "" && !identifier.MatchString(l.ModelID)) || err != nil || interval < time.Second || interval > 24*time.Hour || !finite(l.MaxCost) || l.MaxCost < 0 || l.ScanLimit < 1 || l.ScanLimit > 20 {
		return errors.New("invalid skill learning settings")
	}
	if (l.ValidatorID != "" && !skillScope.MatchString(l.ValidatorID)) || (l.RegressionName != "" && !skillScope.MatchString(l.RegressionName)) || ((l.RegressionName == "") != (l.RegressionInterval == "")) {
		return errors.New("invalid skill learning validator selection")
	}
	if l.RegressionName != "" {
		regressionInterval, err := Duration(l.RegressionInterval)
		if l.ValidatorID == "" || err != nil || regressionInterval < time.Second || regressionInterval > 24*time.Hour {
			return errors.New("invalid skill learning regression selection")
		}
	}
	if !l.Enabled {
		return nil
	}
	if (l.ValidatorID != "" && !s.Skills.AutoActivate) || (l.RegressionName != "" && !s.Skills.Rollback) {
		return errors.New("skill learning validator selection requires activation and regression policy")
	}
	if !s.Skills.Enabled || !s.Skills.AutoDraft || !s.Skills.GenerationBudget.Enabled || s.Skills.Root == "" || s.Skills.Scope == "" || l.MaxCost > s.Skills.GenerationBudget.MaxCost {
		return errors.New("skill learning requires configured skills and generation budget")
	}
	for _, m := range s.Models {
		if m.ID != l.ModelID {
			continue
		}
		if m.ContextTokens < 1 || m.EstimatedCost == nil || !finite(*m.EstimatedCost) || *m.EstimatedCost < 0 || *m.EstimatedCost > l.MaxCost || (m.Locality == "local" && m.RAMBytes == 0) || ((s.Mode == "local_only" || s.Skills.LocalOnly) && m.Locality != "local") || (s.Mode == "cloud_only" && m.Locality != "cloud") {
			return errors.New("learning model unavailable within configured limits")
		}
		for _, provider := range s.Providers {
			if provider.ID == m.Provider && (provider.Kind == "ollama" || provider.Kind == "openai_compatible" || provider.Kind == "codex_app_server") {
				return nil
			}
		}
		return errors.New("learning requires a supported generation provider")
	}
	return errors.New("learning model unavailable within configured limits")
}
