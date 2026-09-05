// Package daemon provides instance-bound cooperative lifecycle control.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var ErrControl = errors.New("daemon control unavailable")
var ErrInstance = errors.New("daemon instance mismatch")
var ErrConflict = ErrInstance

func NewID() string          { var b [32]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func ValidID(id string) bool { return ValidInstanceID(id) }

type Status struct {
	Version    int       `json:"version"`
	InstanceID string    `json:"instance_id"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
}

func ValidInstanceID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s Status) Validate() error {
	if s.Version != 1 || !ValidInstanceID(s.InstanceID) || (s.State != "ready" && s.State != "stopping" && s.State != "degraded") || s.StartedAt.IsZero() || s.StartedAt.Year() < 1970 || s.StartedAt.Year() >= 2261 {
		return ErrControl
	}
	return nil
}

// Controller never signals processes: Stop invokes only its owner's supplied
// cooperative shutdown callback, once. "stopping" is not proof of termination.
type Controller struct {
	mu     sync.Mutex
	status Status
	cancel func()
}

func New(instanceID string, cancel func()) (*Controller, error) {
	if !ValidInstanceID(instanceID) || cancel == nil {
		return nil, ErrControl
	}
	return &Controller{status: Status{Version: 1, InstanceID: instanceID, State: "ready", StartedAt: time.Now().UTC()}, cancel: cancel}, nil
}

func (c *Controller) Current(ctx context.Context) (Status, error) {
	if c == nil || ctx == nil {
		return Status{}, ErrControl
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	return c.status, nil
}

func (c *Controller) Stop(ctx context.Context, id string) (Status, error) {
	if c == nil || ctx == nil {
		return Status{}, ErrControl
	}
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return Status{}, err
	}
	if id != c.status.InstanceID {
		c.mu.Unlock()
		return Status{}, ErrInstance
	}
	first := c.status.State == "ready"
	c.status.State = "stopping"
	status := c.status
	c.mu.Unlock()
	if first {
		c.cancel()
	}
	return status, nil
}
