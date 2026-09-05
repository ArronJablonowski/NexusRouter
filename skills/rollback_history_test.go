package skills

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func repeatedActivationFixture(t *testing.T) (*FileStore, string, Key, string, string) {
	t.Helper()
	ctx := context.Background()
	path := testPath(t)
	s := openTest(t, path)
	draft := sample()
	a, err := s.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, draft.Key, a.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	draft.Steps = []string{"Run additional checks"}
	b, err := s.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, draft.Key, b.ID, a.ID, pass, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, draft.Key, a.ID, b.ID, pass, false); err != nil {
		t.Fatal(err)
	}
	return s, path, draft.Key, a.ID, b.ID
}

func expectActiveVersion(t *testing.T, s *FileStore, key Key, want string) {
	t.Helper()
	active, err := s.Load(context.Background(), key, "")
	if err != nil || active.ID != want {
		t.Fatal("wrong active version", active.ID, want, err)
	}
	items, err := s.Discover(context.Background(), key.Scope, nil, 10)
	if err != nil || len(items) != 1 || items[0].Version != want {
		t.Fatal("catalog disagrees with active version", items, err)
	}
}

func expectRollbackRejectionUnchanged(t *testing.T, s *FileStore, path string, key Key, expected string, want error) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Rollback(context.Background(), key, expected, false); !errors.Is(err, want) {
		t.Fatalf("rollback got %v, want %v", err, want)
	}
	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected rollback mutated catalog", err)
	}
}

func TestRollbackRepeatedVersionHistoryExhaustsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	s, path, key, a, b := repeatedActivationFixture(t)
	if err := s.Rollback(ctx, key, a, false); err != nil {
		t.Fatal(err)
	}
	expectActiveVersion(t, s, key, b)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	expectRollbackRejectionUnchanged(t, s, path, key, a, ErrConflict)
	expectActiveVersion(t, s, key, b)
	if err := s.Rollback(ctx, key, b, false); err != nil {
		t.Fatal(err)
	}
	expectActiveVersion(t, s, key, a)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	for range 2 {
		expectRollbackRejectionUnchanged(t, s, path, key, a, ErrNotFound)
		expectActiveVersion(t, s, key, a)
	}
	// Exhausted undo does not delete either immutable historical version.
	for _, id := range []string{a, b} {
		v, err := s.Load(ctx, key, id)
		if err != nil || v.ID != id {
			t.Fatal(id, v, err)
		}
	}
}

func TestRollbackNewActivationBranchRetainsLegitimateUndo(t *testing.T) {
	for _, branch := range []string{"new_version", "historical_version"} {
		t.Run(branch, func(t *testing.T) {
			ctx := context.Background()
			s, path, key, a, b := repeatedActivationFixture(t)
			if err := s.Rollback(ctx, key, a, false); err != nil {
				t.Fatal(err)
			}
			expectActiveVersion(t, s, key, b)
			next := a
			if branch == "new_version" {
				draft := sample()
				draft.Steps = []string{"Run branch-specific checks"}
				v, err := s.Draft(ctx, draft, false)
				if err != nil {
					t.Fatal(err)
				}
				next = v.ID
			}
			if err := s.Activate(ctx, key, next, b, pass, false); err != nil {
				t.Fatal(err)
			}
			expectActiveVersion(t, s, key, next)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = openTest(t, path)
			if err := s.Rollback(ctx, key, next, false); err != nil {
				t.Fatal("new branch lost its undo", err)
			}
			expectActiveVersion(t, s, key, b)
			if err := s.Rollback(ctx, key, b, false); err != nil {
				t.Fatal("pre-branch undo lost", err)
			}
			expectActiveVersion(t, s, key, a)
			expectRollbackRejectionUnchanged(t, s, path, key, a, ErrNotFound)
		})
	}
}
