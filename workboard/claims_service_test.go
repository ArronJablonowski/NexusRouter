package workboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeLifecycleRepository struct {
	mutations []LifecycleMutation
	replay    OperationReceipt
	found     bool
}

func (f *fakeLifecycleRepository) ApplyLifecycleMutation(_ context.Context, mutation LifecycleMutation) (OperationReceipt, error) {
	f.mutations = append(f.mutations, mutation)
	receipt := lifecycleReceipt(mutation)
	return receipt, nil
}

func (f *fakeLifecycleRepository) ReplayLifecycleMutation(_ context.Context, mutation LifecycleMutation) (OperationReceipt, bool, error) {
	if f.found {
		return f.replay, true, nil
	}
	return OperationReceipt{}, false, nil
}

type fakeRecoveryVerifier struct {
	proof RecoveryProof
	err   error
	calls int
}

func (f *fakeRecoveryVerifier) VerifyRecovery(context.Context, RecoverClaimRequest, Actor) (RecoveryProof, error) {
	f.calls++
	return f.proof, f.err
}

func lifecycleServiceFor(t *testing.T, actor Actor, repository *fakeLifecycleRepository, verifier *fakeRecoveryVerifier, now time.Time) *LifecycleService {
	t.Helper()
	service, err := NewLifecycleService(repository, boardAuthorityStub{authority: Authority{CreationScope: "session", Actor: actor}}, verifier,
		func() time.Time { return now }, time.Minute, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func lifecycleReceipt(m LifecycleMutation) OperationReceipt {
	cardRevision, claimRevision := m.ExpectedCardRevision, m.ExpectedClaimRevision
	if cardRevision == 0 {
		cardRevision = 1
	}
	if claimRevision == 0 {
		claimRevision = 1
	}
	return OperationReceipt{Version: 1, BoardID: m.BoardID, OperationID: "operation-key-01", RequestDigest: m.RequestDigest,
		ResponseDigest: strings.Repeat("b", 64), FirstSequence: 1, LastSequence: 1, EventCount: 1, TransactionBytes: 1,
		BoardRevision: 1, CardID: m.CardID, CardRevision: &cardRevision, ClaimRevision: &claimRevision, Outcome: "committed", CreatedAt: m.Now}
}

func TestLifecycleServiceClaimAndHeartbeatBindWorkerAuthority(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	repository := &fakeLifecycleRepository{}
	service := lifecycleServiceFor(t, Actor{ID: "worker-a", Type: "worker"}, repository, &fakeRecoveryVerifier{}, now)
	if _, err := service.Claim(context.Background(), ClaimRequest{BoardID: "board-a", CardID: "card-a", IdempotencyKey: "claim-key-000001", ExpectedCardRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if len(repository.mutations) != 1 || repository.mutations[0].Actor.ID != "worker-a" || repository.mutations[0].PolicyDigest != strings.Repeat("a", 64) || repository.mutations[0].LeaseTTL != time.Minute {
		t.Fatalf("claim mutation=%+v", repository.mutations)
	}
	if _, err := service.Heartbeat(context.Background(), HeartbeatRequest{BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", ClaimID: "claim-a", IdempotencyKey: "heartbeat-key-01", ExpectedClaimRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if len(repository.mutations) != 2 || repository.mutations[1].Kind != LifecycleHeartbeat || repository.mutations[1].ExpectedClaimRevision != 1 {
		t.Fatalf("heartbeat mutation=%+v", repository.mutations)
	}
	operator := lifecycleServiceFor(t, Actor{ID: "operator", Type: "operator"}, repository, &fakeRecoveryVerifier{}, now)
	if _, err := operator.Claim(context.Background(), ClaimRequest{BoardID: "board-a", CardID: "card-a", IdempotencyKey: "claim-key-000002", ExpectedCardRevision: 2}); err == nil {
		t.Fatal("operator claimed worker lease")
	}
}

func TestLifecycleRecoveryRequiresIndependentVerifiedProof(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	intent := RecoveryIntent{StopProofID: "proof-a", TaskHeadDigest: strings.Repeat("1", 64), ProcessProofDigest: strings.Repeat("2", 64),
		EffectEvidenceDigest: strings.Repeat("3", 64), EffectResolution: EffectFree}
	request := RecoverClaimRequest{BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", ClaimID: "claim-a", IdempotencyKey: "recover-key-0001",
		ExpectedCardRevision: 2, ExpectedClaimRevision: 3, Proof: intent}
	repository := &fakeLifecycleRepository{}
	verifier := &fakeRecoveryVerifier{proof: RecoveryProof{StopProofID: intent.StopProofID, TaskHeadDigest: intent.TaskHeadDigest,
		ProcessProofDigest: intent.ProcessProofDigest, EffectEvidenceDigest: intent.EffectEvidenceDigest, EffectResolution: intent.EffectResolution,
		TaskTerminal: true, ProcessStopped: true}}
	service := lifecycleServiceFor(t, Actor{ID: "supervisor", Type: "system"}, repository, verifier, now)
	if _, err := service.Recover(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 1 || len(repository.mutations) != 1 || repository.mutations[0].Verified == nil {
		t.Fatalf("verification calls=%d mutations=%+v", verifier.calls, repository.mutations)
	}
	bad := *verifier
	bad.proof.ProcessStopped = false
	service = lifecycleServiceFor(t, Actor{ID: "supervisor", Type: "system"}, &fakeLifecycleRepository{}, &bad, now)
	if _, err := service.Recover(context.Background(), request); !errors.Is(err, &Violation{Code: CodeUnsafeRecovery}) {
		t.Fatalf("unsafe recovery error=%v", err)
	}
}

func TestLifecycleRecoveryExactReplayPrecedesVerification(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	intent := RecoveryIntent{StopProofID: "proof-a", TaskHeadDigest: strings.Repeat("1", 64), ProcessProofDigest: strings.Repeat("2", 64),
		EffectEvidenceDigest: strings.Repeat("3", 64), EffectResolution: ResolvedNoReplay}
	request := RecoverClaimRequest{BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", ClaimID: "claim-a", IdempotencyKey: "recover-replay-01",
		ExpectedCardRevision: 2, ExpectedClaimRevision: 3, Proof: intent}
	mutation := LifecycleMutation{Version: 1, Kind: LifecycleRecover, BoardID: request.BoardID, CardID: request.CardID, AttemptID: request.AttemptID,
		ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey, Actor: Actor{ID: "supervisor", Type: "system"},
		ExpectedCardRevision: 2, ExpectedClaimRevision: 3, Recovery: &intent, Now: now}
	mutation.RequestDigest, _ = LifecycleDigest(mutation)
	repository := &fakeLifecycleRepository{found: true, replay: lifecycleReceipt(mutation)}
	verifier := &fakeRecoveryVerifier{err: errors.New("verifier unavailable")}
	service := lifecycleServiceFor(t, mutation.Actor, repository, verifier, now.Add(time.Hour))
	if _, err := service.Recover(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 0 || len(repository.mutations) != 0 {
		t.Fatalf("replay invoked verifier=%d apply=%d", verifier.calls, len(repository.mutations))
	}
	mutation.Now = now.Add(time.Hour)
	got, _ := LifecycleDigest(mutation)
	if got != mutation.RequestDigest {
		t.Fatal("execution time changed semantic digest")
	}
}
