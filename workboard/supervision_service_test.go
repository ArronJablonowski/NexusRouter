package workboard

import (
	"context"
	"testing"
	"time"
)

type attentionRepositoryStub struct {
	scans int
	last  AttentionScan
	items []ClaimAttention
}

func (r *attentionRepositoryStub) ObserveClaimAttention(_ context.Context, scan AttentionScan) ([]ClaimAttention, error) {
	r.scans++
	r.last = scan
	return r.items, nil
}

func (r *attentionRepositoryStub) ListClaimAttention(context.Context, string, int) ([]ClaimAttention, error) {
	return r.items, nil
}

func TestAttentionServiceBindsSystemAuthorityAndBounds(t *testing.T) {
	now := time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)
	repository := &attentionRepositoryStub{}
	service, err := NewAttentionService(repository, boardAuthorityStub{authority: Authority{CreationScope: "session", Actor: Actor{ID: "supervisor", Type: "system"}}},
		func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.Scan(context.Background(), "board-a", 25)
	if err != nil || len(items) != 0 || repository.scans != 1 || repository.last.Actor.ID != "supervisor" ||
		!repository.last.ObservedAt.Equal(now) || !repository.last.StaleBefore.Equal(now.Add(-time.Minute)) || repository.last.Limit != 25 {
		t.Fatalf("scan=%+v items=%+v err=%v", repository.last, items, err)
	}
	if _, err = service.Scan(context.Background(), "board-a", MaxAttentionScanClaims+1); err == nil || repository.scans != 1 {
		t.Fatalf("unbounded scan dispatched: calls=%d err=%v", repository.scans, err)
	}
	operator, err := NewAttentionService(repository, boardAuthorityStub{authority: Authority{CreationScope: "session", Actor: Actor{ID: "operator", Type: "operator"}}},
		func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = operator.Scan(context.Background(), "board-a", 1); err == nil || repository.scans != 1 {
		t.Fatal("operator dispatched supervision scan")
	}
	if _, err = operator.List(context.Background(), "board-a", 1); err != nil {
		t.Fatal("operator attention read rejected", err)
	}
}

func TestAttentionObservationDoesNotReleaseOrReassign(t *testing.T) {
	heartbeat := time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)
	lease := Lease{BoardID: "board-a", CardID: "card-a", AttemptID: "attempt-a", ClaimID: "claim-a", Revision: 3,
		State: LeaseActive, OwnerID: "worker-a", LastHeartbeat: heartbeat, ExpiresAt: heartbeat.Add(10 * time.Minute)}
	attention, err := MarkAttentionFromObservation(lease, 3, heartbeat.Add(2*time.Minute), heartbeat.Add(time.Minute))
	if err != nil || attention.State != LeaseAttention || attention.Revision != 4 || attention.OwnerID != lease.OwnerID ||
		attention.ReleasedAt != nil || !attention.ExpiresAt.Equal(lease.ExpiresAt) {
		t.Fatalf("attention=%+v err=%v", attention, err)
	}
	if _, err = MarkAttentionFromObservation(lease, 3, heartbeat.Add(30*time.Second), heartbeat.Add(-30*time.Second)); err == nil {
		t.Fatal("fresh live claim marked attention")
	}
}
