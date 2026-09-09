package workboard

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeCardStore struct {
	cards       map[string]Card
	graph       Graph
	page        CardPage
	listed      CardFilter
	mutations   []CardMutation
	applyResult Card
	getCalls    int
	graphCalls  int
}

func (f *fakeCardStore) GetCard(_ context.Context, boardID, cardID string) (Card, error) {
	f.getCalls++
	card, ok := f.cards[cardID]
	if !ok || card.BoardID != boardID {
		return Card{}, fail(CodeMissingNode, "card")
	}
	return copyCard(card), nil
}

func (f *fakeCardStore) ListCards(_ context.Context, _ string, filter CardFilter) (CardPage, error) {
	f.listed = filter
	page := f.page
	page.Items = append([]Card(nil), page.Items...)
	return page, nil
}

func (f *fakeCardStore) LoadGraph(_ context.Context, boardID string) (Graph, error) {
	f.graphCalls++
	if f.graph.BoardID != boardID {
		return Graph{}, fail(CodeMissingNode, "board")
	}
	graph := f.graph
	graph.Nodes = append([]Node(nil), graph.Nodes...)
	for index := range graph.Nodes {
		graph.Nodes[index] = copyNode(graph.Nodes[index])
	}
	return graph, nil
}

func (f *fakeCardStore) ApplyCardMutation(_ context.Context, mutation CardMutation) (Card, error) {
	if mutation.Create != nil {
		mutation.Create = copyNewCardPtr(*mutation.Create)
	}
	mutation.Patch = copyPatch(mutation.Patch)
	f.mutations = append(f.mutations, mutation)
	return copyCard(f.applyResult), nil
}

func serviceCard(id string, state State) Card {
	created := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	return Card{ID: id, BoardID: "board-a", Revision: 1, State: state, Rank: "rank-1", Title: "A card",
		Description: "description", Priority: "normal", Labels: []string{}, Dependencies: []string{}, CreatedAt: created, UpdatedAt: created}
}

func serviceGraph(cards ...Card) Graph {
	nodes := make([]Node, 0, len(cards))
	for _, card := range cards {
		nodes = append(nodes, Node{ID: card.ID, BoardID: card.BoardID, ParentID: card.ParentID,
			Dependencies: append([]string(nil), card.Dependencies...)})
	}
	return Graph{BoardID: "board-a", GraphRevision: 3, LayoutRevision: 4, Nodes: nodes}
}

func serviceFor(t *testing.T, store *fakeCardStore) *CardService {
	t.Helper()
	service, err := NewCardService(store)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

const serviceKey = "request-key-0001"

func TestCardServiceCreateBindsReplayIdentityAndOwnsInput(t *testing.T) {
	parent, dependency := serviceCard("parent", Backlog), serviceCard("dependency", Done)
	created := serviceCard("created", Backlog)
	created.ParentID, created.Dependencies, created.Labels = parent.ID, []string{dependency.ID}, []string{"backend"}
	created.RemainingDependencies = 1
	store := &fakeCardStore{graph: serviceGraph(parent, dependency), applyResult: copyCard(created)}
	intent := NewCard{Title: created.Title, Description: created.Description, Priority: created.Priority, Labels: created.Labels,
		ParentID: created.ParentID, Dependencies: created.Dependencies}
	request := CreateCardRequest{BoardID: "board-a", Card: intent, IdempotencyKey: serviceKey, ExpectedBoardRevision: 7, ExpectedGraphRevision: 3}

	result, err := serviceFor(t, store).CreateCard(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.mutations) != 1 {
		t.Fatalf("mutations=%d", len(store.mutations))
	}
	mutation := store.mutations[0]
	if mutation.Version != CardMutationVersion || mutation.IdempotencyKey != serviceKey || mutation.ExpectedBoardRevision != 7 || mutation.ExpectedGraphRevision != 3 ||
		mutation.ExpectedLayoutRevision != 0 || len(mutation.RequestDigest) != 64 {
		t.Fatalf("mutation=%+v", mutation)
	}
	intent.Labels[0], intent.Dependencies[0] = "changed", "changed"
	result.Labels[0] = "caller-change"
	if mutation.Create.Labels[0] != "backend" || mutation.Create.Dependencies[0] != dependency.ID || store.applyResult.Labels[0] != "backend" {
		t.Fatal("card slices alias caller or store state")
	}
}

func TestCardServiceListEnforcesOpaqueBoundedPages(t *testing.T) {
	first, second := serviceCard("a", Ready), serviceCard("b", Backlog)
	store := &fakeCardStore{page: CardPage{Items: []Card{first, second}, HasMore: true, NextCursor: "opaque.cursor-2"}}
	assignee := "worker-a"
	filter := CardFilter{State: Ready, AssigneeID: &assignee, Label: "backend", Limit: 2}
	page, err := serviceFor(t, store).ListCards(context.Background(), "board-a", filter)
	if err != nil || !reflect.DeepEqual(store.listed, filter) || len(page.Items) != 2 {
		t.Fatalf("page=%+v filter=%+v error=%v", page, store.listed, err)
	}
	page.Items[0].Labels = append(page.Items[0].Labels, "changed")
	if len(store.page.Items[0].Labels) != 0 {
		t.Fatal("page aliases store state")
	}

	store.page = CardPage{HasMore: true, NextCursor: "cursor"}
	if _, err = serviceFor(t, store).ListCards(context.Background(), "board-a", CardFilter{Limit: 1}); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("empty advancing page error=%v", err)
	}
	store.page = CardPage{Items: []Card{first}, HasMore: true, NextCursor: strings.Repeat("x", MaxCursorBytes+1)}
	if _, err = serviceFor(t, store).ListCards(context.Background(), "board-a", CardFilter{Limit: 1}); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("cursor bound error=%v", err)
	}
}

func TestCardServiceReviseParentValidatesCompleteGraph(t *testing.T) {
	a, b := serviceCard("a", Backlog), serviceCard("b", Backlog)
	b.ParentID = "a"
	store := &fakeCardStore{cards: map[string]Card{"a": a, "b": b}, graph: serviceGraph(a, b), applyResult: a}
	parent := "b"
	request := ReviseCardRequest{BoardID: "board-a", CardID: "a", IdempotencyKey: serviceKey, Patch: CardPatch{ParentID: &parent},
		ExpectedCardRevision: 1, ExpectedGraphRevision: 3}
	if _, err := serviceFor(t, store).ReviseCard(context.Background(), request); !errors.Is(err, &Violation{Code: CodeCycle}) {
		t.Fatalf("cycle error=%v", err)
	}
	if len(store.mutations) != 0 {
		t.Fatal("cyclic parent mutation reached store")
	}

	parent = ""
	if _, err := serviceFor(t, store).ReviseCard(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if store.mutations[0].ExpectedBoardRevision != 0 || store.mutations[0].ExpectedCardRevision != 1 || store.mutations[0].ExpectedGraphRevision != 3 {
		t.Fatalf("parent revise fences=%+v", store.mutations[0])
	}
}

func TestCardServiceMoveAndReorderUseDomainState(t *testing.T) {
	a, b, done := serviceCard("a", Backlog), serviceCard("b", Backlog), serviceCard("done", Done)
	store := &fakeCardStore{cards: map[string]Card{"a": a, "b": b, "done": done}, graph: serviceGraph(a, b, done), applyResult: a}
	service := serviceFor(t, store)
	move := MoveCardRequest{BoardID: "board-a", CardID: "a", IdempotencyKey: serviceKey, TargetState: Done,
		ExpectedBoardRevision: 7, ExpectedCardRevision: 1, ExpectedLayoutRevision: 4}
	if _, err := service.MoveCard(context.Background(), move); !errors.Is(err, &Violation{Code: CodeIllegalTransition}) {
		t.Fatalf("illegal move error=%v", err)
	}
	move.TargetState, store.applyResult.State = Ready, Ready
	if _, err := service.MoveCard(context.Background(), move); err != nil {
		t.Fatal(err)
	}
	moveMutation := store.mutations[len(store.mutations)-1]
	if moveMutation.ExpectedGraphRevision != 0 || moveMutation.ExpectedBoardRevision != 7 || moveMutation.ExpectedCardRevision != 1 || moveMutation.ExpectedLayoutRevision != 4 {
		t.Fatalf("move fences=%+v", moveMutation)
	}

	reorder := ReorderCardRequest{BoardID: "board-a", CardID: "a", BeforeCardID: "done", IdempotencyKey: serviceKey,
		ExpectedBoardRevision: 7, ExpectedCardRevision: 1, ExpectedLayoutRevision: 4}
	if _, err := service.ReorderCard(context.Background(), reorder); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("cross-state reorder error=%v", err)
	}
	reorder.BeforeCardID, store.applyResult = "b", a
	if _, err := service.ReorderCard(context.Background(), reorder); err != nil {
		t.Fatal(err)
	}
}

func TestCardServiceDependencyMutationAndTraversal(t *testing.T) {
	a, b, c, d := serviceCard("a", Backlog), serviceCard("b", Backlog), serviceCard("c", Backlog), serviceCard("d", Backlog)
	a.Dependencies, b.Dependencies = []string{"b", "c"}, []string{"d"}
	a.RemainingDependencies, b.RemainingDependencies = 2, 1
	store := &fakeCardStore{cards: map[string]Card{"a": a, "b": b, "c": c, "d": d}, graph: serviceGraph(a, b, c, d), applyResult: c}
	service := serviceFor(t, store)
	request := DependencyRequest{BoardID: "board-a", CardID: "c", DependencyID: "a", IdempotencyKey: serviceKey,
		ExpectedCardRevision: 1, ExpectedGraphRevision: 3}
	if _, err := service.AddDependency(context.Background(), request); !errors.Is(err, &Violation{Code: CodeCycle}) {
		t.Fatalf("dependency cycle error=%v", err)
	}
	request.DependencyID = "d"
	if _, err := service.AddDependency(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if store.mutations[0].Kind != MutationDependencyAdd || store.mutations[0].ExpectedBoardRevision != 0 ||
		store.mutations[0].ExpectedCardRevision != 1 || store.mutations[0].ExpectedGraphRevision != 3 {
		t.Fatalf("mutation=%+v", store.mutations[0])
	}

	traversal, err := service.TraverseDependencies(context.Background(), TraverseDependenciesRequest{BoardID: "board-a", CardID: "a", ExpectedGraphRevision: 3, MaxDepth: 2, Direction: TraverseDependencies})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, card := range traversal.Cards {
		got = append(got, card.ID)
	}
	if !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
		t.Fatalf("traversal=%v", got)
	}
	reverse, err := service.TraverseDependencies(context.Background(), TraverseDependenciesRequest{BoardID: "board-a", CardID: "d", ExpectedGraphRevision: 3, MaxDepth: 2, Direction: TraverseDependents})
	if err != nil {
		t.Fatal(err)
	}
	got = got[:0]
	for _, card := range reverse.Cards {
		got = append(got, card.ID)
	}
	if !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("reverse traversal=%v", got)
	}
}

func TestCardMutationDigestIsStableAndIntentBound(t *testing.T) {
	card := serviceCard("a", Backlog)
	request := ReviseCardRequest{BoardID: "board-a", CardID: "a", IdempotencyKey: serviceKey,
		Patch: CardPatch{Description: stringPointer("first")}, ExpectedCardRevision: 1}
	digests := []string{}
	for index, description := range []string{"first", "first", "second", "first"} {
		request.Patch.Description = stringPointer(description)
		if index == 3 {
			request.IdempotencyKey = "request-key-0002"
		}
		store := &fakeCardStore{applyResult: card}
		if _, err := serviceFor(t, store).ReviseCard(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		digests = append(digests, store.mutations[0].RequestDigest)
	}
	if digests[0] != digests[1] || digests[0] == digests[2] || digests[0] != digests[3] {
		t.Fatalf("digests=%v", digests)
	}
}

func TestCardServiceUsesOnlyActionSpecificRevisionFences(t *testing.T) {
	card := serviceCard("a", Backlog)
	store := &fakeCardStore{cards: map[string]Card{"a": card}, graph: serviceGraph(card), applyResult: card}
	service := serviceFor(t, store)
	title := "revised"
	if _, err := service.ReviseCard(context.Background(), ReviseCardRequest{BoardID: "board-a", CardID: "a", IdempotencyKey: serviceKey,
		Patch: CardPatch{Title: &title}, ExpectedCardRevision: 1}); err != nil {
		t.Fatal(err)
	}
	revision := store.mutations[0]
	if revision.ExpectedBoardRevision != 0 || revision.ExpectedCardRevision != 1 || revision.ExpectedGraphRevision != 0 || revision.ExpectedLayoutRevision != 0 || store.graphCalls != 0 {
		t.Fatalf("revise fences=%+v graph_calls=%d", revision, store.graphCalls)
	}

	parent := ""
	beforeMutations, beforeGraphs := len(store.mutations), store.graphCalls
	if _, err := service.ReviseCard(context.Background(), ReviseCardRequest{BoardID: "board-a", CardID: "a", IdempotencyKey: serviceKey,
		Patch: CardPatch{ParentID: &parent}, ExpectedCardRevision: 1}); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("missing graph fence error=%v", err)
	}
	if len(store.mutations) != beforeMutations || store.graphCalls != beforeGraphs {
		t.Fatal("invalid parent revise reached store")
	}

	beforeGets := store.getCalls
	if _, err := service.MoveCard(context.Background(), MoveCardRequest{BoardID: "board-a", CardID: "a", IdempotencyKey: serviceKey,
		TargetState: Ready, ExpectedBoardRevision: 7, ExpectedCardRevision: 1}); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("missing layout fence error=%v", err)
	}
	if store.getCalls != beforeGets || len(store.mutations) != beforeMutations {
		t.Fatal("invalid move reached store")
	}

	beforeGraphs = store.graphCalls
	if _, err := service.AddDependency(context.Background(), DependencyRequest{BoardID: "board-a", CardID: "a", DependencyID: "missing",
		IdempotencyKey: serviceKey, ExpectedGraphRevision: 3}); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("missing card fence error=%v", err)
	}
	if store.graphCalls != beforeGraphs || len(store.mutations) != beforeMutations {
		t.Fatal("invalid dependency mutation reached store")
	}
}

func TestCardServiceRejectsInvalidMutationIdentity(t *testing.T) {
	card := serviceCard("a", Backlog)
	store := &fakeCardStore{graph: serviceGraph(), applyResult: card}
	request := CreateCardRequest{BoardID: "board-a", Card: NewCard{Title: card.Title, Description: card.Description, Priority: card.Priority,
		Labels: []string{}, Dependencies: []string{}}, IdempotencyKey: "has whitespace key", ExpectedBoardRevision: 1, ExpectedGraphRevision: 3}
	if _, err := serviceFor(t, store).CreateCard(context.Background(), request); !errors.Is(err, &Violation{Code: CodeInvalid}) {
		t.Fatalf("error=%v", err)
	}
	if len(store.mutations) != 0 || store.graphCalls != 0 || store.getCalls != 0 {
		t.Fatal("invalid mutation reached store")
	}
}

func stringPointer(value string) *string { return &value }
