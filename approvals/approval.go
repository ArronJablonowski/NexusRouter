// Package approvals defines one-use, exact-bound tool execution approvals.
// A stored approval alone does not authorize execution: the durable store must
// atomically consume it while checking current task and writer-lease ownership.
package approvals

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const (
	Version  = 1
	Pending  = "pending"
	Approved = "approved"
	Denied   = "denied"
	Revoked  = "revoked"
	Consumed = "consumed"
)

var (
	ErrInvalid     = errors.New("invalid tool approval")
	ErrConflict    = errors.New("tool approval state conflict")
	ErrUnavailable = errors.New("tool approval unavailable")
)

// Request identifies a single proposal. Digests are supplied by the trusted
// coordinator from exact arguments, schema and effective policy; no raw prompt,
// arguments, credentials or lease capabilities belong in this record.
type Request struct {
	Version         int                  `json:"version"`
	ID              string               `json:"id"`
	TaskID          string               `json:"task_id"`
	TurnID          string               `json:"turn_id"`
	ToolCallID      string               `json:"tool_call_id"`
	ToolName        string               `json:"tool_name"`
	ToolBehavior    runtime.ToolBehavior `json:"tool_behavior,omitempty"`
	Scope           string               `json:"scope"`
	ArgumentsDigest string               `json:"arguments_digest"`
	SchemaDigest    string               `json:"schema_digest"`
	PolicyDigest    string               `json:"policy_digest"`
	CreatedAt       time.Time            `json:"created_at"`
	ExpiresAt       time.Time            `json:"expires_at"`
}

// Decision is operator-attributed, never model-authored authority. The host
// authenticates Actor before accepting it; a valid string proves no identity.
type Decision struct {
	ID      string    `json:"id"`
	Actor   string    `json:"actor"`
	Allowed bool      `json:"allowed"`
	Time    time.Time `json:"time"`
}

// Record retains the approval and optional revocation decisions. Consumed means
// dispatch authority was spent, not that an effect succeeded or even occurred.
// It must never become reusable after a crash or an uncertain acknowledgement.
type Record struct {
	Request    Request    `json:"request"`
	State      string     `json:"state"`
	Decisions  []Decision `json:"decisions"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}

func (r Request) Validate() error {
	if r.ToolBehavior != "" && !r.ToolBehavior.Valid() {
		return ErrInvalid
	}
	if r.Version != Version || !identifier(r.ID) || !identifier(r.TaskID) || !identifier(r.TurnID) || !identifier(r.ToolCallID) || !toolName(r.ToolName) || !label(r.Scope, 256) || strings.Contains(r.Scope, "*") || !digest(r.ArgumentsDigest) || !digest(r.SchemaDigest) || !digest(r.PolicyDigest) || !validTime(r.CreatedAt) || !validTime(r.ExpiresAt) || !r.ExpiresAt.After(r.CreatedAt) || r.ExpiresAt.Sub(r.CreatedAt) > 10*time.Minute {
		return ErrInvalid
	}
	return nil
}

func (d Decision) Validate() error {
	if !identifier(d.ID) || !label(d.Actor, 128) || !validTime(d.Time) {
		return ErrInvalid
	}
	return nil
}

// Matches compares every binding including the approval ID and validity window,
// using instants rather than Go's location/monotonic clock implementation state.
func (r Request) Matches(other Request) bool {
	if !r.CreatedAt.Equal(other.CreatedAt) || !r.ExpiresAt.Equal(other.ExpiresAt) {
		return false
	}
	r.CreatedAt, r.ExpiresAt = time.Time{}, time.Time{}
	other.CreatedAt, other.ExpiresAt = time.Time{}, time.Time{}
	return r == other
}

func (r Record) Validate() error {
	if r.Request.Validate() != nil || len(r.Decisions) > 2 {
		return ErrInvalid
	}
	for i, d := range r.Decisions {
		if d.Validate() != nil || d.Time.Before(r.Request.CreatedAt) || !d.Time.Before(r.Request.ExpiresAt) {
			return ErrInvalid
		}
		if i > 0 && (d.ID == r.Decisions[i-1].ID || d.Time.Before(r.Decisions[i-1].Time)) {
			return ErrInvalid
		}
	}
	switch r.State {
	case Pending:
		if len(r.Decisions) != 0 || r.ConsumedAt != nil {
			return ErrInvalid
		}
	case Approved, Denied:
		if len(r.Decisions) != 1 || r.Decisions[0].Allowed != (r.State == Approved) || r.ConsumedAt != nil {
			return ErrInvalid
		}
	case Revoked:
		if len(r.Decisions) != 2 || !r.Decisions[0].Allowed || r.Decisions[1].Allowed || r.ConsumedAt != nil {
			return ErrInvalid
		}
	case Consumed:
		if len(r.Decisions) != 1 || !r.Decisions[0].Allowed || r.ConsumedAt == nil || !validTime(*r.ConsumedAt) || r.ConsumedAt.Before(r.Decisions[0].Time) || !r.ConsumedAt.Before(r.Request.ExpiresAt) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func identifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range []byte(s) {
		if c != '-' && c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func toolName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i, c := range []byte(s) {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func label(s string, limit int) bool {
	return len(s) > 0 && len(s) <= limit && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validTime(t time.Time) bool {
	_, offset := t.Zone()
	return t.Year() >= 1970 && t.Year() < 2261 && offset == 0
}
