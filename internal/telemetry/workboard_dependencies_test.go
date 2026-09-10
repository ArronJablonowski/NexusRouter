package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestDependencyPagesBindDirectionRootLimitAndGraph(t *testing.T) {
	ctx := context.Background()
	path, store, service, boardID, rootID, dependencies, dependentID := dependencyGraphFixture(t)
	defer store.Close()
	first, err := store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{Limit: 1, Direction: workboard.DependencyPrerequisites})
	if err != nil || first.Validate() != nil || len(first.Items) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{After: first.NextCursor, Limit: 1, Direction: workboard.DependencyPrerequisites})
	if err != nil || second.Validate() != nil || len(second.Items) != 1 || second.HasMore ||
		second.Items[0].DependencyID == first.Items[0].DependencyID || second.GraphRevision != first.GraphRevision || second.GraphDigest != first.GraphDigest {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	seen := map[string]bool{first.Items[0].DependencyID: true, second.Items[0].DependencyID: true}
	if !seen[dependencies[0]] || !seen[dependencies[1]] {
		t.Fatalf("pagination lost edges: %v", seen)
	}
	dependents, err := store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{Limit: 10, Direction: workboard.DependencyDependents})
	if err != nil || len(dependents.Items) != 1 || dependents.Items[0].CardID != dependentID || dependents.Items[0].DependencyID != rootID {
		t.Fatalf("dependents=%+v err=%v", dependents, err)
	}
	for name, options := range map[string]workboard.DependencyOptions{
		"direction": {After: first.NextCursor, Limit: 1, Direction: workboard.DependencyDependents},
		"limit":     {After: first.NextCursor, Limit: 2, Direction: workboard.DependencyPrerequisites},
	} {
		if _, err = store.ListDependencyEdges(ctx, boardID, rootID, options); !errors.Is(err, ErrWorkboardCursor) {
			t.Fatalf("%s substitution error=%v", name, err)
		}
	}
	if _, err = store.ListDependencyEdges(ctx, boardID, dependencies[0], workboard.DependencyOptions{After: first.NextCursor, Limit: 1, Direction: workboard.DependencyPrerequisites}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("root substitution error=%v", err)
	}
	forged := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if forged == first.NextCursor {
		forged = first.NextCursor[:len(first.NextCursor)-1] + "B"
	}
	if _, err = store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{After: forged, Limit: 1, Direction: workboard.DependencyPrerequisites}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("forged cursor error=%v", err)
	}
	extra := createCardForTest(t, ctx, service, boardID, "dependency-extra-card", "Extra", 8, 8, nil)
	if _, err = service.AddDependency(ctx, workboard.DependencyRequest{BoardID: boardID, CardID: rootID, DependencyID: extra.ID,
		IdempotencyKey: "dependency-extra-edge", ExpectedCardRevision: 3, ExpectedGraphRevision: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{After: first.NextCursor, Limit: 1, Direction: workboard.DependencyPrerequisites}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("graph change did not invalidate cursor: %v", err)
	}
	_ = path
}

func TestDependencyCursorFailsClosedAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	path, store, _, boardID, rootID, _, _ := dependencyGraphFixture(t)
	first, err := store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{Limit: 1, Direction: workboard.DependencyPrerequisites})
	if err != nil || !first.HasMore {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{After: first.NextCursor, Limit: 1, Direction: workboard.DependencyPrerequisites}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("pre-restart cursor survived: %v", err)
	}
}

func TestDependencyPagesRejectStoredGraphDigestTampering(t *testing.T) {
	ctx := context.Background()
	_, store, _, boardID, rootID, _, _ := dependencyGraphFixture(t)
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `UPDATE workboard_boards SET graph_digest=? WHERE id=?`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", boardID); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []workboard.DependencyDirection{workboard.DependencyPrerequisites, workboard.DependencyDependents} {
		if _, err := store.ListDependencyEdges(ctx, boardID, rootID, workboard.DependencyOptions{Limit: 10, Direction: direction}); !errors.Is(err, ErrWorkboardCorrupt) {
			t.Fatalf("direction=%s error=%v", direction, err)
		}
	}
}

func TestDependencyPagesRejectMissingNormalizedEdge(t *testing.T) {
	ctx := context.Background()
	_, store, _, boardID, rootID, dependencies, _ := dependencyGraphFixture(t)
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `DELETE FROM workboard_dependencies WHERE board_id=? AND card_id=? AND dependency_id=?`,
		boardID, rootID, dependencies[0]); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []workboard.DependencyDirection{workboard.DependencyPrerequisites, workboard.DependencyDependents} {
		cardID := rootID
		if direction == workboard.DependencyDependents {
			cardID = dependencies[0]
		}
		if _, err := store.ListDependencyEdges(ctx, boardID, cardID, workboard.DependencyOptions{Limit: 10, Direction: direction}); !errors.Is(err, ErrWorkboardCorrupt) {
			t.Fatalf("direction=%s error=%v", direction, err)
		}
	}
}

func dependencyGraphFixture(t *testing.T) (string, *Store, *workboard.CardService, string, string, []string, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dependencies.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	actor := workboard.Actor{ID: "operator", Type: "operator"}
	created, err := store.CreateWorkboard(ctx, "session", createBoardRequest("dependency-board-key", "Dependencies", ""), actor, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	service, err := workboard.NewCardService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: actor}})
	if err != nil {
		t.Fatal(err)
	}
	root := createCardForTest(t, ctx, service, created.BoardID, "dependency-root-card", "Root", 1, 1, nil)
	first := createCardForTest(t, ctx, service, created.BoardID, "dependency-first-card", "First", 2, 2, nil)
	second := createCardForTest(t, ctx, service, created.BoardID, "dependency-second-card", "Second", 3, 3, nil)
	dependent := createCardForTest(t, ctx, service, created.BoardID, "dependency-reverse-card", "Dependent", 4, 4, nil)
	if _, err = service.AddDependency(ctx, workboard.DependencyRequest{BoardID: created.BoardID, CardID: root.ID, DependencyID: first.ID,
		IdempotencyKey: "dependency-first-edge", ExpectedCardRevision: 1, ExpectedGraphRevision: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AddDependency(ctx, workboard.DependencyRequest{BoardID: created.BoardID, CardID: root.ID, DependencyID: second.ID,
		IdempotencyKey: "dependency-second-edge", ExpectedCardRevision: 2, ExpectedGraphRevision: 6}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AddDependency(ctx, workboard.DependencyRequest{BoardID: created.BoardID, CardID: dependent.ID, DependencyID: root.ID,
		IdempotencyKey: "dependency-reverse-edge", ExpectedCardRevision: 1, ExpectedGraphRevision: 7}); err != nil {
		t.Fatal(err)
	}
	return path, store, service, created.BoardID, root.ID, []string{first.ID, second.ID}, dependent.ID
}
