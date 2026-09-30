package classification

import (
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

const (
	AttemptStarted   = "started"
	AttemptCompleted = "completed"
	AttemptFailed    = "failed"
	AttemptCanceled  = "canceled"

	CodeProviderFailed    = "provider_failed"
	CodeInvalidResponse   = "invalid_response"
	CodeTimeout           = "timeout"
	CodePersistenceFailed = "persistence_failed"
	CodeCanceled          = "canceled"
)

// Attempt is the durable, redacted lifecycle contract for auxiliary
// classification. RequestDigest binds the admitted input without retaining it;
// Decision is the only model-authored output that may be stored.
type Attempt struct {
	Version       int              `json:"version"`
	ID            string           `json:"id"`
	TaskID        string           `json:"task_id"`
	SessionID     string           `json:"session_id"`
	SubmissionID  string           `json:"submission_id,omitempty"`
	RequestDigest string           `json:"request_digest"`
	ConfigID      string           `json:"config_id"`
	Model         string           `json:"model"`
	Provider      string           `json:"provider"`
	EstimatedCost float64          `json:"estimated_cost"`
	Status        string           `json:"status"`
	Code          string           `json:"code,omitempty"`
	StartedAt     time.Time        `json:"started_at"`
	FinishedAt    time.Time        `json:"finished_at,omitempty"`
	Usage         *providers.Usage `json:"usage,omitempty"`
	Elapsed       time.Duration    `json:"elapsed"`
	Decision      *Decision        `json:"decision,omitempty"`
}

func (a Attempt) Validate() error {
	if a.Version != 1 || !identifier(a.ID) || !identifier(a.TaskID) || !identifier(a.SessionID) || a.SubmissionID != "" && !identifier(a.SubmissionID) || !digest(a.RequestDigest) || !digest(a.ConfigID) || !safeModelLabel(a.Model) || !safeLabel(a.Provider) || math.IsNaN(a.EstimatedCost) || math.IsInf(a.EstimatedCost, 0) || a.EstimatedCost < 0 || !utcTime(a.StartedAt) {
		return ErrInvalidInput
	}
	if a.Usage != nil && (a.Usage.InputTokens < 0 || a.Usage.OutputTokens < 0 || a.Usage.InputTokens > math.MaxInt64-a.Usage.OutputTokens) {
		return ErrInvalidInput
	}
	if a.Status == AttemptStarted {
		if a.Code != "" || !a.FinishedAt.IsZero() || a.Usage != nil || a.Elapsed != 0 || a.Decision != nil {
			return ErrInvalidInput
		}
		return nil
	}
	if !utcTime(a.FinishedAt) || a.FinishedAt.Before(a.StartedAt) || a.Elapsed < 0 || a.Elapsed > MaxClassifierTimeout || a.Elapsed > a.FinishedAt.Sub(a.StartedAt) {
		return ErrInvalidInput
	}
	switch a.Status {
	case AttemptCompleted:
		if a.Code != "" || a.Decision == nil || validateStoredDecision(*a.Decision) != nil {
			return ErrInvalidInput
		}
	case AttemptFailed:
		if a.Decision != nil || (a.Code != CodeProviderFailed && a.Code != CodeInvalidResponse && a.Code != CodeTimeout && a.Code != CodePersistenceFailed) {
			return ErrInvalidInput
		}
	case AttemptCanceled:
		if a.Decision != nil || a.Code != CodeCanceled || a.Usage != nil || a.Elapsed != 0 {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

func validateStoredDecision(decision Decision) error {
	if decision.Version != DecisionVersion || !safeLabel(decision.Domain) || decision.Capabilities == nil || len(decision.Capabilities) > MaxDecisionCapabilities {
		return ErrInvalidInput
	}
	for index, capability := range decision.Capabilities {
		if !safeLabel(capability) || index > 0 && decision.Capabilities[index-1] >= capability {
			return ErrInvalidInput
		}
	}
	return nil
}

func identifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || index > 0 && (b == '_' || b == '.' || b == '-') {
			continue
		}
		return false
	}
	return true
}

func digest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func utcTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && value.Year() >= 1970 && value.Year() < 2261 && offset == 0
}
