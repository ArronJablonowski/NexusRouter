package workboard

import (
	"context"
	"strings"
	"testing"
	"time"
)

type modelControlRepository struct {
	mutations []ControlMutation
}

type modelControlVerifier struct{}

func (modelControlVerifier) VerifyControlStop(context.Context, FinalizeCancelRequest, Actor) (RecoveryProof, error) {
	return RecoveryProof{}, nil
}

func (r *modelControlRepository) ApplyControlMutation(_ context.Context, mutation ControlMutation) (OperationReceipt, error) {
	r.mutations = append(r.mutations, mutation)
	revision := mutation.ExpectedCardRevision + 1
	return OperationReceipt{Version: SchemaVersion, BoardID: mutation.BoardID, OperationID: "control-operation", RequestDigest: mutation.RequestDigest,
		ResponseDigest: strings.Repeat("a", 64), FirstSequence: 1, LastSequence: 1, EventCount: 1, TransactionBytes: 1,
		BoardRevision: 2, CardID: mutation.CardID, CardRevision: &revision, Outcome: "committed", CreatedAt: mutation.Now}, nil
}

func (*modelControlRepository) ReplayControlMutation(context.Context, ControlMutation) (OperationReceipt, bool, error) {
	return OperationReceipt{}, false, nil
}

func TestControlRequestsAcceptTrustedModelButFinalizationDoesNot(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	repository := &modelControlRepository{}
	model := Actor{ID: "task-bound-model", Type: "model"}
	service, err := NewControlService(repository, boardAuthorityStub{authority: Authority{CreationScope: "root-agent-tools", Actor: model}},
		modelControlVerifier{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := RequestCardControl{BoardID: "board-a", CardID: "card-a", IdempotencyKey: "model-pause-key-01", ExpectedCardRevision: 3}
	if _, err = service.RequestPause(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "model-cancel-key-1"
	if _, err = service.RequestCancel(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(repository.mutations) != 2 || repository.mutations[0].Actor != model || repository.mutations[1].Actor != model {
		t.Fatalf("mutations = %+v", repository.mutations)
	}
	finalize := FinalizeCancelRequest{BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", ClaimID: "claim-a",
		IdempotencyKey: "model-finalize-key", ExpectedCardRevision: 4, ExpectedClaimRevision: 1,
		Proof: RecoveryIntent{StopProofID: "proof-a", TaskHeadDigest: strings.Repeat("1", 64), ProcessProofDigest: strings.Repeat("2", 64),
			EffectEvidenceDigest: strings.Repeat("3", 64), EffectResolution: EffectFree}}
	if _, err = service.FinalizeCancel(context.Background(), finalize); err == nil {
		t.Fatal("model actor finalized cancellation")
	}
	for _, actorType := range []string{"worker", "system"} {
		blocked, createErr := NewControlService(repository, boardAuthorityStub{authority: Authority{CreationScope: "scope", Actor: Actor{ID: actorType, Type: actorType}}},
			modelControlVerifier{}, func() time.Time { return now })
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, requestErr := blocked.RequestPause(context.Background(), request); requestErr == nil {
			t.Fatalf("%s actor requested pause", actorType)
		}
	}
}
