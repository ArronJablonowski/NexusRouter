package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func learningFixture() skills.LearningState {
	return skills.LearningState{Version: 1, Scope: "project", Name: "learning", Domain: "code", PolicyDigest: strings.Repeat("a", 64), Revision: 1, Phase: "discover"}
}

func TestLearningStateCASRestartAndPinnedSelection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "learning.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	state := learningFixture()
	if _, err = s.LearningState(ctx, state.Scope, state.Name); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if err = s.PutLearningState(ctx, state, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.PutLearningState(ctx, state, 0); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	state.Revision++
	state.Phase = "consume"
	state.ScanRevision = 1
	state.Epoch = 1
	if err = s.PutLearningState(ctx, state, 1); err != nil {
		t.Fatal(err)
	}
	state.Revision++
	state.Phase = "generate"
	state.ConsumeRevision = 1
	if err = s.PutLearningState(ctx, state, 2); err != nil {
		t.Fatal(err)
	}
	state.Revision++
	state.PendingSelectionID = strings.Repeat("b", 64)
	state.PendingBucketID = strings.Repeat("c", 64)
	if err = s.PutLearningState(ctx, state, 3); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LearningState(ctx, state.Scope, state.Name)
	if err != nil || got != state {
		t.Fatal(got, err)
	}
	bad := state
	bad.Revision++
	bad.PendingSelectionID = strings.Repeat("d", 64)
	if err = s.PutLearningState(ctx, bad, 4); !errors.Is(err, skills.ErrInvalid) {
		t.Fatal(err)
	}
	bad = state
	bad.Revision++
	bad.PolicyDigest = strings.Repeat("e", 64)
	if err = s.PutLearningState(ctx, bad, 4); !errors.Is(err, skills.ErrInvalid) {
		t.Fatal(err)
	}
	state.Revision++
	state.BucketAfter = state.PendingBucketID
	state.PendingSelectionID = ""
	state.PendingBucketID = ""
	if err = s.PutLearningState(ctx, state, 4); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if got, err = ro.LearningState(ctx, state.Scope, state.Name); err != nil || got != state {
		t.Fatal(got, err)
	}
}

func TestLearningStateConcurrentCASAndCorruption(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "learning.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	state := learningFixture()
	if err = a.PutLearningState(ctx, state, 0); err != nil {
		t.Fatal(err)
	}
	state.Revision++
	state.Phase = "consume"
	state.ScanRevision = 1
	state.Epoch = 1
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) { defer wg.Done(); results <- s.PutLearningState(ctx, state, 1) }(s)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = a.LearningState(canceled, state.Scope, state.Name); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE learning_states SET body=?`, strings.Repeat("x", 4097)); err != nil {
		t.Fatal(err)
	}
	if got, err := a.LearningState(ctx, state.Scope, state.Name); err == nil || got != (skills.LearningState{}) {
		t.Fatal(got, err)
	}
}
