package app

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// TestCompetingConfiguredWorkboardSchedulersRelyOnDurableClaimCAS qualifies
// duplicate scheduling ownership at the production factory/runner boundary.
// The synchronization wrapper stops immediately before each real runner so
// both independent SQLite handles have built work from the same ready revision.
func TestCompetingConfiguredWorkboardSchedulersRelyOnDurableClaimCAS(t *testing.T) {
	workerRequests := make(chan workboardE2EProviderRequest, 2)
	workerServer := httptest.NewServer(workboardE2EOllamaHandler(t, workerRequests, "factory candidate", 100, 20))
	defer workerServer.Close()
	reviewRequests := make(chan workboardE2EProviderRequest, 2)
	reviewServer := httptest.NewServer(workboardE2EOllamaHandler(t, reviewRequests, workboardE2EAuditResponse(t), 40, 10))
	defer reviewServer.Close()

	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "competing-schedulers.db")
	settings := configuredWorkboardE2ESettings(database, workerServer.URL, reviewServer.URL)
	firstStore, boardID, cardID, _ := readyFactoryCard(t, database, workboard.WorkBudget{
		AttemptLimit: 3, TimeLimitMS: 60_000, TokenLimit: 50_000, CostMicros: 1_000_000})
	defer firstStore.Close()
	secondStore, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()

	stores := []*telemetry.Store{firstStore, secondStore}
	schedulers := make([]*WorkboardScheduler, 0, len(stores))
	entered := make(chan struct{}, len(stores))
	release := make(chan struct{})
	for _, store := range stores {
		service, serviceErr := NewService(settings, nil)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		service.profile = func(context.Context) (resources.Snapshot, error) {
			return resources.Snapshot{Time: time.Now().UTC(), TotalRAM: 100, AvailableRAM: 100}, nil
		}
		plan, prepareErr := PrepareConfiguredWorkboardSchedule(ctx, service, store)
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		scheduler, ok := plan.scheduler.(*WorkboardScheduler)
		if !ok {
			t.Fatalf("configured scheduler=%T", plan.scheduler)
		}
		scheduler.runner = synchronizedWorkboardTaskRunner{
			delegate: scheduler.runner,
			entered:  entered,
			release:  release,
		}
		schedulers = append(schedulers, scheduler)
	}

	results := make(chan workboardE2ECycleResult, len(schedulers))
	var starts sync.WaitGroup
	starts.Add(len(schedulers))
	for _, scheduler := range schedulers {
		go func(scheduler *WorkboardScheduler) {
			starts.Done()
			starts.Wait()
			result, runErr := scheduler.RunCycle(ctx, boardID)
			results <- workboardE2ECycleResult{result: result, err: runErr}
		}(scheduler)
	}
	for range schedulers {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("both configured schedulers did not reach the real runner boundary")
		}
	}
	close(release)

	var launched, succeeded, failed int
	for range schedulers {
		select {
		case outcome := <-results:
			if outcome.err != nil || outcome.result.Scanned != 1 || outcome.result.Ready != 1 {
				t.Fatalf("cycle=%+v err=%v", outcome.result, outcome.err)
			}
			launched += outcome.result.Launched
			succeeded += outcome.result.Succeeded
			failed += outcome.result.Failed
		case <-time.After(10 * time.Second):
			t.Fatal("competing configured scheduler did not join")
		}
	}
	if launched != 2 || succeeded != 1 || failed != 1 || len(workerRequests) != 1 || len(reviewRequests) != 1 {
		t.Fatalf("launched=%d succeeded=%d failed=%d worker_requests=%d review_requests=%d",
			launched, succeeded, failed, len(workerRequests), len(reviewRequests))
	}

	card, cardErr := firstStore.GetCard(ctx, boardID, cardID)
	lifecycles, lifecycleErr := firstStore.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	attempt := lifecycles[cardID].Attempt
	if cardErr != nil || lifecycleErr != nil || card.State != workboard.Done || card.AttemptCount != 1 || card.AcceptanceID == "" ||
		card.CurrentClaimID != "" || attempt == nil || attempt.Validate() != nil || attempt.Claim == nil ||
		attempt.Claim.State != string(workboard.LeaseReleased) || attempt.Candidate == nil ||
		attempt.Candidate.Summary != "factory candidate" || attempt.Acceptance == nil || attempt.Acceptance.ID != card.AcceptanceID ||
		attempt.Acceptance.Decision != "accepted" || attempt.Acceptance.DecidedBy != configuredAcceptanceAuthority ||
		len(attempt.TaskIDs) != 1 || len(attempt.SessionIDs) != 1 {
		t.Fatalf("card=%+v lifecycle=%+v errors=%v/%v", card, lifecycles[cardID], cardErr, lifecycleErr)
	}
	tasks, taskErr := firstStore.ListTasks(ctx, sessions.TaskListOptions{Limit: 10})
	if taskErr != nil || len(tasks.Items) != 1 || tasks.Items[0].TaskID != attempt.TaskIDs[0] ||
		tasks.Items[0].SessionID != attempt.SessionIDs[0] || tasks.Items[0].State != "completed" {
		t.Fatalf("runtime tasks=%+v err=%v", tasks, taskErr)
	}
}

type synchronizedWorkboardTaskRunner struct {
	delegate WorkboardTaskRunner
	entered  chan<- struct{}
	release  <-chan struct{}
}

func (r synchronizedWorkboardTaskRunner) Run(ctx context.Context, task WorkboardWorkerTask) (WorkboardCandidate, error) {
	select {
	case r.entered <- struct{}{}:
	case <-ctx.Done():
		return WorkboardCandidate{}, ctx.Err()
	}
	select {
	case <-r.release:
		return r.delegate.Run(ctx, task)
	case <-ctx.Done():
		return WorkboardCandidate{}, ctx.Err()
	}
}
