package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

type restartSchedulerEvaluator struct{}

func (restartSchedulerEvaluator) EvaluateCandidate(context.Context, workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	return nil, ErrAdmission
}

func TestWorkboardSchedulerRestartTreatsPreexistingClaimsOnlyAsObservedWIP(t *testing.T) {
	tests := []struct {
		name          string
		terminal      bool
		observedAfter time.Duration
		state         workboard.SupervisionState
		reason        workboard.SupervisionReason
	}{
		{name: "healthy", observedAfter: 20 * time.Second,
			state: workboard.SupervisionRunning, reason: workboard.SupervisionLeaseHealthy},
		{name: "stale", observedAfter: 2 * time.Minute,
			state: workboard.SupervisionStalled, reason: workboard.SupervisionLeaseExpired},
		{name: "orphaned", terminal: true, observedAfter: 20 * time.Second,
			state: workboard.SupervisionOrphaned, reason: workboard.SupervisionTaskCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "workboard.db")
			store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
			card, err := store.GetCard(ctx, boardID, cardID)
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			claimAt := card.UpdatedAt.Add(time.Second)
			observedAt := claimAt.Add(test.observedAfter)
			taskID, sessionID := "restart-"+test.name+"-task", "restart-"+test.name+"-session"
			start := runtime.Event{Version: 1, ID: "restart-" + test.name + "-start", TaskID: taskID,
				SessionID: sessionID, CorrelationID: taskID, Sequence: 1, Time: claimAt, Kind: runtime.TaskStarted}
			if err := store.Append(ctx, 0, start); err != nil {
				store.Close()
				t.Fatal(err)
			}
			workerID := "restart-" + test.name + "-worker"
			dispatch, err := NewWorkboardWorkerDispatch(store, workerID, strings.Repeat("7", 64), time.Minute,
				restartSchedulerEvaluator{}, func() time.Time { return claimAt })
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			if _, err = dispatch.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: cardID,
				IdempotencyKey: "restart-" + test.name + "-claim", ExpectedCardRevision: cardRevision,
				TaskID: taskID, SessionID: sessionID}); err != nil {
				store.Close()
				t.Fatal(err)
			}
			if test.terminal {
				terminal := runtime.Event{Version: 1, ID: "restart-" + test.name + "-done", TaskID: taskID,
					SessionID: sessionID, CorrelationID: taskID, Sequence: 2, Time: claimAt.Add(time.Second), Kind: runtime.TaskCompleted}
				if err = store.Append(ctx, 1, terminal); err != nil {
					store.Close()
					t.Fatal(err)
				}
			}
			before := readRestartLifecycle(t, store, boardID, cardID)
			beforeEvents := readRestartEvents(t, store, taskID)
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := telemetry.Open(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			supervision := restartSupervision(t, reopened, observedAt)
			assertRestartSupervision(t, supervision, boardID, cardID, taskID, test.state, test.reason)
			var builds, runs atomic.Int32
			scheduler, err := NewWorkboardScheduler(supervision,
				WorkboardTaskFactoryFunc(func(context.Context, workboard.SupervisionItem) (WorkboardWorkerTask, error) {
					builds.Add(1)
					return WorkboardWorkerTask{}, errors.New("pre-existing claim reached task construction")
				}),
				WorkboardTaskRunnerFunc(func(context.Context, WorkboardWorkerTask) (WorkboardCandidate, error) {
					runs.Add(1)
					return WorkboardCandidate{}, errors.New("pre-existing claim reached provider dispatch")
				}), WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 10})
			if err != nil {
				reopened.Close()
				t.Fatal(err)
			}
			result, err := scheduler.RunCycle(ctx, boardID)
			if err != nil || result != (WorkboardScheduleResult{Scanned: 1, ExistingWIP: 1}) || builds.Load() != 0 || runs.Load() != 0 {
				reopened.Close()
				t.Fatalf("result=%+v builds=%d provider_runs=%d err=%v", result, builds.Load(), runs.Load(), err)
			}
			if after := readRestartLifecycle(t, reopened, boardID, cardID); !reflect.DeepEqual(after, before) {
				reopened.Close()
				t.Fatalf("scheduler changed pre-existing ownership: before=%+v after=%+v", before, after)
			}
			if afterEvents := readRestartEvents(t, reopened, taskID); !reflect.DeepEqual(afterEvents, beforeEvents) {
				reopened.Close()
				t.Fatalf("scheduler changed runtime journal: before=%+v after=%+v", beforeEvents, afterEvents)
			}
			if err = reopened.Close(); err != nil {
				t.Fatal(err)
			}

			verified, err := telemetry.Open(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer verified.Close()
			assertRestartSupervision(t, restartSupervision(t, verified, observedAt), boardID, cardID, taskID, test.state, test.reason)
			if final := readRestartLifecycle(t, verified, boardID, cardID); !reflect.DeepEqual(final, before) {
				t.Fatalf("second restart changed ownership: before=%+v after=%+v", before, final)
			}
		})
	}
}

func restartSupervision(t *testing.T, store *telemetry.Store, now time.Time) *workboard.SupervisionService {
	t.Helper()
	authority := fixedWorkboardAuthority{authority: workboard.Authority{CreationScope: "restart-test", Actor: workboard.Actor{ID: "restart-supervisor", Type: "system"}}}
	service, err := workboard.NewSupervisionService(store, authority, func() time.Time { return now }, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertRestartSupervision(t *testing.T, service *workboard.SupervisionService, boardID, cardID, taskID string,
	state workboard.SupervisionState, reason workboard.SupervisionReason,
) {
	t.Helper()
	page, err := service.Read(context.Background(), boardID, workboard.SupervisionOptions{Limit: 10})
	if err != nil || page.Validate() != nil || len(page.Items) != 1 {
		t.Fatalf("supervision=%+v err=%v", page, err)
	}
	item := page.Items[0]
	if item.CardID != cardID || item.TaskID != taskID || item.State != state || item.Reason != reason || item.Actions.Claim {
		t.Fatalf("supervision item=%+v", item)
	}
	if state == workboard.SupervisionRunning && item.Actions.RecoveryCheck ||
		state != workboard.SupervisionRunning && !item.Actions.RecoveryCheck {
		t.Fatalf("unsafe recovery projection=%+v", item)
	}
}

func readRestartLifecycle(t *testing.T, store *telemetry.Store, boardID, cardID string) workboard.CardLifecycleSnapshot {
	t.Helper()
	states, err := store.ReadCardLifecycleSnapshots(context.Background(), boardID, []string{cardID})
	if err != nil {
		t.Fatal(err)
	}
	state, ok := states[cardID]
	if !ok || state.CardID != cardID || state.Attempt == nil || state.Attempt.Validate() != nil || state.Attempt.Claim == nil {
		t.Fatalf("lifecycle=%+v", state)
	}
	return state
}

func readRestartEvents(t *testing.T, store *telemetry.Store, taskID string) []runtime.Event {
	t.Helper()
	events, err := store.Read(context.Background(), taskID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	return events
}
