package skills

import (
	"context"
	"sort"
)

// InventoryItem lists stored versions, including drafts, without workflow bodies.
type InventoryItem struct {
	Metadata Metadata `json:"metadata"`
	Status   string   `json:"status"`
}

func (s *FileStore) Inventory(ctx context.Context, scope string) ([]InventoryItem, error) {
	out := []InventoryItem{}
	if !s.scopes[scope] {
		return nil, ErrInvalid
	}
	err := s.with(ctx, func(c *catalog) error {
		for _, e := range c.Skills {
			if e.Key.Scope != scope {
				continue
			}
			for _, m := range e.Versions {
				if err := ctx.Err(); err != nil {
					return err
				}
				status := "draft"
				if v, ok := e.Validated[m.Version]; ok && v.Passed && v.Deterministic {
					status = "validated"
				}
				if e.Active == m.Version {
					status = "active"
				}
				m.Tags = append([]string{}, m.Tags...)
				out = append(out, InventoryItem{m, status})
			}
		}
		return nil
	}, false)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Metadata, out[j].Metadata
		if a.Key.Name != b.Key.Name {
			return a.Key.Name < b.Key.Name
		}
		return a.Version < b.Version
	})
	return out, err
}
