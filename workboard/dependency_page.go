package workboard

import "context"

type DependencyDirection string

const (
	DependencyPrerequisites DependencyDirection = "prerequisites"
	DependencyDependents    DependencyDirection = "dependents"
)

type DependencyOptions struct {
	After     string
	Limit     int
	Direction DependencyDirection
}

func (o DependencyOptions) Validate() error {
	if !validCursor(o.After) || o.Limit < 1 || o.Limit > MaxPageItems ||
		(o.Direction != DependencyPrerequisites && o.Direction != DependencyDependents) {
		return fail(CodeInvalid, "dependency_options")
	}
	return nil
}

type DependencyLink struct {
	Version      int
	BoardID      string
	CardID       string
	DependencyID string
}

func (l DependencyLink) Validate() error {
	if l.Version != SchemaVersion || !validID(l.BoardID) || !validID(l.CardID) || !validID(l.DependencyID) || l.CardID == l.DependencyID {
		return fail(CodeInvalid, "dependency_link")
	}
	return nil
}

type DependencyPage struct {
	Version       int
	BoardID       string
	CardID        string
	Direction     DependencyDirection
	GraphRevision int64
	GraphDigest   string
	Items         []DependencyLink
	NextCursor    string
	HasMore       bool
}

func (p DependencyPage) Validate() error {
	if p.Version != SchemaVersion || !validID(p.BoardID) || !validID(p.CardID) ||
		(p.Direction != DependencyPrerequisites && p.Direction != DependencyDependents) || p.GraphRevision < 1 || !digest(p.GraphDigest) ||
		len(p.Items) > MaxPageItems || p.HasMore != (p.NextCursor != "") || !validCursor(p.NextCursor) || p.HasMore && len(p.Items) == 0 {
		return fail(CodeInvalid, "dependency_page")
	}
	seen := map[string]bool{}
	for _, item := range p.Items {
		key := item.CardID + "\x00" + item.DependencyID
		if item.Validate() != nil || item.BoardID != p.BoardID || seen[key] ||
			p.Direction == DependencyPrerequisites && item.CardID != p.CardID ||
			p.Direction == DependencyDependents && item.DependencyID != p.CardID {
			return fail(CodeInvalid, "dependency_page")
		}
		seen[key] = true
	}
	return nil
}

type DependencyPageRepository interface {
	ListDependencyEdges(context.Context, string, string, DependencyOptions) (DependencyPage, error)
}

type DependencyReadService struct {
	repository DependencyPageRepository
	authority  AuthoritySource
}

func NewDependencyReadService(repository DependencyPageRepository, authority AuthoritySource) (*DependencyReadService, error) {
	if repository == nil || authority == nil {
		return nil, fail(CodeInvalid, "service")
	}
	return &DependencyReadService{repository: repository, authority: authority}, nil
}

func (s *DependencyReadService) List(ctx context.Context, boardID, cardID string, options DependencyOptions) (DependencyPage, error) {
	if !validID(boardID) || !validID(cardID) || options.Validate() != nil {
		return DependencyPage{}, fail(CodeInvalid, "dependency_request")
	}
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil || authority.Validate() != nil {
		return DependencyPage{}, fail(CodeInvalid, "authority")
	}
	page, err := s.repository.ListDependencyEdges(ctx, boardID, cardID, options)
	if err != nil {
		return DependencyPage{}, err
	}
	if page.Validate() != nil || page.BoardID != boardID || page.CardID != cardID || page.Direction != options.Direction || len(page.Items) > options.Limit {
		return DependencyPage{}, fail(CodeInvalid, "stored_page")
	}
	return page, nil
}
