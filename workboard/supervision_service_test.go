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

type supervisionRepositoryStub struct {
	queries int
	last    SupervisionQuery
	page    SupervisionPage
}

func (r *supervisionRepositoryStub) ReadSupervisionPage(_ context.Context, query SupervisionQuery) (SupervisionPage, error) {
	r.queries++
	r.last = query
	return r.page, nil
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

func TestSupervisionServiceBindsObservationAndBounds(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	repository := &supervisionRepositoryStub{page: SupervisionPage{Version: 1, BoardID: "board-a", BoardRevision: 1, ObservedAt: now, Items: []SupervisionItem{}}}
	service, err := NewSupervisionService(repository, boardAuthorityStub{authority: Authority{
		CreationScope: "inspection", Actor: Actor{ID: "operator", Type: "operator"},
	}}, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Read(context.Background(), "board-a", SupervisionOptions{Limit: 25})
	if err != nil || page.Validate() != nil || repository.queries != 1 || repository.last.BoardID != "board-a" || repository.last.Limit != 25 ||
		!repository.last.ObservedAt.Equal(now) || !repository.last.StaleBefore.Equal(now.Add(-time.Minute)) {
		t.Fatalf("query=%+v page=%+v err=%v", repository.last, page, err)
	}
	if _, err = service.Read(context.Background(), "board-a", SupervisionOptions{Limit: MaxSupervisionPageItems + 1}); err == nil || repository.queries != 1 {
		t.Fatalf("unbounded read dispatched: calls=%d err=%v", repository.queries, err)
	}
}

func TestSupervisionProjectionValidationRejectsUnsafeActions(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	running := SupervisionItem{Version: 1, BoardID: "board-a", CardID: "card-a", CardRevision: 2,
		State: SupervisionRunning, Reason: SupervisionLeaseHealthy, AttemptID: "attempt-a", ClaimID: "claim-a",
		ClaimRevision: 1, WorkerID: "worker-a", LastHeartbeat: now, ExpiresAt: now.Add(time.Minute),
		Actions: SupervisionActions{PauseRequest: true, CancelRequest: true}}
	if err := running.Validate(); err != nil {
		t.Fatal(err)
	}
	contradictory := running
	contradictory.AssigneeID = "worker-b"
	if contradictory.Validate() == nil {
		t.Fatal("running projection admitted contradictory assignee and worker identities")
	}
	unsafe := running
	unsafe.Actions.RecoveryCheck = true
	if unsafe.Validate() == nil {
		t.Fatal("healthy running claim advertised recovery")
	}
	orphaned := running
	orphaned.State, orphaned.Reason, orphaned.TaskID = SupervisionOrphaned, SupervisionTaskFailed, "task-a"
	orphaned.Actions = SupervisionActions{CancelRequest: true, RecoveryCheck: true}
	if err := orphaned.Validate(); err != nil {
		t.Fatal(err)
	}
	orphaned.TaskID = ""
	if orphaned.Validate() == nil {
		t.Fatal("orphaned state lacked a terminal task binding")
	}
}
