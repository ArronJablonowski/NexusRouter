// Package submissions defines durable asynchronous task intake and status.
package submissions

import (
	"encoding/json"
	"errors"
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
	TaskID          string           `json:"task_id"`
	Text            string           `json:"text"`
	Turns           int              `json:"turns"`
	FinishReason    string           `json:"finish_reason"`
	AuditID         string           `json:"audit_id"`
	AuditStatus     string           `json:"audit_status"`
	PreviousTaskIDs []string         `json:"previous_task_ids"`
	Usage           *providers.Usage `json:"usage,omitempty"`
}
type Claim struct {
	Status  Status          `json:"status"`
	Request json.RawMessage `json:"-"`
	Token   string          `json:"-"`
}
