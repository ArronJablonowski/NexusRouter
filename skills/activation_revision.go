package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ActivationState identifies a skill's activation timeline, not just its active
// version. It is a concurrency precondition, never authorization or proof that
// a version is appropriate for a task. Draft creation alone does not change it.
type ActivationState struct {
	Version  int    `json:"version"`
	Key      Key    `json:"key"`
	Active   string `json:"active"`
	Revision string `json:"revision"`
}

func (s ActivationState) Validate() error {
	if s.Version != 1 || !s.Key.valid() || (s.Active != "" && !versionID(s.Active)) || len(s.Revision) != 64 {
		return ErrInvalid
	}
	decoded, err := hex.DecodeString(s.Revision)
	if err != nil || hex.EncodeToString(decoded) != s.Revision {
		return ErrInvalid
	}
	return nil
}

// RevisionStore is an optional extension for controllers which must distinguish
// reactivation of the same version. Existing Store implementations remain valid.
type RevisionStore interface {
	Store
	ActivationState(context.Context, Key) (ActivationState, error)
	ActivateAt(context.Context, ActivationState, string, Validator, bool) error
	RollbackAt(context.Context, ActivationState, bool) error
}

var _ RevisionStore = (*FileStore)(nil)

func stateForEntry(e entry) (ActivationState, error) {
	if !e.Key.valid() {
		return ActivationState{}, ErrInvalid
	}
	if _, err := activationStack(e); err != nil {
		return ActivationState{}, err
	}
	// Canonicalize empty history so null and [] have the same semantic revision.
	body, err := json.Marshal(struct {
		Version int          `json:"version"`
		Key     Key          `json:"key"`
		Active  string       `json:"active"`
		History []activation `json:"history"`
	}{1, e.Key, e.Active, append([]activation{}, e.Activations...)})
	if err != nil {
		return ActivationState{}, ErrInvalid
	}
	digest := sha256.Sum256(body)
	state := ActivationState{Version: 1, Key: e.Key, Active: e.Active, Revision: hex.EncodeToString(digest[:])}
	if state.Validate() != nil {
		return ActivationState{}, ErrInvalid
	}
	return state, nil
}

// ActivationState reads one validated catalog snapshot without initializing,
// activating, rolling back or repairing any skill.
func (s *FileStore) ActivationState(ctx context.Context, key Key) (ActivationState, error) {
	if ctx == nil || !s.permitted(key) {
		return ActivationState{}, ErrInvalid
	}
	var out ActivationState
	err := s.with(ctx, func(c *catalog) error {
		e, ok := c.Skills[key.index()]
		if !ok {
			return ErrNotFound
		}
		var err error
		out, err = stateForEntry(e)
		return err
	}, false)
	if err != nil {
		return ActivationState{}, err
	}
	return out, nil
}
