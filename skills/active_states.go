package skills

import (
	"context"
	"sort"
)

// ActiveStates reads one catalog snapshot in lexical skill-name order without
// loading draft bodies. The cursor is a traversal position, not an activation
// precondition; callers must use each returned state's revision for later work.
func (s *FileStore) ActiveStates(ctx context.Context, scope, after string, limit int) ([]ActivationState, error) {
	if s == nil || ctx == nil || !identifier.MatchString(scope) || !s.scopes[scope] || (after != "" && !identifier.MatchString(after)) || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	out := []ActivationState{}
	err := s.with(ctx, func(c *catalog) error {
		names := make([]string, 0, len(c.Skills))
		for _, e := range c.Skills {
			if e.Key.Scope == scope && e.Key.Name > after && e.Active != "" {
				names = append(names, e.Key.Name)
			}
		}
		sort.Strings(names)
		if len(names) > limit {
			names = names[:limit]
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			state, err := stateForEntry(c.Skills[(Key{Scope: scope, Name: name}).index()])
			if err != nil || state.Validate() != nil || !versionID(state.Active) {
				return ErrInvalid
			}
			out = append(out, state)
		}
		return nil
	}, false)
	if err != nil {
		return nil, err
	}
	return out, nil
}
