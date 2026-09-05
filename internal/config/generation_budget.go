package config

import (
	"errors"
	"math"
	"time"
)

// GenerationBudget limits aggregate estimated generation expenditure within a
// configured skill scope. It is not a provider invoice or scheduler setting.
type GenerationBudget struct {
	Enabled     bool    `yaml:"enabled" json:"enabled"`
	Window      string  `yaml:"window" json:"window"`
	MaxCost     float64 `yaml:"max_cost" json:"max_cost"`
	MaxAttempts int     `yaml:"max_attempts" json:"max_attempts"`
	MaxInFlight int     `yaml:"max_in_flight" json:"max_in_flight"`
	Cooldown    string  `yaml:"cooldown" json:"cooldown"`
}

// Validate checks disabled configurations too so toggling Enabled cannot expose
// previously ignored invalid limits. Defaults provide a complete disabled policy.
func (b GenerationBudget) Validate() error {
	window, err := Duration(b.Window)
	cooldown, cooldownErr := time.ParseDuration(b.Cooldown)
	if cooldownErr != nil {
		cooldown, cooldownErr = Duration(b.Cooldown)
	}
	if err != nil || cooldownErr != nil || window < time.Minute || window > 30*24*time.Hour || cooldown < 0 || cooldown > window || math.IsNaN(b.MaxCost) || math.IsInf(b.MaxCost, 0) || b.MaxCost < 0 || b.MaxAttempts < 1 || b.MaxAttempts > 1000 || b.MaxInFlight < 1 || b.MaxInFlight > b.MaxAttempts {
		return errors.New("invalid skill generation budget")
	}
	return nil
}
