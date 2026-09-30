package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

const schedulerCrashWorker = "scheduler-crash-worker"

// TestWorkboardSchedulerAfterSIGKILLObservesStaleClaimWithoutRedispatch
// qualifies the scheduler-specific crash boundary: another process atomically
// commits TaskStarted and the Workboard claim, acknowledges that durable
// boundary, and is then killed without running deferred cleanup. A fresh store
// and scheduler must discover the claim from durable state, count it as WIP,
// and never infer from lease expiry that dispatch is authorized.
func TestWorkboardSchedulerAfterSIGKILLObservesStaleClaimWithoutRedispatch(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("SIGKILL qualification requires Unix")
	}
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "scheduler-crash.db")
	store, boardID, cardID, cardRevision := readyWorkboardCardAt(t, database)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	crashWorkboardSchedulerClaimOwner(t, database, boardID, cardID, cardRevision)

	reopened, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	observedAt := time.Now().UTC().Add(2 * time.Minute)
	supervision := restartSupervision(t, reopened, observedAt)
	page, err := supervision.Read(ctx, boardID, workboard.SupervisionOptions{Limit: 10})
	if err != nil || page.Validate() != nil || len(page.Items) != 1 {
		t.Fatalf("post-crash supervision=%+v err=%v", page, err)
	}
	item := page.Items[0]
	if item.CardID != cardID || item.State != workboard.SupervisionStalled ||
		item.Reason != workboard.SupervisionLeaseExpired || item.Actions.Claim || !item.Actions.RecoveryCheck {
		t.Fatalf("post-crash claim projection=%+v", item)
	}
	before := readRestartLifecycle(t, reopened, boardID, cardID)
	beforeEvents := readRestartEvents(t, reopened, item.TaskID)

	var builds, runs atomic.Int32
	scheduler, err := NewWorkboardScheduler(supervision,
		WorkboardTaskFactoryFunc(func(context.Context, workboard.SupervisionItem) (WorkboardWorkerTask, error) {
			builds.Add(1)
			return WorkboardWorkerTask{}, errors.New("crashed claim reached task construction")
		}),
		WorkboardTaskRunnerFunc(func(context.Context, WorkboardWorkerTask) (WorkboardCandidate, error) {
			runs.Add(1)
			return WorkboardCandidate{}, errors.New("crashed claim reached provider dispatch")
		}), WorkboardScheduleLimits{MaxInFlight: 1, ScanLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.RunCycle(ctx, boardID)
	if err != nil || result != (WorkboardScheduleResult{Scanned: 1, ExistingWIP: 1}) ||
		builds.Load() != 0 || runs.Load() != 0 {
		t.Fatalf("cycle=%+v builds=%d provider_runs=%d err=%v", result, builds.Load(), runs.Load(), err)
	}
	after := readRestartLifecycle(t, reopened, boardID, cardID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("scheduler mutated crashed ownership: before=%+v after=%+v", before, after)
	}
	afterEvents := readRestartEvents(t, reopened, item.TaskID)
	if !reflect.DeepEqual(afterEvents, beforeEvents) {
		t.Fatalf("scheduler changed crashed runtime journal: before=%+v after=%+v", beforeEvents, afterEvents)
	}
}

func crashWorkboardSchedulerClaimOwner(t *testing.T, database, boardID, cardID string, cardRevision int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkboardSchedulerCrashClaimOwnerHelper$")
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"),
		"DARWIN_WORKBOARD_SCHEDULER_CRASH_HELPER=1",
		"DARWIN_WORKBOARD_SCHEDULER_CRASH_DB=" + database,
		"DARWIN_WORKBOARD_SCHEDULER_CRASH_BOARD=" + boardID,
		"DARWIN_WORKBOARD_SCHEDULER_CRASH_CARD=" + cardID,
		"DARWIN_WORKBOARD_SCHEDULER_CRASH_REVISION=" + strconv.FormatInt(cardRevision, 10),
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	defer func() {
		if !killed && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	acknowledged := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		acknowledged <- strings.TrimSpace(line)
	}()
	select {
	case acknowledgement := <-acknowledged:
		if acknowledgement != "claim-committed" {
			t.Fatalf("invalid crash-boundary acknowledgement %q: %s", acknowledgement, stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("claim owner missed crash boundary: %v: %s", ctx.Err(), stderr.String())
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("claim owner survived SIGKILL")
	}
	killed = true
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("claim owner did not die by SIGKILL: %v", cmd.ProcessState)
	}
}

func TestWorkboardSchedulerCrashClaimOwnerHelper(t *testing.T) {
	if os.Getenv("DARWIN_WORKBOARD_SCHEDULER_CRASH_HELPER") != "1" {
		return
	}
	ctx := context.Background()
	database := os.Getenv("DARWIN_WORKBOARD_SCHEDULER_CRASH_DB")
	boardID := os.Getenv("DARWIN_WORKBOARD_SCHEDULER_CRASH_BOARD")
	cardID := os.Getenv("DARWIN_WORKBOARD_SCHEDULER_CRASH_CARD")
	cardRevision, err := strconv.ParseInt(os.Getenv("DARWIN_WORKBOARD_SCHEDULER_CRASH_REVISION"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	store, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	taskID, sessionID := "scheduler-crash-task", "scheduler-crash-session"
	dispatch, err := NewWorkboardWorkerDispatch(store, schedulerCrashWorker, strings.Repeat("9", 64), time.Minute,
		coordinatorEvaluator{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Version: 1, ID: "scheduler-crash-start", TaskID: taskID, SessionID: sessionID,
		CorrelationID: taskID, WorkerID: schedulerCrashWorker, Sequence: 1, Time: now, Kind: runtime.TaskStarted}
	_, err = dispatch.ClaimTaskStart(ctx, event, workboard.ClaimRequest{BoardID: boardID, CardID: cardID,
		IdempotencyKey: "scheduler-crash-claim", ExpectedCardRevision: cardRevision, TaskID: taskID, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stdout.WriteString("claim-committed\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(24 * time.Hour)
}
