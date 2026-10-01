package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

func TestReviewQueueRestartAndAdvisoryLearning(t *testing.T) {
	f, routes, key, task, v, _, backend := reviewFixture(t)
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	evaluator := &remoteEvaluatorFixture{}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	deadline := time.Now().Add(time.Hour)
	ctx := context.Background()
	for range 2 {
		state, e := f.client.EnqueueRecordedReview(ctx, q, routes, root, key, task, policy, deadline)
		if e != nil || state.Status != "pending" {
			t.Fatal(state, e)
		}
	}
	changed := task
	changed.Prompt = "different"
	if _, err = f.client.EnqueueRecordedReview(ctx, q, routes, root, key, changed, policy, deadline); err != ErrConflict {
		t.Fatal(err)
	}
	if _, err = f.client.EnqueueRecordedReview(ctx, q, routes, root, key, task, policy, deadline.Add(time.Minute)); err != ErrConflict {
		t.Fatal("deadline silently extended", err)
	}
	if evaluator.calls.Load() != 0 {
		t.Fatal("enqueue evaluated")
	}
	// Reopen process-independent storage. Waiting performs no review.
	q, err = OpenReviewQueue(q.directory)
	if err != nil {
		t.Fatal(err)
	}
	watched := &watchBackend{outcomeBackend: backend, state: "running"}
	f.server.backend = watched
	resolve := func(bool) (RemoteEvaluator, error) { return policy, nil }
	states, err := f.client.ProcessReviewJobs(ctx, q, routes, root, resolve)
	if err != nil || len(states) != 1 || states[0].Status != "pending" || evaluator.calls.Load() != 0 {
		t.Fatal(states, err)
	}
	f.server.backend = backend
	for i := range 3 {
		states, err = f.client.ProcessReviewJobs(ctx, q, routes, root, resolve)
		if err != nil || len(states) != 1 || states[0].Status != "completed" || !states[0].ReviewApplied {
			t.Fatal(states, err)
		}
		if i == 0 {
			// Simulate process loss after durable evaluator completion but before the
			// queue receipt reached disk. Recovery must reconcile, not evaluate again.
			if err = os.Remove(q.path(key, ".result.json")); err != nil {
				t.Fatal(err)
			}
			q, err = OpenExistingReviewQueue(q.directory)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	rank := remoteRank(t, root, v)
	if rank.AdvisorySamples != 1 || rank.ConfirmedSamples != 0 || evaluator.calls.Load() != 1 || backend.submits.Load() != 1 {
		t.Fatal(rank, evaluator.calls.Load(), backend.submits.Load())
	}
}
func TestReviewQueueFailureAndPolicyChangesAreTerminal(t *testing.T) {
	for _, mode := range []string{"failed_evaluator", "revoked", "policy", "storage_scope", "expired", "task_failed"} {
		t.Run(mode, func(t *testing.T) {
			f, routes, key, task, _, _, backend := reviewFixture(t)
			q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "jobs"))
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "evidence")
			e := &remoteEvaluatorFixture{}
			policy := RemoteEvaluator{Evaluator: e, Local: true, Timeout: time.Second}
			deadline := time.Now().Add(time.Hour)
			if mode == "expired" {
				deadline = time.Now().Add(time.Second)
			}
			if _, err = f.client.EnqueueRecordedReview(context.Background(), q, routes, root, key, task, policy, deadline); err != nil {
				t.Fatal(err)
			}
			want := "attention"
			switch mode {
			case "failed_evaluator":
				e.run = func(context.Context, evaluation.EvaluatorRequest) error { return errors.New("private diagnostic") }
			case "revoked":
				writeRegistry(t, f.clientTrust)
			case "policy":
				e.revision = "changed"
			case "storage_scope":
				root = filepath.Join(t.TempDir(), "other")
			case "expired":
				time.Sleep(time.Until(deadline) + time.Millisecond)
				want = "expired"
			case "task_failed":
				f.server.backend = &watchBackend{outcomeBackend: backend, state: "failed"}
				want = "task_failed"
			}
			var resolves atomic.Int32
			resolve := func(bool) (RemoteEvaluator, error) { resolves.Add(1); return policy, nil }
			for range 2 {
				states, err := f.client.ProcessReviewJobs(context.Background(), q, routes, root, resolve)
				if err != nil || len(states) != 1 || states[0].Status != want || states[0].ReviewApplied {
					t.Fatal(states, err)
				}
			}
			calls := int32(0)
			if mode == "failed_evaluator" {
				calls = 1
			}
			if e.calls.Load() != calls || resolves.Load() > 1 || backend.submits.Load() != 1 {
				t.Fatal("terminal attempt repeated", e.calls.Load(), resolves.Load())
			}
		})
	}
}
func TestReviewQueueInterruptedEvaluationNeverReinvokes(t *testing.T) {
	f, routes, key, task, _, _, backend := reviewFixture(t)
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evaluator := &remoteEvaluatorFixture{run: func(context.Context, evaluation.EvaluatorRequest) error { cancel(); return context.Canceled }}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	if _, err = f.client.EnqueueRecordedReview(ctx, q, routes, root, key, task, policy, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	resolve := func(bool) (RemoteEvaluator, error) { return policy, nil }
	if _, err = f.client.ProcessReviewJobs(ctx, q, routes, root, resolve); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	q, err = OpenReviewQueue(q.directory)
	if err != nil {
		t.Fatal(err)
	}
	states, err := f.client.ProcessReviewJobs(context.Background(), q, routes, root, resolve)
	if err != nil || len(states) != 1 || states[0].Status != "attention" || evaluator.calls.Load() != 1 || backend.submits.Load() != 1 {
		t.Fatal(states, err, evaluator.calls.Load())
	}
}
func TestReviewQueueOwnershipAndPrivateFiles(t *testing.T) {
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := lockReviewQueue(q.directory)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := lockReviewQueue(q.directory); e == nil {
		other()
		release()
		t.Fatal("competing worker admitted")
	}
	release()
	release, err = lockReviewQueue(q.directory)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err = os.Chmod(q.directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenReviewQueue(q.directory); err == nil {
		t.Fatal("nonprivate queue admitted")
	}
}

func TestReviewQueueProcessLockHelper(t *testing.T) {
	directory := os.Getenv("NEXUS_TEST_REVIEW_QUEUE_LOCK")
	if directory == "" {
		return
	}
	release, err := lockReviewQueue(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("locked")
	time.Sleep(time.Minute)
}
func TestReviewQueueProcessExitReleasesOwnership(t *testing.T) {
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReviewQueueProcessLockHelper$")
	child.Env = append(os.Environ(), "NEXUS_TEST_REVIEW_QUEUE_LOCK="+q.directory)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatal(line, err)
	}
	if release, e := lockReviewQueue(q.directory); e == nil {
		release()
		t.Fatal("live worker stolen")
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	release, err := lockReviewQueue(q.directory)
	if err != nil {
		t.Fatal("exited worker not released", err)
	}
	release()
}
