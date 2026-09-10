package workboard

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type boardAuthorityStub struct {
	authority Authority
	err       error
}

func (s boardAuthorityStub) WorkboardAuthority(context.Context) (Authority, error) {
	return s.authority, s.err
}

type boardRepositoryStub struct {
	creates  atomic.Int64
	revises  atomic.Int64
	archives atomic.Int64
	lists    atomic.Int64
	reads    atomic.Int64
	events   atomic.Int64
	reviser  Actor
}

func (s *boardRepositoryStub) ReviseWorkboard(_ context.Context, _ ReviseBoardRequest, actor Actor, _ time.Time) (OperationReceipt, error) {
	s.revises.Add(1)
	s.reviser = actor
	return OperationReceipt{}, nil
}

func TestBoardServiceReviseUsesTrustedAuthority(t *testing.T) {
	repository := &boardRepositoryStub{}
	trusted := Actor{ID: "operator-from-session", Type: "operator"}
	service, err := NewBoardService(repository, boardAuthorityStub{authority: Authority{CreationScope: "session", Actor: trusted}}, func() time.Time {
		return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	title := "Revised"
	request := ReviseBoardRequest{Version: 1, BoardID: "board", IdempotencyKey: "request-key-0001", ExpectedRevision: 1, Title: &title}
	if _, err = service.Revise(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if repository.reviser != trusted || repository.revises.Load() != 1 {
		t.Fatalf("trusted actor not forwarded: %+v", repository.reviser)
	}
}

func (s *boardRepositoryStub) CreateWorkboard(context.Context, string, CreateBoardRequest, Actor, time.Time) (OperationReceipt, error) {
	s.creates.Add(1)
	return OperationReceipt{}, nil
}
func (s *boardRepositoryStub) ArchiveWorkboard(context.Context, ArchiveBoardRequest, Actor, time.Time) (OperationReceipt, error) {
	s.archives.Add(1)
	return OperationReceipt{}, nil
}
func (s *boardRepositoryStub) ListWorkboards(context.Context, BoardListOptions) (BoardPage, error) {
	s.lists.Add(1)
	return BoardPage{Version: 1}, nil
}
func (s *boardRepositoryStub) ReadWorkboard(context.Context, string, BoardSnapshotOptions) (BoardSnapshot, error) {
	s.reads.Add(1)
	return BoardSnapshot{}, nil
}
func (s *boardRepositoryStub) ListWorkboardEvents(context.Context, string, BoardEventOptions) (BoardEventPage, error) {
	s.events.Add(1)
	return BoardEventPage{Version: 1, BoardID: "board", HighWaterSequence: 1}, nil
}

func TestBoardServiceDeniesReadsBeforeRepository(t *testing.T) {
	denied := errors.New("denied")
	for name, authority := range map[string]boardAuthorityStub{
		"denied":  {err: denied},
		"invalid": {authority: Authority{CreationScope: "", Actor: Actor{ID: "operator", Type: "operator"}}},
	} {
		t.Run(name, func(t *testing.T) {
			repository := &boardRepositoryStub{}
			service, err := NewBoardService(repository, authority, func() time.Time { return time.Now().UTC() })
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.List(context.Background(), BoardListOptions{Limit: 1}); err == nil {
				t.Fatal("list authority failure accepted")
			}
			if _, err = service.Read(context.Background(), "board", BoardSnapshotOptions{Limit: 1}); err == nil {
				t.Fatal("read authority failure accepted")
			}
			if _, err = service.Events(context.Background(), "board", BoardEventOptions{Limit: 1}); err == nil {
				t.Fatal("event authority failure accepted")
			}
			if repository.lists.Load() != 0 || repository.reads.Load() != 0 || repository.events.Load() != 0 {
				t.Fatalf("repository invoked: lists=%d reads=%d events=%d", repository.lists.Load(), repository.reads.Load(), repository.events.Load())
			}
		})
	}
}

func TestBoardServiceDeniesMutationsBeforeRepository(t *testing.T) {
	denied := errors.New("denied")
	tests := []struct {
		name      string
		authority boardAuthorityStub
	}{
		{name: "authority denial", authority: boardAuthorityStub{err: denied}},
		{name: "invalid authority", authority: boardAuthorityStub{authority: Authority{
			CreationScope: "creation-scope", Actor: Actor{ID: "operator", Type: "invalid"},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &boardRepositoryStub{}
			service, err := NewBoardService(repository, test.authority, func() time.Time {
				return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
			})
			if err != nil {
				t.Fatal(err)
			}
			create := CreateBoardRequest{Version: SchemaVersion, IdempotencyKey: "request-key-0001", Title: "Board"}
			if _, err = service.Create(context.Background(), create); err == nil {
				t.Fatal("create authority failure accepted")
			}
			archive := ArchiveBoardRequest{Version: SchemaVersion, BoardID: "board", IdempotencyKey: "request-key-0002", ExpectedRevision: 1}
			if _, err = service.Archive(context.Background(), archive); err == nil {
				t.Fatal("archive authority failure accepted")
			}
			title := "Revised"
			revise := ReviseBoardRequest{Version: SchemaVersion, BoardID: "board", IdempotencyKey: "request-key-0003", ExpectedRevision: 1, Title: &title}
			if _, err = service.Revise(context.Background(), revise); err == nil {
				t.Fatal("revise authority failure accepted")
			}
			if repository.creates.Load() != 0 || repository.revises.Load() != 0 || repository.archives.Load() != 0 {
				t.Fatalf("repository mutation invoked: creates=%d revises=%d archives=%d", repository.creates.Load(), repository.revises.Load(), repository.archives.Load())
			}
		})
	}
}
