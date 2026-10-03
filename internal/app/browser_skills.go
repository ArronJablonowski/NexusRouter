package app

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/skills"
	"os"
)

type BrowserSkillPage struct {
	Version int                    `json:"version"`
	Enabled bool                   `json:"enabled"`
	Scope   string                 `json:"scope"`
	Items   []skills.InventoryItem `json:"items"`
}

func (s *Service) BrowserSkills(ctx context.Context) (BrowserSkillPage, error) {
	p := BrowserSkillPage{1, s.settings.Skills.Enabled, s.settings.Skills.Scope, []skills.InventoryItem{}}
	if s.settings.Skills.Root == "" || s.settings.Skills.Scope == "" {
		p.Scope = "Not configured"
		return p, nil
	}
	st, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if errors.Is(err, skills.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	defer st.Close()
	p.Items, err = st.Inventory(ctx, s.settings.Skills.Scope)
	return p, err
}
