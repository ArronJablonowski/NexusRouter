package app

import (
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func (s *Service) skillGenerationBudget() (skills.GenerationBudget, error) {
	cfg := s.settings.Skills.GenerationBudget
	window, err := config.Duration(cfg.Window)
	if err != nil {
		return skills.GenerationBudget{}, ErrAdmission
	}
	cooldown, err := time.ParseDuration(cfg.Cooldown)
	if err != nil {
		cooldown, err = config.Duration(cfg.Cooldown)
	}
	if err != nil {
		return skills.GenerationBudget{}, ErrAdmission
	}
	budget := skills.GenerationBudget{Version: 1, Window: window, MaxCost: cfg.MaxCost, MaxAttempts: cfg.MaxAttempts, MaxInFlight: cfg.MaxInFlight, Cooldown: cooldown}
	if budget.Validate() != nil {
		return skills.GenerationBudget{}, ErrAdmission
	}
	return budget, nil
}
