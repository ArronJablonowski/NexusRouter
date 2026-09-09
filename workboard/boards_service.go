package workboard

import (
	"context"
	"time"
)

// AuthoritySource derives trusted workboard attribution from application
// context. Browser/native payloads never supply actor or creation scope.
type AuthoritySource interface {
	WorkboardAuthority(context.Context) (Authority, error)
}

type Authority struct {
	CreationScope string
	Actor         Actor
}

func (a Authority) Validate() error {
	if !validID(a.CreationScope) || a.Actor.Validate() != nil {
		return fail(CodeInvalid, "authority")
	}
	return nil
}

type BoardRepository interface {
	CreateWorkboard(context.Context, string, CreateBoardRequest, Actor, time.Time) (OperationReceipt, error)
	ArchiveWorkboard(context.Context, ArchiveBoardRequest, Actor, time.Time) (OperationReceipt, error)
	ListWorkboards(context.Context, BoardListOptions) (BoardPage, error)
	ReadWorkboard(context.Context, string, BoardSnapshotOptions) (BoardSnapshot, error)
}

type BoardService struct {
	repository BoardRepository
	authority  AuthoritySource
	now        func() time.Time
}

func NewBoardService(repository BoardRepository, authority AuthoritySource, now func() time.Time) (*BoardService, error) {
	if repository == nil || authority == nil || now == nil {
		return nil, fail(CodeInvalid, "service")
	}
	return &BoardService{repository: repository, authority: authority, now: now}, nil
}

func (s *BoardService) Create(ctx context.Context, request CreateBoardRequest) (OperationReceipt, error) {
	if request.Validate() != nil {
		return OperationReceipt{}, fail(CodeInvalid, "request")
	}
	authority, err := s.authorize(ctx)
	if err != nil {
		return OperationReceipt{}, err
	}
	return s.repository.CreateWorkboard(ctx, authority.CreationScope, request, authority.Actor, s.now().UTC())
}

func (s *BoardService) Archive(ctx context.Context, request ArchiveBoardRequest) (OperationReceipt, error) {
	if request.Validate() != nil {
		return OperationReceipt{}, fail(CodeInvalid, "request")
	}
	authority, err := s.authorize(ctx)
	if err != nil {
		return OperationReceipt{}, err
	}
	return s.repository.ArchiveWorkboard(ctx, request, authority.Actor, s.now().UTC())
}

func (s *BoardService) List(ctx context.Context, options BoardListOptions) (BoardPage, error) {
	if options.Validate() != nil {
		return BoardPage{}, fail(CodeInvalid, "list")
	}
	if _, err := s.authorize(ctx); err != nil {
		return BoardPage{}, err
	}
	return s.repository.ListWorkboards(ctx, options)
}

func (s *BoardService) Read(ctx context.Context, boardID string, options BoardSnapshotOptions) (BoardSnapshot, error) {
	if !validID(boardID) || options.Validate() != nil {
		return BoardSnapshot{}, fail(CodeInvalid, "snapshot")
	}
	if _, err := s.authorize(ctx); err != nil {
		return BoardSnapshot{}, err
	}
	return s.repository.ReadWorkboard(ctx, boardID, options)
}

func (s *BoardService) authorize(ctx context.Context) (Authority, error) {
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil {
		return Authority{}, err
	}
	if authority.Validate() != nil {
		return Authority{}, fail(CodeInvalid, "authority")
	}
	return authority, nil
}
