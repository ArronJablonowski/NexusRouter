package app

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type BrowserSkillPage struct {
	Version int                    `json:"version"`
	Enabled bool                   `json:"enabled"`
	Scope   string                 `json:"scope"`
	Items   []skills.InventoryItem `json:"items"`
}

func (s *Service) BrowserSkills(ctx context.Context) (BrowserSkillPage, error) {
	p := BrowserSkillPage{1, s.settings.Skills.Enabled, s.settings.Skills.Scope, []skills.InventoryItem{}}
	st, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if errors.Is(err, skills.ErrNotFound) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	defer st.Close()
	p.Items, err = st.Inventory(ctx, s.settings.Skills.Scope)
	return p, err
}
