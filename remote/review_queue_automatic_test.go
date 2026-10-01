package remote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

type queuedDiscoveryBackend struct {
	cliDiscoveryBackend
	before func()
	calls  atomic.Int32
}

func (b *queuedDiscoveryBackend) Catalogue(ctx context.Context, m []string, c bool, h []string) (Info, error) {
	b.calls.Add(1)
	if b.before != nil {
		b.before()
	}
	return b.cliDiscoveryBackend.Catalogue(ctx, m, c, h)
}
func TestAutomaticReviewIntentPrecedesDispatchAndSurvivesLostResponse(t *testing.T) {
	f, routes, _, task, _, _, backend := reviewFixture(t)
	key := "queued-auto-review-01"
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: int64(task.ContextTokens)}}
	evaluator := &remoteEvaluatorFixture{}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	deadline := time.Now().Add(time.Hour)
	discovered := &queuedDiscoveryBackend{cliDiscoveryBackend: cliDiscoveryBackend{rankingBackend{backend, *task.ExpectedHarnessIdentity}, task}}
	discovered.before = func() {
		job, e := q.read(key)
		if e != nil || job.Automatic == nil || hash(job.Automatic.Request) != hash(request) {
			t.Error("discovery preceded durable intent", job, e)
		}
		if release, e := lockReviewQueue(q.directory); e == nil {
			release()
			t.Error("worker not fenced during dispatch")
		}
	}
	f.server.backend = discovered
	original := f.http.Handler
	var dropped atomic.Bool
	f.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/remote/tasks/"+key && !dropped.Swap(true) {
			saved := httptest.NewRecorder()
			original.ServeHTTP(saved, r)
			if saved.Code != 200 && saved.Code != 201 {
				t.Error("dispatch did not commit", saved.Code)
			}
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			conn.Close()
			return
		}
		original.ServeHTTP(w, r)
	})
	if _, _, err = f.client.DispatchQueuedAutomaticReview(context.Background(), q, routes, root, key, request, policy, deadline); err == nil {
		t.Fatal("lost response reported success")
	}
	if backend.submits.Load() != 2 || discovered.calls.Load() != 1 || evaluator.calls.Load() != 0 {
		t.Fatal("unexpected work before worker")
	}
	q, err = OpenExistingReviewQueue(q.directory)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(bool) (RemoteEvaluator, error) { return policy, nil }
	for range 2 {
		states, e := f.client.ProcessReviewJobs(context.Background(), q, routes, root, resolve)
		if e != nil || len(states) != 1 || states[0].Status != "completed" || !states[0].ReviewApplied {
			t.Fatal(states, e)
		}
	}
	if backend.submits.Load() != 2 || discovered.calls.Load() != 1 || evaluator.calls.Load() != 1 {
		t.Fatal("recovery repeated inference or discovery")
	}
	if _, _, err = f.client.DispatchQueuedAutomaticReview(context.Background(), q, routes, root, key, request, policy, deadline); err != ErrConflict {
		t.Fatal("terminal supervision dispatched", err)
	}
	changed := request
	changed.Prompt = "different"
	if _, _, err = f.client.DispatchQueuedAutomaticReview(context.Background(), q, routes, root, key, changed, policy, deadline); err != ErrConflict {
		t.Fatal("intent overwritten", err)
	}
}
func TestAutomaticReviewUnboundIntentNeverAuthorizesDispatch(t *testing.T) {
	f, routes, _, task, _, _, backend := reviewFixture(t)
	q, err := OpenReviewQueue(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	request := AutomaticRequest{Version: 1, Prompt: task.Prompt, Routing: harness.Request{Version: 1, Task: harness.TaskClass{Domain: task.Domain, Profile: task.Profile, Difficulty: task.HarnessDifficulty}, Mode: "local_only", LocalRequired: true, ContextTokens: int64(task.ContextTokens)}}
	evaluator := &remoteEvaluatorFixture{}
	policy := RemoteEvaluator{Evaluator: evaluator, Local: true, Timeout: time.Second}
	key := "queued-auto-missing-01"
	deadline := time.Now().Add(time.Hour)
	writeRegistry(t, f.clientTrust)
	if _, _, err = f.client.DispatchQueuedAutomaticReview(context.Background(), q, routes, root, key, request, policy, deadline); err == nil {
		t.Fatal("no peer unexpectedly dispatched")
	}
	if _, err = q.read(key); err != nil {
		t.Fatal("intent lost", err)
	}
	// Restoring capability is not authorization for the review worker to launch.
	writeRegistry(t, f.clientTrust, f.serverPeer)
	states, err := f.client.ProcessReviewJobs(context.Background(), q, routes, root, func(bool) (RemoteEvaluator, error) { return policy, nil })
	if err != nil || len(states) != 1 || states[0].Status != "pending" || backend.submits.Load() != 1 || evaluator.calls.Load() != 0 {
		t.Fatal(states, err)
	}
	if _, err = routes.Lookup(key); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worker created binding", err)
	}
	if err = os.Chmod(q.directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.client.DispatchQueuedAutomaticReview(context.Background(), q, routes, root, "unsafe-auto-job-01", request, policy, deadline); err == nil || backend.submits.Load() != 1 {
		t.Fatal("unsafe queue dispatched", err)
	}
}
