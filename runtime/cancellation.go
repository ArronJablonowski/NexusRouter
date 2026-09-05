package runtime

import (
	"errors"
	"time"
)

// ErrCancellationRequested guarantees the rejected append had no effect.
// It is distinct from an ambiguous persistence failure and permits cleanup.
var ErrCancellationRequested = errors.New("task cancellation requested")

// ErrExecutionLeaseLost guarantees the rejected append had no effect. Hosts
// must not retry execution; only bounded terminal/effect recording is safe.
var ErrExecutionLeaseLost = errors.New("execution lease lost")

type CancellationStatus struct {
	Version     int        `json:"version"`
	TaskID      string     `json:"task_id"`
	State       string     `json:"state"`
	Requested   bool       `json:"requested"`
	RequestID   string     `json:"request_id,omitempty"`
	RequestedAt *time.Time `json:"requested_at,omitempty"`
}
