package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestWorkboardSchedulerRunsReadyCardThroughDurableWorkerLifecycleOnce(t *testing.T) {
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	ctx := nativeWorkboardContext(context.Background())
	supervision, err := workboard.NewSupervisionService(store, contextWorkboardAuthority{}, time.Now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := workers.New(1, 25*time.Millisecond, 250*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("6", 64),
		25*time.Millisecond, 250*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int32
	factory := WorkboardTaskFactoryFunc(func(_ context.Context, item workboard.SupervisionItem) (WorkboardWorkerTask, error) {
		builds.Add(1)
		if item.BoardID != boardID || item.CardID != cardID || item.CardRevision != cardRevision {
			t.Fatalf("factory received non-authoritative item: %+v", item)
		}
		return WorkboardWorkerTask{
			TaskID: "scheduler-runtime-task", SessionID: "scheduler-runtime-session", ParentTaskID: "scheduler-parent-task",
			Scope: "workboard-card-" + cardID, FailureEffect: runtime.NoEffect,
			Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
				if err := bindTestWorkboardRuntime(run, handle); err != nil {
					return WorkboardCandidate{}, err
				}
				return WorkboardCandidate{Summary: "scheduler candidate", ArtifactRefs: []string{"artifact://scheduler-test"}}, nil
			},
			Validate: func(_ context.Context, candidate WorkboardCandidate) error {
				if candidate.Summary != "scheduler candidate" {
					return errors.New("candidate summary mismatch")
				}
				return nil
			},
		}, nil
	})
	var workerErr error
	runTask := WorkboardTaskRunnerFunc(func(run context.Context, task WorkboardWorkerTask) (WorkboardCandidate, error) {
		candidate, runErr := runner.Run(run, task)
		workerErr = runErr
		return candidate, runErr
	})
	scheduler, err := NewWorkboardScheduler(supervision, factory, runTask, WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	first, err := scheduler.RunCycle(ctx, boardID)
	if err != nil || first != (WorkboardScheduleResult{Scanned: 1, Ready: 1, Launched: 1, Succeeded: 1}) {
		events, eventErr := store.Read(ctx, "scheduler-runtime-task", 0, 100)
		states, stateErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
		t.Fatalf("first cycle=%+v err=%v worker_err=%v events=%+v event_err=%v lifecycle=%+v lifecycle_err=%v",
			first, err, workerErr, events, eventErr, states[cardID], stateErr)
	}
	snapshot, err := store.ReadWorkboard(ctx, boardID, workboardSnapshotOptions())
	if err != nil || len(snapshot.Cards) != 1 || snapshot.Cards[0].ID != cardID || snapshot.Cards[0].State != workboard.Review {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	lifecycles, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	lifecycle := lifecycles[cardID]
	if err != nil || lifecycle.Attempt == nil || lifecycle.Attempt.Candidate == nil || lifecycle.Attempt.Claim == nil ||
		lifecycle.Attempt.Candidate.Summary != "scheduler candidate" || lifecycle.Attempt.Claim.State != "released" {
		t.Fatalf("lifecycle=%+v err=%v", lifecycle, err)
	}
	second, err := scheduler.RunCycle(ctx, boardID)
	if err != nil || second != (WorkboardScheduleResult{}) || builds.Load() != 1 {
		t.Fatalf("second cycle=%+v builds=%d err=%v", second, builds.Load(), err)
	}
}

func TestWorkboardSchedulerScansAllPagesAndConsumesExistingWIP(t *testing.T) {
	reader := &schedulerReaderStub{pages: map[string]workboard.SupervisionPage{
		"": schedulerPage([]workboard.SupervisionItem{
			schedulerRunning("card-a", workboard.SupervisionRunning),
			schedulerReady("card-b", 7),
		}, "next-page"),
		"next-page": schedulerPage([]workboard.SupervisionItem{
			schedulerRunning("card-c", workboard.SupervisionStalled),
			schedulerRunning("card-d", workboard.SupervisionOrphaned),
			schedulerReady("card-e", 11),
		}, ""),
	}}
	factory := &schedulerFactoryStub{}
	runner := &schedulerRunnerStub{}
	scheduler, err := NewWorkboardScheduler(reader, factory, runner, WorkboardScheduleLimits{MaxInFlight: 4, ScanLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.RunCycle(context.Background(), "board-a")
	if err != nil {
		t.Fatal(err)
	}
	if result != (WorkboardScheduleResult{Scanned: 5, Ready: 2, ExistingWIP: 3, Launched: 1, Succeeded: 1, Deferred: 1}) {
		t.Fatalf("result=%+v", result)
	}
	if got := reader.afters(); len(got) != 2 || got[0] != "" || got[1] != "next-page" {
		t.Fatalf("pagination=%q", got)
	}
	if got := factory.itemsSnapshot(); len(got) != 1 || got[0].CardID != "card-b" || got[0].CardRevision != 7 {
		t.Fatalf("factory items=%+v", got)
	}
	if got := runner.tasksSnapshot(); len(got) != 1 || got[0].BoardID != "board-a" || got[0].CardID != "card-b" || got[0].ExpectedCardRevision != 7 {
		t.Fatalf("runner tasks=%+v", got)
	}
}

func TestWorkboardSchedulerScansBeforeBuildingAndFailsClosedAtScanLimit(t *testing.T) {
	reader := &schedulerReaderStub{pages: map[string]workboard.SupervisionPage{
		"": schedulerPage([]workboard.SupervisionItem{schedulerReady("card-a", 2), schedulerReady("card-b", 3)}, "more"),
	}}
	factory := &schedulerFactoryStub{}
	runner := &schedulerRunnerStub{}
	scheduler, err := NewWorkboardScheduler(reader, factory, runner, WorkboardScheduleLimits{MaxInFlight: 2, ScanLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scheduler.RunCycle(context.Background(), "board-a"); !errors.Is(err, ErrWorkboardScheduleLimit) {
		t.Fatalf("err=%v", err)
	}
	if len(factory.itemsSnapshot()) != 0 || len(runner.tasksSnapshot()) != 0 {
		t.Fatalf("partial scan dispatched work: factory=%+v runner=%+v", factory.itemsSnapshot(), runner.tasksSnapshot())
	}
}

func TestWorkboardSchedulerContainsReaderPanic(t *testing.T) {
	scheduler, err := NewWorkboardScheduler(panicSchedulerReader{}, &schedulerFactoryStub{}, &schedulerRunnerStub{},
		WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scheduler.RunCycle(context.Background(), "board-a"); !errors.Is(err, ErrWorkboardSchedule) {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkboardSchedulerContainsFactoryAndRunnerFailures(t *testing.T) {
	reader := &schedulerReaderStub{pages: map[string]workboard.SupervisionPage{
		"": schedulerPage([]workboard.SupervisionItem{
			schedulerReady("card-a", 2), schedulerReady("card-b", 3), schedulerReady("card-c", 4),
		}, ""),
	}}
	factoryErr := errors.New("factory unavailable")
	runnerErr := errors.New("worker rejected claim")
	factory := &schedulerFactoryStub{failures: map[string]error{"card-a": factoryErr}, returnWrongBinding: true}
	runner := &schedulerRunnerStub{failures: map[string]error{"card-b": runnerErr}}
	scheduler, err := NewWorkboardScheduler(reader, factory, runner, WorkboardScheduleLimits{MaxInFlight: 3, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.RunCycle(context.Background(), "board-a")
	if err != nil {
		t.Fatal(err)
	}
	if result != (WorkboardScheduleResult{Scanned: 3, Ready: 3, Launched: 2, Succeeded: 1, Failed: 2}) {
		t.Fatalf("result=%+v", result)
	}
	tasks := runner.tasksSnapshot()
	byCard := make(map[string]WorkboardWorkerTask, len(tasks))
	for _, task := range tasks {
		byCard[task.CardID] = task
	}
	if len(tasks) != 2 || byCard["card-b"].BoardID != "board-a" || byCard["card-b"].ExpectedCardRevision != 3 ||
		byCard["card-c"].BoardID != "board-a" || byCard["card-c"].ExpectedCardRevision != 4 {
		t.Fatalf("authoritative bindings were not enforced: %+v", tasks)
	}
}

func TestWorkboardSchedulerContainsFactoryAndRunnerPanics(t *testing.T) {
	reader := &schedulerReaderStub{pages: map[string]workboard.SupervisionPage{
		"": schedulerPage([]workboard.SupervisionItem{
			schedulerReady("card-a", 2), schedulerReady("card-b", 3), schedulerReady("card-c", 4),
		}, ""),
	}}
	factory := &schedulerFactoryStub{panics: map[string]bool{"card-a": true}}
	runner := &schedulerRunnerStub{panics: map[string]bool{"card-b": true}}
	scheduler, err := NewWorkboardScheduler(reader, factory, runner, WorkboardScheduleLimits{MaxInFlight: 3, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.RunCycle(context.Background(), "board-a")
	if err != nil {
		t.Fatal(err)
	}
	if result != (WorkboardScheduleResult{Scanned: 3, Ready: 3, Launched: 2, Succeeded: 1, Failed: 2}) {
		t.Fatalf("panic escaped or stopped cycle: %+v", result)
	}
}

func TestWorkboardSchedulerCancellationJoinsLaunchedWork(t *testing.T) {
	reader := &schedulerReaderStub{pages: map[string]workboard.SupervisionPage{
		"": schedulerPage([]workboard.SupervisionItem{schedulerReady("card-a", 2), schedulerReady("card-b", 3)}, ""),
	}}
	entered := make(chan struct{}, 2)
	var active atomic.Int32
	runner := &schedulerRunnerStub{run: func(ctx context.Context, _ WorkboardWorkerTask) (WorkboardCandidate, error) {
		active.Add(1)
		defer active.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return WorkboardCandidate{}, ctx.Err()
	}}
	scheduler, err := NewWorkboardScheduler(reader, &schedulerFactoryStub{}, runner,
		WorkboardScheduleLimits{MaxInFlight: 2, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, cycleErr := scheduler.RunCycle(ctx, "board-a")
		done <- cycleErr
	}()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler did not launch bounded workers")
		}
	}
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) || active.Load() != 0 {
			t.Fatalf("err=%v active=%d", err, active.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cycle returned before joining canceled work")
	}
}

func TestWorkboardSchedulerSerializesSameInstanceCycles(t *testing.T) {
	reader := &gatedSchedulerReader{entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	scheduler, err := NewWorkboardScheduler(reader, &schedulerFactoryStub{}, &schedulerRunnerStub{},
		WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, cycleErr := scheduler.RunCycle(context.Background(), "board-a")
			done <- cycleErr
		}()
	}
	select {
	case <-reader.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first cycle did not enter read")
	}
	select {
	case <-reader.entered:
		t.Fatal("same scheduler allowed overlapping cycle reads")
	case <-time.After(75 * time.Millisecond):
	}
	reader.release <- struct{}{}
	select {
	case <-reader.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second cycle did not proceed after gate release")
	}
	reader.release <- struct{}{}
	for range 2 {
		if cycleErr := <-done; cycleErr != nil {
			t.Fatal(cycleErr)
		}
	}
	if reader.maximum.Load() != 1 {
		t.Fatalf("maximum concurrent reads=%d", reader.maximum.Load())
	}
}

func TestSeparateWorkboardSchedulersRelyOnRunnerClaimCAS(t *testing.T) {
	reader := &schedulerReaderStub{pages: map[string]workboard.SupervisionPage{
		"": schedulerPage([]workboard.SupervisionItem{schedulerReady("card-a", 9)}, ""),
	}}
	runner := &claimCASRunner{claimed: make(map[string]bool)}
	first, err := NewWorkboardScheduler(reader, &schedulerFactoryStub{}, runner, WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewWorkboardScheduler(reader, &schedulerFactoryStub{}, runner, WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan WorkboardScheduleResult, 2)
	for _, scheduler := range []*WorkboardScheduler{first, second} {
		go func(s *WorkboardScheduler) {
			result, cycleErr := s.RunCycle(context.Background(), "board-a")
			if cycleErr != nil {
				t.Errorf("cycle: %v", cycleErr)
			}
			results <- result
		}(scheduler)
	}
	a, b := <-results, <-results
	if a.Launched+b.Launched != 2 || a.Succeeded+b.Succeeded != 1 || a.Failed+b.Failed != 1 || runner.calls.Load() != 2 {
		t.Fatalf("results=%+v %+v runner calls=%d", a, b, runner.calls.Load())
	}
}

type schedulerReaderStub struct {
	mu      sync.Mutex
	pages   map[string]workboard.SupervisionPage
	options []workboard.SupervisionOptions
}

type panicSchedulerReader struct{}

func (panicSchedulerReader) Read(context.Context, string, workboard.SupervisionOptions) (workboard.SupervisionPage, error) {
	panic("reader panic")
}

func (s *schedulerReaderStub) Read(_ context.Context, boardID string, options workboard.SupervisionOptions) (workboard.SupervisionPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.options = append(s.options, options)
	page, ok := s.pages[options.After]
	if !ok {
		return workboard.SupervisionPage{}, errors.New("unexpected supervision cursor")
	}
	page.Items = append([]workboard.SupervisionItem{}, page.Items...)
	page.BoardID = boardID
	for index := range page.Items {
		page.Items[index].BoardID = boardID
	}
	return page, nil
}

func (s *schedulerReaderStub) afters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, len(s.options))
	for index := range s.options {
		result[index] = s.options[index].After
	}
	return result
}

type schedulerFactoryStub struct {
	mu                 sync.Mutex
	items              []workboard.SupervisionItem
	failures           map[string]error
	panics             map[string]bool
	returnWrongBinding bool
}

func (s *schedulerFactoryStub) BuildWorkboardTask(_ context.Context, item workboard.SupervisionItem) (WorkboardWorkerTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
	if s.panics[item.CardID] {
		panic("factory panic")
	}
	if err := s.failures[item.CardID]; err != nil {
		return WorkboardWorkerTask{}, err
	}
	task := WorkboardWorkerTask{BoardID: item.BoardID, CardID: item.CardID, ExpectedCardRevision: item.CardRevision}
	if s.returnWrongBinding {
		task.BoardID, task.CardID, task.ExpectedCardRevision = "untrusted-board", "untrusted-card", 999
	}
	return task, nil
}

func (s *schedulerFactoryStub) itemsSnapshot() []workboard.SupervisionItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workboard.SupervisionItem{}, s.items...)
}

type schedulerRunnerStub struct {
	mu       sync.Mutex
	tasks    []WorkboardWorkerTask
	failures map[string]error
	panics   map[string]bool
	run      func(context.Context, WorkboardWorkerTask) (WorkboardCandidate, error)
}

func (s *schedulerRunnerStub) Run(ctx context.Context, task WorkboardWorkerTask) (WorkboardCandidate, error) {
	s.mu.Lock()
	s.tasks = append(s.tasks, task)
	run, failure, shouldPanic := s.run, s.failures[task.CardID], s.panics[task.CardID]
	s.mu.Unlock()
	if shouldPanic {
		panic("runner panic")
	}
	if run != nil {
		return run(ctx, task)
	}
	return WorkboardCandidate{}, failure
}

func (s *schedulerRunnerStub) tasksSnapshot() []WorkboardWorkerTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WorkboardWorkerTask{}, s.tasks...)
}

type gatedSchedulerReader struct {
	entered, release chan struct{}
	active           atomic.Int32
	maximum          atomic.Int32
}

func (s *gatedSchedulerReader) Read(ctx context.Context, boardID string, _ workboard.SupervisionOptions) (workboard.SupervisionPage, error) {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for observed := s.maximum.Load(); active > observed && !s.maximum.CompareAndSwap(observed, active); observed = s.maximum.Load() {
	}
	s.entered <- struct{}{}
	select {
	case <-s.release:
		return schedulerPageForBoard(boardID, nil, ""), nil
	case <-ctx.Done():
		return workboard.SupervisionPage{}, ctx.Err()
	}
}

type claimCASRunner struct {
	mu      sync.Mutex
	claimed map[string]bool
	calls   atomic.Int32
}

func (r *claimCASRunner) Run(_ context.Context, task WorkboardWorkerTask) (WorkboardCandidate, error) {
	r.calls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed[task.BoardID+"/"+task.CardID] {
		return WorkboardCandidate{}, errors.New("claim revision conflict")
	}
	r.claimed[task.BoardID+"/"+task.CardID] = true
	return WorkboardCandidate{}, nil
}

func schedulerReady(cardID string, revision int64) workboard.SupervisionItem {
	return workboard.SupervisionItem{Version: 1, BoardID: "board-a", CardID: cardID, CardRevision: revision,
		State: workboard.SupervisionReady, Reason: workboard.SupervisionDependenciesSatisfied,
		Actions: workboard.SupervisionActions{Claim: true}}
}

func schedulerRunning(cardID string, state workboard.SupervisionState) workboard.SupervisionItem {
	now := time.Date(2026, 9, 10, 16, 0, 0, 0, time.UTC)
	reason := workboard.SupervisionLeaseHealthy
	actions := workboard.SupervisionActions{PauseRequest: true, CancelRequest: true}
	taskID := ""
	if state == workboard.SupervisionStalled {
		reason = workboard.SupervisionHeartbeatStale
		actions = workboard.SupervisionActions{CancelRequest: true, RecoveryCheck: true}
	}
	if state == workboard.SupervisionOrphaned {
		reason = workboard.SupervisionTaskFailed
		taskID = "task-" + cardID
		actions = workboard.SupervisionActions{CancelRequest: true, RecoveryCheck: true}
	}
	return workboard.SupervisionItem{Version: 1, BoardID: "board-a", CardID: cardID, CardRevision: 2,
		State: state, Reason: reason, AttemptID: "attempt-" + cardID, ClaimID: "claim-" + cardID,
		ClaimRevision: 1, WorkerID: "worker-" + cardID, TaskID: taskID, LastHeartbeat: now, ExpiresAt: now.Add(time.Minute), Actions: actions}
}

func schedulerPage(items []workboard.SupervisionItem, next string) workboard.SupervisionPage {
	return schedulerPageForBoard("board-a", items, next)
}

func schedulerPageForBoard(boardID string, items []workboard.SupervisionItem, next string) workboard.SupervisionPage {
	return workboard.SupervisionPage{Version: 1, BoardID: boardID, BoardRevision: 1,
		ObservedAt: time.Date(2026, 9, 10, 16, 0, 0, 0, time.UTC), Items: items, NextCursor: next, HasMore: next != ""}
}
