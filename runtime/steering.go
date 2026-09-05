package runtime

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxSteeringBytes = 64 << 10
const MaxSteeringMessages = 32

var (
	// ErrSteeringPending guarantees the rejected completion append had no effect.
	ErrSteeringPending = errors.New("task has pending steering")
	ErrSteeringClosed  = errors.New("task no longer accepts steering")
	ErrSteeringLimit   = errors.New("task steering limit reached")
)

type SteeringMessage struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	TaskID          string    `json:"task_id"`
	Text            string    `json:"text"`
	State           string    `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
	AppliedSequence *int64    `json:"applied_sequence,omitempty"`
}

// SteeringReceipt is safe control metadata, deliberately excluding guidance.
type SteeringReceipt struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	TaskID          string    `json:"task_id"`
	State           string    `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
	AppliedSequence *int64    `json:"applied_sequence,omitempty"`
}

func (m SteeringMessage) Receipt() SteeringReceipt {
	r := SteeringReceipt{Version: m.Version, ID: m.ID, TaskID: m.TaskID, State: m.State, CreatedAt: m.CreatedAt}
	if m.AppliedSequence != nil {
		value := *m.AppliedSequence
		r.AppliedSequence = &value
	}
	return r
}

func ValidSteeringText(text string) bool {
	return len(text) > 0 && len(text) <= MaxSteeringBytes && utf8.ValidString(text) && strings.TrimSpace(text) != ""
}

func validSteeringID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (m SteeringMessage) Validate() error {
	if !ValidSteeringText(m.Text) {
		return ErrInvalidRun
	}
	return m.Receipt().Validate()
}

func (m SteeringReceipt) Validate() error {
	if m.Version != 1 || !validSteeringID(m.ID) || !validSteeringID(m.TaskID) || m.CreatedAt.IsZero() || m.CreatedAt.Year() < 1 || m.CreatedAt.Year() > 9999 {
		return ErrInvalidRun
	}
	if m.State == "pending" && m.AppliedSequence == nil {
		return nil
	}
	if m.State == "applied" && m.AppliedSequence != nil && *m.AppliedSequence > 1 {
		return nil
	}
	return ErrInvalidRun
}

// SteeringSource peeks without consuming. The journal must atomically record
// steering.applied and mark the same message consumed before returning success.
type SteeringSource interface {
	NextSteering(context.Context, string) (*SteeringMessage, error)
}
