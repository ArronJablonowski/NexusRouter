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
	archives atomic.Int64
	lists    atomic.Int64
	reads    atomic.Int64
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
			if repository.lists.Load() != 0 || repository.reads.Load() != 0 {
				t.Fatalf("repository invoked: lists=%d reads=%d", repository.lists.Load(), repository.reads.Load())
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
			if repository.creates.Load() != 0 || repository.archives.Load() != 0 {
				t.Fatalf("repository mutation invoked: creates=%d archives=%d", repository.creates.Load(), repository.archives.Load())
			}
		})
	}
}
