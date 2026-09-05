package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

func TestRecoveryFencesPausedRuntimeBeforeProviderDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, _ := store(t)
	digest := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	queued, err := db.CreateSubmission(ctx, digest("key"), digest(`{}`), digest("config"), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	old, err := db.ClaimSubmission(ctx, digest("config"), time.Now(), time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	paused, release := make(chan struct{}), make(chan struct{})
	oldDone := make(chan error, 1)
	loop := runtime.Loop{Journal: journal(func(ctx context.Context, seq int64, event runtime.Event) error {
		if event.Kind == runtime.TaskStarted {
			close(paused)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return db.AppendSubmission(ctx, seq, event, queued.ID, old.Token)
	}), Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Error("old owner dispatched after recovery")
		return nil
	})}
	r := runRequest()
	r.TaskID = "old-owner-task"
	r.SubmissionID = queued.ID
	go func() { _, err := loop.Run(ctx, r); oldDone <- err }()
	select {
	case <-paused:
	case <-ctx.Done():
		t.Fatal("runner not paused")
	}
	recovered, err := db.RecoverUndispatched(ctx, queued.ID, digest("config"), time.Now())
	if err != nil || !recovered {
		t.Fatal(recovered, err)
	}
	newOwner, err := db.ClaimSubmission(ctx, digest("config"), time.Now(), time.Minute)
	if err != nil || newOwner.Status.ID != queued.ID || newOwner.Token == old.Token {
		t.Fatal(newOwner.Status, err)
	}
	close(release)
	select {
	case err := <-oldDone:
		if !errors.Is(err, runtime.ErrExecutionLeaseLost) || errors.Is(err, runtime.ErrPersistence) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("old runner did not return")
	}
	events, err := db.Read(ctx, r.TaskID, 0, 100)
	if err != nil || len(events) != 0 {
		t.Fatal("old task was created", events, err)
	}
	calls := 0
	loop.Journal = journal(func(ctx context.Context, seq int64, event runtime.Event) error {
		return db.AppendSubmission(ctx, seq, event, queued.ID, newOwner.Token)
	})
	loop.Provider = model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
	})
	r.TaskID = "new-owner-task"
	if _, err := loop.Run(ctx, r); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
	history, err := db.RecoveryHistory(ctx, queued.ID)
	if err != nil || len(history) != 1 || history[0].Action != "queued" {
		t.Fatal(history, err)
	}
}
