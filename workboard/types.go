// Package workboard defines deterministic domain rules for durable workboards.
// It has no persistence or transport dependencies; stores are responsible for
// applying successful results with compare-and-swap updates in one transaction.
package workboard

import (
	"fmt"
	"time"
)

const (
	MaxIdentifierBytes = 128
	MaxDependencies    = 64
	MaxReverseFanout   = 64
	MaxGraphDepth      = 64
	MaxGraphVisits     = 10_000
	MaxCardsPerBoard   = 10_000
	MaxLeaseTTL        = 10 * time.Minute
	MinLeaseTTL        = time.Millisecond
)

type ErrorCode string

const (
	CodeInvalid           ErrorCode = "invalid"
	CodeStaleRevision     ErrorCode = "stale_revision"
	CodeIllegalTransition ErrorCode = "illegal_transition"
	CodeMissingNode       ErrorCode = "missing_node"
	CodeCrossBoard        ErrorCode = "cross_board"
	CodeCycle             ErrorCode = "cycle"
	CodeDepthExhausted    ErrorCode = "depth_exhausted"
	CodeVisitsExhausted   ErrorCode = "visits_exhausted"
	CodeLimitExceeded     ErrorCode = "limit_exceeded"
	CodeLeaseExpired      ErrorCode = "lease_expired"
	CodeLeaseOwner        ErrorCode = "lease_owner_mismatch"
	CodeUnsafeRecovery    ErrorCode = "unsafe_recovery"
)

// Violation is safe to branch on and deliberately does not echo input values.
type Violation struct {
	Code  ErrorCode
	Field string
}

func (e *Violation) Error() string {
	if e == nil {
		return ""
	}
	if e.Field == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Field)
}

func (e *Violation) Is(target error) bool {
	other, ok := target.(*Violation)
	return ok && e != nil && e.Code == other.Code
}

func fail(code ErrorCode, field string) error { return &Violation{Code: code, Field: field} }

type State string

const (
	Backlog    State = "backlog"
	Ready      State = "ready"
	InProgress State = "in_progress"
	Blocked    State = "blocked"
	Review     State = "review"
	Done       State = "done"
	Canceled   State = "canceled"
)

func validState(state State) bool {
	switch state {
	case Backlog, Ready, InProgress, Blocked, Review, Done, Canceled:
		return true
	default:
		return false
	}
}

type Command string

const (
	Move            Command = "card.move"
	ClaimCard       Command = "card.claim"
	BlockCard       Command = "card.block"
	UnblockCard     Command = "card.unblock"
	RecoverClaim    Command = "claim.recover"
	SubmitCandidate Command = "candidate.submit"
	AcceptCandidate Command = "acceptance.accept"
	RejectCandidate Command = "acceptance.reject"
	FinalizeCancel  Command = "card.cancel_finalize"
)

func validID(value string) bool {
	if len(value) < 1 || len(value) > MaxIdentifierBytes {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
