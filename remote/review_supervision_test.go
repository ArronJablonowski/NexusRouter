package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewSupervisionBusyThenStorageFailure(t *testing.T) {
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "queue"))
	if err != nil {
		t.Fatal(err)
	}
	routes, err := OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := lockReviewQueue(q.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second, err := lockReviewQueue(q.directory); !errors.Is(err, ErrReviewQueueBusy) {
		if second != nil {
			second()
		}
		t.Fatal("lock contention not distinguished", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	policy := func(bool) (RemoteEvaluator, error) {
		t.Error("invalid queue invoked evaluator policy")
		return RemoteEvaluator{}, ErrInvalid
	}
	go func() { done <- (&Client{}).runReviewJobs(ctx, q, routes, t.TempDir(), policy, time.Millisecond) }()
	select {
	case err := <-done:
		t.Fatal("busy queue stopped supervision", err)
	case <-time.After(20 * time.Millisecond):
	}
	// A storage/integrity error must stop supervision after contention ends.
	if err := os.WriteFile(filepath.Join(q.directory, "invalid.job.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("storage failure silently retried")
	}
}

func TestReviewSupervisionCancellation(t *testing.T) {
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "queue"))
	if err != nil {
		t.Fatal(err)
	}
	routes, err := OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Client{}).RunReviewJobs(ctx, q, routes, "", func(bool) (RemoteEvaluator, error) { return RemoteEvaluator{}, ErrInvalid })
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join")
	}
}

func TestReviewSupervisionReopenDoesNotRepeatEvaluation(t *testing.T) {
	f, routes, key, task, _, _, _ := reviewFixture(t)
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "queue"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	evaluator := &remoteEvaluatorFixture{}
	reviewer := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	policy := func(bool) (RemoteEvaluator, error) { return reviewer, nil }
	if _, err := f.client.EnqueueRecordedReview(context.Background(), q, routes, root, key, task, reviewer, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if attempt == 1 {
			// Recover an interrupted terminal publication from the evaluator ledger.
			if err := os.Remove(q.path(key, ".result.json")); err != nil {
				t.Fatal(err)
			}
		}
		q, err = OpenExistingReviewQueue(q.directory)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		queue := q
		go func() { done <- f.client.runReviewJobs(ctx, queue, routes, root, policy, time.Millisecond) }()
		limit := time.Now().Add(2 * time.Second)
		for {
			state, e := q.Status(key)
			if e != nil {
				cancel()
				<-done
				t.Fatal(e)
			}
			if state.Status == "completed" {
				break
			}
			if time.Now().After(limit) {
				cancel()
				<-done
				t.Fatal("review did not complete", state)
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if evaluator.calls.Load() != 1 {
			t.Fatal("evaluation repeated", evaluator.calls.Load())
		}
	}
}
