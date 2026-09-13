package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardScheduleSupervisorRunsImmediateDynamicPassesWithoutOverlap(t *testing.T) {
	lister := &supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a"), supervisorBoard("board-c")}, {supervisorBoard("board-b")}}}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	serialEntered := make(chan struct{})
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
			close(serialEntered)
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
	case <-serialEntered:
		t.Fatal("boards within one pass overlapped")
	default:
	}
	close(releaseFirst)
	select {
	case <-serialEntered:
	case <-time.After(time.Second):
		t.Fatal("second board in the pass did not run serially")
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("next pass did not rediscover the dynamic board list")
	}
	if maximum.Load() != 1 || lister.callsCount() != 2 {
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
		"additional page": {Version: workboard.SchemaVersion, Items: []workboard.Board{supervisorBoard("board-a")}, HasMore: true, NextCursor: "next"},
		"malformed":       {Version: workboard.SchemaVersion + 1, Items: []workboard.Board{supervisorBoard("board-a")}},
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
	returned := make(chan struct{})
	lister := &supervisorBoardLister{boards: [][]workboard.Board{{supervisorBoard("board-a")}}}
	runner := WorkboardCycleRunnerFunc(func(ctx context.Context, _ string) (WorkboardScheduleResult, error) {
		close(entered)
		<-ctx.Done()
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

type WorkboardCycleRunnerFunc func(context.Context, string) (WorkboardScheduleResult, error)

func (f WorkboardCycleRunnerFunc) RunCycle(ctx context.Context, boardID string) (WorkboardScheduleResult, error) {
	return f(ctx, boardID)
}

type supervisorBoardLister struct {
	mu         sync.Mutex
	calls      int
	boards     [][]workboard.Board
	pages      []workboard.BoardPage
	failures   []error
	panicCalls map[int]bool
}

func (s *supervisorBoardLister) ListWorkboards(_ context.Context, options workboard.BoardListOptions) (workboard.BoardPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	call := s.calls
	if options != (workboard.BoardListOptions{Limit: workboard.MaxPageItems, State: "active"}) {
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
