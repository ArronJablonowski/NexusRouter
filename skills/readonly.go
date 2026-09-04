package skills

import "context"

// OpenReadOnly inspects an existing private store without creating directories,
// lock files, or changing permissions. Each read uses one atomic catalog snapshot
// and immutable version files; it does not acquire the mutation lock.
func OpenReadOnly(path string, scopes []string) (*FileStore, error) {
	return openStore(path, scopes, true)
}

// History contains metadata only, preserving progressive workflow loading.
type History struct {
	Key      Key        `json:"key"`
	Active   string     `json:"active"`
	Versions []Metadata `json:"versions"`
}

// History returns draft and activated versions in creation order.
func (s *FileStore) History(ctx context.Context, key Key) (History, error) {
	var result History
	if !s.permitted(key) {
		return result, ErrInvalid
	}
	err := s.with(ctx, func(c *catalog) error {
		e, ok := c.Skills[key.index()]
		if !ok {
			return ErrNotFound
		}
		result = History{Key: e.Key, Active: e.Active, Versions: e.Versions}
		return nil
	}, false)
	return result, err
}
