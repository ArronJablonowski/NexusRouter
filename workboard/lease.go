package workboard

import (
	"encoding/hex"
	"time"
)

type LeaseState string

const (
	LeaseActive    LeaseState = "active"
	LeaseAttention LeaseState = "attention"
	LeaseReleased  LeaseState = "released"
)

type Lease struct {
	BoardID       string
	CardID        string
	AttemptID     string
	ClaimID       string
	Revision      int64
	State         LeaseState
	OwnerID       string
	LastHeartbeat time.Time
	ExpiresAt     time.Time
	ReleasedAt    *time.Time
}

func ValidateLease(lease Lease) error {
	if !validID(lease.BoardID) || !validID(lease.CardID) || !validID(lease.AttemptID) ||
		!validID(lease.ClaimID) || !validID(lease.OwnerID) || lease.Revision < 1 ||
		!validTime(lease.LastHeartbeat) || !validTime(lease.ExpiresAt) || !lease.ExpiresAt.After(lease.LastHeartbeat) ||
		lease.ExpiresAt.Sub(lease.LastHeartbeat) > MaxLeaseTTL {
		return fail(CodeInvalid, "lease")
	}
	switch lease.State {
	case LeaseActive, LeaseAttention:
		if lease.ReleasedAt != nil {
			return fail(CodeInvalid, "released_at")
		}
	case LeaseReleased:
		if lease.ReleasedAt == nil || !validTime(*lease.ReleasedAt) || lease.ReleasedAt.Before(lease.LastHeartbeat) {
			return fail(CodeInvalid, "released_at")
		}
	default:
		return fail(CodeInvalid, "lease_state")
	}
	return nil
}

type Heartbeat struct {
	OwnerID          string
	ExpectedRevision int64
	Now              time.Time
	TTL              time.Duration
}

func ApplyHeartbeat(lease Lease, heartbeat Heartbeat) (Lease, error) {
	if err := ValidateLease(lease); err != nil || !validID(heartbeat.OwnerID) || !validTime(heartbeat.Now) ||
		heartbeat.TTL < MinLeaseTTL || heartbeat.TTL > MaxLeaseTTL {
		return Lease{}, fail(CodeInvalid, "heartbeat")
	}
	if heartbeat.ExpectedRevision != lease.Revision {
		return Lease{}, fail(CodeStaleRevision, "claim_revision")
	}
	if heartbeat.OwnerID != lease.OwnerID {
		return Lease{}, fail(CodeLeaseOwner, "owner")
	}
	if heartbeat.Now.Before(lease.LastHeartbeat) {
		return Lease{}, fail(CodeInvalid, "heartbeat_time")
	}
	if lease.State == LeaseReleased || !heartbeat.Now.Before(lease.ExpiresAt) {
		return Lease{}, fail(CodeLeaseExpired, "claim")
	}
	nextExpiry := heartbeat.Now.Add(heartbeat.TTL).UTC()
	if lease.ExpiresAt.After(nextExpiry) {
		nextExpiry = lease.ExpiresAt
	}
	lease.Revision++
	lease.State = LeaseActive
	lease.LastHeartbeat = heartbeat.Now.UTC()
	lease.ExpiresAt = nextExpiry
	return lease, nil
}

func MarkAttention(lease Lease, expectedRevision int64, now time.Time) (Lease, error) {
	if err := ValidateLease(lease); err != nil || !validTime(now) {
		return Lease{}, fail(CodeInvalid, "attention")
	}
	if expectedRevision != lease.Revision {
		return Lease{}, fail(CodeStaleRevision, "claim_revision")
	}
	if lease.State != LeaseActive || now.Before(lease.ExpiresAt) {
		return Lease{}, fail(CodeInvalid, "attention")
	}
	lease.Revision++
	lease.State = LeaseAttention
	return lease, nil
}

// MarkAttentionFromObservation records a supervisor observation without
// releasing ownership or implying that execution has stopped. A claim is
// eligible when its lease expired or its last heartbeat crossed the configured
// stale boundary.
func MarkAttentionFromObservation(lease Lease, expectedRevision int64, now, staleBefore time.Time) (Lease, error) {
	if err := ValidateLease(lease); err != nil || !validTime(now) || !validTime(staleBefore) || staleBefore.After(now) {
		return Lease{}, fail(CodeInvalid, "attention")
	}
	if expectedRevision != lease.Revision {
		return Lease{}, fail(CodeStaleRevision, "claim_revision")
	}
	if lease.State != LeaseActive || now.Before(lease.ExpiresAt) && lease.LastHeartbeat.After(staleBefore) {
		return Lease{}, fail(CodeInvalid, "attention")
	}
	lease.Revision++
	lease.State = LeaseAttention
	return lease, nil
}

type EffectResolution string

const (
	EffectFree       EffectResolution = "effect_free"
	ResolvedNoReplay EffectResolution = "resolved_no_replay"
)

type RecoveryProof struct {
	StopProofID          string
	TaskHeadDigest       string
	ProcessProofDigest   string
	EffectEvidenceDigest string
	EffectResolution     EffectResolution
	TaskTerminal         bool
	ProcessStopped       bool
}

type Recovery struct {
	ExpectedRevision int64
	Now              time.Time
	Proof            RecoveryProof
}

type RecoveryResult struct {
	Lease            Lease
	ResultingState   State
	NoReplayRequired bool
}

type WorkerFailure struct {
	OwnerID          string
	ExpectedRevision int64
	Now              time.Time
	EffectResolution EffectResolution
}

// ApplyWorkerFailure releases only a claim still owned by the worker when the
// host has positively classified the failed execution as effect-free. It does
// not infer safety from a timeout, cancellation, or missing acknowledgement.
func ApplyWorkerFailure(lease Lease, failure WorkerFailure) (Lease, error) {
	if err := ValidateLease(lease); err != nil || !validID(failure.OwnerID) || !validTime(failure.Now) ||
		failure.EffectResolution != EffectFree {
		return Lease{}, fail(CodeInvalid, "failure")
	}
	if failure.ExpectedRevision != lease.Revision {
		return Lease{}, fail(CodeStaleRevision, "claim_revision")
	}
	if failure.OwnerID != lease.OwnerID {
		return Lease{}, fail(CodeLeaseOwner, "owner")
	}
	if lease.State == LeaseReleased || failure.Now.Before(lease.LastHeartbeat) {
		return Lease{}, fail(CodeIllegalTransition, "claim")
	}
	released := failure.Now.UTC()
	lease.Revision++
	lease.State = LeaseReleased
	lease.ReleasedAt = &released
	return lease, nil
}

func ApplyRecovery(lease Lease, recovery Recovery) (RecoveryResult, error) {
	if err := ValidateLease(lease); err != nil || !validTime(recovery.Now) {
		return RecoveryResult{}, fail(CodeInvalid, "recovery")
	}
	if recovery.ExpectedRevision != lease.Revision {
		return RecoveryResult{}, fail(CodeStaleRevision, "claim_revision")
	}
	if recovery.Now.Before(lease.LastHeartbeat) {
		return RecoveryResult{}, fail(CodeInvalid, "recovery_time")
	}
	proof := recovery.Proof
	if lease.State == LeaseReleased || !validID(proof.StopProofID) || !digest(proof.TaskHeadDigest) ||
		!digest(proof.ProcessProofDigest) || !digest(proof.EffectEvidenceDigest) ||
		!proof.TaskTerminal || !proof.ProcessStopped ||
		(proof.EffectResolution != EffectFree && proof.EffectResolution != ResolvedNoReplay) {
		return RecoveryResult{}, fail(CodeUnsafeRecovery, "proof")
	}
	released := recovery.Now.UTC()
	lease.Revision++
	lease.State = LeaseReleased
	lease.ReleasedAt = &released
	return RecoveryResult{Lease: lease, ResultingState: Ready, NoReplayRequired: true}, nil
}

func digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func validTime(value time.Time) bool {
	_, offset := value.Zone()
	return value.Year() >= 1970 && value.Year() < 2261 && offset == 0
}
