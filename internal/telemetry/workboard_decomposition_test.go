package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func decompositionStore(t *testing.T, path string, limits workboard.DecompositionLimits, config string, actor workboard.Actor) (*Store, *workboard.CardService, string) {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateWorkboard(context.Background(), "session", createBoardRequest("decomposition-board-key", "Decomposition", ""),
		workboard.Actor{ID: "operator", Type: "operator"}, time.Now().UTC())
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	policy, err := workboard.NewDecompositionPolicy(limits, strings.Repeat(config, 64))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	service, err := workboard.NewCardServiceWithDecomposition(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}}, policy)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, service, created.BoardID
}

func decompositionCard(title, parent string) workboard.NewCard {
	return workboard.NewCard{Title: title, Priority: "normal", Labels: []string{}, ParentID: parent, Dependencies: []string{},
		Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}
}

func readAdmissionBody(t *testing.T, store *Store, operationID string) workboard.DecompositionAdmission {
	t.Helper()
	var body []byte
	if err := store.db.QueryRow(`SELECT body FROM workboard_decomposition_admissions WHERE operation_id=?`, operationID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var admission workboard.DecompositionAdmission
	if strictJSON(body, &admission) != nil || admission.Validate() != nil {
		t.Fatalf("invalid admission: %s", body)
	}
	return admission
}

func TestWorkboardDecompositionAdmissionReplayDriftAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	limits := workboard.DecompositionLimits{Version: 1, MaxDepth: 3, MaxChildren: 2}
	actor := workboard.Actor{ID: "model-a", Type: "model"}
	store, service, boardID := decompositionStore(t, path, limits, "a", actor)
	request := workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "model-root-create", ExpectedBoardRevision: 1, ExpectedGraphRevision: 1,
		Card: decompositionCard("Root", "")}
	created, err := service.CreateCard(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	admission := readAdmissionBody(t, store, created.Receipt.OperationID)
	if admission.CardID != created.ID || admission.Actor != actor || admission.Limits != limits || admission.Depth != 1 || admission.DirectChildren != 0 {
		t.Fatalf("admission=%+v", admission)
	}
	events, err := store.ListWorkboardEvents(ctx, boardID, workboard.BoardEventOptions{Limit: 10})
	if err != nil || len(events.Items) != 2 || !events.Items[1].HasDecompositionAdmission() ||
		events.Items[1].DecompositionAdmissionDigest != admission.AdmissionDigest {
		t.Fatalf("events=%+v error=%v", events, err)
	}
	replayed, err := service.CreateCard(ctx, request)
	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("replay=%+v error=%v", replayed, err)
	}
	var admissionCount int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_decomposition_admissions`).Scan(&admissionCount); err != nil || admissionCount != 1 {
		t.Fatalf("admissions=%d error=%v", admissionCount, err)
	}
	driftedPolicy, _ := workboard.NewDecompositionPolicy(limits, strings.Repeat("b", 64))
	drifted, _ := workboard.NewCardServiceWithDecomposition(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}}, driftedPolicy)
	if _, err = drifted.CreateCard(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-key drift error=%v", err)
	}
	fresh := request
	fresh.IdempotencyKey, fresh.ExpectedBoardRevision, fresh.ExpectedGraphRevision = "model-root-fresh1", 2, 2
	freshResult, err := drifted.CreateCard(ctx, fresh)
	if err != nil || readAdmissionBody(t, store, freshResult.Receipt.OperationID).ConfigDigest != strings.Repeat("b", 64) {
		t.Fatalf("fresh drift result=%+v error=%v", freshResult, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, _ = workboard.NewCardServiceWithDecomposition(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}},
		mustDecompositionPolicy(t, limits, "a"))
	replayed, err = service.CreateCard(ctx, request)
	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("restart replay=%+v error=%v", replayed, err)
	}
}

func mustDecompositionPolicy(t *testing.T, limits workboard.DecompositionLimits, config string) workboard.DecompositionPolicy {
	t.Helper()
	policy, err := workboard.NewDecompositionPolicy(limits, strings.Repeat(config, 64))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestWorkboardDecompositionParentLineageAndConcurrentFanout(t *testing.T) {
	ctx := context.Background()
	limits := workboard.DecompositionLimits{Version: 1, MaxDepth: 4, MaxChildren: 2}
	store, service, boardID := decompositionStore(t, filepath.Join(t.TempDir(), "state.db"), limits, "c", workboard.Actor{ID: "worker-a", Type: "worker"})
	defer store.Close()
	parent, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "worker-parent-key", ExpectedBoardRevision: 1,
		ExpectedGraphRevision: 1, Card: decompositionCard("Parent", "")})
	if err != nil {
		t.Fatal(err)
	}
	parentAdmission := readAdmissionBody(t, store, parent.Receipt.OperationID)
	looserPolicy := mustDecompositionPolicy(t, workboard.DecompositionLimits{Version: 1, MaxDepth: 8, MaxChildren: 8}, "7")
	looser, err := workboard.NewCardServiceWithDecomposition(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session",
		Actor: workboard.Actor{ID: "worker-a", Type: "worker"}}}, looserPolicy)
	if err != nil {
		t.Fatal(err)
	}
	child, err := looser.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "worker-child-key1", ExpectedBoardRevision: 2,
		ExpectedGraphRevision: 2, Card: decompositionCard("Child", parent.ID)})
	if err != nil {
		t.Fatal(err)
	}
	childAdmission := readAdmissionBody(t, store, child.Receipt.OperationID)
	if childAdmission.ParentAdmissionID != parentAdmission.AdmissionID || childAdmission.ParentAdmissionDigest != parentAdmission.AdmissionDigest ||
		childAdmission.Limits.MaxChildren > parentAdmission.Limits.MaxChildren || childAdmission.Limits.MaxDepth > parentAdmission.Limits.MaxDepth ||
		childAdmission.ConfigDigest != strings.Repeat("7", 64) {
		t.Fatalf("parent=%+v child=%+v", parentAdmission, childAdmission)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, createErr := looser.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "concurrent-child-" + string(rune('a'+i)),
				ExpectedBoardRevision: 3, ExpectedGraphRevision: 3, Card: decompositionCard("Concurrent", parent.ID)})
			results <- createErr
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for createErr := range results {
		if createErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successes=%d", successes)
	}
	var children int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_cards WHERE board_id=? AND parent_id=?`, boardID, parent.ID).Scan(&children); err != nil || children != 2 {
		t.Fatalf("children=%d error=%v", children, err)
	}
	var cardsBefore, eventsBefore, operationsBefore, admissionsBefore int
	if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM workboard_cards),(SELECT count(*) FROM workboard_events),
		(SELECT count(*) FROM workboard_operations),(SELECT count(*) FROM workboard_decomposition_admissions)`).
		Scan(&cardsBefore, &eventsBefore, &operationsBefore, &admissionsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = looser.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "fanout-exhausted-key", ExpectedBoardRevision: 4,
		ExpectedGraphRevision: 4, Card: decompositionCard("Over capacity", parent.ID)}); !errors.Is(err, &workboard.Violation{Code: workboard.CodeLimitExceeded}) {
		t.Fatalf("post-race fanout error=%v", err)
	}
	var cardsAfter, eventsAfter, operationsAfter, admissionsAfter int
	if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM workboard_cards),(SELECT count(*) FROM workboard_events),
		(SELECT count(*) FROM workboard_operations),(SELECT count(*) FROM workboard_decomposition_admissions)`).
		Scan(&cardsAfter, &eventsAfter, &operationsAfter, &admissionsAfter); err != nil ||
		cardsAfter != cardsBefore || eventsAfter != eventsBefore || operationsAfter != operationsBefore || admissionsAfter != admissionsBefore {
		t.Fatalf("post-race counts before=%d/%d/%d/%d after=%d/%d/%d/%d error=%v", cardsBefore, eventsBefore, operationsBefore,
			admissionsBefore, cardsAfter, eventsAfter, operationsAfter, admissionsAfter, err)
	}
}

func TestWorkboardDecompositionLineageUsesLatestReparentEvent(t *testing.T) {
	ctx := context.Background()
	limits := workboard.DecompositionLimits{Version: 1, MaxDepth: 4, MaxChildren: 3}
	store, service, boardID := decompositionStore(t, filepath.Join(t.TempDir(), "state.db"), limits, "f", workboard.Actor{ID: "model-a", Type: "model"})
	defer store.Close()
	first, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "lineage-first-key", ExpectedBoardRevision: 1,
		ExpectedGraphRevision: 1, Card: decompositionCard("First", "")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "lineage-second-key", ExpectedBoardRevision: 2,
		ExpectedGraphRevision: 2, Card: decompositionCard("Second", "")})
	if err != nil {
		t.Fatal(err)
	}
	reparented, err := service.ReviseCard(ctx, workboard.ReviseCardRequest{BoardID: boardID, CardID: first.ID, IdempotencyKey: "lineage-reparent-key",
		ExpectedCardRevision: 1, ExpectedGraphRevision: 3, Patch: workboard.CardPatch{ParentID: &second.ID}})
	if err != nil {
		t.Fatal(err)
	}
	reparentAdmission := readAdmissionBody(t, store, reparented.Receipt.OperationID)
	child, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "lineage-child-key1", ExpectedBoardRevision: 4,
		ExpectedGraphRevision: 4, Card: decompositionCard("Child", first.ID)})
	if err != nil {
		t.Fatal(err)
	}
	childAdmission := readAdmissionBody(t, store, child.Receipt.OperationID)
	if childAdmission.ParentAdmissionID != reparentAdmission.AdmissionID || childAdmission.ParentAdmissionDigest != reparentAdmission.AdmissionDigest {
		t.Fatalf("reparent=%+v child=%+v", reparentAdmission, childAdmission)
	}
}

func TestWorkboardDecompositionRejectsBeforeMutationAndRollsBack(t *testing.T) {
	ctx := context.Background()
	limits := workboard.DecompositionLimits{Version: 1, MaxDepth: 2, MaxChildren: 2}
	store, service, boardID := decompositionStore(t, filepath.Join(t.TempDir(), "state.db"), limits, "d", workboard.Actor{ID: "model-a", Type: "model"})
	defer store.Close()
	root, err := service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "bounded-root-key1", ExpectedBoardRevision: 1,
		ExpectedGraphRevision: 1, Card: decompositionCard("Root", "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "larger-child-key1", ExpectedBoardRevision: 2,
		ExpectedGraphRevision: 2, Card: decompositionCard("Allowed child", root.ID)}); err != nil {
		t.Fatalf("larger policy did not persist decomposition: %v", err)
	}
	smallerPolicy := mustDecompositionPolicy(t, workboard.DecompositionLimits{Version: 1, MaxDepth: 1, MaxChildren: 2}, "8")
	smaller, err := workboard.NewCardServiceWithDecomposition(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session",
		Actor: workboard.Actor{ID: "model-a", Type: "model"}}}, smallerPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = smaller.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "smaller-child-key1", ExpectedBoardRevision: 3,
		ExpectedGraphRevision: 3, Card: decompositionCard("Rejected child", root.ID)}); !errors.Is(err, &workboard.Violation{Code: workboard.CodeDepthExhausted}) {
		t.Fatalf("depth error=%v", err)
	}
	var cards, events, operations, admissions int
	if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM workboard_cards),(SELECT count(*) FROM workboard_events),
		(SELECT count(*) FROM workboard_operations),(SELECT count(*) FROM workboard_decomposition_admissions)`).Scan(&cards, &events, &operations, &admissions); err != nil ||
		cards != 2 || events != 3 || operations != 3 || admissions != 2 {
		t.Fatalf("after rejection cards=%d events=%d operations=%d admissions=%d error=%v", cards, events, operations, admissions, err)
	}
	if _, err = store.db.Exec(`CREATE TRIGGER test_reject_decomposition BEFORE INSERT ON workboard_decomposition_admissions
		BEGIN SELECT RAISE(ABORT,'test rejection'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "rollback-root-key", ExpectedBoardRevision: 3,
		ExpectedGraphRevision: 3, Card: decompositionCard("Rollback", "")}); err == nil {
		t.Fatal("admission insert rejection did not abort mutation")
	}
	if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM workboard_cards),(SELECT count(*) FROM workboard_events),
		(SELECT count(*) FROM workboard_operations),(SELECT count(*) FROM workboard_decomposition_admissions)`).Scan(&cards, &events, &operations, &admissions); err != nil ||
		cards != 2 || events != 3 || operations != 3 || admissions != 2 {
		t.Fatalf("rollback cards=%d events=%d operations=%d admissions=%d error=%v", cards, events, operations, admissions, err)
	}
}

func TestWorkboardWorkerCannotBypassDecompositionPolicy(t *testing.T) {
	ctx := context.Background()
	store, _, boardID := decompositionStore(t, filepath.Join(t.TempDir(), "state.db"), workboard.DefaultDecompositionLimits(), "e",
		workboard.Actor{ID: "worker-a", Type: "worker"})
	defer store.Close()
	mutation := workboard.CardMutation{Version: 1, Kind: workboard.MutationCreate, BoardID: boardID, IdempotencyKey: "worker-bypass-key", Actor: workboard.Actor{ID: "worker-a", Type: "worker"},
		ExpectedBoardRevision: 1, ExpectedGraphRevision: 1, Create: func() *workboard.NewCard { c := decompositionCard("Bypass", ""); return &c }()}
	mutation.RequestDigest, _ = cardMutationDigest(mutation)
	if _, err := store.ApplyCardMutation(ctx, mutation); err == nil {
		t.Fatal("worker mutation without host policy was admitted")
	}
	var cards int
	if err := store.db.QueryRow(`SELECT count(*) FROM workboard_cards`).Scan(&cards); err != nil || cards != 0 {
		t.Fatalf("cards=%d error=%v", cards, err)
	}
}
