package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// WorkboardBoardLister supplies stable cursor pages for one bounded active-board
// snapshot. The supervisor reads and validates the complete snapshot before it
// permits any board cycle to mutate durable state.
type WorkboardBoardLister interface {
	ListWorkboards(context.Context, workboard.BoardListOptions) (workboard.BoardPage, error)
}

// WorkboardCycleRunner runs one fully joined scheduling cycle for one board.
// WorkboardScheduler implements this interface.
type WorkboardCycleRunner interface {
	RunCycle(context.Context, string) (WorkboardScheduleResult, error)
}

// WorkboardScheduleSupervisor owns repeated application-level scheduling
// passes. Each pass discovers the current active boards and starts one bounded
// cycle per board so a slow board cannot prevent another from progressing. It
// is composed by the stock daemon only when explicitly enabled.
type WorkboardScheduleSupervisor struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	status string
	code   string
}

// WorkboardScheduleHealth is supervisor-owned until daemon health composition
// has a distinct Workboard scheduler component. Reusing the generic supervisor
// health component would collide with the existing worker supervisor singleton.
type WorkboardScheduleHealth struct {
	Status string `json:"status"`
	Code   string `json:"code"`
}

func (h WorkboardScheduleHealth) Validate() error {
	if h.Status == "unknown" && h.Code == "supervisor_starting" ||
		h.Status == "healthy" && h.Code == "supervisor_ok" ||
		h.Status == "degraded" && h.Code == "supervisor_stalled" ||
		h.Status == "degraded" && h.Code == "supervisor_error" ||
		h.Status == "unavailable" && h.Code == "supervisor_stopping" ||
		h.Status == "unavailable" && h.Code == "supervisor_stopped" {
		return nil
	}
	return ErrWorkboardSchedule
}

// StartWorkboardScheduleSupervisor starts an immediate pass and then waits a
// full interval after each joined pass. It therefore cannot overlap passes or
// accumulate ticker work while a pass is slow.
func StartWorkboardScheduleSupervisor(ctx context.Context, lister WorkboardBoardLister,
	cycles WorkboardCycleRunner, interval time.Duration,
) (*WorkboardScheduleSupervisor, error) {
	if ctx == nil || ctx.Err() != nil || lister == nil || cycles == nil || interval <= 0 {
		return nil, ErrAdmission
	}
	runCtx, cancel := context.WithCancel(ctx)
	s := &WorkboardScheduleSupervisor{cancel: cancel, done: make(chan struct{}), status: "unknown", code: "supervisor_starting"}
	go s.run(runCtx, lister, cycles, interval)
	return s, nil
}

func (s *WorkboardScheduleSupervisor) run(ctx context.Context, lister WorkboardBoardLister,
	cycles WorkboardCycleRunner, interval time.Duration,
) {
	defer close(s.done)
	defer s.setHealth("unavailable", "supervisor_stopped")
	for {
		if ctx.Err() != nil {
			return
		}
		result := make(chan error, 1)
		go func() { result <- runWorkboardSchedulePass(ctx, lister, cycles) }()
		stall := time.NewTimer(interval)
		var err error
		select {
		case err = <-result:
			stall.Stop()
		case <-stall.C:
			s.setHealth("degraded", "supervisor_stalled")
			err = <-result
		}
		if ctx.Err() != nil {
			s.setHealth("unavailable", "supervisor_stopping")
			return
		}
		if err != nil {
			s.setHealth("degraded", "supervisor_error")
		} else {
			s.setHealth("healthy", "supervisor_ok")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func runWorkboardSchedulePass(ctx context.Context, lister WorkboardBoardLister, cycles WorkboardCycleRunner) error {
	boards, err := listScheduledWorkboards(ctx, lister)
	if err != nil {
		return errors.Join(ErrWorkboardSchedule, err)
	}
	results := make(chan error, len(boards))
	for _, board := range boards {
		go func(boardID string) {
			_, cycleErr := runScheduledWorkboardCycle(ctx, cycles, boardID)
			results <- cycleErr
		}(board.ID)
	}
	var passErr error
	for range boards {
		if cycleErr := <-results; cycleErr != nil {
			passErr = errors.Join(passErr, cycleErr)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if passErr != nil {
		return errors.Join(ErrWorkboardSchedule, passErr)
	}
	return nil
}

func listScheduledWorkboards(ctx context.Context, lister WorkboardBoardLister) ([]workboard.Board, error) {
	const pageLimit = 25
	boards := make([]workboard.Board, 0, workboard.MaxBoards)
	seen := make(map[string]bool, workboard.MaxBoards)
	after := ""
	for {
		page, err := listScheduledWorkboardPage(ctx, lister,
			workboard.BoardListOptions{After: after, Limit: pageLimit, State: "active"})
		if err != nil {
			return nil, errors.Join(ErrWorkboardSchedule, err)
		}
		if page.Validate() != nil {
			return nil, ErrWorkboardSchedule
		}
		for _, board := range page.Items {
			if board.State != "active" || seen[board.ID] {
				return nil, ErrWorkboardSchedule
			}
			if len(boards) >= workboard.MaxBoards {
				return nil, ErrWorkboardScheduleLimit
			}
			seen[board.ID] = true
			boards = append(boards, board)
		}
		if !page.HasMore {
			return boards, nil
		}
		if len(boards) >= workboard.MaxBoards {
			return nil, ErrWorkboardScheduleLimit
		}
		if page.NextCursor == after {
			return nil, ErrWorkboardSchedule
		}
		after = page.NextCursor
	}
}

func listScheduledWorkboardPage(ctx context.Context, lister WorkboardBoardLister,
	options workboard.BoardListOptions,
) (page workboard.BoardPage, err error) {
	defer func() {
		if recover() != nil {
			page, err = workboard.BoardPage{}, ErrWorkboardSchedule
		}
	}()
	return lister.ListWorkboards(ctx, options)
}

func runScheduledWorkboardCycle(ctx context.Context, cycles WorkboardCycleRunner, boardID string) (result WorkboardScheduleResult, err error) {
	defer func() {
		if recover() != nil {
			result, err = WorkboardScheduleResult{}, ErrWorkboardSchedule
		}
	}()
	return cycles.RunCycle(ctx, boardID)
}

func (s *WorkboardScheduleSupervisor) setHealth(status, code string) {
	s.mu.Lock()
	s.status, s.code = status, code
	s.mu.Unlock()
}

// Health returns only bounded supervisor state; callback errors and board
// identifiers are never exposed. A later successful pass clears degradation.
func (s *WorkboardScheduleSupervisor) Health() WorkboardScheduleHealth {
	check := WorkboardScheduleHealth{Status: "unknown", Code: "supervisor_starting"}
	if s == nil {
		return check
	}
	s.mu.Lock()
	check.Status, check.Code = s.status, s.code
	s.mu.Unlock()
	return check
}

// Close cancels the current list or board cycle and joins the supervisor. It is
// safe for concurrent and repeated callers.
func (s *WorkboardScheduleSupervisor) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.setHealth("unavailable", "supervisor_stopping")
		s.cancel()
		<-s.done
	})
	return nil
}
