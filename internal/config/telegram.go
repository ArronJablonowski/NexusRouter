package config

import (
	"errors"
	"path/filepath"
	"strings"
)

// Telegram is opt-in. Only an environment reference, never a token, belongs in
// configuration. The first release deliberately permits only local text models.
type Telegram struct {
	Enabled        bool    `yaml:"enabled" json:"enabled"`
	TokenEnv       string  `yaml:"token_env,omitempty" json:"token_env,omitempty"`
	AllowedUsers   []int64 `yaml:"allowed_users,omitempty" json:"allowed_users,omitempty"`
	ModelID        string  `yaml:"model_id,omitempty" json:"model_id,omitempty"`
	StateDirectory string  `yaml:"state_directory,omitempty" json:"state_directory,omitempty"`
}

func (s Settings) validateTelegram() error {
	c := s.Telegram
	if !c.Enabled {
		return nil
	}
	invalid := errors.New("Telegram requires an environment token reference, private state directory, allowed numeric users, and a configured local chat model")
	if s.Mode == "cloud_only" || !envName.MatchString(c.TokenEnv) || !filepath.IsAbs(c.StateDirectory) || filepath.Clean(c.StateDirectory) != c.StateDirectory || len(c.StateDirectory) > 4096 || strings.ContainsAny(c.StateDirectory, "\x00\r\n") || len(c.AllowedUsers) < 1 || len(c.AllowedUsers) > 32 {
		return invalid
	}
	database, err := filepath.Abs(s.Telemetry.Database)
	if err != nil || database == filepath.Join(c.StateDirectory, "tasks.db") {
		return invalid
	}
	seen := map[int64]bool{}
	for _, id := range c.AllowedUsers {
		if id <= 0 || seen[id] {
			return invalid
		}
		seen[id] = true
	}
	for _, m := range s.Models {
		if m.ID == c.ModelID && m.Locality == "local" && m.EstimatedCost != nil && *m.EstimatedCost == 0 {
			for _, capability := range m.Capabilities {
				if capability == "chat" {
					return nil
				}
			}
		}
	}
	return invalid
}
