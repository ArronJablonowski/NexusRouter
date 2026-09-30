package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardScheduleSupervisorRunsImmediateDynamicPassesWithoutOverlap(t *testing.T) {
	lister := &supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a"), supervisorBoard("board-c")}, {supervisorBoard("board-b")}}}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	concurrentEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	var active, maximum atomic.Int32
	runner := WorkboardCycleRunnerFunc(func(ctx context.Context, boardID string) (WorkboardScheduleResult, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		switch boardID {
		case "board-a":
			close(firstEntered)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return WorkboardScheduleResult{}, ctx.Err()
			}
		case "board-b":
			close(secondEntered)
		case "board-c":
			close(concurrentEntered)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return WorkboardScheduleResult{}, ctx.Err()
			}
		}
		return WorkboardScheduleResult{}, nil
	})
	supervisor, err := StartWorkboardScheduleSupervisor(context.Background(), lister, runner, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("initial scheduling pass was not immediate")
	}
	// Several intervals pass while the joined cycle is blocked. No second list
	// or cycle may begin and no missed ticks may accumulate.
	time.Sleep(35 * time.Millisecond)
	if got := lister.callsCount(); got != 1 {
		t.Fatalf("blocked pass overlapped or accumulated ticks: %d list calls", got)
	}
	select {
	case <-concurrentEntered:
	case <-time.After(time.Second):
		t.Fatal("blocked board starved another board in the same pass")
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("next pass did not rediscover the dynamic board list")
	}
	if maximum.Load() != 2 || lister.callsCount() != 2 {
		t.Fatalf("passes overlapped or board snapshot was not refreshed: max=%d lists=%d", maximum.Load(), lister.callsCount())
	}
}

func TestWorkboardScheduleSupervisorValidatesCompleteActiveSnapshotBeforeCycles(t *testing.T) {
	tests := map[string]workboard.BoardPage{
		"archived board": {Version: workboard.SchemaVersion, Items: []workboard.Board{supervisorBoard("board-a"), func() workboard.Board {
			board := supervisorBoard("board-b")
			board.State = "archived"
			return board
		}()}},
		"malformed": {Version: workboard.SchemaVersion + 1, Items: []workboard.Board{supervisorBoard("board-a")}},
	}
	for name, page := range tests {
		t.Run(name, func(t *testing.T) {
			lister := &supervisorBoardLister{pages: []workboard.BoardPage{page}}
			var cycles atomic.Int32
			runner := WorkboardCycleRunnerFunc(func(context.Context, string) (WorkboardScheduleResult, error) {
				cycles.Add(1)
				return WorkboardScheduleResult{}, nil
			})
			supervisor, err := StartWorkboardScheduleSupervisor(context.Background(), lister, runner, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close()
			awaitSupervisorHealth(t, supervisor, "degraded", "supervisor_error")
			if cycles.Load() != 0 {
				t.Fatalf("invalid board snapshot launched %d cycles", cycles.Load())
			}
		})
	}
}

func TestWorkboardScheduleSupervisorDiscoversBoundedPagesBeforeRunning(t *testing.T) {
	lister := &supervisorBoardLister{pages: []workboard.BoardPage{
		{Version: workboard.SchemaVersion, Items: []workboard.Board{supervisorBoard("board-a")}, HasMore: true, NextCursor: "page-two"},
		{Version: workboard.SchemaVersion, Items: []workboard.Board{supervisorBoard("board-b")}},
	}}
	var mu sync.Mutex
	var boards []string
	runner := WorkboardCycleRunnerFunc(func(_ context.Context, boardID string) (WorkboardScheduleResult, error) {
		mu.Lock()
		boards = append(boards, boardID)
		mu.Unlock()
		return WorkboardScheduleResult{}, nil
	})
	if err := runWorkboardSchedulePass(context.Background(), lister, runner); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(boards) != 2 || !containsExact(boards, "board-a") || !containsExact(boards, "board-b") {
		t.Fatalf("scheduled boards=%q", boards)
	}
	if got := lister.aftersSnapshot(); len(got) != 2 || got[0] != "" || got[1] != "page-two" {
		t.Fatalf("board cursors=%q", got)
	}
}

func TestWorkboardScheduleSupervisorDiscoversDurableBoardsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "boards.db")
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	for index := range 26 {
		title := fmt.Sprintf("Board %02d", index)
		_, err = bridge.NativeMutate(ctx, webui.BoardRequest{Version: webui.ContractVersion, Action: webui.BoardCreate,
			IdempotencyKey: fmt.Sprintf("scheduler-durable-board-%02d", index), Title: &title})
		if err != nil {
			store.Close()
			t.Fatal(index, err)
		}
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seen := make(map[string]bool)
	var seenMu sync.Mutex
	err = runWorkboardSchedulePass(ctx, store, WorkboardCycleRunnerFunc(func(_ context.Context, boardID string) (WorkboardScheduleResult, error) {
		seenMu.Lock()
		seen[boardID] = true
		seenMu.Unlock()
		return WorkboardScheduleResult{}, nil
	}))
	if err != nil || len(seen) != 26 {
		t.Fatalf("durable boards=%d err=%v", len(seen), err)
	}
}

func TestWorkboardScheduleSupervisorRejectsCrossPageDriftAndLimitBeforeRunning(t *testing.T) {
	full := make([]workboard.Board, 25)
	for index := range full {
		full[index] = supervisorBoard(fmt.Sprintf("board-%02d", index))
	}
	tests := map[string][]workboard.BoardPage{
		"duplicate": {
			{Version: workboard.SchemaVersion, Items: []workboard.Board{supervisorBoard("board-a")}, HasMore: true, NextCursor: "next"},
			{Version: workboard.SchemaVersion, Items: []workboard.Board{supervisorBoard("board-a")}},
		},
		"limit": {
			{Version: workboard.SchemaVersion, Items: full, HasMore: true, NextCursor: "page-2"},
			{Version: workboard.SchemaVersion, Items: full, HasMore: true, NextCursor: "page-3"},
			{Version: workboard.SchemaVersion, Items: full, HasMore: true, NextCursor: "page-4"},
			{Version: workboard.SchemaVersion, Items: full, HasMore: true, NextCursor: "page-5"},
		},
	}
	for name, pages := range tests {
		t.Run(name, func(t *testing.T) {
			// Give each page distinct valid boards in the limit fixture.
			if name == "limit" {
				for page := range pages {
					pages[page].Items = make([]workboard.Board, 25)
					for item := range pages[page].Items {
						pages[page].Items[item] = supervisorBoard(fmt.Sprintf("board-%03d", page*25+item))
					}
				}
			}
			var cycles atomic.Int32
			err := runWorkboardSchedulePass(context.Background(), &supervisorBoardLister{pages: pages},
				WorkboardCycleRunnerFunc(func(context.Context, string) (WorkboardScheduleResult, error) {
					cycles.Add(1)
					return WorkboardScheduleResult{}, nil
				}))
			if err == nil || cycles.Load() != 0 {
				t.Fatalf("err=%v cycles=%d", err, cycles.Load())
			}
		})
	}
}

func TestWorkboardScheduleSupervisorRecoversHealthAfterErrorsAndPanics(t *testing.T) {
	transient := errors.New("transient")
	lister := &supervisorBoardLister{failures: []error{transient, nil}, panicCalls: map[int]bool{3: true}}
	supervisor, err := StartWorkboardScheduleSupervisor(context.Background(), lister,
		WorkboardCycleRunnerFunc(func(context.Context, string) (WorkboardScheduleResult, error) { return WorkboardScheduleResult{}, nil }),
		20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	awaitSupervisorHealth(t, supervisor, "degraded", "supervisor_error")
	awaitSupervisorHealth(t, supervisor, "healthy", "supervisor_ok")
	awaitSupervisorCallCount(t, lister, 3)
	awaitSupervisorHealth(t, supervisor, "degraded", "supervisor_error")
	awaitSupervisorCallCount(t, lister, 4)
	awaitSupervisorHealth(t, supervisor, "healthy", "supervisor_ok")
	if check := supervisor.Health(); check.Validate() != nil {
		t.Fatalf("invalid supervisor health: %+v", check)
	}
}

func TestWorkboardScheduleSupervisorContainsCyclePanicAndRecovers(t *testing.T) {
	lister := &supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a")}}}
	var calls atomic.Int32
	runner := WorkboardCycleRunnerFunc(func(context.Context, string) (WorkboardScheduleResult, error) {
		if calls.Add(1) == 1 {
			panic("cycle")
		}
		return WorkboardScheduleResult{}, nil
	})
	supervisor, err := StartWorkboardScheduleSupervisor(context.Background(), lister, runner, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	awaitSupervisorHealth(t, supervisor, "degraded", "supervisor_error")
	awaitSupervisorHealth(t, supervisor, "healthy", "supervisor_ok")
}

func TestWorkboardScheduleSupervisorFailureDoesNotStarveLaterBoards(t *testing.T) {
	for name, fail := range map[string]func() error{
		"error": func() error { return errors.New("cycle failed") },
		"panic": func() error { panic("cycle") },
	} {
		t.Run(name, func(t *testing.T) {
			lister := &supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a"), supervisorBoard("board-b")}}}
			secondRan := make(chan struct{})
			runner := WorkboardCycleRunnerFunc(func(_ context.Context, boardID string) (WorkboardScheduleResult, error) {
				if boardID == "board-a" {
					return WorkboardScheduleResult{}, fail()
				}
				close(secondRan)
				return WorkboardScheduleResult{}, nil
			})
			supervisor, err := StartWorkboardScheduleSupervisor(context.Background(), lister, runner, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close()
			select {
			case <-secondRan:
			case <-time.After(time.Second):
				t.Fatal("failure on board A starved board B")
			}
			awaitSupervisorHealth(t, supervisor, "degraded", "supervisor_error")
		})
	}
}

func TestWorkboardScheduleSupervisorCloseCancelsJoinsAndIsConcurrentSafe(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	lister := &supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a")}}}
	runner := WorkboardCycleRunnerFunc(func(ctx context.Context, _ string) (WorkboardScheduleResult, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		close(returned)
		return WorkboardScheduleResult{}, ctx.Err()
	})
	supervisor, err := StartWorkboardScheduleSupervisor(context.Background(), lister, runner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cycle did not start")
	}
	const callers = 8
	errs := make(chan error, callers)
	for range callers {
		go func() { errs <- supervisor.Close() }()
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cycle did not observe cancellation")
	}
	awaitSupervisorHealth(t, supervisor, "unavailable", "supervisor_stopping")
	close(release)
	for range callers {
		if err = <-errs; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-returned:
	default:
		t.Fatal("Close returned before the canceled cycle joined")
	}
	if err = supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	awaitSupervisorHealth(t, supervisor, "unavailable", "supervisor_stopped")
}

func TestWorkboardScheduleSupervisorReportsStallAndRecovers(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	runner := WorkboardCycleRunnerFunc(func(ctx context.Context, _ string) (WorkboardScheduleResult, error) {
		close(entered)
		select {
		case <-release:
			return WorkboardScheduleResult{}, nil
		case <-ctx.Done():
			return WorkboardScheduleResult{}, ctx.Err()
		}
	})
	supervisor, err := StartWorkboardScheduleSupervisor(context.Background(),
		&supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a")}}}, runner, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	<-entered
	awaitSupervisorHealth(t, supervisor, "degraded", "supervisor_stalled")
	close(release)
	awaitSupervisorHealth(t, supervisor, "healthy", "supervisor_ok")
}

type WorkboardCycleRunnerFunc func(context.Context, string) (WorkboardScheduleResult, error)

func (f WorkboardCycleRunnerFunc) RunCycle(ctx context.Context, boardID string) (WorkboardScheduleResult, error) {
	return f(ctx, boardID)
}

type supervisorBoardLister struct {
	mu         sync.Mutex
	calls      int
	afters     []string
	boards     [][]workboard.Board
	pages      []workboard.BoardPage
	failures   []error
	panicCalls map[int]bool
}

func (s *supervisorBoardLister) ListWorkboards(_ context.Context, options workboard.BoardListOptions) (workboard.BoardPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.afters = append(s.afters, options.After)
	call := s.calls
	if options.Limit != 25 || options.State != "active" {
		return workboard.BoardPage{}, errors.New("unexpected list options")
	}
	if s.panicCalls[call] {
		panic("list")
	}
	if call <= len(s.failures) && s.failures[call-1] != nil {
		return workboard.BoardPage{}, s.failures[call-1]
	}
	if len(s.pages) != 0 {
		index := min(call-1, len(s.pages)-1)
		return s.pages[index], nil
	}
	var boards []workboard.Board
	if len(s.boards) != 0 {
		index := min(call-1, len(s.boards)-1)
		boards = s.boards[index]
	}
	return workboard.BoardPage{Version: workboard.SchemaVersion, Items: boards}, nil
}

func (s *supervisorBoardLister) aftersSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.afters...)
}

func (s *supervisorBoardLister) callsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func supervisorBoard(id string) workboard.Board {
	now := time.Now().UTC()
	return workboard.Board{Version: workboard.SchemaVersion, ID: id, Revision: 1, LayoutRevision: 1, EventSequence: 1,
		State: "active", Title: id, CreatedAt: now, UpdatedAt: now}
}

func awaitSupervisorHealth(t *testing.T, supervisor *WorkboardScheduleSupervisor, status, code string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		check := supervisor.Health()
		if check.Status == status && check.Code == code {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("health did not become %s/%s: %+v", status, code, supervisor.Health())
}

func awaitSupervisorCallCount(t *testing.T, lister *supervisorBoardLister, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if lister.callsCount() >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("list calls did not reach %d: %d", count, lister.callsCount())
}
