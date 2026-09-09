// Package submissions defines durable asynchronous task intake and status.
package submissions

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

const MaxRequestBytes = 8 << 20
const MaxQueued = 128

var ErrConflict = errors.New("submission conflict")
var ErrCapacity = errors.New("submission queue full")
var ErrLeaseLost = errors.New("submission execution lease lost")
var ErrInvalid = errors.New("invalid submission")

type Status struct {
	Version         int        `json:"version"`
	ID              string     `json:"id"`
	State           string     `json:"state"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ConfigDigest    string     `json:"config_digest"`
	TaskIDs         []string   `json:"task_ids"`
	CancelRequested bool       `json:"cancel_requested"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	LeaseExpired    bool       `json:"lease_expired"`
	Result          *Result    `json:"result,omitempty"`
	ErrorCode       string     `json:"error_code,omitempty"`
}
type Result struct {
	TaskID             string           `json:"task_id"`
	Text               string           `json:"text"`
	Turns              int              `json:"turns"`
	FinishReason       string           `json:"finish_reason"`
	AuditID            string           `json:"audit_id"`
	AuditStatus        string           `json:"audit_status"`
	PreviousTaskIDs    []string         `json:"previous_task_ids"`
	RouteEstimatedCost *float64         `json:"route_estimated_cost,omitempty"`
	Usage              *providers.Usage `json:"usage,omitempty"`
}
type Claim struct {
	Status  Status          `json:"status"`
	Request json.RawMessage `json:"-"`
	Token   string          `json:"-"`
}

// BranchSourceFence binds a queued branch to one exact, completed source
// history. It is persisted inside the canonical submission envelope rather
// than maintained as mutable side state.
type BranchSourceFence struct {
	Version          int    `json:"version"`
	TaskID           string `json:"task_id"`
	SessionID        string `json:"session_id"`
	HeadSequence     int64  `json:"head_sequence"`
	HeadEventID      string `json:"head_event_id"`
	HistoryDigest    string `json:"history_digest"`
	SourcePrivacy    string `json:"source_privacy"`
	EffectivePrivacy string `json:"effective_privacy"`
}

func (f BranchSourceFence) Validate() error {
	digest, err := hex.DecodeString(f.HistoryDigest)
	if f.Version != 1 || !validID(f.TaskID) || !validID(f.SessionID) || !validID(f.HeadEventID) || f.HeadSequence < 1 || f.HeadSequence > 10000 || err != nil || len(digest) != 32 || strings.ToLower(f.HistoryDigest) != f.HistoryDigest {
		return ErrInvalid
	}
	if f.SourcePrivacy != "" && f.SourcePrivacy != "local_only" && f.SourcePrivacy != "cloud_allowed" {
		return ErrInvalid
	}
	if f.EffectivePrivacy != "local_only" && f.EffectivePrivacy != "cloud_allowed" {
		return ErrInvalid
	}
	if f.SourcePrivacy != "cloud_allowed" && f.EffectivePrivacy != "local_only" {
		return ErrInvalid
	}
	return nil
}
