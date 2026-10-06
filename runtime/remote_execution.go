package runtime

import (
	"errors"
	"time"
)

// RemoteExecution is persisted with the submission. Only one remote hop and
// one local child level are allowed; workers never inherit this capability.
type RemoteExecution struct {
	Mode          string    `json:"mode"`
	SpecialistIDs []string  `json:"specialist_ids,omitempty"`
	MaxCalls      int       `json:"max_calls,omitempty"`
	Deadline      time.Time `json:"deadline,omitempty"`
	Depth         int       `json:"depth"`
}

func (r RemoteExecution) Validate() error {
	bad := errors.New("invalid remote execution bounds")
	if r.Depth != 1 {
		return bad
	}
	switch r.Mode {
	case "direct", "consult":
		if r.Mode == "consult" && r.Deadline.IsZero() {
			return bad
		}
		if len(r.SpecialistIDs) != 0 || r.MaxCalls != 0 {
			return bad
		}
	case "commander":
		if len(r.SpecialistIDs) < 1 || len(r.SpecialistIDs) > 8 || r.MaxCalls < 1 || r.MaxCalls > 16 || r.Deadline.IsZero() {
			return bad
		}
		seen := map[string]bool{}
		for _, id := range r.SpecialistIDs {
			if len(id) < 1 || len(id) > 128 || seen[id] {
				return bad
			}
			seen[id] = true
		}
	default:
		return bad
	}
	return nil
}
