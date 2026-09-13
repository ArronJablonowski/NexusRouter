package app

import (
	"context"
	"errors"
	"sync"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

const maxWorkboardScheduleScan = 10_000

var (
	ErrWorkboardSchedule      = errors.New("workboard scheduling failed")
	ErrWorkboardScheduleLimit = errors.New("workboard scheduling scan limit exceeded")
)

// WorkboardSupervisionReader supplies the authoritative discover-don't-track
// projection used by one scheduling cycle. Implementations must preserve the
// observation snapshot across cursor pages.
type WorkboardSupervisionReader interface {
	Read(context.Context, string, workboard.SupervisionOptions) (workboard.SupervisionPage, error)
}

// WorkboardTaskFactory constructs execution intent for an already discovered
// card. It must not dispatch providers or tools: the runner first obtains the
// durable card claim, then invokes the task callback.
type WorkboardTaskFactory interface {
	BuildWorkboardTask(context.Context, workboard.SupervisionItem) (WorkboardWorkerTask, error)
}

type WorkboardTaskFactoryFunc func(context.Context, workboard.SupervisionItem) (WorkboardWorkerTask, error)

func (f WorkboardTaskFactoryFunc) BuildWorkboardTask(ctx context.Context, item workboard.SupervisionItem) (WorkboardWorkerTask, error) {
	return f(ctx, item)
}

type WorkboardTaskRunner interface {
	Run(context.Context, WorkboardWorkerTask) (WorkboardCandidate, error)
}

type WorkboardTaskRunnerFunc func(context.Context, WorkboardWorkerTask) (WorkboardCandidate, error)

func (f WorkboardTaskRunnerFunc) Run(ctx context.Context, task WorkboardWorkerTask) (WorkboardCandidate, error) {
	return f(ctx, task)
}

type WorkboardScheduleLimits struct {
	MaxInFlight int
	ScanLimit   int
}

// WorkboardScheduleResult contains bounded, non-sensitive cycle accounting.
// Failed includes factory and runner failures. Deferred cards were observed as
// ready but not considered because existing or newly launched work filled the
// configured WIP bound.
type WorkboardScheduleResult struct {
	Scanned     int
	Ready       int
	ExistingWIP int
	Launched    int
	Succeeded   int
	Failed      int
	Deferred    int
}

// WorkboardScheduler performs one bounded scheduling cycle for one board. It
// retains no ownership truth between cycles; durable supervision and claim CAS
// remain authoritative across daemon restarts. Claim CAS prevents duplicate
// ownership of one card; deployment still permits only one daemon scheduler.
type WorkboardScheduler struct {
	reader  WorkboardSupervisionReader
	factory WorkboardTaskFactory
	runner  WorkboardTaskRunner
	limits  WorkboardScheduleLimits
	cycles  *workboardCycleGate
}

// workboardCycleGate serializes duplicate cycles for one board without making
// unrelated boards wait behind it. The entry set and simultaneously admitted
// cycles are both bounded by the durable Workboard board limit. Entries are
// removed after their final holder or waiter leaves, so arbitrary rejected IDs
// cannot accumulate process-lifetime state.
type workboardCycleGate struct {
	mu      sync.Mutex
	entries map[string]*workboardCycleGateEntry
	slots   chan struct{}
}

type workboardCycleGateEntry struct {
	token chan struct{}
	refs  int
}

func NewWorkboardScheduler(reader WorkboardSupervisionReader, factory WorkboardTaskFactory,
	runner WorkboardTaskRunner, limits WorkboardScheduleLimits,
) (*WorkboardScheduler, error) {
	if reader == nil || factory == nil || runner == nil || limits.MaxInFlight < 1 || limits.MaxInFlight > 64 ||
		limits.ScanLimit < 1 || limits.ScanLimit > maxWorkboardScheduleScan {
		return nil, ErrAdmission
	}
	return &WorkboardScheduler{reader: reader, factory: factory, runner: runner, limits: limits,
		cycles: &workboardCycleGate{entries: make(map[string]*workboardCycleGateEntry), slots: make(chan struct{}, workboard.MaxBoards)}}, nil
}

func (s *WorkboardScheduler) RunCycle(ctx context.Context, boardID string) (WorkboardScheduleResult, error) {
	result := WorkboardScheduleResult{}
	if s == nil || ctx == nil || boardID == "" {
		return result, ErrAdmission
	}
	release, err := s.cycles.acquire(ctx, boardID)
	if err != nil {
		return result, err
	}
	defer release()

	ready, err := s.discover(ctx, boardID, &result)
	if err != nil {
		return result, err
	}
	available := s.limits.MaxInFlight - result.ExistingWIP
	if available < 1 {
		result.Deferred = result.Ready
		return result, nil
	}

	tasks := make([]WorkboardWorkerTask, 0, available)
	considered := 0
	for _, item := range ready {
		if len(tasks) >= available || ctx.Err() != nil {
			break
		}
		considered++
		task, buildErr := buildScheduledWorkboardTask(ctx, s.factory, item)
		if buildErr != nil {
			result.Failed++
			continue
		}
		// Scheduling authority, never a factory or model, binds the durable
		// card identity and exact revision observed by this cycle.
		task.BoardID = item.BoardID
		task.CardID = item.CardID
		task.ExpectedCardRevision = item.CardRevision
		task.WorkerID = item.AssigneeID
		tasks = append(tasks, task)
	}
	result.Deferred = result.Ready - considered
	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	var outcomes sync.WaitGroup
	var outcomeMu sync.Mutex
	result.Launched = len(tasks)
	for _, task := range tasks {
		outcomes.Add(1)
		go func(task WorkboardWorkerTask) {
			defer outcomes.Done()
			_, runErr := runScheduledWorkboardTask(ctx, s.runner, task)
			outcomeMu.Lock()
			defer outcomeMu.Unlock()
			if runErr != nil {
				result.Failed++
			} else {
				result.Succeeded++
			}
		}(task)
	}
	outcomes.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

func (g *workboardCycleGate) acquire(ctx context.Context, boardID string) (func(), error) {
	if g == nil || ctx == nil || boardID == "" {
		return nil, ErrAdmission
	}
	g.mu.Lock()
	entry := g.entries[boardID]
	if entry == nil {
		if len(g.entries) >= workboard.MaxBoards {
			g.mu.Unlock()
			return nil, ErrWorkboardScheduleLimit
		}
		entry = &workboardCycleGateEntry{token: make(chan struct{}, 1)}
		g.entries[boardID] = entry
	}
	entry.refs++
	g.mu.Unlock()

	forget := func() {
		g.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(g.entries, boardID)
		}
		g.mu.Unlock()
	}
	select {
	case entry.token <- struct{}{}:
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		<-entry.token
		forget()
		return nil, ctx.Err()
	}
	return func() {
		<-g.slots
		<-entry.token
		forget()
	}, nil
}

func buildScheduledWorkboardTask(ctx context.Context, factory WorkboardTaskFactory, item workboard.SupervisionItem) (task WorkboardWorkerTask, err error) {
	defer func() {
		if recover() != nil {
			task, err = WorkboardWorkerTask{}, ErrWorkboardSchedule
		}
	}()
	return factory.BuildWorkboardTask(ctx, item)
}

func runScheduledWorkboardTask(ctx context.Context, runner WorkboardTaskRunner, task WorkboardWorkerTask) (candidate WorkboardCandidate, err error) {
	defer func() {
		if recover() != nil {
			candidate, err = WorkboardCandidate{}, ErrWorkboardSchedule
		}
	}()
	return runner.Run(ctx, task)
}

func (s *WorkboardScheduler) discover(ctx context.Context, boardID string, result *WorkboardScheduleResult) ([]workboard.SupervisionItem, error) {
	ready := make([]workboard.SupervisionItem, 0, min(s.limits.MaxInFlight, s.limits.ScanLimit))
	after := ""
	for {
		remaining := s.limits.ScanLimit - result.Scanned
		if remaining < 1 {
			return nil, ErrWorkboardScheduleLimit
		}
		page, err := readScheduledWorkboardPage(ctx, s.reader, boardID,
			workboard.SupervisionOptions{After: after, Limit: min(remaining, workboard.MaxSupervisionPageItems)})
		if err != nil {
			return nil, errors.Join(ErrWorkboardSchedule, err)
		}
		if page.Validate() != nil || page.BoardID != boardID {
			return nil, ErrWorkboardSchedule
		}
		result.Scanned += len(page.Items)
		for _, item := range page.Items {
			if item.State == workboard.SupervisionReady {
				result.Ready++
				ready = append(ready, item)
			} else {
				result.ExistingWIP++
			}
		}
		if !page.HasMore {
			return ready, nil
		}
		if result.Scanned >= s.limits.ScanLimit || page.NextCursor == after {
			return nil, ErrWorkboardScheduleLimit
		}
		after = page.NextCursor
	}
}

func readScheduledWorkboardPage(ctx context.Context, reader WorkboardSupervisionReader, boardID string,
	options workboard.SupervisionOptions,
) (page workboard.SupervisionPage, err error) {
	defer func() {
		if recover() != nil {
			page, err = workboard.SupervisionPage{}, ErrWorkboardSchedule
		}
	}()
	return reader.Read(ctx, boardID, options)
}
