package skills

import (
	"context"
	"encoding/hex"
	"time"
)

// RegressionMonitorGuard is a trusted metadata admission hook, not a validator.
// It receives scalar copies while the catalog lock is held and must be bounded,
// read-only, cancellation-cooperative and never reenter this store.
type RegressionMonitorGuard func(context.Context, RegressionMonitorState, RegressionMonitorCheck) error

type RegressionMonitorState struct {
	Version            int           `json:"version"`
	Scope              string        `json:"scope"`
	Name               string        `json:"name"`
	ValidatorID        string        `json:"validator_id"`
	PolicyDigest       string        `json:"policy_digest"`
	Interval           time.Duration `json:"interval"`
	Revision           int64         `json:"revision"`
	After              string        `json:"after"`
	PendingOperationID string        `json:"pending_operation_id"`
	NextDue            time.Time     `json:"next_due"`
}

type RegressionMonitorCheck struct {
	Version        int             `json:"version"`
	Scope          string          `json:"scope"`
	MonitorName    string          `json:"monitor_name"`
	ValidatorID    string          `json:"validator_id"`
	PolicyDigest   string          `json:"policy_digest"`
	OperationID    string          `json:"operation_id"`
	Sequence       int64           `json:"sequence"`
	Expected       ActivationState `json:"expected"`
	PreviousCursor string          `json:"previous_cursor"`
	PreparedAt     time.Time       `json:"prepared_at"`
	Status         string          `json:"status"`
	Code           string          `json:"code"`
	FinishedAt     time.Time       `json:"finished_at"`
}

func monitorTime(t time.Time) bool {
	_, offset := t.Zone()
	return t.Year() >= 1970 && t.Year() < 2261 && offset == 0
}
func monitorDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func (s RegressionMonitorState) Validate() error {
	if s.Version != 1 || !(Key{s.Scope, s.Name}).valid() || !identifier.MatchString(s.ValidatorID) || !monitorDigest(s.PolicyDigest) || s.Interval < time.Second || s.Interval > 24*time.Hour || s.Revision < 0 || s.After != "" && !identifier.MatchString(s.After) || s.PendingOperationID != "" && !identifier.MatchString(s.PendingOperationID) || !monitorTime(s.NextDue) {
		return ErrInvalid
	}
	return nil
}
func (c RegressionMonitorCheck) Validate() error {
	if c.Version != 1 || !(Key{c.Scope, c.MonitorName}).valid() || !identifier.MatchString(c.ValidatorID) || !monitorDigest(c.PolicyDigest) || !identifier.MatchString(c.OperationID) || c.Sequence < 1 || c.Expected.Validate() != nil || c.Expected.Active == "" || c.Expected.Key.Scope != c.Scope || c.PreviousCursor != "" && !identifier.MatchString(c.PreviousCursor) || c.Expected.Key.Name <= c.PreviousCursor || !monitorTime(c.PreparedAt) {
		return ErrInvalid
	}
	switch c.Status {
	case "pending":
		if c.Code != "" || !c.FinishedAt.IsZero() {
			return ErrInvalid
		}
	case "completed":
		if c.Code != "" || !monitorTime(c.FinishedAt) {
			return ErrInvalid
		}
	case "failed":
		if c.Code != "check_failed" && c.Code != "stale_activation" || !monitorTime(c.FinishedAt) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validateRegressionMonitors(c *catalog) error {
	if len(c.RegressionMonitors) > 1000 || len(c.RegressionMonitorChecks) > 1000 || (len(c.RegressionMonitors) > 0 || len(c.RegressionMonitorChecks) > 0) && c.Schema < 5 {
		return ErrInvalid
	}
	for index, s := range c.RegressionMonitors {
		if s.Validate() != nil || index != (Key{s.Scope, s.Name}).index() {
			return ErrInvalid
		}
		if s.PendingOperationID != "" {
			p, ok := c.RegressionMonitorChecks[s.PendingOperationID]
			if !ok || p.Status != "pending" || p.Sequence != s.Revision || p.Scope != s.Scope || p.MonitorName != s.Name || p.PreviousCursor != s.After {
				return ErrInvalid
			}
		}
	}
	seen := map[string]map[int64]bool{}
	for id, p := range c.RegressionMonitorChecks {
		index := (Key{p.Scope, p.MonitorName}).index()
		s, ok := c.RegressionMonitors[index]
		if id != p.OperationID || p.Validate() != nil || !ok || p.ValidatorID != s.ValidatorID || p.PolicyDigest != s.PolicyDigest || p.Sequence > s.Revision {
			return ErrInvalid
		}
		if seen[index] == nil {
			seen[index] = map[int64]bool{}
		}
		if seen[index][p.Sequence] {
			return ErrInvalid
		}
		seen[index][p.Sequence] = true
		if p.Status == "pending" && s.PendingOperationID != id || p.Status != "pending" && (s.PendingOperationID == id || p.Sequence >= s.Revision) {
			return ErrInvalid
		}
		o, receipt := c.RegressionOperations[id]
		if receipt && (o.Expected != p.Expected || o.ValidatorID != p.ValidatorID) || p.Status == "completed" && !receipt || p.Status == "failed" && receipt {
			return ErrInvalid
		}
	}
	return nil
}

func runRegressionMonitorGuard(ctx context.Context, guard RegressionMonitorGuard, s RegressionMonitorState, p RegressionMonitorCheck) (err error) {
	if guard == nil {
		return ErrInvalid
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	defer func() {
		if recover() != nil {
			err = ErrValidation
		}
	}()
	if guard(ctx, s, p) != nil {
		return ErrValidation
	}
	return ctx.Err()
}

func (s *FileStore) RegressionMonitorState(ctx context.Context, scope, name string) (RegressionMonitorState, error) {
	if s == nil || ctx == nil || !s.permitted(Key{scope, name}) {
		return RegressionMonitorState{}, ErrInvalid
	}
	var out RegressionMonitorState
	err := s.with(ctx, func(c *catalog) error {
		var ok bool
		out, ok = c.RegressionMonitors[(Key{scope, name}).index()]
		if !ok {
			return ErrNotFound
		}
		return nil
	}, false)
	if err != nil {
		return RegressionMonitorState{}, err
	}
	return out, nil
}
func (s *FileStore) RegressionMonitorCheck(ctx context.Context, scope, name, operationID string) (RegressionMonitorCheck, error) {
	if s == nil || ctx == nil || !s.permitted(Key{scope, name}) || !identifier.MatchString(operationID) {
		return RegressionMonitorCheck{}, ErrInvalid
	}
	var out RegressionMonitorCheck
	err := s.with(ctx, func(c *catalog) error {
		var ok bool
		out, ok = c.RegressionMonitorChecks[operationID]
		if !ok {
			return ErrNotFound
		}
		if out.Scope != scope || out.MonitorName != name {
			return ErrConflict
		}
		return nil
	}, false)
	if err != nil {
		return RegressionMonitorCheck{}, err
	}
	return out, nil
}
