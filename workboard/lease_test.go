package workboard

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func leaseFixture() Lease {
	now := time.Date(2026, 9, 9, 18, 0, 0, 0, time.UTC)
	return Lease{BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", ClaimID: "claim-a", Revision: 2, State: LeaseActive, OwnerID: "worker-a", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute)}
}

func TestHeartbeatUsesStrictExpiryAndFences(t *testing.T) {
	lease := leaseFixture()
	valid := Heartbeat{OwnerID: lease.OwnerID, ExpectedRevision: lease.Revision, Now: lease.LastHeartbeat.Add(30 * time.Second), TTL: time.Minute}
	updated, err := ApplyHeartbeat(lease, valid)
	if err != nil || updated.Revision != 3 || !updated.LastHeartbeat.Equal(valid.Now) || !updated.ExpiresAt.Equal(valid.Now.Add(time.Minute)) {
		t.Fatalf("lease=%+v error=%v", updated, err)
	}
	for _, test := range []struct {
		name string
		edit func(*Heartbeat)
		code ErrorCode
	}{
		{"stale", func(h *Heartbeat) { h.ExpectedRevision-- }, CodeStaleRevision},
		{"owner", func(h *Heartbeat) { h.OwnerID = "worker-b" }, CodeLeaseOwner},
		{"at expiry", func(h *Heartbeat) { h.Now = lease.ExpiresAt }, CodeLeaseExpired},
		{"after expiry", func(h *Heartbeat) { h.Now = lease.ExpiresAt.Add(time.Nanosecond) }, CodeLeaseExpired},
		{"clock regression", func(h *Heartbeat) { h.Now = lease.LastHeartbeat.Add(-time.Second) }, CodeInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := valid
			test.edit(&h)
			_, err := ApplyHeartbeat(lease, h)
			if !errors.Is(err, &Violation{Code: test.code}) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	lease.ExpiresAt = lease.LastHeartbeat
	if ValidateLease(lease) == nil {
		t.Fatal("zero-duration lease accepted")
	}
	lease.ExpiresAt = lease.LastHeartbeat.Add(MaxLeaseTTL)
	if ValidateLease(lease) != nil {
		t.Fatal("maximum lease TTL rejected")
	}
	lease.ExpiresAt = lease.LastHeartbeat.Add(MaxLeaseTTL + time.Nanosecond)
	if ValidateLease(lease) == nil {
		t.Fatal("unbounded stored lease accepted")
	}
}

func TestExpiryMarksAttentionWithoutReleasingOwnership(t *testing.T) {
	lease := leaseFixture()
	if _, err := MarkAttention(lease, lease.Revision, lease.ExpiresAt.Add(-time.Nanosecond)); err == nil {
		t.Fatal("live lease marked attention")
	}
	attention, err := MarkAttention(lease, lease.Revision, lease.ExpiresAt)
	if err != nil || attention.State != LeaseAttention || attention.OwnerID != lease.OwnerID || attention.ReleasedAt != nil || attention.Revision != 3 {
		t.Fatalf("attention=%+v error=%v", attention, err)
	}
	if _, err = ApplyHeartbeat(attention, Heartbeat{OwnerID: lease.OwnerID, ExpectedRevision: 3, Now: lease.ExpiresAt, TTL: time.Minute}); !errors.Is(err, &Violation{Code: CodeLeaseExpired}) {
		t.Fatalf("expired attention revived: %v", err)
	}
}

func TestWorkerFailureReleasesOnlyOwnedEffectFreeLease(t *testing.T) {
	lease := leaseFixture()
	failure := WorkerFailure{OwnerID: lease.OwnerID, ExpectedRevision: lease.Revision,
		Now: lease.LastHeartbeat.Add(time.Second), EffectResolution: EffectFree}
	released, err := ApplyWorkerFailure(lease, failure)
	if err != nil || released.State != LeaseReleased || released.Revision != lease.Revision+1 || released.ReleasedAt == nil ||
		!released.ReleasedAt.Equal(failure.Now) {
		t.Fatalf("released=%+v err=%v", released, err)
	}
	for _, test := range []struct {
		name string
		edit func(*WorkerFailure)
		code ErrorCode
	}{
		{"stale", func(f *WorkerFailure) { f.ExpectedRevision-- }, CodeStaleRevision},
		{"wrong owner", func(f *WorkerFailure) { f.OwnerID = "worker-b" }, CodeLeaseOwner},
		{"uncertain effect", func(f *WorkerFailure) { f.EffectResolution = "uncertain" }, CodeInvalid},
		{"resolved effect", func(f *WorkerFailure) { f.EffectResolution = ResolvedNoReplay }, CodeInvalid},
		{"clock regression", func(f *WorkerFailure) { f.Now = lease.LastHeartbeat.Add(-time.Second) }, CodeIllegalTransition},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := failure
			test.edit(&candidate)
			if _, err := ApplyWorkerFailure(lease, candidate); !errors.Is(err, &Violation{Code: test.code}) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func recoveryFixture() Recovery {
	return Recovery{
		ExpectedRevision: 2,
		Now:              leaseFixture().ExpiresAt.Add(time.Second),
		Proof: RecoveryProof{
			StopProofID: "stop-proof", TaskHeadDigest: strings.Repeat("a", 64),
			ProcessProofDigest: strings.Repeat("b", 64), EffectEvidenceDigest: strings.Repeat("c", 64),
			EffectResolution: EffectFree, TaskTerminal: true, ProcessStopped: true,
		},
	}
}

func TestRecoveryRequiresIndependentSafeProofAndNeverReplays(t *testing.T) {
	lease := leaseFixture()
	recovery := recoveryFixture()
	result, err := ApplyRecovery(lease, recovery)
	if err != nil || result.ResultingState != Ready || !result.NoReplayRequired || result.Lease.State != LeaseReleased || result.Lease.Revision != 3 || result.Lease.ReleasedAt == nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	tests := []struct {
		name string
		edit func(*Recovery)
		code ErrorCode
	}{
		{"stale revision", func(r *Recovery) { r.ExpectedRevision-- }, CodeStaleRevision},
		{"task running", func(r *Recovery) { r.Proof.TaskTerminal = false }, CodeUnsafeRecovery},
		{"process running", func(r *Recovery) { r.Proof.ProcessStopped = false }, CodeUnsafeRecovery},
		{"uncertain effect", func(r *Recovery) { r.Proof.EffectResolution = "uncertain" }, CodeUnsafeRecovery},
		{"bad task digest", func(r *Recovery) { r.Proof.TaskHeadDigest = "secret" }, CodeUnsafeRecovery},
		{"clock regression", func(r *Recovery) { r.Now = lease.LastHeartbeat.Add(-time.Second) }, CodeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := recovery
			test.edit(&r)
			_, err := ApplyRecovery(lease, r)
			if !errors.Is(err, &Violation{Code: test.code}) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
