package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardCardMutationsReplayRestartAndGraph(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("board-create-key1", "Cards", ""), actor, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	authority := telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}}
	service, err := workboard.NewCardService(store, authority)
	if err != nil {
		t.Fatal(err)
	}
	dependency, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: created.BoardID, IdempotencyKey: "create-dependency", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
		Card: workboard.NewCard{Title: "Dependency", Priority: "normal", Labels: []string{"Core"}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	cardRequest := workboard.CreateCardRequest{BoardID: created.BoardID, IdempotencyKey: "create-card-key01", ExpectedBoardRevision: 2, ExpectedGraphRevision: 2,
		Card: workboard.NewCard{Title: "Implement", Description: "Durable card", Priority: "high", Labels: []string{"API", "MVP"}, AssigneeID: "worker-1", ParentID: dependency.ID,
			Dependencies: []string{dependency.ID}, Budget: workboard.WorkBudget{AttemptLimit: 4, TimeLimitMS: 60_000, TokenLimit: 50_000, CostMicros: 7_500}, Criteria: workboardTestCriteria()}}
	card, err := service.CreateCard(ctx, cardRequest)
	if err != nil {
		t.Fatal(err)
	}
	if card.Revision != 1 || card.CriteriaRevision != 1 || card.State != workboard.Backlog || card.RemainingDependencies != 1 || len(card.Dependencies) != 1 ||
		card.Budget != cardRequest.Card.Budget || !reflect.DeepEqual(card.Criteria, cardRequest.Card.Criteria) || card.Receipt.Validate() != nil {
		t.Fatalf("created card = %+v", card)
	}
	var committedResponse []byte
	if err = store.db.QueryRow(`SELECT response FROM workboard_operations WHERE operation_id=?`, card.Receipt.OperationID).Scan(&committedResponse); err != nil {
		t.Fatal(err)
	}
	var committed cardMutationResponse
	if strictJSON(committedResponse, &committed) != nil {
		t.Fatalf("invalid committed response: %s", committedResponse)
	}
	committedDigest, digestErr := cardMutationResponseDigest(committed)
	if digestErr != nil || committed.Receipt.ResponseDigest != committedDigest || !reflect.DeepEqual(committed.Receipt, card.Receipt) || !reflect.DeepEqual(committed.Card, card.Card) {
		t.Fatalf("committed response=%+v result=%+v", committed, card)
	}
	graph, err := store.LoadGraph(ctx, created.BoardID)
	if err != nil || graph.GraphRevision != 3 || graph.LayoutRevision != 3 || len(graph.Nodes) != 2 {
		t.Fatalf("graph=%+v err=%v", graph, err)
	}
	page, err := service.ListCards(ctx, created.BoardID, workboard.CardFilter{Limit: 1})
	if err != nil || len(page.Items) != 1 || !page.HasMore {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := service.ListCards(ctx, created.BoardID, workboard.CardFilter{Limit: 1, After: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.HasMore || next.Items[0].ID == page.Items[0].ID {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	filtered, err := service.ListCards(ctx, created.BoardID, workboard.CardFilter{Limit: 10, Label: "api"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != card.ID {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
	createMutation := workboard.CardMutation{Version: 1, Kind: workboard.MutationCreate, BoardID: cardRequest.BoardID, IdempotencyKey: cardRequest.IdempotencyKey, Actor: actor,
		ExpectedBoardRevision: cardRequest.ExpectedBoardRevision, ExpectedGraphRevision: cardRequest.ExpectedGraphRevision, Create: &cardRequest.Card}
	createMutation.RequestDigest, _ = cardMutationDigest(createMutation)
	replay, err := service.CreateCard(ctx, cardRequest)
	if err != nil || !reflect.DeepEqual(replay, card) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	otherService, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "other-operator", Type: "operator"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = otherService.CreateCard(ctx, cardRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("different principal replay error=%v", err)
	}
	changedMutation := createMutation
	changedCard := *createMutation.Create
	changedCard.Title = "Different"
	changedMutation.Create = &changedCard
	changedMutation.RequestDigest, _ = cardMutationDigest(changedMutation)
	if _, err = store.ApplyCardMutation(ctx, changedMutation); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key changed request error=%v", err)
	}
	newTitle := "Implemented"
	newBudget := workboard.WorkBudget{AttemptLimit: 5, TimeLimitMS: 120_000, TokenLimit: 75_000, CostMicros: 9_000}
	revised, err := service.ReviseCard(ctx, workboard.ReviseCardRequest{BoardID: created.BoardID, CardID: card.ID, IdempotencyKey: "revise-card-key01", ExpectedCardRevision: 1,
		Patch: workboard.CardPatch{Title: &newTitle, Budget: &newBudget}})
	if err != nil || revised.Title != newTitle || revised.Revision != 2 || revised.Budget != newBudget || revised.Receipt.Validate() != nil {
		t.Fatalf("revised=%+v err=%v", revised, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, _ = workboard.NewCardService(store, authority)
	replay, err = service.CreateCard(ctx, cardRequest)
	if err != nil || !reflect.DeepEqual(replay, card) || replay.Title == revised.Title {
		t.Fatalf("restart replay lost original response: %+v err=%v", replay, err)
	}
	stored, err := store.GetCard(ctx, created.BoardID, card.ID)
	if err != nil || stored.Title != newTitle || stored.Budget != newBudget || !reflect.DeepEqual(stored.Criteria, cardRequest.Card.Criteria) {
		t.Fatalf("current projection=%+v err=%v", stored, err)
	}
}

func TestWorkboardDependencyMoveReorderAndFences(t *testing.T) {
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	first := createCardForTest(t, ctx, service, boardID, "first-card-key01", "First", 1, 1, nil)
	second := createCardForTest(t, ctx, service, boardID, "second-card-key1", "Second", 2, 2, nil)
	dependent := createCardForTest(t, ctx, service, boardID, "dependent-key001", "Dependent", 3, 3, []string{first.ID})
	removed, err := service.RemoveDependency(ctx, workboard.DependencyRequest{BoardID: boardID, CardID: dependent.ID, DependencyID: first.ID,
		IdempotencyKey: "remove-dep-key01", ExpectedCardRevision: 1, ExpectedGraphRevision: 4})
	if err != nil || removed.RemainingDependencies != 0 || removed.Revision != 2 {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	moved, err := service.MoveCard(ctx, workboard.MoveCardRequest{BoardID: boardID, CardID: dependent.ID, IdempotencyKey: "move-ready-key01",
		TargetState: workboard.Ready, ExpectedBoardRevision: 5, ExpectedCardRevision: 2, ExpectedLayoutRevision: 4})
	if err != nil || moved.State != workboard.Ready || moved.Revision != 3 {
		t.Fatalf("moved=%+v err=%v", moved, err)
	}
	reordered, err := service.ReorderCard(ctx, workboard.ReorderCardRequest{BoardID: boardID, CardID: second.ID, BeforeCardID: first.ID,
		IdempotencyKey: "reorder-card-key", ExpectedBoardRevision: 6, ExpectedCardRevision: 1, ExpectedLayoutRevision: 5})
	if err != nil || reordered.Rank >= first.Rank {
		t.Fatalf("reordered=%+v first=%+v err=%v", reordered, first, err)
	}
	if _, err = service.AddDependency(ctx, workboard.DependencyRequest{BoardID: boardID, CardID: first.ID, DependencyID: dependent.ID,
		IdempotencyKey: "stale-graph-key1", ExpectedCardRevision: 1, ExpectedGraphRevision: 4}); !errors.Is(err, &workboard.Violation{Code: workboard.CodeStaleRevision}) {
		t.Fatalf("stale graph error = %v", err)
	}
	if _, err = store.ListCards(ctx, boardID, workboard.CardFilter{Limit: 1, After: "tampered.cursor"}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("tampered cursor error = %v", err)
	}
}

func TestWorkboardAnchoredCrossColumnMoveAndReplay(t *testing.T) {
	ctx := context.Background()
	_, service, boardID := cardTestStore(t, ctx)
	anchor := createCardForTest(t, ctx, service, boardID, "anchor-card-key1", "Anchor", 1, 1, nil)
	source := createCardForTest(t, ctx, service, boardID, "source-card-key1", "Source", 2, 2, nil)
	anchorResult, err := service.MoveCard(ctx, workboard.MoveCardRequest{BoardID: boardID, CardID: anchor.ID, IdempotencyKey: "move-anchor-key01",
		TargetState: workboard.Ready, ExpectedBoardRevision: 3, ExpectedCardRevision: 1, ExpectedLayoutRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	anchor = anchorResult.Card
	request := workboard.MoveCardRequest{BoardID: boardID, CardID: source.ID, IdempotencyKey: "move-before-key01", TargetState: workboard.Ready,
		BeforeCardID: anchor.ID, ExpectedBoardRevision: 4, ExpectedCardRevision: 1, ExpectedLayoutRevision: 4}
	moved, err := service.MoveCard(ctx, request)
	if err != nil || moved.State != workboard.Ready || moved.Rank >= anchor.Rank {
		t.Fatalf("anchored move=%+v anchor=%+v err=%v", moved, anchor, err)
	}
	replay, err := service.MoveCard(ctx, request)
	if err != nil || !reflect.DeepEqual(replay, moved) {
		t.Fatalf("anchored replay=%+v err=%v", replay, err)
	}
}

func TestWorkboardCardMutationRollbackAndConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	if _, err := store.db.Exec(`CREATE TRIGGER test_fail_card_event BEFORE INSERT ON workboard_events WHEN NEW.kind='card.create' BEGIN SELECT RAISE(ABORT,'injected card event failure'); END`); err != nil {
		t.Fatal(err)
	}
	request := workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "rollback-card-key", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
		Card: workboard.NewCard{Title: "Rollback", Priority: "normal", Labels: []string{}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}}
	if _, err := service.CreateCard(ctx, request); err == nil || !strings.Contains(err.Error(), "injected card event failure") {
		t.Fatalf("rollback error=%v", err)
	}
	var cards int
	if err := store.db.QueryRow(`SELECT count(*) FROM workboard_cards WHERE board_id=?`, boardID).Scan(&cards); err != nil || cards != 0 {
		t.Fatalf("cards=%d err=%v", cards, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER test_fail_card_event`); err != nil {
		t.Fatal(err)
	}
	const callers = 8
	results := make([]workboard.CardMutationResult, callers)
	errs := make([]error, callers)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index], errs[index] = service.CreateCard(ctx, request)
		}()
	}
	group.Wait()
	for index := range results {
		if errs[index] != nil || !reflect.DeepEqual(results[index], results[0]) {
			t.Fatalf("caller %d result=%+v err=%v", index, results[index], errs[index])
		}
	}
	var actorID, actorType, eventCardID string
	if err := store.db.QueryRow(`SELECT actor_id,actor_type,card_id FROM workboard_events WHERE kind='card.create'`).Scan(&actorID, &actorType, &eventCardID); err != nil || actorID != "operator" || actorType != "operator" || eventCardID != results[0].ID {
		t.Fatalf("event actor=%s/%s card=%s err=%v", actorID, actorType, eventCardID, err)
	}
}

func TestWorkboardCardNormalizedCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	card, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "corrupt-card-key", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
		Card: workboard.NewCard{Title: "Card", Priority: "normal", Labels: []string{"Original"}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workboard_card_labels SET label='Changed' WHERE board_id=? AND card_id=?`, boardID, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCard(ctx, boardID, card.ID); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("corruption error=%v", err)
	}
	if _, err := store.ReadWorkboard(ctx, boardID, workboard.BoardSnapshotOptions{Limit: 100}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("snapshot corruption error=%v", err)
	}
}

func TestWorkboardCardCriteriaCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	card := createCardForTest(t, ctx, service, boardID, "criteria-card-key", "Criteria", 1, 1, nil)
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM workboard_criteria WHERE board_id=? AND card_id=? AND criteria_revision=1`, boardID, card.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("criteria count=%d err=%v", count, err)
	}
	if _, err := store.db.Exec(`UPDATE workboard_criteria SET description='tampered' WHERE board_id=? AND card_id=?`, boardID, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCard(ctx, boardID, card.ID); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("criteria corruption error=%v", err)
	}
}

func TestWorkboardCardMutationLeavesBoardProjectionConsistent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	card, boardID := readyLifecycleCard(t, ctx, store, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	board, graphRevision, _, err := readBoardRow(ctx, store.db, boardID)
	if err != nil || board.Revision != 3 || board.LayoutRevision != 3 || graphRevision != 2 || card.Revision != 2 {
		t.Fatalf("board=%+v graph_revision=%d card=%+v err=%v", board, graphRevision, card, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = readBoardRow(ctx, tx, boardID); err != nil {
		t.Fatalf("writer transaction board projection err=%v", err)
	}
}

func TestWorkboardCardReplayRequiresImmutableEvent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, string)
	}{
		{"deleted", func(t *testing.T, store *Store, operationID string) {
			if _, err := store.db.Exec(`DELETE FROM workboard_events WHERE operation_id=?`, operationID); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt", func(t *testing.T, store *Store, operationID string) {
			if _, err := store.db.Exec(`UPDATE workboard_events SET actor_id='tampered' WHERE operation_id=?`, operationID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, service, boardID := cardTestStore(t, ctx)
			request := workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "journal-replay-key", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
				Card: workboard.NewCard{Title: "Journal", Priority: "normal", Labels: []string{}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}}
			if _, err := service.CreateCard(ctx, request); err != nil {
				t.Fatal(err)
			}
			var operationID string
			if err := store.db.QueryRow(`SELECT operation_id FROM workboard_events WHERE board_id=? AND kind='card.create'`, boardID).Scan(&operationID); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, store, operationID)
			if _, err := service.CreateCard(ctx, request); !errors.Is(err, ErrWorkboardCorrupt) {
				t.Fatalf("replay with %s event = %v", test.name, err)
			}
		})
	}
}

func TestWorkboardCardReplayRejectsValidResponseCardTampering(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*workboard.Card)
	}{
		{"title", func(card *workboard.Card) { card.Title = "Changed" }},
		{"budget", func(card *workboard.Card) { card.Budget.TokenLimit++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, service, boardID := cardTestStore(t, ctx)
			request := workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "response-replay-key", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
				Card: workboard.NewCard{Title: "Journal", Priority: "normal", Labels: []string{}, Dependencies: []string{}, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}}
			created, err := service.CreateCard(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			var response []byte
			if err = store.db.QueryRow(`SELECT response FROM workboard_operations WHERE operation_id=?`, created.Receipt.OperationID).Scan(&response); err != nil {
				t.Fatal(err)
			}
			var envelope cardMutationResponse
			if strictJSON(response, &envelope) != nil {
				t.Fatal("invalid committed response fixture")
			}
			test.mutate(&envelope.Card)
			if envelope.Card.Validate() != nil {
				t.Fatal("tamper fixture must remain a valid card")
			}
			response, err = json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.db.Exec(`UPDATE workboard_operations SET response=? WHERE operation_id=?`, response, created.Receipt.OperationID); err != nil {
				t.Fatal(err)
			}
			if _, err = service.CreateCard(ctx, request); !errors.Is(err, ErrWorkboardCorrupt) {
				t.Fatalf("valid %s tamper replay error = %v", test.name, err)
			}
		})
	}
}

func TestWorkboardRejectsCrossBoardDependencyWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	card := createCardForTest(t, ctx, service, boardID, "local-card-key01", "Local", 1, 1, nil)
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	otherBoard, err := store.CreateWorkboard(ctx, "other-session", createBoardRequest("other-board-key1", "Other", ""), actor, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	otherService, _ := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "other-session", Actor: actor}})
	other := createCardForTest(t, ctx, otherService, otherBoard.BoardID, "other-card-key01", "Other", 1, 1, nil)
	_, err = service.AddDependency(ctx, workboard.DependencyRequest{BoardID: boardID, CardID: card.ID, DependencyID: other.ID,
		IdempotencyKey: "cross-board-key1", ExpectedCardRevision: 1, ExpectedGraphRevision: 2})
	if !errors.Is(err, &workboard.Violation{Code: workboard.CodeCrossBoard}) {
		t.Fatalf("cross-board error=%v", err)
	}
	graph, graphErr := store.LoadGraph(ctx, boardID)
	if graphErr != nil || graph.GraphRevision != 2 || len(graph.Nodes) != 1 || len(graph.Nodes[0].Dependencies) != 0 {
		t.Fatalf("graph changed after rejection: %+v err=%v", graph, graphErr)
	}
}

func TestWorkboardRejectsDependencyCycleWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, service, boardID := cardTestStore(t, ctx)
	first := createCardForTest(t, ctx, service, boardID, "cycle-first-key1", "First", 1, 1, nil)
	second := createCardForTest(t, ctx, service, boardID, "cycle-second-key", "Second", 2, 2, nil)
	if _, err := service.AddDependency(ctx, workboard.DependencyRequest{BoardID: boardID, CardID: first.ID, DependencyID: second.ID,
		IdempotencyKey: "cycle-edge-one01", ExpectedCardRevision: 1, ExpectedGraphRevision: 3}); err != nil {
		t.Fatal(err)
	}
	_, err := service.AddDependency(ctx, workboard.DependencyRequest{BoardID: boardID, CardID: second.ID, DependencyID: first.ID,
		IdempotencyKey: "cycle-edge-two02", ExpectedCardRevision: 1, ExpectedGraphRevision: 4})
	if !errors.Is(err, &workboard.Violation{Code: workboard.CodeCycle}) {
		t.Fatalf("cycle error=%v", err)
	}
	graph, graphErr := store.LoadGraph(ctx, boardID)
	if graphErr != nil || graph.GraphRevision != 4 {
		t.Fatalf("graph revision changed after cycle: %+v err=%v", graph, graphErr)
	}
	second, err = store.GetCard(ctx, boardID, second.ID)
	if err != nil || second.Revision != 1 || len(second.Dependencies) != 0 {
		t.Fatalf("card changed after cycle: %+v err=%v", second, err)
	}
}

func cardTestStore(t *testing.T, ctx context.Context) (*Store, *workboard.CardService, string) {
	t.Helper()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	receipt, err := store.CreateWorkboard(ctx, "session", createBoardRequest("create-board-key1", "Board", ""), workboard.Actor{ID: "operator", Type: "operator"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	service, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "operator", Type: "operator"}}})
	if err != nil {
		t.Fatal(err)
	}
	return store, service, receipt.BoardID
}

type telemetryCardAuthority struct {
	authority workboard.Authority
	err       error
}

func (a telemetryCardAuthority) WorkboardAuthority(context.Context) (workboard.Authority, error) {
	return a.authority, a.err
}

func createCardForTest(t *testing.T, ctx context.Context, service *workboard.CardService, boardID, key, title string, boardRevision, graphRevision int64, deps []string) workboard.Card {
	t.Helper()
	if deps == nil {
		deps = []string{}
	}
	card, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: key, ExpectedBoardRevision: boardRevision, ExpectedGraphRevision: graphRevision,
		Card: workboard.NewCard{Title: title, Priority: "normal", Labels: []string{}, Dependencies: deps, Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	return card.Card
}

func workboardTestBudget() workboard.WorkBudget {
	return workboard.WorkBudget{AttemptLimit: 3, TimeLimitMS: 30_000, TokenLimit: 10_000, CostMicros: 1_000}
}

func workboardTestCriteria() []workboard.AcceptanceCriterion {
	return []workboard.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic", ValidatorID: "go-test", Description: "Tests pass.", Required: true}}
}
