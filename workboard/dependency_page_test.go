package workboard

import (
	"context"
	"strings"
	"testing"
)

type dependencyRepositoryStub struct {
	page    DependencyPage
	options DependencyOptions
	boardID string
	cardID  string
	calls   int
}

func (s *dependencyRepositoryStub) ListDependencyEdges(_ context.Context, boardID, cardID string, options DependencyOptions) (DependencyPage, error) {
	s.calls++
	s.boardID, s.cardID, s.options = boardID, cardID, options
	return s.page, nil
}

func TestDependencyReadServiceAuthorizesAndFencesProjection(t *testing.T) {
	page := DependencyPage{Version: 1, BoardID: "board-a", CardID: "card-a", Direction: DependencyPrerequisites,
		GraphRevision: 2, GraphDigest: strings.Repeat("a", 64),
		Items: []DependencyLink{{Version: 1, BoardID: "board-a", CardID: "card-a", DependencyID: "card-b"}}}
	repository := &dependencyRepositoryStub{page: page}
	service, err := NewDependencyReadService(repository, boardAuthorityStub{authority: Authority{
		CreationScope: "native-api", Actor: Actor{ID: "api_operator", Type: "operator"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	options := DependencyOptions{Limit: 1, Direction: DependencyPrerequisites}
	got, err := service.List(context.Background(), "board-a", "card-a", options)
	if err != nil || got.Validate() != nil || repository.calls != 1 || repository.boardID != "board-a" || repository.cardID != "card-a" || repository.options != options {
		t.Fatalf("page=%+v repository=%+v err=%v", got, repository, err)
	}

	repository.page.CardID = "card-c"
	if _, err = service.List(context.Background(), "board-a", "card-a", options); err == nil {
		t.Fatal("mismatched repository projection accepted")
	}
	unauthorized, err := NewDependencyReadService(repository, boardAuthorityStub{})
	if err != nil {
		t.Fatal(err)
	}
	repository.calls = 0
	if _, err = unauthorized.List(context.Background(), "board-a", "card-a", options); err == nil || repository.calls != 0 {
		t.Fatal("unauthorized dependency read dispatched")
	}
}
