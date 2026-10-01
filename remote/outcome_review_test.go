package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type outcomeBackend struct {
	fakeBackend
	status  submissions.Status
	events  []runtime.Event
	submits atomic.Int32
}

func (b *outcomeBackend) Submit(context.Context, string, Task) (submissions.Status, error) {
	b.submits.Add(1)
	return b.status, nil
}
func (b *outcomeBackend) Status(context.Context, string) (submissions.Status, error) {
	return b.status, nil
}
func (b *outcomeBackend) Events(_ context.Context, _ string, after int64, _ int) (sessions.EventPage, error) {
	return sessions.EventPage{Version: 1, TaskID: b.status.TaskIDs[0], SessionID: b.events[0].SessionID, State: "completed", FromSequence: after, NextSequence: int64(len(b.events)), HeadSequence: int64(len(b.events)), Events: b.events[after:]}, nil
}
func reviewFixture(t *testing.T) (*fixture, *RouteStore, string, Task, VerifiedOutcome, OutcomeReview, *outcomeBackend) {
	t.Helper()
	f := setup(t)
	task, status, events := outcomeFixture(t)
	b := &outcomeBackend{status: status, events: events}
	f.server.backend = b
	f.clientPeer.Models = []string{task.ModelID}
	f.clientPeer.Harnesses = []string{task.HarnessID}
	f.serverPeer.Models = []string{task.ModelID}
	f.serverPeer.Harnesses = []string{task.HarnessID}
	writeRegistry(t, f.clientTrust, f.serverPeer)
	writeRegistry(t, f.serverTrust, f.clientPeer)
	routes, err := OpenRouteStore(filepath.Join(t.TempDir(), "routes"))
	if err != nil {
		t.Fatal(err)
	}
	key := "reviewed-request-01"
	if _, err = f.client.DispatchRecorded(context.Background(), routes, "node-a", key, task); err != nil {
		t.Fatal(err)
	}
	v, err := f.client.RecordedOutcome(context.Background(), routes, key, task)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := v.Receipt().Digest()
	execution, _ := v.Receipt().Execution.Digest()
	evaluation := OutcomeReview{Version: 1, ReceiptSHA256: receipt, Review: harness.Review{Version: 1, ID: "review-first", ExecutionDigest: execution, Verdict: "passed", Method: "automated_ai", MethodVersion: "fixture-v1", Reviewer: "fixture-evaluator", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}}
	return f, routes, key, task, v, evaluation, b
}
func remoteRank(t *testing.T, root string, v VerifiedOutcome) harness.Ranked {
	t.Helper()
	now := time.Now().UTC()
	scope := hash(struct{ Destination, Caller string }{v.receipt.Route.Destination, v.receipt.Route.CallerFingerprint})
	ledger, err := harness.OpenEvidenceStore(filepath.Join(root, scope, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	snapshot, err := ledger.Snapshot(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	e := v.receipt.Execution
	s, err := harness.Select(harness.Request{Version: 1, Task: e.Task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: e.Actual, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}, snapshot, now, 0)
	if err != nil || len(s.Ranked) != 1 {
		t.Fatal(s, err)
	}
	return s.Ranked[0]
}
func TestRemoteReviewBoundRevisionAndWithdrawal(t *testing.T) {
	f, routes, key, task, v, review, b := reviewFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "evidence")
	for range 2 {
		if err := f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, review); err != nil {
			t.Fatal(err)
		}
	}
	ranked := remoteRank(t, root, v)
	if ranked.AdvisorySamples != 1 || ranked.ConfirmedSamples != 0 || ranked.PendingOutputs != 0 {
		t.Fatal(ranked)
	}
	next := review
	next.Review.ID = "review-corrected"
	next.Review.ExpectedHead = review.Review.ID
	next.Review.Method = "deterministic"
	next.Review.Verdict = "failed"
	next.Review.Quality = 0
	next.Review.CreatedAt = time.Now().UTC()
	if err := f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, next); err != nil {
		t.Fatal(err)
	}
	if err := f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, review); err != nil {
		t.Fatal("identical old retry", err)
	}
	ranked = remoteRank(t, root, v)
	if ranked.ConfirmedSamples != 1 || ranked.AdvisorySamples != 0 || ranked.Correctness >= .5 {
		t.Fatal(ranked)
	}
	stale := next
	stale.Review.ID = "stale"
	stale.Review.Verdict = "passed"
	if err := f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, stale); !errors.Is(err, harness.ErrConflict) {
		t.Fatal(err)
	}
	withdrawn := OutcomeReview{Version: 1, ReceiptSHA256: review.ReceiptSHA256, Review: harness.Review{Version: 1, ID: "withdrawn", ExecutionDigest: review.Review.ExecutionDigest, ExpectedHead: next.Review.ID, Verdict: "withdrawn", Reviewer: "fixture-evaluator", CreatedAt: time.Now().UTC()}}
	if err := f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, withdrawn); err != nil {
		t.Fatal(err)
	}
	ranked = remoteRank(t, root, v)
	if ranked.PendingOutputs != 1 || ranked.ConfirmedSamples != 0 || ranked.AdvisorySamples != 0 {
		t.Fatal(ranked)
	}
	if b.submits.Load() != 1 {
		t.Fatal("review dispatched work", b.submits.Load())
	}
}
func TestRemoteReviewWrongBindingsWriteNothing(t *testing.T) {
	for _, mode := range []string{"receipt", "execution", "future", "before-completion", "revoked", "output", "failed", "running"} {
		t.Run(mode, func(t *testing.T) {
			f, routes, key, task, v, r, b := reviewFixture(t)
			root := filepath.Join(t.TempDir(), "evidence")
			switch mode {
			case "receipt":
				other := v.Receipt()
				other.Route.Destination = "node-b"
				r.ReceiptSHA256, _ = other.Digest()
			case "execution":
				r.Review.ExecutionDigest = hash("other")
			case "future":
				r.Review.CreatedAt = time.Now().Add(time.Hour)
			case "before-completion":
				r.Review.CreatedAt = v.receipt.Execution.CompletedAt.Add(-time.Second)
			case "revoked":
				writeRegistry(t, f.serverTrust)
			case "output":
				b.status.Result.Text = "changed"
			case "failed", "running":
				b.status.State = mode
			}
			if err := f.client.ReviewRecordedOutcome(context.Background(), routes, root, key, task, r); err == nil {
				t.Fatal("invalid review accepted")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("wrote evidence before verifying binding", err)
			}
			if b.submits.Load() != 1 {
				t.Fatal("review dispatched work")
			}
		})
	}
}
func TestRemoteReviewConcurrentRevisionsHaveOneWinner(t *testing.T) {
	f, routes, key, task, v, r, _ := reviewFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "evidence")
	if err := f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, r); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"revision-a", "revision-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			next := r
			next.Review.ID = id
			next.Review.ExpectedHead = r.Review.ID
			next.Review.CreatedAt = time.Now().UTC()
			results <- f.client.ReviewRecordedOutcome(ctx, routes, root, key, task, next)
		}(id)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, harness.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	if got := remoteRank(t, root, v); got.AdvisorySamples != 1 {
		t.Fatal(got)
	}
}
